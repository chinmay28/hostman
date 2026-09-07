package deploy

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os/user"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chinmay28/deployer/server/internal/hosts"
	"github.com/chinmay28/deployer/server/internal/sshx"
	"github.com/chinmay28/deployer/server/internal/store"
	"github.com/chinmay28/deployer/server/internal/testutil"
)

// env is a HostMan wired up against a throwaway sshd on localhost, so
// deployments run real commands over a real SSH connection.
type env struct {
	db      *store.DB
	svc     *hosts.Service
	runner  *Runner
	checker *Checker
	host    *store.Host
}

func newEnv(t *testing.T) *env {
	t.Helper()
	testutil.RequireSSHD(t)

	db, err := store.Open(filepath.Join(t.TempDir(), "deployer.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	ctx := context.Background()
	id, err := sshx.EnsureIdentity(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	port := testutil.StartSSHD(t, id.AuthorizedKey())
	me, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	host, err := db.CreateHost(ctx, &store.Host{
		Name: "localhost", Address: "127.0.0.1", Port: port, Username: me.Username,
	})
	if err != nil {
		t.Fatal(err)
	}

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	svc := hosts.NewService(db, id, nil)
	checker := NewChecker(db, svc, log)
	return &env{
		db: db, svc: svc, checker: checker, host: host,
		runner: NewRunner(db, svc, checker, log),
	}
}

func (e *env) app(t *testing.T, a *store.App) *store.App {
	t.Helper()
	created, err := e.db.CreateApp(context.Background(), a)
	if err != nil {
		t.Fatal(err)
	}
	return created
}

// wait blocks until the deployment stops running.
func (e *env) wait(t *testing.T, id int64) *store.Deployment {
	t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		dep, err := e.db.GetDeployment(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		if dep.Done() {
			return dep
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("deployment %d never finished", id)
	return nil
}

func TestDeploymentSucceedsAndRecordsInstallation(t *testing.T) {
	e := newEnv(t)
	app := e.app(t, &store.App{
		Name:           "countroster",
		InstallCommand: `echo installing on {{host}} port {{port}}`,
		Params:         []store.Param{{Name: "port", Label: "Port", Default: "8787"}},
	})

	started, err := e.runner.Start(context.Background(), app.ID, e.host.ID, map[string]string{"port": "9000"})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if started.Status != store.DeployRunning {
		t.Errorf("initial status = %q, want running", started.Status)
	}

	dep := e.wait(t, started.ID)
	if dep.Status != store.DeploySucceeded {
		t.Fatalf("status = %q, error %q, log:\n%s", dep.Status, dep.Error, dep.Log)
	}
	if dep.ExitCode == nil || *dep.ExitCode != 0 {
		t.Errorf("exitCode = %v, want 0", dep.ExitCode)
	}
	if !strings.Contains(dep.Log, "installing on 127.0.0.1 port 9000") {
		t.Errorf("log missing the command output:\n%s", dep.Log)
	}
	if !strings.Contains(dep.Log, "==> Succeeded") {
		t.Errorf("log missing the outcome banner:\n%s", dep.Log)
	}
	if dep.FinishedAt == nil {
		t.Error("finishedAt not recorded")
	}

	// A successful deployment is what makes an app "installed" on a host.
	in, err := e.db.FindInstallation(context.Background(), app.ID, e.host.ID)
	if err != nil {
		t.Fatalf("FindInstallation: %v", err)
	}
	if in.Params["port"] != "9000" {
		t.Errorf("saved params = %v, want the values used for this deploy", in.Params)
	}
	if in.LastDeploymentID == nil || *in.LastDeploymentID != dep.ID {
		t.Errorf("lastDeploymentId = %v, want %d", in.LastDeploymentID, dep.ID)
	}
}

func TestFailedDeploymentRecordsExitCode(t *testing.T) {
	e := newEnv(t)
	app := e.app(t, &store.App{
		Name:           "broken",
		InstallCommand: `echo "about to fail" >&2; exit 7`,
	})

	started, err := e.runner.Start(context.Background(), app.ID, e.host.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	dep := e.wait(t, started.ID)

	if dep.Status != store.DeployFailed {
		t.Fatalf("status = %q, want failed", dep.Status)
	}
	if dep.ExitCode == nil || *dep.ExitCode != 7 {
		t.Errorf("exitCode = %v, want 7", dep.ExitCode)
	}
	if !strings.Contains(dep.Error, "7") {
		t.Errorf("error = %q, want it to mention the exit status", dep.Error)
	}
	// stderr belongs in the log too, or debugging from a phone is hopeless.
	if !strings.Contains(dep.Log, "about to fail") {
		t.Errorf("stderr missing from log:\n%s", dep.Log)
	}
	if _, err := e.db.FindInstallation(context.Background(), app.ID, e.host.ID); !errors.Is(err, store.ErrNotFound) {
		t.Error("a failed deployment must not count as installed")
	}
}

// The real-world case this protects: `curl ... | sudo bash` where curl 404s.
// Without pipefail the shell reports bash's exit status and the deploy looks
// like it worked.
func TestFailingPipeFailsTheDeployment(t *testing.T) {
	e := newEnv(t)
	if !remoteShellHasPipefail(t, e) {
		t.Skip("remote login shell does not support pipefail")
	}
	app := e.app(t, &store.App{
		Name:           "piped",
		InstallCommand: `sh -c "exit 22" | cat`,
	})

	started, err := e.runner.Start(context.Background(), app.ID, e.host.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	dep := e.wait(t, started.ID)
	if dep.Status != store.DeployFailed {
		t.Fatalf("status = %q, want failed; a failing pipe stage must not be masked\nlog:\n%s", dep.Status, dep.Log)
	}
	if dep.ExitCode == nil || *dep.ExitCode != 22 {
		t.Errorf("exitCode = %v, want 22 from the failing stage", dep.ExitCode)
	}
}

func remoteShellHasPipefail(t *testing.T, e *env) bool {
	t.Helper()
	client, err := e.svc.Connect(context.Background(), e.host)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	res, err := client.Run(context.Background(), "set -o pipefail 2>/dev/null && echo yes")
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(res.Stdout) == "yes"
}

func TestConcurrentDeploymentIsRejectedAndCancelWorks(t *testing.T) {
	e := newEnv(t)
	app := e.app(t, &store.App{Name: "slow", InstallCommand: `echo started; sleep 60`})

	started, err := e.runner.Start(context.Background(), app.ID, e.host.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	// Same app, same host, while the first is still going.
	if _, err := e.runner.Start(context.Background(), app.ID, e.host.ID, nil); !errors.Is(err, ErrAlreadyRunning) {
		t.Errorf("second Start = %v, want ErrAlreadyRunning", err)
	}

	if err := e.runner.Cancel(started.ID); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	dep := e.wait(t, started.ID)
	if dep.Status != store.DeployCanceled {
		t.Errorf("status = %q, want canceled", dep.Status)
	}
	if !strings.Contains(dep.Log, "==> Canceled") {
		t.Errorf("log should say it was canceled:\n%s", dep.Log)
	}

	// Once it stops, the app/host pair is free again.
	if _, err := e.runner.Start(context.Background(), app.ID, e.host.ID, nil); err != nil {
		t.Errorf("Start after cancel = %v, want success", err)
	}
	if err := e.runner.Cancel(3); err == nil {
		t.Error("canceling an unknown deployment should fail")
	}
}

func TestLogsStreamWhileRunning(t *testing.T) {
	e := newEnv(t)
	app := e.app(t, &store.App{
		Name:           "chatty",
		InstallCommand: `for i in 1 2 3; do echo "line $i"; sleep 0.4; done`,
	})

	started, err := e.runner.Start(context.Background(), app.ID, e.host.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	run := e.runner.Active(started.ID)
	if run == nil {
		t.Fatal("no active run to subscribe to")
	}
	backlog, updates, unsubscribe := run.Subscribe()
	defer unsubscribe()

	seen := string(backlog)
	timeout := time.After(60 * time.Second)
	for !strings.Contains(seen, "line 3") {
		select {
		case chunk, open := <-updates:
			if !open {
				t.Fatalf("stream closed before all output arrived; saw:\n%s", seen)
			}
			seen += string(chunk)
		case <-timeout:
			t.Fatalf("timed out waiting for streamed output; saw:\n%s", seen)
		}
	}
	// The banner naming the command is part of what a subscriber sees.
	if !strings.Contains(seen, "==> Deploying chatty") {
		t.Errorf("stream missing the header:\n%s", seen)
	}

	dep := e.wait(t, started.ID)
	if dep.Status != store.DeploySucceeded {
		t.Fatalf("status = %q", dep.Status)
	}
	// What was streamed is also what got persisted.
	for _, want := range []string{"line 1", "line 2", "line 3"} {
		if !strings.Contains(dep.Log, want) {
			t.Errorf("stored log missing %q:\n%s", want, dep.Log)
		}
	}
}

func TestDeployRejectsUnknownAppOrHost(t *testing.T) {
	e := newEnv(t)
	app := e.app(t, &store.App{Name: "x", InstallCommand: "true"})

	if _, err := e.runner.Start(context.Background(), 999, e.host.ID, nil); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("unknown app = %v, want ErrNotFound", err)
	}
	if _, err := e.runner.Start(context.Background(), app.ID, 999, nil); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("unknown host = %v, want ErrNotFound", err)
	}
}

func TestHTTPHealthCheck(t *testing.T) {
	e := newEnv(t)
	// The host must be known-online before health checks run against it.
	if _, err := e.svc.Probe(context.Background(), e.host); err != nil {
		t.Fatalf("Probe: %v", err)
	}

	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer up.Close()
	down := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer down.Close()

	cases := []struct {
		name, target, want string
	}{
		{"healthy", up.URL + "/", store.HealthPassing},
		{"unhealthy", down.URL + "/", store.HealthFailing},
		{"unreachable", "http://127.0.0.1:1/", store.HealthFailing},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			app := e.app(t, &store.App{
				Name:           tc.name,
				InstallCommand: "echo ok",
				HealthType:     store.HealthHTTP,
				HealthTarget:   tc.target,
			})
			started, err := e.runner.Start(context.Background(), app.ID, e.host.ID, nil)
			if err != nil {
				t.Fatal(err)
			}
			if dep := e.wait(t, started.ID); dep.Status != store.DeploySucceeded {
				t.Fatalf("deployment %d failed: %s", i, dep.Error)
			}
			in, err := e.db.FindInstallation(context.Background(), app.ID, e.host.ID)
			if err != nil {
				t.Fatal(err)
			}
			status, detail := e.checker.CheckOne(context.Background(), in)
			if status != tc.want {
				t.Errorf("health = %q (%s), want %q", status, detail, tc.want)
			}
			// The result is persisted, not just returned.
			reloaded, err := e.db.GetInstallation(context.Background(), in.ID)
			if err != nil {
				t.Fatal(err)
			}
			if reloaded.HealthStatus != tc.want || reloaded.HealthCheckedAt == nil {
				t.Errorf("stored health = %q at %v", reloaded.HealthStatus, reloaded.HealthCheckedAt)
			}
		})
	}
}

func TestHealthCheckUsesInstallationParams(t *testing.T) {
	e := newEnv(t)
	if _, err := e.svc.Probe(context.Background(), e.host); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/custom" {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	app := e.app(t, &store.App{
		Name:           "parameterized",
		InstallCommand: "echo {{path}}",
		Params:         []store.Param{{Name: "path", Label: "Path", Default: "custom"}},
		HealthType:     store.HealthHTTP,
		HealthTarget:   srv.URL + "/{{path}}",
	})
	started, err := e.runner.Start(context.Background(), app.ID, e.host.ID, map[string]string{"path": "custom"})
	if err != nil {
		t.Fatal(err)
	}
	e.wait(t, started.ID)

	in, err := e.db.FindInstallation(context.Background(), app.ID, e.host.ID)
	if err != nil {
		t.Fatal(err)
	}
	if status, detail := e.checker.CheckOne(context.Background(), in); status != store.HealthPassing {
		t.Errorf("health = %q (%s), want passing — the saved param should fill the URL", status, detail)
	}
}

func TestHealthCheckSkipsOfflineHosts(t *testing.T) {
	e := newEnv(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()

	app := e.app(t, &store.App{
		Name: "offline", InstallCommand: "echo ok",
		HealthType: store.HealthHTTP, HealthTarget: srv.URL,
	})
	started, err := e.runner.Start(context.Background(), app.ID, e.host.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	e.wait(t, started.ID)

	if err := e.db.MarkHostFailed(context.Background(), e.host.ID, store.StatusOffline, "unplugged"); err != nil {
		t.Fatal(err)
	}
	in, err := e.db.FindInstallation(context.Background(), app.ID, e.host.ID)
	if err != nil {
		t.Fatal(err)
	}
	status, detail := e.checker.CheckOne(context.Background(), in)
	if status != store.HealthUnknown {
		t.Errorf("health = %q, want unknown while the host is offline", status)
	}
	if !strings.Contains(detail, "offline") {
		t.Errorf("detail = %q, want it to explain why", detail)
	}
}

func TestShutdownStopsRunningDeployments(t *testing.T) {
	e := newEnv(t)
	app := e.app(t, &store.App{Name: "long", InstallCommand: "sleep 120"})
	started, err := e.runner.Start(context.Background(), app.ID, e.host.ID, nil)
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	e.runner.Shutdown(ctx)

	dep, err := e.db.GetDeployment(context.Background(), started.ID)
	if err != nil {
		t.Fatal(err)
	}
	if dep.Status == store.DeployRunning {
		t.Error("shutdown left a deployment recorded as running")
	}
}

// discardLogger keeps test output readable.
func discardLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }
