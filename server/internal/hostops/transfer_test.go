package hostops

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chinmay28/hostman/server/internal/sshx"
	"github.com/chinmay28/hostman/server/internal/store"
)

// The jobs are tested the way the scripts are: by running what would be sent
// over SSH through a local shell, with the manager's one seam — the call that
// reaches a host — pointed at /bin/sh. The bookkeeping, the scripts and the
// pipeline between them are all real; only sshd is missing.

type noForwarder struct{ refuseConnector }

func (noForwarder) Identity() *sshx.Identity { return nil }

// localTransfers runs every job through a local shell, with bins ahead on
// the PATH and env on top of the test's own. sudo is shadowed by one that
// declines, so the scripts run as the test does — a test proves what a script
// does, not what sudo does with it — and the stand-ins in bins are found.
func localTransfers(t *testing.T, bins []string, env ...string) *Transfers {
	t.Helper()
	nosudo := t.TempDir()
	if err := os.WriteFile(filepath.Join(nosudo, "sudo"), []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	path := strings.Join(append(append([]string{}, bins...), nosudo, os.Getenv("PATH")), ":")
	env = append(env, "PATH="+path)
	tr := NewTransfers(noForwarder{refuseConnector{t}}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	tr.run = func(ctx context.Context, _ *store.Host, cmd, stdin string, out io.Writer, _ bool) (int, error) {
		run := exec.CommandContext(ctx, "/bin/sh", "-c", cmd)
		run.Stdin = strings.NewReader(stdin)
		run.Stdout = out
		run.Stderr = out
		run.Env = append(os.Environ(), env...)
		err := run.Run()
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			return exit.ExitCode(), nil
		}
		if err != nil {
			return -1, err
		}
		return 0, nil
	}
	return tr
}

// settle waits for a job to leave the running state.
func settle(t *testing.T, tr *Transfers, id string) *Job {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		job, err := tr.Get(id)
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if job.State != StateRunning {
			return job
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("the job never finished")
	return nil
}

// tree makes a folder with a file, a nested file, a mode worth preserving
// and a symlink, so a copy that lost any of them shows.
func tree(t *testing.T) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "Umbrel")
	if err := os.MkdirAll(filepath.Join(root, "app-data", "nested"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "docker-compose.yml"), []byte("services: {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "app-data", "nested", "run.sh"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("docker-compose.yml", filepath.Join(root, "compose-link")); err != nil {
		t.Fatal(err)
	}
	return root
}

// sameTree checks that a copy of tree() has everything tree() put there.
func sameTree(t *testing.T, root string) {
	t.Helper()
	if got, err := os.ReadFile(filepath.Join(root, "docker-compose.yml")); err != nil || string(got) != "services: {}\n" {
		t.Errorf("docker-compose.yml = %q, %v", got, err)
	}
	st, err := os.Stat(filepath.Join(root, "app-data", "nested", "run.sh"))
	if err != nil {
		t.Fatalf("nested file: %v", err)
	}
	if st.Mode().Perm() != 0o755 {
		t.Errorf("run.sh mode = %o, want the executable bit kept", st.Mode().Perm())
	}
	if target, err := os.Readlink(filepath.Join(root, "compose-link")); err != nil || target != "docker-compose.yml" {
		t.Errorf("symlink = %q, %v; want it carried as a link", target, err)
	}
}

func TestCopyJobDuplicatesAFolder(t *testing.T) {
	tr := localTransfers(t, nil)
	src := tree(t)
	dir := t.TempDir()
	h := &store.Host{ID: 1, Name: "pi"}

	job, err := tr.Copy(context.Background(), h, src, dir)
	if err != nil {
		t.Fatalf("Copy: %v", err)
	}
	if job.Kind != KindCopy || job.State != StateRunning || job.Path != src || job.To != dir || job.HostID != 1 {
		t.Errorf("started job = %+v", job)
	}
	done := settle(t, tr, job.ID)
	if done.State != StateDone {
		t.Fatalf("state = %s (%s)\n%s", done.State, done.Error, done.Log)
	}
	sameTree(t, filepath.Join(dir, "Umbrel"))
	sameTree(t, src) // still there: it was a copy
	if done.FinishedAt == nil {
		t.Error("a finished job should say when")
	}
}

func TestMoveJobMovesAFolder(t *testing.T) {
	tr := localTransfers(t, nil)
	src := tree(t)
	dir := t.TempDir()
	h := &store.Host{ID: 1, Name: "pi"}

	job, err := tr.Move(context.Background(), h, src, dir)
	if err != nil {
		t.Fatalf("Move: %v", err)
	}
	done := settle(t, tr, job.ID)
	if done.State != StateDone {
		t.Fatalf("state = %s (%s)\n%s", done.State, done.Error, done.Log)
	}
	sameTree(t, filepath.Join(dir, "Umbrel"))
	if _, err := os.Lstat(src); !os.IsNotExist(err) {
		t.Errorf("the source is still there after a move: %v", err)
	}
}

// Nothing here overwrites. A name already taken at the destination is a
// failure that names it, and the thing in the way is left alone.
func TestCopyAndMoveRefuseToOverwrite(t *testing.T) {
	tr := localTransfers(t, nil)
	h := &store.Host{ID: 1, Name: "pi"}
	for _, kind := range []string{KindCopy, KindMove} {
		t.Run(kind, func(t *testing.T) {
			src := tree(t)
			dir := t.TempDir()
			taken := filepath.Join(dir, "Umbrel")
			if err := os.WriteFile(taken, []byte("mine"), 0o644); err != nil {
				t.Fatal(err)
			}
			start := tr.Copy
			if kind == KindMove {
				start = tr.Move
			}
			job, err := start(context.Background(), h, src, dir)
			if err != nil {
				t.Fatal(err)
			}
			done := settle(t, tr, job.ID)
			if done.State != StateFailed || !strings.Contains(done.Error, "already exists") {
				t.Errorf("state = %s, error = %q; want a failure naming the clash", done.State, done.Error)
			}
			if got, _ := os.ReadFile(taken); string(got) != "mine" {
				t.Errorf("the file in the way was touched: %q", got)
			}
			sameTree(t, src)
		})
	}
}

// A copy that fails part way is taken back off the disk: half a folder that
// reads as the whole one is worse than no folder.
func TestCopyJobRemovesAPartialCopy(t *testing.T) {
	bin := t.TempDir()
	fakeCp := "#!/bin/sh\n# a cp that gives up half way\nmkdir -p \"$3\" && : > \"$3/half\"\nexit 1\n"
	if err := os.WriteFile(filepath.Join(bin, "cp"), []byte(fakeCp), 0o755); err != nil {
		t.Fatal(err)
	}
	tr := localTransfers(t, []string{bin})
	src := tree(t)
	dir := t.TempDir()

	job, err := tr.Copy(context.Background(), &store.Host{ID: 1}, src, dir)
	if err != nil {
		t.Fatal(err)
	}
	done := settle(t, tr, job.ID)
	if done.State != StateFailed {
		t.Fatalf("state = %s, want failed", done.State)
	}
	if _, err := os.Stat(filepath.Join(dir, "Umbrel")); !os.IsNotExist(err) {
		t.Errorf("the partial copy was left behind: %v", err)
	}
	if !strings.Contains(done.Log, "removed the partial copy") {
		t.Errorf("the log should say the partial copy went:\n%s", done.Log)
	}
}

// The checks that need no host are made before a job exists: a folder into
// itself, a copy onto itself, the root, a relative path.
func TestCopyAndMoveRefuseBeforeStarting(t *testing.T) {
	tr := localTransfers(t, nil)
	h := &store.Host{ID: 1}
	cases := []struct{ name, from, dir, want string }{
		{"into itself", "/media/Elements/Umbrel", "/media/Elements/Umbrel/app-data", "inside itself"},
		{"onto itself", "/media/Elements/Umbrel", "/media/Elements/Umbrel", "inside itself"},
		{"where it already is", "/media/Elements/Umbrel", "/media/Elements", "already in"},
		{"the root", "/", "/tmp", "refusing"},
		{"a relative source", "Umbrel", "/tmp", "absolute"},
		{"a relative destination", "/media/Elements/Umbrel", "tmp", "absolute"},
		{"no destination", "/media/Elements/Umbrel", "", "required"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for _, start := range []func(context.Context, *store.Host, string, string) (*Job, error){tr.Copy, tr.Move} {
				_, err := start(context.Background(), h, tc.from, tc.dir)
				if err == nil || !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), tc.want) {
					t.Errorf("err = %v, want an invalid request mentioning %q", err, tc.want)
				}
			}
		})
	}
	if jobs := tr.ForHost(1); len(jobs) != 0 {
		t.Errorf("%d jobs were started for requests that were refused", len(jobs))
	}
}

// A path that looks like a flag or a word to the shell goes through intact.
func TestCopyJobSurvivesAwkwardNames(t *testing.T) {
	tr := localTransfers(t, nil)
	dir := t.TempDir()
	src := filepath.Join(dir, "-it's $HOME; here")
	if err := os.WriteFile(src, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(dir, "elsewhere")
	if err := os.Mkdir(dest, 0o755); err != nil {
		t.Fatal(err)
	}
	job, err := tr.Copy(context.Background(), &store.Host{ID: 1}, src, dest)
	if err != nil {
		t.Fatal(err)
	}
	done := settle(t, tr, job.ID)
	if done.State != StateDone {
		t.Fatalf("state = %s (%s)\n%s", done.State, done.Error, done.Log)
	}
	if got, err := os.ReadFile(filepath.Join(dest, "-it's $HOME; here")); err != nil || string(got) != "x" {
		t.Errorf("copy = %q, %v", got, err)
	}
}

// fakeSSH stands in for the ssh client on the source host. It writes what it
// was asked down, and runs the remote command locally with the same stdin,
// so the whole pipeline — tar, the far side's script, tar again — runs for
// real with only the network missing.
func fakeSSH(t *testing.T) (bin, record string) {
	t.Helper()
	bin = t.TempDir()
	record = filepath.Join(t.TempDir(), "ssh-args")
	script := `#!/bin/sh
: > "$HOSTMAN_TEST_RECORD"
for a in "$@"; do
  printf '%s\n' "$a" >> "$HOSTMAN_TEST_RECORD"
  case "$a" in UserKnownHostsFile=*) cp "${a#UserKnownHostsFile=}" "$HOSTMAN_TEST_RECORD.known";; esac
done
eval "cmd=\${$#}"
exec sh -c "$cmd"
`
	if err := os.WriteFile(filepath.Join(bin, "ssh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin, record
}

// agentSocket makes a socket the send script can find where the forwarded
// agent would be.
func agentSocket(t *testing.T) string {
	t.Helper()
	sock := filepath.Join(t.TempDir(), "agent.sock")
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	return sock
}

func TestSendJobCarriesAFolderToAnotherHost(t *testing.T) {
	bin, record := fakeSSH(t)
	sock := agentSocket(t)
	tr := localTransfers(t, []string{bin}, "HOSTMAN_TEST_RECORD="+record, "SSH_AUTH_SOCK="+sock)
	src := tree(t)
	dir := t.TempDir()
	from := &store.Host{ID: 1, Name: "pi"}
	to := &store.Host{ID: 2, Name: "nas", Address: "nas.local", Port: 2222, Username: "chinmay", HostKey: "ssh-ed25519 AAAAC3fake"}

	job, err := tr.Send(context.Background(), from, src, to, dir)
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if job.Kind != KindSend || job.DestHostID != 2 || job.DestHost != "nas" || job.HostID != 1 {
		t.Errorf("started job = %+v", job)
	}
	done := settle(t, tr, job.ID)
	if done.State != StateDone {
		t.Fatalf("state = %s (%s)\n%s", done.State, done.Error, done.Log)
	}
	sameTree(t, filepath.Join(dir, "Umbrel"))
	sameTree(t, src)

	args, err := os.ReadFile(record)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"-p\n2222\n", "chinmay@nas.local\n", "StrictHostKeyChecking=yes\n", "BatchMode=yes\n", "GlobalKnownHostsFile=/dev/null\n"} {
		if !strings.Contains(string(args), want) {
			t.Errorf("ssh was not told %q:\n%s", strings.TrimSpace(want), args)
		}
	}
	known, err := os.ReadFile(record + ".known")
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(known)); got != "[nas.local]:2222 ssh-ed25519 AAAAC3fake" {
		t.Errorf("known_hosts handed to ssh = %q, want the pinned key on its port", got)
	}
	// Both hosts see the job.
	if len(tr.ForHost(1)) != 1 || len(tr.ForHost(2)) != 1 || len(tr.ForHost(3)) != 0 {
		t.Error("a send should be listed under its source and its destination, and nowhere else")
	}
}

// What the far side refuses comes back as the failure, in its words.
func TestSendJobReportsTheFarSide(t *testing.T) {
	bin, record := fakeSSH(t)
	sock := agentSocket(t)
	tr := localTransfers(t, []string{bin}, "HOSTMAN_TEST_RECORD="+record, "SSH_AUTH_SOCK="+sock)
	src := tree(t)
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "Umbrel"), 0o755); err != nil {
		t.Fatal(err)
	}
	to := &store.Host{ID: 2, Name: "nas", Address: "nas.local", Username: "chinmay", HostKey: "ssh-ed25519 AAAAC3fake"}

	job, err := tr.Send(context.Background(), &store.Host{ID: 1}, src, to, dir)
	if err != nil {
		t.Fatal(err)
	}
	done := settle(t, tr, job.ID)
	if done.State != StateFailed || !strings.Contains(done.Error, "already exists on the destination") {
		t.Errorf("state = %s, error = %q", done.State, done.Error)
	}
	known, _ := os.ReadFile(record + ".known")
	if got := strings.TrimSpace(string(known)); got != "nas.local ssh-ed25519 AAAAC3fake" {
		t.Errorf("known_hosts = %q, want the plain host on the default port", got)
	}
}

// Without the agent there is nothing to sign in with, and the script says so
// rather than letting ssh fail obscurely.
func TestSendJobNeedsTheAgent(t *testing.T) {
	bin, record := fakeSSH(t)
	tr := localTransfers(t, []string{bin}, "HOSTMAN_TEST_RECORD="+record, "SSH_AUTH_SOCK=")
	to := &store.Host{ID: 2, Name: "nas", Address: "nas.local", Username: "chinmay", HostKey: "ssh-ed25519 AAAAC3fake"}
	job, err := tr.Send(context.Background(), &store.Host{ID: 1}, tree(t), to, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	done := settle(t, tr, job.ID)
	if done.State != StateFailed || !strings.Contains(done.Error, "agent was not forwarded") {
		t.Errorf("state = %s, error = %q", done.State, done.Error)
	}
}

func TestSendRefusesBeforeStarting(t *testing.T) {
	tr := localTransfers(t, nil)
	src := &store.Host{ID: 1, Name: "pi"}
	good := &store.Host{ID: 2, Name: "nas", Address: "nas.local", Username: "chinmay", HostKey: "ssh-ed25519 AAAA"}
	cases := []struct {
		name string
		from string
		dst  *store.Host
		dir  string
		want string
	}{
		{"the same host", "/media/x", &store.Host{ID: 1, Name: "pi", Address: "pi.local", Username: "u", HostKey: "k"}, "/tmp", "use Copy"},
		{"a host never connected to", "/media/x", &store.Host{ID: 2, Name: "nas", Address: "nas.local", Username: "u"}, "/tmp", "not connected"},
		{"a loopback address", "/media/x", &store.Host{ID: 2, Name: "home", Address: "127.0.0.1", Username: "u", HostKey: "k"}, "/tmp", "means nothing to another host"},
		{"localhost by name", "/media/x", &store.Host{ID: 2, Name: "home", Address: "localhost", Username: "u", HostKey: "k"}, "/tmp", "means nothing to another host"},
		{"a user name with a space", "/media/x", &store.Host{ID: 2, Name: "nas", Address: "nas.local", Username: "a b", HostKey: "k"}, "/tmp", "cannot be used"},
		{"the root", "/", good, "/tmp", "refusing"},
		{"a relative path", "x", good, "/tmp", "absolute"},
		{"a relative destination", "/media/x", good, "backups", "absolute"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := tr.Send(context.Background(), src, tc.from, tc.dst, tc.dir)
			if err == nil || !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want an invalid request mentioning %q", err, tc.want)
			}
		})
	}
	if len(tr.ForHost(1)) != 0 {
		t.Error("refused sends should not become jobs")
	}
}

// Cancelling kills the session; the job reads as cancelled whatever the host
// said on the way out, and can then be dismissed.
func TestCancelAndForget(t *testing.T) {
	tr := localTransfers(t, nil)
	started := make(chan struct{})
	tr.run = func(ctx context.Context, _ *store.Host, _, _ string, out io.Writer, _ bool) (int, error) {
		close(started)
		<-ctx.Done()
		io.WriteString(out, "Killed\n")
		return 137, nil
	}
	job, err := tr.Copy(context.Background(), &store.Host{ID: 1}, "/media/x", "/tmp")
	if err != nil {
		t.Fatal(err)
	}
	<-started
	if err := tr.Forget(job.ID); !errors.Is(err, ErrJobRunning) {
		t.Errorf("Forget while running = %v, want ErrJobRunning", err)
	}
	if err := tr.Cancel(job.ID); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	done := settle(t, tr, job.ID)
	if done.State != StateCancelled || done.Error != "" {
		t.Errorf("after cancel: state = %s, error = %q", done.State, done.Error)
	}
	if err := tr.Forget(job.ID); err != nil {
		t.Fatalf("Forget: %v", err)
	}
	if _, err := tr.Get(job.ID); !errors.Is(err, ErrJobNotFound) {
		t.Errorf("Get after Forget = %v, want ErrJobNotFound", err)
	}
	if err := tr.Cancel("nope"); !errors.Is(err, ErrJobNotFound) {
		t.Errorf("Cancel of an unknown job = %v", err)
	}
}

// A job that failed to connect at all is a failed job with the dial error.
func TestJobRecordsATransportFailure(t *testing.T) {
	tr := localTransfers(t, nil)
	tr.run = func(context.Context, *store.Host, string, string, io.Writer, bool) (int, error) {
		return -1, errors.New("connect to pi.local:22: no route to host")
	}
	job, err := tr.Copy(context.Background(), &store.Host{ID: 1}, "/media/x", "/tmp")
	if err != nil {
		t.Fatal(err)
	}
	done := settle(t, tr, job.ID)
	if done.State != StateFailed || !strings.Contains(done.Error, "no route to host") {
		t.Errorf("state = %s, error = %q", done.State, done.Error)
	}
}

// Finished jobs are kept for a while and then forgotten; running ones stay.
func TestFinishedJobsAreForgottenInTime(t *testing.T) {
	tr := localTransfers(t, nil)
	clock := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	tr.now = func() time.Time { return clock }
	tr.run = func(context.Context, *store.Host, string, string, io.Writer, bool) (int, error) { return 0, nil }
	job, err := tr.Copy(context.Background(), &store.Host{ID: 1}, "/media/x", "/tmp")
	if err != nil {
		t.Fatal(err)
	}
	settle(t, tr, job.ID)
	clock = clock.Add(KeepFinished / 2)
	if len(tr.ForHost(1)) != 1 {
		t.Error("a job that finished half an hour ago should still be listed")
	}
	clock = clock.Add(KeepFinished)
	if len(tr.ForHost(1)) != 0 {
		t.Error("a job that finished long ago should have been forgotten")
	}
}

func TestTailBufferKeepsTheEnd(t *testing.T) {
	b := &tailBuffer{max: 8}
	io.WriteString(b, "abcdef")
	io.WriteString(b, "ghijkl")
	if got := b.String(); got != "efghijkl" {
		t.Errorf("tail = %q, want the last 8 bytes", got)
	}
	if got := lastLine("one\ntwo\n\n  \n", "none"); got != "two" {
		t.Errorf("lastLine = %q", got)
	}
	if got := lastLine("  \n", "none"); got != "none" {
		t.Errorf("lastLine of nothing = %q, want the fallback", got)
	}
}

// The agent socket is handed to the script as $1 by the shell that still has
// it, so it arrives whether or not sudo scrubs the environment.
func TestWithAgentHandsTheSocketThrough(t *testing.T) {
	run := exec.Command("/bin/sh", "-c", withAgent(`printf '%s\n' "$1" "$2" "$3"`, "/media/it's here", "x"))
	run.Env = append(os.Environ(), "SSH_AUTH_SOCK=/tmp/ssh-abc/agent.1")
	out, err := run.Output()
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if got := string(out); got != "/tmp/ssh-abc/agent.1\n/media/it's here\nx\n" {
		t.Errorf("script saw %q", got)
	}
}
