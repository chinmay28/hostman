package api

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/chinmay28/deployer/server/internal/store"
)

// fakeSelf stands in for the selfhost manager.
type fakeSelf struct{ id string }

func (f fakeSelf) MachineID() string { return f.id }

func TestSelfViewBeforeAnythingIsRegistered(t *testing.T) {
	s, h := testServer(t, "")
	s.Self = fakeSelf{id: "abc123"}
	s.Version = "deadbeef"
	s.SelfRef = "main"

	view := decode[selfView](t, do(t, h, "GET", "/api/self", ""))
	if view.Version != "deadbeef" || view.MachineID != "abc123" {
		t.Errorf("view = %+v", view)
	}
	if view.Host != nil || view.App != nil {
		t.Errorf("nothing should be registered yet: %+v", view)
	}
	if view.Ready {
		t.Error("an update cannot be ready with no home host")
	}
	if !strings.Contains(view.Blocked, "registered") {
		t.Errorf("blocked = %q, want it to explain the missing host", view.Blocked)
	}
}

func TestSelfUpdateRefusesWithoutAHomeHost(t *testing.T) {
	_, h := testServer(t, "")
	w := do(t, h, "POST", "/api/self/update", `{"ref":"main"}`)
	if w.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", w.Code)
	}
	if got := decode[apiError](t, w).Error; !strings.Contains(got, "isn't registered") {
		t.Errorf("error = %q", got)
	}
}

// registerSelf sets up a home host and the updater app the way selfhost would.
func registerSelf(t *testing.T, s *Server, online bool) (*store.Host, *store.App) {
	t.Helper()
	ctx := context.Background()
	host, err := s.DB.CreateHost(ctx, &store.Host{
		Name: "nakedpi", Address: "127.0.0.1", Port: 22, Username: "chinmay",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.DB.SetHostSelf(ctx, host.ID, true, "abc123"); err != nil {
		t.Fatal(err)
	}
	if online {
		if err := s.DB.MarkHostOnline(ctx, host.ID, store.HostFacts{
			Hostname: "nakedpi", OS: "Debian 12", SudoOK: true, MachineID: "abc123", IsSelf: true,
		}); err != nil {
			t.Fatal(err)
		}
	}
	app, err := s.DB.CreateApp(ctx, &store.App{
		Name:           "HostMan",
		InstallCommand: "curl -fsSL https://example.com/{{ref}}/quickstart.sh | sudo bash",
		Params:         []store.Param{{Name: "ref", Label: "Version", Default: "main"}},
		SelfUpdate:     true,
	})
	if err != nil {
		t.Fatal(err)
	}
	host, _ = s.DB.GetHost(ctx, host.ID)
	return host, app
}

func TestSelfViewReportsWhyAnUpdateIsBlocked(t *testing.T) {
	s, h := testServer(t, "")
	s.Self = fakeSelf{id: "abc123"}
	host, _ := registerSelf(t, s, false)

	// Registered but never reached.
	view := decode[selfView](t, do(t, h, "GET", "/api/self", ""))
	if view.Host == nil || !view.Host.IsSelf {
		t.Fatalf("home host missing from the view: %+v", view.Host)
	}
	if view.Ready || !strings.Contains(view.Blocked, "SSH") {
		t.Errorf("blocked = %q, ready = %v; want it to point at SSH", view.Blocked, view.Ready)
	}

	// Reachable, but the SSH user cannot use sudo.
	ctx := context.Background()
	if err := s.DB.MarkHostOnline(ctx, host.ID, store.HostFacts{
		Hostname: "nakedpi", SudoOK: false, MachineID: "abc123", IsSelf: true,
	}); err != nil {
		t.Fatal(err)
	}
	view = decode[selfView](t, do(t, h, "GET", "/api/self", ""))
	if view.Ready || !strings.Contains(view.Blocked, "sudo") {
		t.Errorf("blocked = %q, ready = %v; want it to point at sudo", view.Blocked, view.Ready)
	}
}

func TestSelfViewIsReadyWhenEverythingIsInPlace(t *testing.T) {
	s, h := testServer(t, "")
	s.Self = fakeSelf{id: "abc123"}
	s.SelfRef = "main"
	registerSelf(t, s, true)

	view := decode[selfView](t, do(t, h, "GET", "/api/self", ""))
	if !view.Ready {
		t.Fatalf("not ready: %q", view.Blocked)
	}
	if view.App == nil || !view.App.SelfUpdate {
		t.Errorf("app = %+v, want the self-update app", view.App)
	}
	if view.Ref != "main" {
		t.Errorf("ref = %q, want the configured default", view.Ref)
	}
	if view.Running != nil {
		t.Errorf("runningDeploymentId = %v, want none", view.Running)
	}
}

// The default offered next time should be the version actually installed.
func TestSelfViewPrefersTheLastDeployedRef(t *testing.T) {
	s, h := testServer(t, "")
	s.Self = fakeSelf{id: "abc123"}
	s.SelfRef = "main"
	host, app := registerSelf(t, s, true)

	ctx := context.Background()
	dep, err := s.DB.CreateDeployment(ctx, &store.Deployment{
		AppID: app.ID, HostID: host.ID, Command: "x", Params: map[string]string{"ref": "v1.4"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.DB.FinishDeployment(ctx, dep.ID, store.DeploySucceeded, nil, "", "done"); err != nil {
		t.Fatal(err)
	}
	if err := s.DB.UpsertInstallation(ctx, app.ID, host.ID, map[string]string{"ref": "v1.4"}, dep.ID); err != nil {
		t.Fatal(err)
	}

	view := decode[selfView](t, do(t, h, "GET", "/api/self", ""))
	if view.Ref != "v1.4" {
		t.Errorf("ref = %q, want the last deployed version", view.Ref)
	}
}

func TestSelfViewSurfacesARunningUpdate(t *testing.T) {
	s, h := testServer(t, "")
	s.Self = fakeSelf{id: "abc123"}
	host, app := registerSelf(t, s, true)

	dep, err := s.DB.CreateDeployment(context.Background(), &store.Deployment{
		AppID: app.ID, HostID: host.ID, Command: "x", DetachedLog: "/tmp/x.log",
	})
	if err != nil {
		t.Fatal(err)
	}

	view := decode[selfView](t, do(t, h, "GET", "/api/self", ""))
	if view.Running == nil || *view.Running != dep.ID {
		t.Fatalf("runningDeploymentId = %v, want %d", view.Running, dep.ID)
	}
	if view.Ready || !strings.Contains(view.Blocked, "already running") {
		t.Errorf("blocked = %q, ready = %v", view.Blocked, view.Ready)
	}
}

// The update endpoint targets the home host without being told which host.
func TestSelfUpdateTargetsTheHomeHost(t *testing.T) {
	s, h := testServer(t, "")
	s.Self = fakeSelf{id: "abc123"}
	host, app := registerSelf(t, s, true)

	// The host is unreachable in a unit test, so the deployment will fail —
	// what matters here is that it was created against the right app and host
	// with the ref that was asked for.
	w := do(t, h, "POST", "/api/self/update", `{"ref":"v2.0"}`)
	if w.Code != http.StatusAccepted {
		t.Fatalf("status = %d, body %s", w.Code, w.Body)
	}
	dep := decode[store.Deployment](t, w)
	if dep.AppID != app.ID || dep.HostID != host.ID {
		t.Errorf("deployment = app %d host %d, want app %d host %d",
			dep.AppID, dep.HostID, app.ID, host.ID)
	}
	if dep.Params["ref"] != "v2.0" {
		t.Errorf("params = %v, want the requested ref", dep.Params)
	}
	if !strings.Contains(dep.Command, "v2.0") {
		t.Errorf("command = %q, want the ref substituted", dep.Command)
	}
	// HostMan updating itself must be recorded as detached, or a restart
	// would lose the log.
	stored, err := s.DB.GetDeployment(context.Background(), dep.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.DetachedLog == "" {
		t.Error("a self-update on the home host must be detached")
	}
	if !strings.Contains(stored.DetachedLog, fmt.Sprint(dep.ID)) {
		t.Errorf("detached log %q should be unique per deployment", stored.DetachedLog)
	}
}

// Omitting the ref falls back to the app's default rather than erroring.
func TestSelfUpdateWithoutARefUsesTheDefault(t *testing.T) {
	s, h := testServer(t, "")
	s.Self = fakeSelf{id: "abc123"}
	registerSelf(t, s, true)

	w := do(t, h, "POST", "/api/self/update", "")
	if w.Code != http.StatusAccepted {
		t.Fatalf("status = %d, body %s", w.Code, w.Body)
	}
	if dep := decode[store.Deployment](t, w); dep.Params["ref"] != "main" {
		t.Errorf("params = %v, want the app default", dep.Params)
	}
}

// Hosts are exposed with the flag the UI badges them by.
func TestHostListMarksTheHomeHost(t *testing.T) {
	s, h := testServer(t, "")
	registerSelf(t, s, true)

	list := decode[[]hostView](t, do(t, h, "GET", "/api/hosts", ""))
	if len(list) != 1 {
		t.Fatalf("got %d hosts", len(list))
	}
	if !list[0].IsSelf {
		t.Error("the home host should be flagged in the host list")
	}
	if !strings.Contains(do(t, h, "GET", "/api/hosts", "").Body.String(), `"isSelf":true`) {
		t.Error("isSelf should be serialized for the UI")
	}
}
