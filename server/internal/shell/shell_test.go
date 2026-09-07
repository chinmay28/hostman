package shell

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"
)

// What is worth proving here is the part a real sshd would not exercise: that a
// client which was away gets back exactly the bytes it missed, that one which
// was away too long is told so rather than handed a torn screen, that a reader
// wakes on output instead of polling for it, and that a shell nobody is
// watching is eventually let go. All of that is HostMan's own bookkeeping, so
// these run against a terminal that never leaves the process. The SSH path
// underneath is covered separately, against a real sshd.

func testManager(t *testing.T) (*Manager, *time.Time) {
	t.Helper()
	clock := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	m := NewManager(nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	m.now = func() time.Time { return clock }
	return m, &clock
}

// adopt gives the test a session over a terminal it drives itself.
func adopt(t *testing.T, m *Manager) (*Session, *Loopback) {
	t.Helper()
	s, term := m.AdoptLoopback(1, "pi", 80, 24)
	t.Cleanup(func() { term.Close() })
	return s, term
}

// fakeConn stands in for the SSH connection under a terminal. Closing it is the
// last thing a finishing session does, so a test waits for it rather than
// reading a flag the moment a read comes back.
type fakeConn struct {
	shut chan struct{}
	once sync.Once
}

func (c *fakeConn) Close() error {
	c.once.Do(func() { close(c.shut) })
	return nil
}

// wait reports whether the connection was given back within a moment.
func (c *fakeConn) wait() bool {
	select {
	case <-c.shut:
		return true
	case <-time.After(2 * time.Second):
		return false
	}
}

// read waits for output, failing rather than hanging if none comes.
func read(t *testing.T, s *Session, from int64) Chunk {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	c, err := s.Read(ctx, from)
	if err != nil {
		t.Fatalf("read from %d: %v", from, err)
	}
	return c
}

func TestReadFollowsTheScreen(t *testing.T) {
	m, _ := testManager(t)
	s, term := adopt(t, m)

	term.Print("pi@raspberrypi:~$ ")
	first := read(t, s, 0)
	if got := string(first.Data); got != "pi@raspberrypi:~$ " {
		t.Fatalf("first read = %q", got)
	}

	// The second read starts where the first stopped and sees only what is new.
	term.Print("uptime\r\n")
	second := read(t, s, first.Next)
	if got := string(second.Data); got != "uptime\r\n" {
		t.Fatalf("second read = %q", got)
	}
	if second.From != first.Next {
		t.Fatalf("second chunk starts at %d, want %d", second.From, first.Next)
	}
}

// A read with nothing to show waits for output rather than returning empty,
// which is what keeps a stream from becoming a poll.
func TestReadWaitsForOutput(t *testing.T) {
	m, _ := testManager(t)
	s, term := adopt(t, m)

	done := make(chan Chunk, 1)
	go func() { done <- read(t, s, 0) }()

	select {
	case c := <-done:
		t.Fatalf("read returned %q before anything was drawn", c.Data)
	case <-time.After(50 * time.Millisecond):
	}

	term.Print("ok")
	select {
	case c := <-done:
		if string(c.Data) != "ok" {
			t.Fatalf("read = %q", c.Data)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("read did not wake when the screen changed")
	}
}

// The phone locked, the shell kept printing, and it printed more than is kept.
// The client is moved forward to the oldest byte still held rather than being
// handed a chunk that starts in the middle of nothing.
func TestReadSkipsPastWhatWasDropped(t *testing.T) {
	m, _ := testManager(t)
	s, term := adopt(t, m)

	// Two full buffers of output, written in pieces so the trim happens the way
	// it would in life.
	line := strings.Repeat("x", 4<<10)
	for written := 0; written < Scrollback*2; written += len(line) {
		term.Print(line)
	}
	// Drain up to the end so the writer is not blocked on the pipe.
	c := read(t, s, 0)
	for !c.Done && c.Next < int64(Scrollback*2) {
		c = read(t, s, c.Next)
	}

	old := read(t, s, 0)
	if old.From == 0 {
		t.Fatal("a read from the beginning should have been moved forward")
	}
	if int64(len(old.Data)) > Scrollback {
		t.Fatalf("kept %d bytes, more than the %d scrollback", len(old.Data), Scrollback)
	}
	if old.From+int64(len(old.Data)) != old.Next {
		t.Fatalf("chunk from %d of %d bytes does not reach %d", old.From, len(old.Data), old.Next)
	}
}

func TestWriteReachesTheShellAndResizeReachesThePTY(t *testing.T) {
	m, _ := testManager(t)
	s, term := adopt(t, m)

	if err := s.Write([]byte("ls -l\r")); err != nil {
		t.Fatalf("write: %v", err)
	}
	if got := string(term.Keystrokes()); got != "ls -l\r" {
		t.Fatalf("the shell got %q", got)
	}
	// Ctrl-C is a byte, not a signal request: it goes down the same pipe.
	if err := s.Write([]byte{0x03}); err != nil {
		t.Fatalf("write ctrl-c: %v", err)
	}
	if got := string(term.Keystrokes()); got != "ls -l\r\x03" {
		t.Fatalf("the shell got %q", got)
	}

	if err := s.Resize(120, 40); err != nil {
		t.Fatalf("resize: %v", err)
	}
	if cols, rows := term.Size(); cols != 120 || rows != 40 {
		t.Fatalf("pty is %dx%d, want 120x40", cols, rows)
	}
	if info := s.Info(); info.Cols != 120 || info.Rows != 40 {
		t.Fatalf("session says %dx%d", info.Cols, info.Rows)
	}
}

func TestSizeIsClamped(t *testing.T) {
	m, _ := testManager(t)
	s, term := adopt(t, m)

	if err := s.Resize(0, 0); err != nil {
		t.Fatalf("resize: %v", err)
	}
	if cols, rows := term.Size(); cols != minCols || rows != minRows {
		t.Fatalf("a zero size became %dx%d, want %dx%d", cols, rows, minCols, minRows)
	}
	if err := s.Resize(1<<20, 1<<20); err != nil {
		t.Fatalf("resize: %v", err)
	}
	if cols, rows := term.Size(); cols != maxCols || rows != maxRows {
		t.Fatalf("an absurd size became %dx%d, want %dx%d", cols, rows, maxCols, maxRows)
	}
}

// A shell that exits leaves its last words readable and says how it ended,
// rather than the session simply disappearing under whoever was watching.
func TestExitEndsTheReadWithAReason(t *testing.T) {
	m, _ := testManager(t)
	s, term := adopt(t, m)

	term.Print("logout\r\n")
	c := read(t, s, 0)
	if string(c.Data) != "logout\r\n" {
		t.Fatalf("read = %q", c.Data)
	}

	term.Exit(3)
	final := read(t, s, c.Next)
	if !final.Done {
		t.Fatal("the read after the shell exited should be the last one")
	}
	if !strings.Contains(final.Exit, "status 3") {
		t.Fatalf("exit reason = %q, want it to name the status", final.Exit)
	}
	if s.Running() {
		t.Fatal("the session still says it is running")
	}
	if err := s.Write([]byte("x")); !errors.Is(err, ErrClosed) {
		t.Fatalf("writing to an exited shell = %v, want ErrClosed", err)
	}
}

// The connection under an exited shell is given back, rather than being held
// until the session is reaped.
func TestExitReleasesTheConnection(t *testing.T) {
	m, _ := testManager(t)
	// The one case that needs the connection and the terminal to be different
	// things, so it reaches past AdoptLoopback for a session of its own.
	_, term := m.AdoptLoopback(1, "pi", 80, 24)
	conn := &fakeConn{shut: make(chan struct{})}
	s := m.adopt(1, "pi", conn, term, 80, 24)

	term.Exit(0)
	read(t, s, 0)
	if !conn.wait() {
		t.Fatal("the SSH connection was left open after the shell exited")
	}
}

// A session nobody is watching is let go; one somebody is watching is not,
// however long it has been quiet.
func TestReapClosesIdleSessionsOnly(t *testing.T) {
	m, clock := testManager(t)
	idle, _ := adopt(t, m)
	watched, _ := adopt(t, m)
	defer watched.Attach()()

	*clock = clock.Add(2 * Idle)
	m.reap()

	if _, err := m.Get(idle.ID()); !errors.Is(err, ErrNotFound) {
		t.Fatalf("the idle session survived: %v", err)
	}
	if !strings.Contains(idle.Info().Exit, "left alone") {
		t.Fatalf("reaped session's exit = %q", idle.Info().Exit)
	}
	if _, err := m.Get(watched.ID()); err != nil {
		t.Fatalf("the watched session was reaped: %v", err)
	}
}

// Detaching starts the idle clock again — a client that read for an hour and
// then closed the tab should not buy the session another hour.
func TestDetachingStartsTheIdleClock(t *testing.T) {
	m, clock := testManager(t)
	s, _ := adopt(t, m)

	detach := s.Attach()
	*clock = clock.Add(2 * Idle)
	m.reap()
	if _, err := m.Get(s.ID()); err != nil {
		t.Fatalf("a watched session was reaped: %v", err)
	}

	detach()
	m.reap()
	if _, err := m.Get(s.ID()); err != nil {
		t.Fatalf("a session detached this instant was reaped: %v", err)
	}
	*clock = clock.Add(2 * Idle)
	m.reap()
	if _, err := m.Get(s.ID()); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a session left alone after detaching survived: %v", err)
	}
}

// Typing at a shell counts as being there, which is what keeps a terminal that
// is streaming through a proxy HostMan cannot see from being reaped mid-command.
func TestWritingKeepsASessionAlive(t *testing.T) {
	m, clock := testManager(t)
	s, _ := adopt(t, m)

	*clock = clock.Add(Idle - time.Minute)
	if err := s.Write([]byte("x")); err != nil {
		t.Fatalf("write: %v", err)
	}
	*clock = clock.Add(2 * time.Minute)
	m.reap()
	if _, err := m.Get(s.ID()); err != nil {
		t.Fatalf("a session typed at a moment ago was reaped: %v", err)
	}
}

func TestCloseHostEndsEveryShellOnIt(t *testing.T) {
	m, _ := testManager(t)
	mine, _ := adopt(t, m)
	also, _ := adopt(t, m)

	elsewhere, other := m.AdoptLoopback(2, "pi", 80, 24)
	defer other.Close()

	m.CloseHost(1)
	for _, s := range []*Session{mine, also} {
		if _, err := m.Get(s.ID()); !errors.Is(err, ErrNotFound) {
			t.Fatalf("a shell on the removed host survived: %v", err)
		}
		if s.Running() {
			t.Fatal("a shell on the removed host is still running")
		}
	}
	if _, err := m.Get(elsewhere.ID()); err != nil {
		t.Fatalf("a shell on another host was closed: %v", err)
	}
}

func TestForHostListsOldestFirst(t *testing.T) {
	m, clock := testManager(t)
	first, _ := adopt(t, m)
	*clock = clock.Add(time.Minute)
	second, _ := adopt(t, m)
	*clock = clock.Add(time.Minute)
	third, _ := adopt(t, m)

	got := m.ForHost(1)
	if len(got) != 3 {
		t.Fatalf("listed %d shells, want 3", len(got))
	}
	for i, want := range []*Session{first, second, third} {
		if got[i].ID() != want.ID() {
			t.Fatalf("shell %d is %s, want %s", i, got[i].ID(), want.ID())
		}
	}
}

func TestWriteRefusesMoreThanAShellWillTake(t *testing.T) {
	m, _ := testManager(t)
	s, _ := adopt(t, m)

	if err := s.Write(make([]byte, MaxInput+1)); err == nil {
		t.Fatal("an oversized write was accepted")
	}
	if err := s.Write(make([]byte, MaxInput)); err != nil {
		t.Fatalf("a write at the limit was refused: %v", err)
	}
}

// Two ids are never the same, which is the whole security of an opaque handle.
func TestIDsAreDistinct(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 1000; i++ {
		id := newID()
		if seen[id] {
			t.Fatalf("%s came up twice", id)
		}
		seen[id] = true
	}
}

// An offset past the end is treated as the end rather than as a wait for output
// that has already happened.
func TestReadFromPastTheEndWaitsForWhatIsNext(t *testing.T) {
	m, _ := testManager(t)
	s, term := adopt(t, m)
	term.Print("first")
	// Read it back first: printing only hands the bytes to the pump, and an
	// offset is not past the end until the end is where it is going to be.
	first := read(t, s, 0)

	done := make(chan Chunk, 1)
	go func() { done <- read(t, s, first.Next+1<<20) }()
	select {
	case c := <-done:
		t.Fatalf("a read from past the end returned %q", c.Data)
	case <-time.After(50 * time.Millisecond):
	}

	term.Print("second")
	select {
	case c := <-done:
		if string(c.Data) != "second" {
			t.Fatalf("read = %q, want only what came after", c.Data)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("a read from past the end never woke")
	}
}
