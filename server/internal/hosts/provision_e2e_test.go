package hosts

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chinmay28/deployer/server/internal/sshx"
	"github.com/chinmay28/deployer/server/internal/store"
	"github.com/chinmay28/deployer/server/internal/testutil"
)

// The whole of provisioning against a real sshd that accepts passwords: the
// password handshake, the two setup commands, and the key-only connection that
// proves they worked.
//
// It is opt-in because it changes the machine it runs on — it creates a
// throwaway user and writes /etc/sudoers.d/deployer, exactly as it would on a
// host. Both are removed afterwards, but don't point it at a machine HostMan
// already manages. Run it with `make test-provision`.
func TestProvisionEndToEnd(t *testing.T) {
	if os.Getenv("DEPLOYER_E2E") != "1" {
		t.Skip("set DEPLOYER_E2E=1 to run the provisioning end-to-end test")
	}
	testutil.RequireSSHD(t)

	const user = "deployere2e"
	// A quote and a shell metacharacter, to show the password never reaches a
	// shell: it goes over the SSH handshake and sudo's stdin, nowhere else.
	const password = `s3cret-p@ss'w$ord`

	if out, err := exec.Command("useradd", "-m", "-s", "/bin/bash", "-G", "sudo", user).CombinedOutput(); err != nil {
		t.Skipf("cannot create the test user: %v %s", err, out)
	}
	t.Cleanup(func() {
		exec.Command("userdel", "-r", user).Run()
		os.Remove("/etc/sudoers.d/deployer")
	})
	chpasswd := exec.Command("chpasswd")
	chpasswd.Stdin = strings.NewReader(user + ":" + password + "\n")
	if out, err := chpasswd.CombinedOutput(); err != nil {
		t.Fatalf("chpasswd: %v %s", err, out)
	}

	port := startPasswordSSHD(t)
	db, err := store.Open(filepath.Join(t.TempDir(), "deployer.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	id, err := sshx.EnsureIdentity(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	svc := NewService(db, id, nil)
	h, err := db.CreateHost(ctx, &store.Host{Name: "e2e", Address: "127.0.0.1", Port: port, Username: user})
	if err != nil {
		t.Fatal(err)
	}
	authorizedKeys := filepath.Join("/home", user, ".ssh", "authorized_keys")

	// Nothing has authorized HostMan yet, so the ordinary route is shut.
	if _, err := svc.Probe(ctx, h); err == nil {
		t.Fatal("probe succeeded before the host was provisioned")
	}

	wrong := svc.Provision(ctx, h, "not-the-password")
	if wrong.OK {
		t.Fatal("provisioning succeeded with the wrong password")
	}
	if len(wrong.Steps) != 1 || wrong.Steps[0].OK {
		t.Errorf("steps = %+v, want sign-in as the only, failed step", wrong.Steps)
	}
	if !strings.Contains(strings.Join(wrong.Hints, " "), "password") {
		t.Errorf("hints = %v, want the password mentioned", wrong.Hints)
	}
	// A refused password must not leave a half-configured host behind.
	if _, err := os.Stat(authorizedKeys); !os.IsNotExist(err) {
		t.Errorf("authorized_keys exists after a failed sign-in (stat err = %v)", err)
	}

	res := svc.Provision(ctx, h, password)
	if !res.OK {
		t.Fatalf("provision failed: %+v", res)
	}
	if !res.SudoOK {
		t.Errorf("passwordless sudo not enabled: %+v", res.Steps)
	}

	authorized, err := os.ReadFile(authorizedKeys)
	if err != nil {
		t.Fatalf("read authorized_keys: %v", err)
	}
	if strings.TrimSpace(string(authorized)) != id.AuthorizedKey() {
		t.Errorf("authorized_keys = %q, want HostMan's key", authorized)
	}
	sudoers, err := os.ReadFile("/etc/sudoers.d/deployer")
	if err != nil {
		t.Fatalf("read the sudoers drop-in: %v", err)
	}
	if string(sudoers) != user+" ALL=(ALL) NOPASSWD:ALL\n" {
		t.Errorf("sudoers = %q", sudoers)
	}
	if info, err := os.Stat("/etc/sudoers.d/deployer"); err == nil && info.Mode().Perm() != 0o440 {
		t.Errorf("sudoers mode = %o, want 440", info.Mode().Perm())
	}

	// Running it again is how a user retries, so it has to be a no-op.
	again := svc.Provision(ctx, h, password)
	if !again.OK || !again.SudoOK {
		t.Errorf("second run failed: %+v", again)
	}
	after, _ := os.ReadFile(authorizedKeys)
	if string(after) != string(authorized) {
		t.Errorf("authorized_keys changed on the second run: %q", after)
	}

	// The point of all of it: the host now works the ordinary way, key only.
	probe, err := svc.Probe(ctx, h)
	if err != nil {
		t.Fatalf("probe after provisioning: %v", err)
	}
	if !probe.Facts.SudoOK {
		t.Error("the probe still reports no passwordless sudo")
	}
}

// startPasswordSSHD boots an sshd that accepts passwords as well as keys — the
// shape of a host that has never met HostMan.
func startPasswordSSHD(t *testing.T) int {
	t.Helper()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	hostKey := filepath.Join(dir, "host_ed25519")
	if out, err := exec.Command("ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-f", hostKey).CombinedOutput(); err != nil {
		t.Fatalf("ssh-keygen: %v: %s", err, out)
	}

	port := testutil.FreePort(t)
	cfg := filepath.Join(dir, "sshd_config")
	conf := fmt.Sprintf(`Port %d
ListenAddress 127.0.0.1
HostKey %s
PidFile %s
PasswordAuthentication yes
PubkeyAuthentication yes
PermitRootLogin no
StrictModes no
UsePAM no
KbdInteractiveAuthentication no
PrintMotd no
LogLevel ERROR
`, port, hostKey, filepath.Join(dir, "sshd.pid"))
	if err := os.WriteFile(cfg, []byte(conf), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll("/run/sshd", 0o755); err != nil {
		t.Skipf("cannot create sshd privilege separation directory: %v", err)
	}

	cmd := exec.Command("/usr/sbin/sshd", "-D", "-f", cfg)
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start sshd: %v", err)
	}
	t.Cleanup(func() {
		cmd.Process.Kill()
		cmd.Wait()
	})

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 200*time.Millisecond); err == nil {
			conn.Close()
			return port
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("sshd did not start listening on port %d", port)
	return 0
}
