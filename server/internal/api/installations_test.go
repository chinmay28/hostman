package api

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/chinmay28/deployer/server/internal/store"
)

// installed puts an app on a host without going near SSH, which is all these
// tests need: ports are read off what the app declares, not off the machine.
func installed(t *testing.T, s *Server, app *store.App) *store.Installation {
	t.Helper()
	ctx := context.Background()
	created, err := s.DB.CreateApp(ctx, app)
	if err != nil {
		t.Fatal(err)
	}
	host, err := s.DB.CreateHost(ctx, &store.Host{
		Name: "nakedpi", Address: "nakedpi.local", Port: 22, Username: "pi",
	})
	if err != nil {
		t.Fatal(err)
	}
	values := map[string]string{}
	for _, p := range created.Params {
		values[p.Name] = p.Default
	}
	dep, err := s.DB.CreateDeployment(ctx, &store.Deployment{
		AppID: created.ID, HostID: host.ID, Command: "true", Params: values,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.DB.UpsertInstallation(ctx, created.ID, host.ID, values, dep.ID); err != nil {
		t.Fatal(err)
	}
	in, err := s.DB.FindInstallation(ctx, created.ID, host.ID)
	if err != nil {
		t.Fatal(err)
	}
	return in
}

// The port an app answers on is never stored, so every response that carries
// an installation has to work it out again. Missing it on one of them is how
// the dashboard ends up disagreeing with the app's own page.
func TestInstallationResponsesCarryPorts(t *testing.T) {
	s, h := testServer(t, "")
	in := installed(t, s, &store.App{
		Name:           "photos",
		InstallCommand: "install --port {{port}}",
		Params:         []store.Param{{Name: "port", Label: "Port", Default: "8787"}},
		HealthType:     store.HealthHTTP,
		HealthTarget:   "http://{{host}}:{{port}}/healthz",
	})

	want := []int{8787}
	list := decode[[]store.Installation](t, do(t, h, "GET", "/api/installations", ""))
	if len(list) != 1 {
		t.Fatalf("installations = %d, want 1", len(list))
	}
	if fmt.Sprint(list[0].Ports) != fmt.Sprint(want) {
		t.Errorf("list ports = %v, want %v", list[0].Ports, want)
	}

	one := decode[store.Installation](t, do(t, h, "GET", fmt.Sprintf("/api/installations/%d", in.ID), ""))
	if fmt.Sprint(one.Ports) != fmt.Sprint(want) {
		t.Errorf("installation ports = %v, want %v", one.Ports, want)
	}

	ov := decode[overview](t, do(t, h, "GET", "/api/overview", ""))
	if len(ov.Installations) != 1 {
		t.Fatalf("overview installations = %d, want 1", len(ov.Installations))
	}
	if fmt.Sprint(ov.Installations[0].Ports) != fmt.Sprint(want) {
		t.Errorf("overview ports = %v, want %v", ov.Installations[0].Ports, want)
	}
}

// The link that opens an app is worked out the same way its ports are, so it
// has to appear in the same three places — the tab, the app's page and the
// dashboard all offer the button.
func TestInstallationResponsesCarryURL(t *testing.T) {
	s, h := testServer(t, "")
	in := installed(t, s, &store.App{
		Name:           "photos",
		InstallCommand: "install --port {{port}}",
		Params:         []store.Param{{Name: "port", Label: "Port", Default: "8787"}},
		HealthType:     store.HealthHTTP,
		HealthTarget:   "http://{{host}}:{{port}}/healthz",
	})

	want := "http://nakedpi.local:8787/"
	list := decode[[]store.Installation](t, do(t, h, "GET", "/api/installations", ""))
	if len(list) != 1 {
		t.Fatalf("installations = %d, want 1", len(list))
	}
	if list[0].URL != want {
		t.Errorf("list url = %q, want %q", list[0].URL, want)
	}

	one := decode[store.Installation](t, do(t, h, "GET", fmt.Sprintf("/api/installations/%d", in.ID), ""))
	if one.URL != want {
		t.Errorf("installation url = %q, want %q", one.URL, want)
	}

	ov := decode[overview](t, do(t, h, "GET", "/api/overview", ""))
	if len(ov.Installations) != 1 {
		t.Fatalf("overview installations = %d, want 1", len(ov.Installations))
	}
	if ov.Installations[0].URL != want {
		t.Errorf("overview url = %q, want %q", ov.Installations[0].URL, want)
	}
}

// The version an app is running is worked out from the parameters it was
// deployed with, so like its ports it has to be recomputed on every response
// that carries an installation.
func TestInstallationResponsesCarryVersion(t *testing.T) {
	s, h := testServer(t, "")
	in := installed(t, s, &store.App{
		Name:           "photos",
		InstallCommand: "install --ref {{ref}} --port {{port}}",
		Params: []store.Param{
			{Name: "ref", Label: "Version", Default: "v1.4.0"},
			{Name: "port", Label: "Port", Default: "8787"},
		},
		HealthType:   store.HealthHTTP,
		HealthTarget: "http://{{host}}:{{port}}/healthz",
	})

	want := "v1.4.0"
	list := decode[[]store.Installation](t, do(t, h, "GET", "/api/installations", ""))
	if len(list) != 1 {
		t.Fatalf("installations = %d, want 1", len(list))
	}
	if list[0].Version != want {
		t.Errorf("list version = %q, want %q", list[0].Version, want)
	}

	one := decode[store.Installation](t, do(t, h, "GET", fmt.Sprintf("/api/installations/%d", in.ID), ""))
	if one.Version != want {
		t.Errorf("installation version = %q, want %q", one.Version, want)
	}

	ov := decode[overview](t, do(t, h, "GET", "/api/overview", ""))
	if len(ov.Installations) != 1 {
		t.Fatalf("overview installations = %d, want 1", len(ov.Installations))
	}
	if ov.Installations[0].Version != want {
		t.Errorf("overview version = %q, want %q", ov.Installations[0].Version, want)
	}
}

// An app that says nothing about ports must not have one invented for it.
func TestInstallationWithoutPortsSaysNothing(t *testing.T) {
	s, h := testServer(t, "")
	installed(t, s, &store.App{
		Name:           "backup",
		InstallCommand: "install",
		HealthType:     store.HealthSystemd,
		HealthTarget:   "backup.service",
	})

	w := do(t, h, "GET", "/api/installations", "")
	list := decode[[]store.Installation](t, w)
	if len(list) != 1 {
		t.Fatalf("installations = %d, want 1", len(list))
	}
	if len(list[0].Ports) != 0 {
		t.Errorf("ports = %v, want none", list[0].Ports)
	}
	if list[0].URL != "" {
		t.Errorf("url = %q, want none", list[0].URL)
	}
	if list[0].Version != "" {
		t.Errorf("version = %q, want none", list[0].Version)
	}
	// The fields stay off the wire entirely rather than going out empty.
	if strings.Contains(w.Body.String(), `"ports"`) {
		t.Errorf("response names ports for an app that has none: %s", w.Body)
	}
	if strings.Contains(w.Body.String(), `"url"`) {
		t.Errorf("response names a url for an app HostMan cannot place: %s", w.Body)
	}
	if strings.Contains(w.Body.String(), `"version"`) {
		t.Errorf("response names a version for an app that pins none: %s", w.Body)
	}
}
