package claude

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"sync"
	"time"

	"github.com/chinmay28/hostman/server/internal/sshx"
)

// Loopback is a Claude Code process that never leaves the process: the test
// prints what the CLI would have written, and reads back what was sent to it.
//
// It is exported rather than kept in a test file because the API handlers
// need it too — they have a session to serve and no host to get one from.
type Loopback struct {
	stdout *io.PipeReader
	emit   *io.PipeWriter
	lines  chan string

	mu     sync.Mutex
	closed bool
	code   int
	stderr string

	ended sync.Once
	done  chan struct{}
}

// AdoptLoopback opens a session on a process the caller drives, and registers
// it the way a real one would be.
func (m *Manager) AdoptLoopback(hostID int64, user string, opts Options) (*Session, *Loopback) {
	if opts.Mode == "" {
		opts.Mode = "default"
	}
	r, w := io.Pipe()
	p := &Loopback{stdout: r, emit: w, lines: make(chan string, 64), done: make(chan struct{})}
	s := m.adopt(hostID, user, nil, p, opts)
	m.mu.Lock()
	m.sessions[s.id] = s
	m.mu.Unlock()
	return s, p
}

// Emit writes one line to stdout, the way the CLI would.
func (l *Loopback) Emit(line string) {
	_, _ = l.emit.Write([]byte(line + "\n"))
}

// Next is the next line sent to the process's stdin, or "" if none comes
// within the wait.
func (l *Loopback) Next(wait time.Duration) string {
	select {
	case s := <-l.lines:
		return s
	case <-time.After(wait):
		return ""
	}
}

// Exit ends the process with a status and whatever it last said on stderr.
func (l *Loopback) Exit(code int, stderr string) {
	l.mu.Lock()
	l.code, l.stderr = code, stderr
	l.mu.Unlock()
	l.emit.Close()
	l.ended.Do(func() { close(l.done) })
}

func (l *Loopback) Read(p []byte) (int, error) { return l.stdout.Read(p) }

func (l *Loopback) Write(p []byte) (int, error) {
	l.mu.Lock()
	closed := l.closed
	l.mu.Unlock()
	if closed {
		return 0, errors.New("the process is closed")
	}
	sc := bufio.NewScanner(bytes.NewReader(p))
	sc.Buffer(make([]byte, 64<<10), maxLine)
	for sc.Scan() {
		l.lines <- sc.Text()
	}
	return len(p), nil
}

func (l *Loopback) Status() int { <-l.done; return l.code }

func (l *Loopback) Stderr() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.stderr
}

func (l *Loopback) Close() error {
	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		return nil
	}
	l.closed = true
	l.mu.Unlock()
	l.emit.CloseWithError(sshx.ErrTerminalClosed)
	l.ended.Do(func() { close(l.done) })
	return nil
}
