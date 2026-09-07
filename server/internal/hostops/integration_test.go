package hostops

import (
	"context"
	"os"
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

// The scripts are covered against a real filesystem elsewhere in this package.
// What is left to prove is the SSH path around them: that a base64 body survives
// the channel, that stdin reaches the far side, and that an exit code comes back
// as one. These run against a throwaway sshd on localhost and skip where none
// can be started.

func sshEnv(t *testing.T) (*Service, *store.Host) {
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
	return NewService(hosts.NewService(db, id, nil)), h
}

func TestFilesOverSSH(t *testing.T) {
	svc, host := sshEnv(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	dir := t.TempDir()
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "hostman.conf")

	// Content chosen to break anything that treats the body as shell text.
	const content = "# written from a phone\nname = 'it''s fine'\npath = \"$HOME/x\"\n☕\n"
	if err := svc.Write(ctx, host, path, content); err != nil {
		t.Fatalf("Write: %v", err)
	}
	onDisk, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(onDisk) != content {
		t.Fatalf("on disk = %q, want %q", onDisk, content)
	}

	file, err := svc.Read(ctx, host, path)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if file.Content != content {
		t.Errorf("read back = %q, want %q", file.Content, content)
	}
	if file.Binary || file.Truncated || file.Size != int64(len(content)) {
		t.Errorf("file = %+v, want %d bytes of plain text", file, len(content))
	}
	if file.AsUser == "" {
		t.Error("asUser is empty, want whoever the commands ran as")
	}

	listing, err := svc.List(ctx, host, dir)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(listing.Entries) != 1 || listing.Entries[0].Name != "hostman.conf" {
		t.Fatalf("entries = %+v, want just the one file", listing.Entries)
	}
	if listing.Path != dir {
		t.Errorf("path = %q, want %q", listing.Path, dir)
	}

	usage, err := svc.Usage(ctx, host, dir)
	if err != nil {
		t.Fatalf("Usage: %v", err)
	}
	if usage.Files != 1 || usage.Dirs != 0 || usage.Bytes <= 0 {
		t.Errorf("usage = %+v, want one file, no folders, some disk", usage)
	}
	if _, err := svc.Usage(ctx, host, path); err == nil {
		t.Error("measuring a file succeeded, want it refused: the listing already has its size")
	}

	// Changing the mode, first on the file and then on the directory with
	// everything under it, through the same connection path.
	mode, err := svc.Chmod(ctx, host, path, "600", false)
	if err != nil {
		t.Fatalf("Chmod: %v", err)
	}
	if mode != "600" {
		t.Errorf("mode = %q, want the 600 the host read back", mode)
	}
	if _, err := svc.Chmod(ctx, host, dir, "777", true); err != nil {
		t.Fatalf("recursive Chmod: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o777 {
		t.Errorf("the file inside is %o, want 777 — recursive means the contents too", got)
	}
	if _, err := svc.Chmod(ctx, host, path, "u+x", false); err == nil {
		t.Error("a symbolic mode was accepted, want it refused before anything is sent")
	}

	// Renaming, then deleting through the same connection path.
	moved := filepath.Join(dir, "renamed.conf")
	if err := svc.Rename(ctx, host, path, moved); err != nil {
		t.Fatalf("Rename: %v", err)
	}
	if err := svc.Remove(ctx, host, moved, false); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	listing, err = svc.List(ctx, host, dir)
	if err != nil {
		t.Fatalf("List after delete: %v", err)
	}
	if len(listing.Entries) != 0 {
		t.Errorf("entries = %+v, want an empty directory", listing.Entries)
	}
}

// The boot report is the largest thing this package sends and receives: a
// multi-argument command carrying a shell script twice, and two base64 chunks of
// log coming back. The scripts and the verdict are tested elsewhere; what this
// proves is that the whole of it survives a real channel.
func TestLastBootOverSSH(t *testing.T) {
	svc, host := sshEnv(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	report, err := svc.LastBoot(ctx, host)
	if err != nil {
		t.Fatalf("LastBoot: %v", err)
	}
	// What is true of any machine, whatever it remembers about restarting: it
	// is up, it has been up for some time, and something ran the script.
	if report.UptimeS <= 0 || report.BootedAt.IsZero() {
		t.Errorf("uptime = %d, bootedAt = %v, want both read from /proc", report.UptimeS, report.BootedAt)
	}
	if report.AsUser == "" {
		t.Error("asUser is empty, want the account the script ran as")
	}
	if report.Kernel == "" {
		t.Error("kernel is empty, want what uname reported")
	}
	// A verdict is always reached, even where the verdict is that there is
	// nothing to go on — an empty cause would mean diagnose never ran.
	if report.Cause == "" || report.Confidence == "" || report.Headline == "" {
		t.Errorf("verdict = %+v, want a cause, a confidence and a headline", report)
	}
	if report.Signs == nil || report.Restarts == nil || report.Reasons == nil {
		t.Error("the evidence lists should be empty, never absent — the UI maps over them")
	}
}

// An empty path means the SSH user's home, which only the host can answer.
func TestListHomeOverSSH(t *testing.T) {
	svc, host := sshEnv(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	listing, err := svc.List(ctx, host, "")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	me, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	if listing.Path != me.HomeDir {
		t.Errorf("path = %q, want the SSH user's home %q", listing.Path, me.HomeDir)
	}
}

// A failure on the host has to arrive as an error, not as an empty success.
func TestFileErrorsOverSSH(t *testing.T) {
	svc, host := sshEnv(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	if _, err := svc.Read(ctx, host, "/no/such/file"); err == nil {
		t.Error("reading a missing file succeeded")
	} else if !strings.Contains(err.Error(), "no such file") {
		t.Errorf("err = %v, want the host's own words", err)
	}
	if _, err := svc.List(ctx, host, "/no/such/directory"); err == nil {
		t.Error("listing a missing directory succeeded")
	}

	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "inner"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := svc.Remove(ctx, host, dir, false); err == nil {
		t.Error("removing a directory with contents succeeded, want it refused")
	}
	if err := svc.Remove(ctx, host, dir, true); err != nil {
		t.Errorf("recursive remove: %v", err)
	}
}
