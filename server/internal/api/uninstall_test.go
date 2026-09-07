package api

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chinmay28/deployer/server/internal/store"
)

// An app that never said how to remove itself cannot be removed, and says so
// rather than running nothing and reporting success.
func TestUninstallRefusedWithoutACommand(t *testing.T) {
	s, h := testServer(t, "")
	in := installed(t, s, &store.App{Name: "photos", InstallCommand: "install"})

	w := do(t, h, "POST", fmt.Sprintf("/api/installations/%d/uninstall", in.ID), "")
	if w.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409 (body %s)", w.Code, w.Body)
	}
	if got := decode[apiError](t, w).Error; !strings.Contains(got, "no uninstall command") {
		t.Errorf("error = %q, want it to say the app has none", got)
	}
	// Nothing ran, so nothing was recorded and the installation stands.
	if got := decode[[]store.Deployment](t, do(t, h, "GET", "/api/deployments", "")); len(got) != 1 {
		t.Errorf("deployments = %d, want only the install", len(got))
	}
	if got := decode[[]store.Installation](t, do(t, h, "GET", "/api/installations", "")); len(got) != 1 {
		t.Errorf("installations = %d, want the record kept", len(got))
	}
}

func TestUninstallOfAMissingInstallation(t *testing.T) {
	_, h := testServer(t, "")
	if w := do(t, h, "POST", "/api/installations/99/uninstall", ""); w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", w.Code)
	}
}

// HostMan removing itself from the machine it runs on would take the log, the
// record and the UI down with it half way through, leaving nothing to say what
// happened. An update survives that restart by running detached and being
// picked back up; an uninstall has nothing left to pick it back up.
func TestUninstallRefusesToRemoveHostManFromItsOwnMachine(t *testing.T) {
	s, h := testServer(t, "")
	s.Self = fakeSelf{id: "abc123"}
	host, app := registerSelf(t, s, true)

	ctx := context.Background()
	app.UninstallCommand = "curl -fsSL https://example.com/quickstart.sh | sudo bash -s -- --uninstall"
	if err := s.DB.UpdateApp(ctx, app); err != nil {
		t.Fatal(err)
	}
	dep, err := s.DB.CreateDeployment(ctx, &store.Deployment{
		AppID: app.ID, HostID: host.ID, Command: "install",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.DB.UpsertInstallation(ctx, app.ID, host.ID, map[string]string{"ref": "main"}, dep.ID); err != nil {
		t.Fatal(err)
	}
	in, err := s.DB.FindInstallation(ctx, app.ID, host.ID)
	if err != nil {
		t.Fatal(err)
	}

	w := do(t, h, "POST", fmt.Sprintf("/api/installations/%d/uninstall", in.ID), "")
	if w.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409 (body %s)", w.Code, w.Body)
	}
	if got := decode[apiError](t, w).Error; !strings.Contains(got, "can't uninstall itself") {
		t.Errorf("error = %q, want it to explain the refusal", got)
	}
}

// The whole path, over a real SSH connection: the uninstall command runs on the
// host, its log is kept like any other deployment's, and the installation is
// forgotten because the app is no longer there.
func TestUninstallEndToEndThroughAPI(t *testing.T) {
	s, h := testServer(t, "")
	hostID := localHost(t, s, h)

	// The command leaves a mark on the filesystem, so "it ran" is something the
	// test can check rather than infer from an exit status.
	marker := filepath.Join(t.TempDir(), "removed")
	w := do(t, h, "POST", "/api/apps", fmt.Sprintf(`{
		"name": "demo",
		"installCommand": "echo installed on {{host}}",
		"uninstallCommand": "echo removing from {{host}}; touch %s"
	}`, marker))
	if w.Code != http.StatusCreated {
		t.Fatalf("create app: %s", w.Body)
	}
	app := decode[store.App](t, w)

	w = do(t, h, "POST", fmt.Sprintf("/api/apps/%d/deploy", app.ID), fmt.Sprintf(`{"hostId":%d}`, hostID))
	if w.Code != http.StatusAccepted {
		t.Fatalf("deploy status = %d, body %s", w.Code, w.Body)
	}
	install := waitForDeployment(t, h, decode[store.Deployment](t, w).ID)
	if install.Status != store.DeploySucceeded {
		t.Fatalf("install status = %q: %s", install.Status, install.Error)
	}
	if install.Kind != store.KindInstall {
		t.Errorf("install kind = %q, want %q", install.Kind, store.KindInstall)
	}

	installs := decode[[]store.Installation](t, do(t, h, "GET", "/api/installations", ""))
	if len(installs) != 1 {
		t.Fatalf("installations = %d, want 1", len(installs))
	}

	w = do(t, h, "POST", fmt.Sprintf("/api/installations/%d/uninstall", installs[0].ID), "")
	if w.Code != http.StatusAccepted {
		t.Fatalf("uninstall status = %d, body %s", w.Code, w.Body)
	}
	removal := waitForDeployment(t, h, decode[store.Deployment](t, w).ID)
	if removal.Status != store.DeploySucceeded {
		t.Fatalf("uninstall status = %q, error %q, log:\n%s", removal.Status, removal.Error, removal.Log)
	}
	if removal.Kind != store.KindUninstall {
		t.Errorf("kind = %q, want %q", removal.Kind, store.KindUninstall)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Errorf("the uninstall command did not run on the host: %v", err)
	}
	// The log says which of the two it was, in the words the log opens with.
	if !strings.Contains(removal.Log, "Uninstalling demo from") {
		t.Errorf("log does not read as an uninstall:\n%s", removal.Log)
	}
	if !strings.Contains(removal.Log, "removing from 127.0.0.1") {
		t.Errorf("log missing the command's output:\n%s", removal.Log)
	}

	// The app is gone from the host, so HostMan's record of it being there is
	// gone too — along with the health check that would keep asking after it.
	if got := decode[[]store.Installation](t, do(t, h, "GET", "/api/installations", "")); len(got) != 0 {
		t.Errorf("installations after uninstall = %+v, want none", got)
	}
	// The history stays: both runs are the record of what was done to the host.
	history := decode[[]store.Deployment](t, do(t, h, "GET", "/api/deployments", ""))
	if len(history) != 2 {
		t.Fatalf("history = %d entries, want the install and the uninstall", len(history))
	}
	// The app itself stays too: removing it from one host is not deleting it.
	if apps := decode[[]store.App](t, do(t, h, "GET", "/api/apps", "")); len(apps) != 1 {
		t.Errorf("apps = %d, want the app kept", len(apps))
	}

	// With the installation gone there is nothing left to uninstall.
	if w := do(t, h, "POST", fmt.Sprintf("/api/installations/%d/uninstall", installs[0].ID), ""); w.Code != http.StatusNotFound {
		t.Errorf("second uninstall status = %d, want 404", w.Code)
	}
}

// A failing uninstall leaves the installation alone: the app may well still be
// on the host, and a record that quietly disappeared would be the worse of the
// two wrong answers.
func TestFailedUninstallKeepsTheInstallation(t *testing.T) {
	s, h := testServer(t, "")
	hostID := localHost(t, s, h)

	w := do(t, h, "POST", "/api/apps",
		`{"name":"demo","installCommand":"true","uninstallCommand":"echo cannot remove >&2; exit 3"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("create app: %s", w.Body)
	}
	app := decode[store.App](t, w)

	w = do(t, h, "POST", fmt.Sprintf("/api/apps/%d/deploy", app.ID), fmt.Sprintf(`{"hostId":%d}`, hostID))
	waitForDeployment(t, h, decode[store.Deployment](t, w).ID)
	installs := decode[[]store.Installation](t, do(t, h, "GET", "/api/installations", ""))
	if len(installs) != 1 {
		t.Fatalf("installations = %d, want 1", len(installs))
	}

	w = do(t, h, "POST", fmt.Sprintf("/api/installations/%d/uninstall", installs[0].ID), "")
	if w.Code != http.StatusAccepted {
		t.Fatalf("uninstall status = %d, body %s", w.Code, w.Body)
	}
	removal := waitForDeployment(t, h, decode[store.Deployment](t, w).ID)
	if removal.Status != store.DeployFailed {
		t.Fatalf("status = %q, want failed", removal.Status)
	}
	if removal.ExitCode == nil || *removal.ExitCode != 3 {
		t.Errorf("exit code = %v, want 3", removal.ExitCode)
	}
	if got := decode[[]store.Installation](t, do(t, h, "GET", "/api/installations", "")); len(got) != 1 {
		t.Errorf("installations after a failed uninstall = %d, want it kept", len(got))
	}
}
