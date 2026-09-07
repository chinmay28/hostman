package api

import (
	"context"
	"fmt"
	"net/http"
	"os/user"
	"strings"
	"testing"
	"time"

	"github.com/chinmay28/hostman/server/internal/sshx"
	"github.com/chinmay28/hostman/server/internal/store"
	"github.com/chinmay28/hostman/server/internal/testutil"
)

func TestCreateAppValidation(t *testing.T) {
	_, h := testServer(t, "")
	cases := []struct {
		name, body, wantMsg string
	}{
		{"no name", `{"installCommand":"true"}`, "name is required"},
		{"no command", `{"name":"x"}`, "install command is required"},
		{
			"unknown placeholder",
			`{"name":"x","installCommand":"install --port {{port}}"}`,
			"unknown placeholder",
		},
		{
			"quoted placeholder",
			`{"name":"x","installCommand":"echo \"{{tag}}\"","params":[{"name":"tag"}]}`,
			"remove the quotes",
		},
		{
			"bad param name",
			`{"name":"x","installCommand":"true","params":[{"name":"my-port"}]}`,
			"parameter names must",
		},
		{
			"duplicate param",
			`{"name":"x","installCommand":"true","params":[{"name":"p"},{"name":"p"}]}`,
			"duplicate parameter",
		},
		{
			"bad health type",
			`{"name":"x","installCommand":"true","healthType":"ping"}`,
			"health check type must be",
		},
		{
			"http health without url",
			`{"name":"x","installCommand":"true","healthType":"http"}`,
			"needs a URL",
		},
		{
			"http health with bad url",
			`{"name":"x","installCommand":"true","healthType":"http","healthTarget":"pi.local:8787"}`,
			"must start with http",
		},
		{
			"systemd health without unit",
			`{"name":"x","installCommand":"true","healthType":"systemd"}`,
			"needs a unit name",
		},
		{
			"health url naming something the app never declared",
			`{"name":"x","installCommand":"true","healthType":"http","healthTarget":"http://{HOST}:{PORT}/"}`,
			"unknown placeholder",
		},
		// An uninstall command is optional, but one that is given is checked
		// as closely as the install command beside it. Finding out it was
		// wrong while trying to remove something is the worst moment for it.
		{
			"uninstall command naming an undeclared parameter",
			`{"name":"x","installCommand":"true","uninstallCommand":"remove --port {{port}}"}`,
			"uninstall command has unknown placeholder",
		},
		{
			"quoted placeholder in the uninstall command",
			`{"name":"x","installCommand":"true","uninstallCommand":"remove \"{{tag}}\"","params":[{"name":"tag"}]}`,
			"remove the quotes",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := do(t, h, "POST", "/api/apps", tc.body)
			if w.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400 (body %s)", w.Code, w.Body)
			}
			if got := decode[apiError](t, w).Error; !strings.Contains(got, tc.wantMsg) {
				t.Errorf("error = %q, want it to mention %q", got, tc.wantMsg)
			}
		})
	}
}

func TestAppLifecycle(t *testing.T) {
	_, h := testServer(t, "")

	body := `{
		"name": "CountRoster",
		"description": "Roster counter",
		"installCommand": "curl -fsSL https://raw.githubusercontent.com/chinmay28/countroster/main/scripts/quickstart.sh | sudo bash",
		"params": [{"name":"port","label":"Port","default":"8787"}],
		"healthType": "http",
		"healthTarget": "http://{{host}}:{{port}}/"
	}`
	w := do(t, h, "POST", "/api/apps", body)
	if w.Code != http.StatusCreated {
		t.Fatalf("create status = %d, body %s", w.Code, w.Body)
	}
	app := decode[store.App](t, w)
	if len(app.Params) != 1 || app.Params[0].Label != "Port" {
		t.Errorf("params = %+v", app.Params)
	}

	if w := do(t, h, "POST", "/api/apps", body); w.Code != http.StatusConflict {
		t.Errorf("duplicate name status = %d, want 409", w.Code)
	}

	// A partial PATCH leaves the rest of the app alone.
	w = do(t, h, "PATCH", fmt.Sprintf("/api/apps/%d", app.ID), `{"description":"Updated"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("patch status = %d, body %s", w.Code, w.Body)
	}
	patched := decode[store.App](t, w)
	if patched.Description != "Updated" || patched.InstallCommand != app.InstallCommand {
		t.Errorf("patched = %+v", patched)
	}
	if len(patched.Params) != 1 {
		t.Errorf("patch dropped params: %+v", patched.Params)
	}

	// Switching to no health check clears the target.
	w = do(t, h, "PATCH", fmt.Sprintf("/api/apps/%d", app.ID), `{"healthType":"none"}`)
	if got := decode[store.App](t, w); got.HealthTarget != "" {
		t.Errorf("healthTarget = %q, want it cleared", got.HealthTarget)
	}

	if w := do(t, h, "GET", "/api/apps/999", ""); w.Code != http.StatusNotFound {
		t.Errorf("get missing status = %d, want 404", w.Code)
	}
	if w := do(t, h, "DELETE", fmt.Sprintf("/api/apps/%d", app.ID), ""); w.Code != http.StatusNoContent {
		t.Errorf("delete status = %d, want 204", w.Code)
	}
	if apps := decode[[]store.App](t, do(t, h, "GET", "/api/apps", "")); len(apps) != 0 {
		t.Errorf("apps after delete = %+v", apps)
	}
}

func TestDeployValidationErrors(t *testing.T) {
	_, h := testServer(t, "")
	do(t, h, "POST", "/api/hosts", `{"name":"pi","address":"127.0.0.1","username":"nobody"}`)
	do(t, h, "POST", "/api/apps", `{"name":"app","installCommand":"install --token {{token}}",
		"params":[{"name":"token","label":"Token","required":true}]}`)

	if w := do(t, h, "POST", "/api/apps/1/deploy", `{}`); w.Code != http.StatusBadRequest {
		t.Errorf("no hostId status = %d, want 400", w.Code)
	}
	w := do(t, h, "POST", "/api/apps/1/deploy", `{"hostId":1}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("missing param status = %d, want 400 (body %s)", w.Code, w.Body)
	}
	if got := decode[apiError](t, w).Error; !strings.Contains(got, "Token") {
		t.Errorf("error = %q, want it to name the missing parameter", got)
	}
	if w := do(t, h, "POST", "/api/apps/1/deploy", `{"hostId":99}`); w.Code != http.StatusNotFound {
		t.Errorf("unknown host status = %d, want 404", w.Code)
	}
	if w := do(t, h, "POST", "/api/apps/99/deploy", `{"hostId":1}`); w.Code != http.StatusNotFound {
		t.Errorf("unknown app status = %d, want 404", w.Code)
	}
}

// localHost adds a host pointing at a throwaway sshd, so deployments made
// through the API actually run.
func localHost(t *testing.T, s *Server, h http.Handler) int64 {
	t.Helper()
	testutil.RequireSSHD(t)
	id, err := sshx.EnsureIdentity(t.Context(), s.DB)
	if err != nil {
		t.Fatal(err)
	}
	port := testutil.StartSSHD(t, id.AuthorizedKey())
	me, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	w := do(t, h, "POST", "/api/hosts",
		fmt.Sprintf(`{"name":"localhost","address":"127.0.0.1","port":%d,"username":%q}`, port, me.Username))
	if w.Code != http.StatusCreated {
		t.Fatalf("create host: %s", w.Body)
	}
	return decode[hostView](t, w).ID
}

func waitForDeployment(t *testing.T, h http.Handler, id int64) store.Deployment {
	t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		dep := decode[store.Deployment](t, do(t, h, "GET", fmt.Sprintf("/api/deployments/%d", id), ""))
		if dep.Status != store.DeployRunning {
			return dep
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("deployment %d never finished", id)
	return store.Deployment{}
}

func TestDeployEndToEndThroughAPI(t *testing.T) {
	s, h := testServer(t, "")
	hostID := localHost(t, s, h)

	w := do(t, h, "POST", "/api/apps", `{"name":"demo","installCommand":"echo deployed to {{host}}"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("create app: %s", w.Body)
	}
	appID := decode[store.App](t, w).ID

	w = do(t, h, "POST", fmt.Sprintf("/api/apps/%d/deploy", appID), fmt.Sprintf(`{"hostId":%d}`, hostID))
	if w.Code != http.StatusAccepted {
		t.Fatalf("deploy status = %d, body %s", w.Code, w.Body)
	}
	started := decode[store.Deployment](t, w)

	dep := waitForDeployment(t, h, started.ID)
	if dep.Status != store.DeploySucceeded {
		t.Fatalf("status = %q, error %q, log:\n%s", dep.Status, dep.Error, dep.Log)
	}
	if !strings.Contains(dep.Log, "deployed to 127.0.0.1") {
		t.Errorf("log missing output:\n%s", dep.Log)
	}
	// The detail view is a page of its own, so it needs the names too.
	if dep.AppName != "demo" || dep.HostName != "localhost" {
		t.Errorf("deployment detail = app %q, host %q; want both named", dep.AppName, dep.HostName)
	}

	// The app now shows up as installed, with the host and app named for the UI.
	installs := decode[[]store.Installation](t, do(t, h, "GET", "/api/installations", ""))
	if len(installs) != 1 {
		t.Fatalf("installations = %d, want 1", len(installs))
	}
	if installs[0].AppName != "demo" || installs[0].HostName != "localhost" {
		t.Errorf("installation = %+v, want app and host names filled in", installs[0])
	}
	if installs[0].LastStatus != store.DeploySucceeded {
		t.Errorf("lastStatus = %q, want succeeded", installs[0].LastStatus)
	}

	// One-tap redeploy reuses the saved parameters.
	w = do(t, h, "POST", fmt.Sprintf("/api/installations/%d/redeploy", installs[0].ID), "")
	if w.Code != http.StatusAccepted {
		t.Fatalf("redeploy status = %d, body %s", w.Code, w.Body)
	}
	second := decode[store.Deployment](t, w)
	if second.ID == started.ID {
		t.Error("redeploy reused the previous deployment record")
	}
	if got := waitForDeployment(t, h, second.ID); got.Status != store.DeploySucceeded {
		t.Errorf("redeploy status = %q: %s", got.Status, got.Error)
	}

	// History is newest first and scoped by app.
	history := decode[[]store.Deployment](t, do(t, h, "GET", fmt.Sprintf("/api/deployments?appId=%d", appID), ""))
	if len(history) != 2 {
		t.Fatalf("history = %d entries, want 2", len(history))
	}
	if history[0].ID != second.ID {
		t.Errorf("history not newest-first: %+v", history)
	}
	// Listings omit logs; they would swamp a phone.
	if history[0].Log != "" {
		t.Error("list response should not include logs")
	}

	// The dashboard's single request has everything on it.
	ov := decode[overview](t, do(t, h, "GET", "/api/overview", ""))
	if len(ov.Hosts) != 1 || len(ov.Installations) != 1 {
		t.Errorf("overview = %d hosts, %d installations",
			len(ov.Hosts), len(ov.Installations))
	}

	// Forgetting an installation leaves the deployment history intact.
	if w := do(t, h, "DELETE", fmt.Sprintf("/api/installations/%d", installs[0].ID), ""); w.Code != http.StatusNoContent {
		t.Errorf("forget status = %d, want 204", w.Code)
	}
	if got := decode[[]store.Installation](t, do(t, h, "GET", "/api/installations", "")); len(got) != 0 {
		t.Errorf("installations after forget = %+v", got)
	}
	if got := decode[[]store.Deployment](t, do(t, h, "GET", "/api/deployments", "")); len(got) != 2 {
		t.Errorf("deployment history = %d, want it kept", len(got))
	}
}

func TestDeploymentLogStream(t *testing.T) {
	s, h := testServer(t, "")
	hostID := localHost(t, s, h)

	w := do(t, h, "POST", "/api/apps",
		`{"name":"chatty","installCommand":"for i in 1 2 3; do echo line $i; sleep 0.3; done"}`)
	appID := decode[store.App](t, w).ID

	w = do(t, h, "POST", fmt.Sprintf("/api/apps/%d/deploy", appID), fmt.Sprintf(`{"hostId":%d}`, hostID))
	dep := decode[store.Deployment](t, w)

	// The stream stays open until the deployment ends, then reports its state.
	w = do(t, h, "GET", fmt.Sprintf("/api/deployments/%d/stream", dep.ID), "")
	if w.Code != http.StatusOK {
		t.Fatalf("stream status = %d", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); ct != "text/event-stream" {
		t.Errorf("content-type = %q, want text/event-stream", ct)
	}
	body := w.Body.String()
	for _, want := range []string{"event: log", "data: line 1", "data: line 3", "event: status"} {
		if !strings.Contains(body, want) {
			t.Errorf("stream missing %q:\n%s", want, body)
		}
	}
	if !strings.Contains(body, `"status":"succeeded"`) {
		t.Errorf("stream did not report the final status:\n%s", body)
	}

	// Streaming a finished deployment replays the stored log and closes.
	w = do(t, h, "GET", fmt.Sprintf("/api/deployments/%d/stream", dep.ID), "")
	replay := w.Body.String()
	if !strings.Contains(replay, "data: line 3") || !strings.Contains(replay, "event: status") {
		t.Errorf("replay incomplete:\n%s", replay)
	}
}

func TestCancelDeploymentThroughAPI(t *testing.T) {
	s, h := testServer(t, "")
	hostID := localHost(t, s, h)

	w := do(t, h, "POST", "/api/apps", `{"name":"slow","installCommand":"sleep 60"}`)
	appID := decode[store.App](t, w).ID
	w = do(t, h, "POST", fmt.Sprintf("/api/apps/%d/deploy", appID), fmt.Sprintf(`{"hostId":%d}`, hostID))
	dep := decode[store.Deployment](t, w)

	// A second deploy of the same app to the same host is refused.
	if w := do(t, h, "POST", fmt.Sprintf("/api/apps/%d/deploy", appID), fmt.Sprintf(`{"hostId":%d}`, hostID)); w.Code != http.StatusConflict {
		t.Errorf("concurrent deploy status = %d, want 409", w.Code)
	}

	if w := do(t, h, "POST", fmt.Sprintf("/api/deployments/%d/cancel", dep.ID), ""); w.Code != http.StatusAccepted {
		t.Fatalf("cancel status = %d, body %s", w.Code, w.Body)
	}
	if got := waitForDeployment(t, h, dep.ID); got.Status != store.DeployCanceled {
		t.Errorf("status = %q, want canceled", got.Status)
	}
	if w := do(t, h, "POST", fmt.Sprintf("/api/deployments/%d/cancel", dep.ID), ""); w.Code != http.StatusConflict {
		t.Errorf("cancel of a finished deployment = %d, want 409", w.Code)
	}
}

// A deployment that is handed over mid-restart must not end the stream with a
// terminal-looking status, or the page waits forever for a verdict that has
// already been sent.
func TestStreamOfAHandedOverDeploymentDoesNotLookFinished(t *testing.T) {
	s, h := testServer(t, "")
	host, app := registerSelf(t, s, true)

	// A detached deployment left running by a previous process.
	dep, err := s.DB.CreateDeployment(context.Background(), &store.Deployment{
		AppID: app.ID, HostID: host.ID, Command: "x", DetachedLog: "/tmp/handover.log",
	})
	if err != nil {
		t.Fatal(err)
	}

	w := do(t, h, "GET", fmt.Sprintf("/api/deployments/%d/stream", dep.ID), "")
	body := w.Body.String()
	if strings.Contains(body, "event: status") {
		t.Errorf("stream reported a verdict for a deployment still running:\n%s", body)
	}
	if !strings.Contains(body, "event: handover") {
		t.Errorf("stream should say it was handed over:\n%s", body)
	}
}
