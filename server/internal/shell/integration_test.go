package shell

import (
	"context"
	"io"
	"log/slog"
	"os/user"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chinmay28/hostman/server/internal/hosts"
	"github.com/chinmay28/hostman/server/internal/sshx"
	"github.com/chinmay28/hostman/server/internal/store"
	"github.com/chinmay28/hostman/server/internal/testutil"
)

// The bookkeeping is covered against a fake terminal elsewhere in this package.
// What is left is the SSH path: that a pty is granted, that a login shell comes
// up on it, that a command typed at it runs, and that a window change reaches
// it. These run against a throwaway sshd on localhost and skip where none can
// be started.

func sshManager(t *testing.T) (*Manager, *store.Host) {
	t.Helper()
	testutil.RequireSSHD(t)

	db, err := store.Open(filepath.Join(t.TempDir(), "hostman.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	id, err := sshx.EnsureIdentity(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	port := testutil.StartSSHD(t, id.AuthorizedKey())

	me, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	h, err := db.CreateHost(context.Background(), &store.Host{
		Name: "localhost", Address: "127.0.0.1", Port: port, Username: me.Username,
	})
	if err != nil {
		t.Fatal(err)
	}
	m := NewManager(hosts.NewService(db, id, nil), slog.New(slog.NewTextHandler(io.Discard, nil)))
	return m, h
}

// awaitScreen reads until what is on the screen contains want, so a test does
// not depend on which read a prompt or a line of output lands in.
func awaitScreen(t *testing.T, s *Session, want string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	var screen strings.Builder
	for from := int64(0); ; {
		c, err := s.Read(ctx, from)
		if err != nil {
			t.Fatalf("waiting for %q, saw %q: %v", want, screen.String(), err)
		}
		screen.Write(c.Data)
		if strings.Contains(screen.String(), want) {
			return screen.String()
		}
		if c.Done {
			t.Fatalf("the shell ended (%s) before %q appeared; saw %q", c.Exit, want, screen.String())
		}
		from = c.Next
	}
}

func TestShellRunsACommandOverSSH(t *testing.T) {
	m, h := sshManager(t)

	s, err := m.Open(context.Background(), h, 100, 30)
	if err != nil {
		t.Fatalf("open a shell: %v", err)
	}
	defer m.Close(s.ID())

	// A marker rather than a prompt: what a login shell prints before it is
	// ready is the host's business, and none of it is ours to predict.
	if err := s.Write([]byte("echo dep''loyer-was-here\n")); err != nil {
		t.Fatalf("type at the shell: %v", err)
	}
	awaitScreen(t, s, "hostman-was-here")

	if info := s.Info(); !info.Running || info.Offset == 0 {
		t.Fatalf("session says running=%v offset=%d", info.Running, info.Offset)
	}
}

// The pty is real, which is the difference between this and running a command:
// the shell can see how wide its window is, and see it change.
func TestShellHasATerminalOfTheRightSize(t *testing.T) {
	m, h := sshManager(t)

	s, err := m.Open(context.Background(), h, 100, 30)
	if err != nil {
		t.Fatalf("open a shell: %v", err)
	}
	defer m.Close(s.ID())

	if err := s.Write([]byte("tty >/dev/null && echo has''-a-tty\n")); err != nil {
		t.Fatalf("type at the shell: %v", err)
	}
	awaitScreen(t, s, "has-a-tty")

	if err := s.Resize(132, 43); err != nil {
		t.Fatalf("resize: %v", err)
	}
	// The shell reads its own window rather than being told, so this only
	// passes if the window change actually reached the pty.
	if err := s.Write([]byte("echo \"w=$(tput cols) h=$(tput lines)\"\n")); err != nil {
		t.Fatalf("type at the shell: %v", err)
	}
	awaitScreen(t, s, "w=132 h=43")
}

// Typing `exit` ends the shell, and the session says so rather than hanging.
func TestShellEndsWhenTheShellExits(t *testing.T) {
	m, h := sshManager(t)

	s, err := m.Open(context.Background(), h, 80, 24)
	if err != nil {
		t.Fatalf("open a shell: %v", err)
	}
	defer m.Close(s.ID())

	if err := s.Write([]byte("exit 7\n")); err != nil {
		t.Fatalf("type at the shell: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	for from := int64(0); ; {
		c, err := s.Read(ctx, from)
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		if c.Done {
			if !strings.Contains(c.Exit, "7") {
				t.Fatalf("exit reason = %q, want it to name status 7", c.Exit)
			}
			return
		}
		from = c.Next
	}
}

// A host is allowed only so many shells at once, so a client stuck reconnecting
// cannot open SSH connections until the host refuses them.
func TestOpenRefusesMoreThanAHostIsAllowed(t *testing.T) {
	m, h := sshManager(t)

	for i := 0; i < PerHost; i++ {
		s, err := m.Open(context.Background(), h, 80, 24)
		if err != nil {
			t.Fatalf("open shell %d: %v", i+1, err)
		}
		defer m.Close(s.ID())
	}
	if _, err := m.Open(context.Background(), h, 80, 24); err != ErrTooMany {
		t.Fatalf("the %dth shell was allowed: %v", PerHost+1, err)
	}
}
