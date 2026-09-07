package shell

import (
	"errors"
	"io"
	"sync"

	"github.com/chinmay28/hostman/server/internal/sshx"
)

// Loopback is a terminal that never leaves the process: the test prints what
// the shell would have drawn, and reads back what was typed at it.
//
// It is exported rather than kept in a test file because the layers above this
// package need it too — the API handlers have a session to serve and no host to
// get one from. Everything it stands in for is proved against a real sshd in
// this package's integration tests; what it makes testable is everything else.
type Loopback struct {
	screen *io.PipeReader
	draw   *io.PipeWriter

	mu     sync.Mutex
	typed  []byte
	cols   int
	rows   int
	closed bool
	code   int

	// ended guards done, which exiting and closing may both reach.
	ended sync.Once
	done  chan struct{}
}

// AdoptLoopback opens a session on a terminal the caller drives, and registers
// it the way a real one would be.
func (m *Manager) AdoptLoopback(hostID int64, user string, cols, rows int) (*Session, *Loopback) {
	r, w := io.Pipe()
	term := &Loopback{screen: r, draw: w, cols: cols, rows: rows, done: make(chan struct{})}
	s := m.adopt(hostID, user, term, term, cols, rows)
	m.mu.Lock()
	m.sessions[s.id] = s
	m.mu.Unlock()
	return s, term
}

// Print puts bytes on the screen, the way the shell would.
func (l *Loopback) Print(s string) {
	// A closed terminal swallows what is drawn on it, exactly as a real one
	// does once the shell behind it is gone.
	_, _ = l.draw.Write([]byte(s))
}

// Exit ends the shell with a status, the way typing `exit` would.
func (l *Loopback) Exit(code int) {
	l.mu.Lock()
	l.code = code
	l.mu.Unlock()
	l.draw.Close()
	l.ended.Do(func() { close(l.done) })
}

// Keystrokes is everything typed at the shell so far.
func (l *Loopback) Keystrokes() []byte {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]byte(nil), l.typed...)
}

// Size is the window the shell was last told it had.
func (l *Loopback) Size() (int, int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.cols, l.rows
}

func (l *Loopback) Read(p []byte) (int, error) { return l.screen.Read(p) }

func (l *Loopback) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return 0, errors.New("the terminal is closed")
	}
	l.typed = append(l.typed, p...)
	return len(p), nil
}

func (l *Loopback) Resize(cols, rows int) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.cols, l.rows = cols, rows
	return nil
}

// Status blocks until the shell has ended and reports how, matching what a real
// terminal does.
func (l *Loopback) Status() int { <-l.done; return l.code }

func (l *Loopback) Close() error {
	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		return nil
	}
	l.closed = true
	l.mu.Unlock()
	l.draw.CloseWithError(sshx.ErrTerminalClosed)
	l.ended.Do(func() { close(l.done) })
	return nil
}
