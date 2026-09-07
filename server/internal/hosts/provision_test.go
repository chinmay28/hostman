package hosts

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chinmay28/deployer/server/internal/sshx"
	"github.com/chinmay28/deployer/server/internal/store"
)

const testKey = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIExampleKeyForTests000000000000000000 deployer"

// runAuthorize executes the real authorize script over a real SSH connection,
// with HOME pointed at a directory the test owns, and returns what it printed.
func runAuthorize(t *testing.T, client *sshx.Client, home, key string) string {
	t.Helper()
	cmd := "HOME=" + sshx.Quote(home) + "\nexport HOME\n" + fmt.Sprintf(authorizeScript, sshx.Quote(key))
	res, err := client.Run(context.Background(), cmd)
	if err != nil {
		t.Fatalf("run authorize script: %v", err)
	}
	if res.ExitCode != 0 {
		t.Fatalf("authorize script exited %d: %s", res.ExitCode, res.Stderr)
	}
	return strings.TrimSpace(res.Stdout)
}

func TestAuthorizeScriptAddsTheKeyExactlyOnce(t *testing.T) {
	_, svc, h := testEnv(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	client, err := svc.Connect(ctx, h)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer client.Close()

	home := t.TempDir()
	authorized := filepath.Join(home, ".ssh", "authorized_keys")

	if out := runAuthorize(t, client, home, testKey); out != "added" {
		t.Errorf("first run said %q, want added", out)
	}
	body, err := os.ReadFile(authorized)
	if err != nil {
		t.Fatalf("read authorized_keys: %v", err)
	}
	if string(body) != testKey+"\n" {
		t.Errorf("authorized_keys = %q, want just the key", body)
	}

	// Running setup twice is normal — a retry, or a host added again — and must
	// not stack up duplicate keys.
	if out := runAuthorize(t, client, home, testKey); out != "already" {
		t.Errorf("second run said %q, want already", out)
	}
	after, err := os.ReadFile(authorized)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(body) {
		t.Errorf("authorized_keys changed on the second run: %q", after)
	}

	// sshd ignores the file unless the permissions are tight.
	info, err := os.Stat(authorized)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("authorized_keys mode = %o, want 600", perm)
	}
	dir, err := os.Stat(filepath.Join(home, ".ssh"))
	if err != nil {
		t.Fatal(err)
	}
	if perm := dir.Mode().Perm(); perm != 0o700 {
		t.Errorf(".ssh mode = %o, want 700", perm)
	}
}

// A file whose last line has no newline would otherwise get HostMan's key
// spliced onto the end of someone else's, silently breaking both.
func TestAuthorizeScriptKeepsExistingKeysIntact(t *testing.T) {
	_, svc, h := testEnv(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	client, err := svc.Connect(ctx, h)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer client.Close()

	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".ssh"), 0o700); err != nil {
		t.Fatal(err)
	}
	existing := "ssh-rsa AAAAB3NzaC1yc2EAAAADAQABAAABgQCexisting someone@elsewhere"
	authorized := filepath.Join(home, ".ssh", "authorized_keys")
	if err := os.WriteFile(authorized, []byte(existing), 0o600); err != nil {
		t.Fatal(err)
	}

	runAuthorize(t, client, home, testKey)

	body, err := os.ReadFile(authorized)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimRight(string(body), "\n"), "\n")
	if len(lines) != 2 || lines[0] != existing || lines[1] != testKey {
		t.Errorf("authorized_keys = %q, want the existing key then HostMan's", body)
	}
}

func TestProvisionRefusesAnEmptyPassword(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "deployer.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	id, err := sshx.EnsureIdentity(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	svc := NewService(db, id, nil)

	// No connection is attempted, so this needs no host to exist.
	res := svc.Provision(ctx, &store.Host{Name: "pi", Address: "10.0.0.1", Port: 22, Username: "pi"}, "  ")
	if res.OK {
		t.Fatal("provisioning succeeded without a password")
	}
	if res.Error == "" || len(res.Steps) != 1 || res.Steps[0].OK {
		t.Errorf("result = %+v, want a single failed step with a reason", res)
	}
}

func TestSignInHintsExplainARefusedPassword(t *testing.T) {
	h := &store.Host{Address: "nakedpi.local", Username: "pi"}

	hints := strings.Join(signInHints(h, fmt.Errorf("dial: %w", sshx.ErrAuthFailed)), " ")
	if !strings.Contains(hints, "password for pi") {
		t.Errorf("auth failure hints = %q, want the password mentioned", hints)
	}
	if !strings.Contains(hints, "Settings") {
		t.Errorf("auth failure hints = %q, want the manual route offered", hints)
	}

	hints = strings.Join(signInHints(h, fmt.Errorf("wrapped: %w", sshx.ErrHostKeyChanged)), " ")
	if !strings.Contains(hints, "host's SSH key changed") {
		t.Errorf("host key hints = %q, want the key change explained", hints)
	}

	hints = strings.Join(signInHints(h, fmt.Errorf("connect: no route to host")), " ")
	if !strings.Contains(hints, "nakedpi.local") {
		t.Errorf("unreachable hints = %q, want the address mentioned", hints)
	}
}

func TestCommandErrorPrefersTheLastStderrLine(t *testing.T) {
	tests := []struct {
		name string
		res  *sshx.Result
		want string
	}{
		{"stderr wins", &sshx.Result{Stdout: "working\n", Stderr: "first\nsudo: a password is required\n", ExitCode: 1},
			"sudo: a password is required"},
		{"falls back to stdout", &sshx.Result{Stdout: "chmod: no such file\n", ExitCode: 2},
			"chmod: no such file"},
		{"never empty", &sshx.Result{ExitCode: 7}, "the command failed with exit code 7"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := commandError(tt.res); got != tt.want {
				t.Errorf("commandError = %q, want %q", got, tt.want)
			}
		})
	}
}
