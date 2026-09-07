// Package shell runs an interactive login shell on a host and keeps it alive
// between visits.
//
// The session lives here rather than in the browser, and that is the whole
// design. A phone drops its connection constantly — the screen locks, another
// app comes to the front, wifi hands over to cellular — and a terminal that
// lived in the page would lose the shell every time, along with the directory
// it was in, the command half typed, and the program it was running. So the
// shell is held open on HostMan's side, everything it prints is kept, and a
// client that comes back asks for the bytes it missed and carries on. Closing
// the screen is not the same as closing the shell.
//
// Like the rest of HostMan there is no agent on the host: this is one SSH
// session with a pty on it, which is what `ssh host` already is.
package shell

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"time"

	"github.com/chinmay28/deployer/server/internal/sshx"
	"github.com/chinmay28/deployer/server/internal/store"
)

const (
	// Scrollback is how much of the screen a session keeps for a client that
	// comes back. It is a byte count rather than a line count because that is
	// what bounds the memory, and because a line of a redrawing program like
	// top is not a line of a log.
	Scrollback = 256 << 10

	// Idle is how long a session with nobody watching stays open. Long enough
	// to answer a phone call and come back to the same shell; short enough that
	// a forgotten tab does not hold an SSH connection to a host all week.
	Idle = 15 * time.Minute

	// PerHost bounds the shells one host can have open at once, so a client
	// stuck in a reconnect loop cannot open connections until sshd refuses.
	PerHost = 4

	// MaxInput bounds one write. A keystroke is a byte; this is room for a
	// pasted command and not for a pasted file.
	MaxInput = 64 << 10
)

// Size limits keep a bad or hostile geometry from reaching the host's pty.
const (
	minCols, maxCols = 20, 500
	minRows, maxRows = 5, 200
)

// ErrNotFound means the session id is unknown: it never existed, or it was
// reaped after being left alone too long.
var ErrNotFound = errors.New("that shell is no longer open")

// ErrTooMany means the host already has as many shells as it is allowed.
var ErrTooMany = errors.New("too many shells are already open on this host")

// ErrClosed means the shell has exited; its output can still be read.
var ErrClosed = errors.New("the shell has exited")

// Connector opens SSH connections to hosts. hosts.Service satisfies it.
type Connector interface {
	Connect(ctx context.Context, h *store.Host) (*sshx.Client, error)
}

// pty is the part of a terminal a session uses: read the screen, write the
// keyboard, change the window, find out how it ended. *sshx.Terminal satisfies
// it, and so does a fake that never leaves the process — which is what lets the
// buffering, the waking and the reaping be tested without an sshd.
type pty interface {
	io.ReadWriteCloser
	Resize(cols, rows int) error
	Status() int
}

// Manager owns the open shells.
type Manager struct {
	conn Connector
	log  *slog.Logger
	idle time.Duration
	// now is time.Now except in tests, which need to be able to age a session
	// without waiting a quarter of an hour.
	now func() time.Time

	mu       sync.Mutex
	sessions map[string]*Session
}

// NewManager builds a Manager that connects through c.
func NewManager(c Connector, log *slog.Logger) *Manager {
	return &Manager{
		conn:     c,
		log:      log,
		idle:     Idle,
		now:      time.Now,
		sessions: map[string]*Session{},
	}
}

// Info is what a client is told about a session. It carries no output: the
// screen comes from Read, which can start where the client left off.
type Info struct {
	ID     string `json:"id"`
	HostID int64  `json:"hostId"`
	// User is who the shell runs as, which is the SSH user and not root: a
	// shell is the one place in HostMan that does not quietly elevate, because
	// a prompt that says one thing and runs as another is how mistakes happen.
	User      string    `json:"user"`
	Cols      int       `json:"cols"`
	Rows      int       `json:"rows"`
	StartedAt time.Time `json:"startedAt"`
	// Offset is how many bytes the shell has produced so far. A client that has
	// never seen this session reads from 0; one that is reconnecting reads from
	// where it stopped.
	Offset int64 `json:"offset"`
	// Running is false once the shell has exited, which leaves the session
	// readable but not writable until it is reaped.
	Running bool `json:"running"`
	// Exit says how it ended, in words, and is empty while it is running.
	Exit string `json:"exit"`
	// Watchers is how many screens are attached, which is how a second phone
	// discovers it is looking at the same shell as the first.
	Watchers int `json:"watchers"`
}

// Session is one shell.
type Session struct {
	id        string
	hostID    int64
	user      string
	startedAt time.Time

	// conn is the SSH connection under the terminal. Closing the terminal ends
	// the shell; closing this is what gives the socket back.
	conn io.Closer
	term pty
	log  *slog.Logger
	now  func() time.Time

	mu sync.Mutex
	// buf holds the tail of the output, and dropped counts what has fallen off
	// the front of it. total is every byte ever produced, so an offset stays
	// meaningful after the buffer has wrapped many times over.
	buf     []byte
	dropped int64
	total   int64
	// change is closed and replaced whenever there is something new to see,
	// which is how a reader waits without polling and still honours its
	// context.
	change   chan struct{}
	cols     int
	rows     int
	watchers int
	lastSeen time.Time
	running  bool
	exit     string
	// because is the reason HostMan ended the shell, recorded before the
	// terminal is closed. Without it the pump wins the race and writes down the
	// symptom — "closed" — in place of the cause.
	because string
}

// Chunk is a slice of the screen, and where it came from.
type Chunk struct {
	Data []byte
	// From is the offset Data starts at. It is past the offset asked for when
	// the shell outran the buffer while nobody was watching, which is the one
	// case where a reconnecting client has genuinely missed something.
	From int64
	Next int64
	// Done means the shell has exited and there will be nothing after this.
	Done bool
	Exit string
}

// Open starts a shell on a host.
func (m *Manager) Open(ctx context.Context, h *store.Host, cols, rows int) (*Session, error) {
	cols, rows = clampSize(cols, rows)

	m.mu.Lock()
	open := 0
	for _, s := range m.sessions {
		if s.hostID == h.ID && s.Running() {
			open++
		}
	}
	m.mu.Unlock()
	if open >= PerHost {
		return nil, ErrTooMany
	}

	client, err := m.conn.Connect(ctx, h)
	if err != nil {
		return nil, err
	}
	term, err := client.Shell(cols, rows)
	if err != nil {
		client.Close()
		return nil, err
	}

	s := m.adopt(h.ID, h.Username, client, term, cols, rows)

	m.mu.Lock()
	m.sessions[s.id] = s
	m.mu.Unlock()
	m.log.Info("shell: opened", "host", h.Name, "session", s.id, "cols", cols, "rows", rows)
	return s, nil
}

// adopt wraps a live terminal in a session and starts reading its screen. It is
// the seam the tests use: everything after this point is HostMan's own
// bookkeeping and has no SSH in it.
func (m *Manager) adopt(hostID int64, user string, conn io.Closer, term pty, cols, rows int) *Session {
	now := m.now()
	s := &Session{
		id:        newID(),
		hostID:    hostID,
		user:      user,
		startedAt: now,
		conn:      conn,
		term:      term,
		log:       m.log,
		now:       m.now,
		change:    make(chan struct{}),
		cols:      cols,
		rows:      rows,
		lastSeen:  now,
		running:   true,
	}
	go s.pump()
	return s
}

// Get returns a session by id.
func (m *Manager) Get(id string) (*Session, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[id]
	if !ok {
		return nil, ErrNotFound
	}
	return s, nil
}

// ForHost lists the shells open on a host, oldest first, so a client arriving
// on the screen can rejoin one instead of starting another.
func (m *Manager) ForHost(hostID int64) []*Session {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []*Session
	for _, s := range m.sessions {
		if s.hostID == hostID {
			out = append(out, s)
		}
	}
	sortByStart(out)
	return out
}

// Close ends a session and forgets it.
func (m *Manager) Close(id string) error {
	m.mu.Lock()
	s, ok := m.sessions[id]
	delete(m.sessions, id)
	m.mu.Unlock()
	if !ok {
		return ErrNotFound
	}
	s.shutdown("closed from HostMan")
	return nil
}

// CloseHost ends every shell on a host. Called when a host is removed, so a
// forgotten host does not leave a live connection behind it.
func (m *Manager) CloseHost(hostID int64) {
	m.mu.Lock()
	var doomed []*Session
	for id, s := range m.sessions {
		if s.hostID == hostID {
			doomed = append(doomed, s)
			delete(m.sessions, id)
		}
	}
	m.mu.Unlock()
	for _, s := range doomed {
		s.shutdown("the host was removed from HostMan")
	}
}

// Run reaps sessions nobody is watching until ctx is done, then closes the rest.
func (m *Manager) Run(ctx context.Context) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			m.closeAll()
			return
		case <-ticker.C:
			m.reap()
		}
	}
}

// reap closes what has been left alone past the idle window. A shell that has
// already exited is kept for the same window rather than vanishing, so a client
// that reconnects into one is told it ended instead of that it never existed.
func (m *Manager) reap() {
	cutoff := m.now().Add(-m.idle)
	m.mu.Lock()
	var doomed []*Session
	for id, s := range m.sessions {
		if s.idleSince(cutoff) {
			doomed = append(doomed, s)
			delete(m.sessions, id)
		}
	}
	m.mu.Unlock()
	for _, s := range doomed {
		m.log.Info("shell: reaped an idle session", "session", s.id, "host", s.hostID)
		s.shutdown("closed after being left alone")
	}
}

func (m *Manager) closeAll() {
	m.mu.Lock()
	all := make([]*Session, 0, len(m.sessions))
	for id, s := range m.sessions {
		all = append(all, s)
		delete(m.sessions, id)
	}
	m.mu.Unlock()
	for _, s := range all {
		s.shutdown("HostMan is shutting down")
	}
}

// --- session ---

// ID is the opaque handle a client holds onto across reconnects.
func (s *Session) ID() string { return s.id }

// HostID is the host this shell runs on.
func (s *Session) HostID() int64 { return s.hostID }

// Running reports whether the shell is still there to type at.
func (s *Session) Running() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.running
}

// Info describes the session as it stands.
func (s *Session) Info() Info {
	s.mu.Lock()
	defer s.mu.Unlock()
	return Info{
		ID:        s.id,
		HostID:    s.hostID,
		User:      s.user,
		Cols:      s.cols,
		Rows:      s.rows,
		StartedAt: s.startedAt,
		Offset:    s.total,
		Running:   s.running,
		Exit:      s.exit,
		Watchers:  s.watchers,
	}
}

// Write sends keystrokes to the shell.
func (s *Session) Write(p []byte) error {
	if len(p) > MaxInput {
		return fmt.Errorf("that is more than a shell will take at once (%d bytes)", len(p))
	}
	s.mu.Lock()
	running := s.running
	s.lastSeen = s.now()
	s.mu.Unlock()
	if !running {
		return ErrClosed
	}
	if _, err := s.term.Write(p); err != nil {
		return fmt.Errorf("send to the shell: %w", err)
	}
	return nil
}

// Resize tells the shell the window changed — turning a phone sideways, or
// bringing up the keyboard.
func (s *Session) Resize(cols, rows int) error {
	cols, rows = clampSize(cols, rows)
	s.mu.Lock()
	running := s.running
	unchanged := s.cols == cols && s.rows == rows
	if running {
		s.cols, s.rows = cols, rows
	}
	s.lastSeen = s.now()
	s.mu.Unlock()
	if !running {
		return ErrClosed
	}
	if unchanged {
		return nil
	}
	return s.term.Resize(cols, rows)
}

// Attach marks a screen as watching, which is what keeps the session out of the
// reaper's way. The returned function must be called when it stops watching.
func (s *Session) Attach() func() {
	s.mu.Lock()
	s.watchers++
	s.lastSeen = s.now()
	s.mu.Unlock()

	var once sync.Once
	return func() {
		once.Do(func() {
			s.mu.Lock()
			if s.watchers > 0 {
				s.watchers--
			}
			s.lastSeen = s.now()
			s.mu.Unlock()
		})
	}
}

// Read returns whatever the shell has produced since from. When there is
// nothing it waits — for output, for the shell to exit, or for ctx to be done.
func (s *Session) Read(ctx context.Context, from int64) (Chunk, error) {
	for {
		s.mu.Lock()
		if from < s.dropped {
			from = s.dropped
		}
		// An offset past the end is moved back to it, so a client that made one
		// up waits for whatever the shell prints next instead of for the bytes
		// between here and a number that will never be reached.
		if from > s.total {
			from = s.total
		}
		if from < s.total {
			c := Chunk{
				Data: append([]byte(nil), s.buf[from-s.dropped:]...),
				From: from,
				Next: s.total,
			}
			s.mu.Unlock()
			return c, nil
		}
		if !s.running {
			c := Chunk{From: from, Next: from, Done: true, Exit: s.exit}
			s.mu.Unlock()
			return c, nil
		}
		change := s.change
		s.mu.Unlock()

		select {
		case <-change:
		case <-ctx.Done():
			return Chunk{}, ctx.Err()
		}
	}
}

// pump moves the screen from the host into the buffer until the shell ends.
func (s *Session) pump() {
	buf := make([]byte, 32<<10)
	for {
		n, err := s.term.Read(buf)
		if n > 0 {
			s.append(buf[:n])
		}
		if err != nil {
			s.finish(exitReason(s.term, err))
			return
		}
	}
}

func (s *Session) append(p []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.buf = append(s.buf, p...)
	s.total += int64(len(p))
	// Trimmed back to three quarters rather than to the limit, so a busy shell
	// pays for the copy once every quarter-buffer instead of on every write.
	if len(s.buf) > Scrollback {
		cut := len(s.buf) - Scrollback*3/4
		s.buf = append(s.buf[:0], s.buf[cut:]...)
		s.dropped += int64(cut)
	}
	s.notify()
}

// finish records how the shell ended and wakes every reader.
func (s *Session) finish(reason string) {
	s.mu.Lock()
	if !s.running {
		s.mu.Unlock()
		return
	}
	if s.because != "" {
		reason = s.because
	}
	s.running = false
	s.exit = reason
	s.lastSeen = s.now()
	s.notify()
	s.mu.Unlock()
	s.conn.Close()
}

// notify wakes everyone waiting. The caller holds mu.
func (s *Session) notify() {
	close(s.change)
	s.change = make(chan struct{})
}

// shutdown ends the shell from HostMan's side, saying why.
func (s *Session) shutdown(reason string) {
	// The reason goes down first: closing the terminal is what wakes the pump,
	// and whichever of the two reaches finish first should still record this.
	s.mu.Lock()
	if s.because == "" {
		s.because = reason
	}
	s.mu.Unlock()
	s.term.Close()
	s.finish(reason)
}

func (s *Session) idleSince(cutoff time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.watchers == 0 && s.lastSeen.Before(cutoff)
}

// exitReason turns the end of the output into a sentence. The shell's own exit
// status is the usual case; the rest is the connection going away underneath
// it, which is a different thing and worth saying so.
func exitReason(term pty, err error) string {
	if errors.Is(err, sshx.ErrTerminalClosed) {
		return "closed"
	}
	if err != nil && !errors.Is(err, io.EOF) {
		return "the connection to the host dropped: " + err.Error()
	}
	switch code := term.Status(); {
	case code == 0:
		return "the shell exited"
	case code < 0:
		return "the shell ended without a status"
	default:
		return fmt.Sprintf("the shell exited with status %d", code)
	}
}

func clampSize(cols, rows int) (int, int) {
	return clamp(cols, minCols, maxCols), clamp(rows, minRows, maxRows)
}

func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func newID() string {
	b := make([]byte, 16)
	// crypto/rand.Read never fails on any platform HostMan runs on, and the
	// signature keeps the error only for form.
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func sortByStart(list []*Session) {
	for i := 1; i < len(list); i++ {
		for j := i; j > 0 && list[j].startedAt.Before(list[j-1].startedAt); j-- {
			list[j], list[j-1] = list[j-1], list[j]
		}
	}
}
