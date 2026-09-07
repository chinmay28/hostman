// Package claude holds conversations with Claude Code running on hosts, and
// keeps them going between visits.
//
// A session here is one `claude` process on a host, started over SSH with its
// standard streams held open, and everything it has said kept on HostMan's
// side as a log of events. That is the same shape as a shell, for the same
// reason: a phone drops its connection every time it locks, and a
// conversation that lived in the page would lose its history, the question
// Claude was in the middle of asking, and the work it was in the middle of
// doing. So the process belongs to HostMan, a screen that comes back asks
// for the events it missed, and closing the screen is not the same as ending
// the session.
//
// What is different from a shell is that the stream has a grammar. Claude
// asks permission and waits; the answer has to go back to the right question,
// and if two phones are watching, the one that answers settles it for both.
// The model and the permission mode can change mid-conversation by a request
// the CLI has to acknowledge. All of that matching lives here, on top of the
// wire format in claudecli.
//
// Like the rest of HostMan there is no agent on the host: the process is
// Claude Code itself, installed for the SSH user, working in a directory of
// theirs, with their sign-in.
package claude

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/chinmay28/deployer/server/internal/claudecli"
	"github.com/chinmay28/deployer/server/internal/sshx"
	"github.com/chinmay28/deployer/server/internal/store"
)

const (
	// Idle is how long a session with nobody watching stays open. Longer than
	// a shell's, because a conversation is worth more than a prompt: coming
	// back after lunch to the same session is the ordinary case, and the
	// process costs the host nothing while it waits.
	Idle = time.Hour

	// PerHost bounds the sessions one host can have open at once. Each is a
	// Claude Code process, which is not a small thing on a Raspberry Pi.
	PerHost = 3

	// MaxLog is how much of a session's history is kept, in encoded bytes. A
	// conversation that outgrows it loses its oldest events, which the screen
	// is told about, rather than holding a Pi's worth of memory.
	MaxLog = 2 << 20

	// MaxMessage bounds one message from the user.
	MaxMessage = 32 << 10

	// controlTimeout is how long a model or mode change waits for the CLI to
	// acknowledge it. The CLI answers these at once; a wait past this means
	// the process is wedged, and the caller should hear that.
	controlTimeout = 20 * time.Second

	// maxLine bounds one line from the CLI. A tool result carrying a whole
	// file is a big line, and the scanner must not choke on it.
	maxLine = 8 << 20
)

// ErrNotFound means the session id is unknown: it never existed, or it was
// reaped after being left alone too long.
var ErrNotFound = errors.New("that session is no longer open")

// ErrTooMany means the host already has as many sessions as it is allowed.
var ErrTooMany = errors.New("too many Claude sessions are already open on this host")

// ErrClosed means the process has exited; its history can still be read.
var ErrClosed = errors.New("the session has ended")

// ErrNoRequest means the permission request being answered is not waiting:
// it was answered already, from this screen or another, or was withdrawn.
var ErrNoRequest = errors.New("that request is no longer waiting for an answer")

// Connector opens SSH connections to hosts. hosts.Service satisfies it.
type Connector interface {
	Connect(ctx context.Context, h *store.Host) (*sshx.Client, error)
}

// process is the part of a running CLI a session uses: read its stdout, write
// its stdin, find out how it ended. *sshx.Process satisfies it, and so does a
// fake that never leaves the process, which is what the tests drive.
type process interface {
	io.ReadWriteCloser
	Status() int
	Stderr() string
}

// Options is how a session is started. It is claudecli's, re-exported so the
// API does not have to know about the wire package.
type Options = claudecli.Options

// Manager owns the open sessions.
type Manager struct {
	conn Connector
	log  *slog.Logger
	idle time.Duration
	// now is time.Now except in tests, which need to age a session without
	// waiting an hour.
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

// Info is what a client is told about a session. It carries no history: that
// comes from Read, which can start where the client left off.
type Info struct {
	ID     string `json:"id"`
	HostID int64  `json:"hostId"`
	// User is who Claude runs as on the host: the SSH user, with that user's
	// sign-in and that user's files.
	User string `json:"user"`
	// Name is what the session is called: what it was started as, or, failing
	// that, the first thing the user said.
	Name string `json:"name"`
	Dir  string `json:"dir"`
	// Model and Mode are what the CLI reported, which is the truth: an alias
	// asked for at the start comes back as the model it resolved to.
	Model string `json:"model"`
	Mode  string `json:"mode"`
	// CLISessionID is the id the CLI knows the conversation by, so it can be
	// resumed on the host with `claude --resume`.
	CLISessionID string    `json:"cliSessionId"`
	StartedAt    time.Time `json:"startedAt"`
	// Offset is how many events the session has produced. A client that has
	// never seen it reads from 0; one reconnecting reads from where it stopped.
	Offset int64 `json:"offset"`
	// Running is false once the process has exited.
	Running bool `json:"running"`
	// Busy is true while Claude is working on a turn — from a message being
	// sent until the result comes back.
	Busy bool `json:"busy"`
	// Pending is how many permission requests are waiting for an answer.
	Pending int `json:"pending"`
	// Exit says how it ended, in words, and is empty while it is running.
	Exit string `json:"exit"`
	// Watchers is how many screens are attached.
	Watchers int `json:"watchers"`
	// Cost and Turns are the session's running totals as the CLI reports
	// them; Context is how full the conversation is, against ContextWindow.
	Cost          float64 `json:"cost"`
	Turns         int     `json:"turns"`
	Context       int     `json:"context"`
	ContextWindow int     `json:"contextWindow"`
}

// Entry is one event in a session's history, numbered so a client can say
// where it got to.
type Entry struct {
	Seq int64     `json:"seq"`
	At  time.Time `json:"at"`
	claudecli.Event
}

// Chunk is a slice of the history, and where it came from.
type Chunk struct {
	Entries []Entry
	// From is the sequence number Entries starts at. It is past the number
	// asked for when the log outgrew MaxLog while nobody was watching, which
	// is the one case where a reconnecting client has genuinely missed
	// something.
	From int64
	Next int64
	// Done means the process has exited and there will be nothing after this.
	Done bool
	Exit string
}

// Session is one conversation.
type Session struct {
	id        string
	hostID    int64
	user      string
	opts      Options
	startedAt time.Time

	conn io.Closer
	proc process
	log  *slog.Logger
	now  func() time.Time

	// wmu serialises writes to the process, which is one stream that two
	// screens may both be writing to.
	wmu sync.Mutex

	mu sync.Mutex
	// entries is the tail of the history, dropped is the sequence number of
	// the first one still kept, and size is how many encoded bytes they take.
	entries []Entry
	dropped int64
	size    int
	// change is closed and replaced whenever there is something new to see.
	change chan struct{}
	// pending is the permission requests waiting for an answer, by request
	// id; waiters is the control requests HostMan made and is waiting on.
	pending map[string]claudecli.Event
	waiters map[string]chan claudecli.Event
	nextReq int

	name          string
	model         string
	mode          string
	cwd           string
	busy          bool
	running       bool
	exit          string
	because       string
	cost          float64
	turns         int
	context       int
	contextWindow int
	watchers      int
	lastSeen      time.Time
}

// Open starts a session on a host.
func (m *Manager) Open(ctx context.Context, h *store.Host, opts Options) (*Session, error) {
	if opts.Mode == "" {
		opts.Mode = claudecli.ModeDefault
	}
	if opts.SessionID == "" {
		opts.SessionID = claudecli.NewSessionID()
	}
	cmd, err := claudecli.Command(opts)
	if err != nil {
		return nil, err
	}

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
	proc, err := client.Start(cmd)
	if err != nil {
		client.Close()
		return nil, err
	}

	s := m.adopt(h.ID, h.Username, client, proc, opts)
	m.mu.Lock()
	m.sessions[s.id] = s
	m.mu.Unlock()
	m.log.Info("claude: opened a session", "host", h.Name, "session", s.id, "dir", opts.Dir, "model", opts.Model, "mode", opts.Mode)
	return s, nil
}

// adopt wraps a live process in a session and starts reading it. It is the
// seam the tests use: everything after this point is HostMan's own
// bookkeeping and has no SSH in it.
func (m *Manager) adopt(hostID int64, user string, conn io.Closer, proc process, opts Options) *Session {
	now := m.now()
	s := &Session{
		id:        newID(),
		hostID:    hostID,
		user:      user,
		opts:      opts,
		startedAt: now,
		conn:      conn,
		proc:      proc,
		log:       m.log,
		now:       m.now,
		change:    make(chan struct{}),
		pending:   map[string]claudecli.Event{},
		waiters:   map[string]chan claudecli.Event{},
		name:      opts.Name,
		model:     opts.Model,
		mode:      opts.Mode,
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

// ForHost lists the sessions on a host, oldest first, so a client arriving on
// the screen can rejoin one instead of starting another.
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
	s.shutdown("ended from HostMan")
	return nil
}

// CloseHost ends every session on a host. Called when a host is removed.
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

// Run reaps sessions nobody is watching until ctx is done, then closes the
// rest.
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

// reap closes what has been left alone past the idle window. A session that
// has already ended is kept for the same window rather than vanishing, so a
// client that reconnects into one is told it ended instead of that it never
// existed. A session Claude is still working in is left alone too: the point
// of starting something and putting the phone down is that it finishes.
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
		m.log.Info("claude: reaped an idle session", "session", s.id, "host", s.hostID)
		s.shutdown("ended after being left alone")
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

// HostID is the host this session runs on.
func (s *Session) HostID() int64 { return s.hostID }

// Running reports whether the process is still there to talk to.
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
		ID:            s.id,
		HostID:        s.hostID,
		User:          s.user,
		Name:          s.name,
		Dir:           s.dirLocked(),
		Model:         s.model,
		Mode:          s.mode,
		CLISessionID:  s.opts.SessionID,
		StartedAt:     s.startedAt,
		Offset:        s.dropped + int64(len(s.entries)),
		Running:       s.running,
		Busy:          s.busy,
		Pending:       len(s.pending),
		Exit:          s.exit,
		Watchers:      s.watchers,
		Cost:          s.cost,
		Turns:         s.turns,
		Context:       s.context,
		ContextWindow: s.contextWindow,
	}
}

// dirLocked is the directory as the CLI reported it, or as it was asked for
// before the CLI has said.
func (s *Session) dirLocked() string {
	if s.cwd != "" {
		return s.cwd
	}
	if s.opts.Dir == "" {
		return "~"
	}
	return s.opts.Dir
}

// Send gives Claude a message from the user.
func (s *Session) Send(text string) error {
	text = strings.TrimSpace(text)
	if text == "" {
		return errors.New("there is nothing to send")
	}
	if len(text) > MaxMessage {
		return fmt.Errorf("that is more than a message will take (%d bytes)", len(text))
	}
	s.mu.Lock()
	if !s.running {
		s.mu.Unlock()
		return ErrClosed
	}
	if s.name == "" {
		s.name = firstLine(text)
	}
	s.busy = true
	s.lastSeen = s.now()
	s.appendLocked(claudecli.Event{Kind: claudecli.KindUser, Text: text})
	s.mu.Unlock()
	return s.write(claudecli.UserMessage(text))
}

// Answer settles a permission request. always hands the CLI's suggested rule
// back with a yes, so the same thing is not asked again this session.
func (s *Session) Answer(requestID string, allow, always bool, reason string) error {
	s.mu.Lock()
	if !s.running {
		s.mu.Unlock()
		return ErrClosed
	}
	req, ok := s.pending[requestID]
	if !ok {
		s.mu.Unlock()
		return ErrNoRequest
	}
	delete(s.pending, requestID)
	behavior := "deny"
	if allow {
		behavior = "allow"
	}
	s.lastSeen = s.now()
	s.appendLocked(claudecli.Event{
		Kind:      claudecli.KindAnswered,
		RequestID: requestID,
		ToolUseID: req.ToolUseID,
		Behavior:  behavior,
		Text:      reason,
	})
	s.mu.Unlock()

	var line []byte
	switch {
	case allow && always:
		line = claudecli.Allow(requestID, req.Input, req.Suggestions)
	case allow:
		line = claudecli.Allow(requestID, req.Input, nil)
	default:
		line = claudecli.Deny(requestID, reason)
	}
	return s.write(line)
}

// SetModel switches models from the next message on. It waits for the CLI to
// accept, so a model the host does not have is an error here and not a
// surprise later.
func (s *Session) SetModel(ctx context.Context, model string) error {
	model = strings.TrimSpace(model)
	if model == "" {
		return errors.New("a model is required")
	}
	if _, err := s.request(ctx, func(id string) []byte { return claudecli.SetModel(id, model) }); err != nil {
		return err
	}
	s.mu.Lock()
	s.model = model
	s.appendLocked(claudecli.Event{Kind: claudecli.KindModel, Model: model})
	s.mu.Unlock()
	return nil
}

// SetMode changes the permission mode.
func (s *Session) SetMode(ctx context.Context, mode string) error {
	if !claudecli.ValidMode(mode) {
		return fmt.Errorf("%q is not a permission mode", mode)
	}
	if _, err := s.request(ctx, func(id string) []byte { return claudecli.SetMode(id, mode) }); err != nil {
		return err
	}
	s.mu.Lock()
	s.mode = mode
	s.appendLocked(claudecli.Event{Kind: claudecli.KindMode, Mode: mode})
	s.mu.Unlock()
	return nil
}

// Interrupt stops what Claude is doing. The turn ends with a result like any
// other, which is what clears Busy.
func (s *Session) Interrupt(ctx context.Context) error {
	_, err := s.request(ctx, claudecli.Interrupt)
	return err
}

// request sends a control request and waits for the CLI's answer.
func (s *Session) request(ctx context.Context, build func(id string) []byte) (claudecli.Event, error) {
	s.mu.Lock()
	if !s.running {
		s.mu.Unlock()
		return claudecli.Event{}, ErrClosed
	}
	s.nextReq++
	id := "deployer-" + strconv.Itoa(s.nextReq)
	ch := make(chan claudecli.Event, 1)
	s.waiters[id] = ch
	s.lastSeen = s.now()
	change := s.change
	s.mu.Unlock()

	forget := func() {
		s.mu.Lock()
		delete(s.waiters, id)
		s.mu.Unlock()
	}
	if err := s.write(build(id)); err != nil {
		forget()
		return claudecli.Event{}, err
	}

	ctx, cancel := context.WithTimeout(ctx, controlTimeout)
	defer cancel()
	for {
		select {
		case ev := <-ch:
			if !ev.OK {
				if ev.Error == "" {
					ev.Error = "Claude refused"
				}
				return ev, errors.New(ev.Error)
			}
			return ev, nil
		case <-ctx.Done():
			forget()
			return claudecli.Event{}, fmt.Errorf("Claude did not answer: %w", ctx.Err())
		case <-change:
			// Something happened; if it was the process ending, say so
			// rather than waiting out the timeout.
			s.mu.Lock()
			running := s.running
			change = s.change
			s.mu.Unlock()
			if !running {
				forget()
				return claudecli.Event{}, ErrClosed
			}
		}
	}
}

// Attach marks a screen as watching, which is what keeps the session out of
// the reaper's way. The returned function must be called when it stops.
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

// Read returns the events since from. When there are none it waits — for an
// event, for the process to exit, or for ctx to be done.
func (s *Session) Read(ctx context.Context, from int64) (Chunk, error) {
	for {
		s.mu.Lock()
		total := s.dropped + int64(len(s.entries))
		if from < s.dropped {
			from = s.dropped
		}
		if from > total {
			from = total
		}
		if from < total {
			c := Chunk{
				Entries: append([]Entry(nil), s.entries[from-s.dropped:]...),
				From:    from,
				Next:    total,
				// The exit event is the last thing ever appended, so a chunk
				// that reaches the end of an ended session is the end.
				Done: !s.running,
				Exit: s.exit,
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

// write sends one line to the process.
func (s *Session) write(line []byte) error {
	s.wmu.Lock()
	defer s.wmu.Unlock()
	if _, err := s.proc.Write(line); err != nil {
		return fmt.Errorf("send to Claude: %w", err)
	}
	return nil
}

// pump reads the process's stdout until it ends, turning lines into history.
func (s *Session) pump() {
	sc := bufio.NewScanner(s.proc)
	sc.Buffer(make([]byte, 64<<10), maxLine)
	for sc.Scan() {
		line := sc.Bytes()
		if len(strings.TrimSpace(string(line))) == 0 {
			continue
		}
		events, err := claudecli.Decode(line)
		if err != nil {
			s.log.Debug("claude: unreadable line", "session", s.id, "err", err)
			continue
		}
		for _, ev := range events {
			s.handle(ev)
		}
	}
	reason := "Claude exited"
	if err := sc.Err(); err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, sshx.ErrTerminalClosed) {
		reason = "the connection to Claude was lost: " + err.Error()
	} else if code := s.proc.Status(); code != 0 {
		reason = exitReason(code, s.proc.Stderr())
	}
	s.finish(reason)
}

// handle folds one event into the session's state and history.
func (s *Session) handle(ev claudecli.Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch ev.Kind {
	case claudecli.KindInit:
		s.model, s.mode, s.cwd = ev.Model, ev.Mode, ev.Cwd
	case claudecli.KindPermission:
		s.pending[ev.RequestID] = ev
	case claudecli.KindPermissionCancelled:
		if _, ok := s.pending[ev.RequestID]; !ok {
			return
		}
		delete(s.pending, ev.RequestID)
	case claudecli.KindControlRequest:
		// Hooks, dialogs and MCP relays are for an SDK host with code behind
		// it. Saying no at once is what keeps the CLI from waiting forever.
		go s.write(claudecli.Refuse(ev.RequestID, "HostMan does not handle "+ev.Subtype))
		return
	case claudecli.KindControlResponse:
		if ch, ok := s.waiters[ev.RequestID]; ok {
			delete(s.waiters, ev.RequestID)
			ch <- ev
		}
		return
	case claudecli.KindResult:
		s.busy = false
		s.cost = ev.Cost
		s.turns = ev.Turns
		if ev.ContextWindow > 0 {
			s.contextWindow = ev.ContextWindow
		}
		// A turn that ended leaves nothing to answer.
		for id := range s.pending {
			delete(s.pending, id)
		}
	case claudecli.KindAssistant:
		if ev.Context > 0 {
			s.context = ev.Context
		}
	}
	s.appendLocked(ev)
}

// appendLocked adds an event to the history and wakes every reader. The
// caller holds s.mu.
func (s *Session) appendLocked(ev claudecli.Event) {
	e := Entry{Seq: s.dropped + int64(len(s.entries)), At: s.now(), Event: ev}
	s.entries = append(s.entries, e)
	s.size += sizeOf(e)
	// Trimmed back to three quarters rather than to the limit, so a long
	// conversation pays for the copy once every quarter-log and not on every
	// event.
	if s.size > MaxLog {
		target := MaxLog * 3 / 4
		cut := 0
		for cut < len(s.entries)-1 && s.size > target {
			s.size -= sizeOf(s.entries[cut])
			cut++
		}
		s.entries = append(s.entries[:0], s.entries[cut:]...)
		s.dropped += int64(cut)
	}
	s.notifyLocked()
}

func (s *Session) notifyLocked() {
	close(s.change)
	s.change = make(chan struct{})
}

func sizeOf(e Entry) int {
	b, err := json.Marshal(e)
	if err != nil {
		return 0
	}
	return len(b)
}

// finish records how the process ended and wakes every reader.
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
	s.busy = false
	s.exit = reason
	for id := range s.pending {
		delete(s.pending, id)
	}
	for id, ch := range s.waiters {
		delete(s.waiters, id)
		ch <- claudecli.Event{Kind: claudecli.KindControlResponse, RequestID: id, Error: reason}
	}
	s.appendLocked(claudecli.Event{Kind: claudecli.KindExit, Text: reason})
	s.mu.Unlock()
	s.log.Info("claude: session ended", "session", s.id, "host", s.hostID, "reason", reason)
	if s.conn != nil {
		s.conn.Close()
	}
}

// shutdown ends the process on purpose, recording why before the pump can
// write down the symptom instead.
func (s *Session) shutdown(reason string) {
	s.mu.Lock()
	if s.running && s.because == "" {
		s.because = reason
	}
	s.mu.Unlock()
	s.proc.Close()
	s.finish(reason)
}

// idleSince reports whether nobody has touched the session since cutoff and
// Claude is not in the middle of something.
func (s *Session) idleSince(cutoff time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.watchers > 0 {
		return false
	}
	if s.running && s.busy {
		return false
	}
	return s.lastSeen.Before(cutoff)
}

func exitReason(code int, stderr string) string {
	if code == 97 {
		return "the working directory does not exist on the host"
	}
	last := ""
	for _, l := range strings.Split(strings.TrimSpace(stderr), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			last = l
		}
	}
	if last != "" {
		if len(last) > 200 {
			last = last[:200] + "…"
		}
		return fmt.Sprintf("Claude exited with status %d: %s", code, last)
	}
	if code == 127 {
		return "claude is not installed on the host, or not on the PATH"
	}
	if code < 0 {
		return "the connection to the host was lost"
	}
	return fmt.Sprintf("Claude exited with status %d", code)
}

func firstLine(text string) string {
	if i := strings.IndexByte(text, '\n'); i >= 0 {
		text = text[:i]
	}
	if len(text) > 60 {
		text = text[:60] + "…"
	}
	return text
}

func newID() string {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b[:])
}

func sortByStart(s []*Session) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j].startedAt.Before(s[j-1].startedAt); j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}
