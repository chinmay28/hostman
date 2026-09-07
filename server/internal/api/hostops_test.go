package api

import (
	"fmt"
	"net/http"
	"testing"
)

// These cover the answers the API gives on its own — the ones that come back
// before anything connects to a host. What happens on the host is the hostops
// package's business, and is tested there against a real filesystem.

// newHost creates a host row to hang requests off.
func newHost(t *testing.T, h http.Handler) int64 {
	t.Helper()
	w := do(t, h, "POST", "/api/hosts", `{"name":"pi","address":"nakedpi.local","username":"chinmay"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("create host: %d %s", w.Code, w.Body)
	}
	return decode[hostView](t, w).ID
}

func TestFileRequestsValidateThePath(t *testing.T) {
	_, h := testServer(t, "")
	id := newHost(t, h)

	cases := []struct{ name, method, path, body string }{
		{"read without a path", "GET", "/api/hosts/%d/files/content", ""},
		{"read a relative path", "GET", "/api/hosts/%d/files/content?path=etc/hosts", ""},
		{"read a home-relative path", "GET", "/api/hosts/%d/files/content?path=~/.bashrc", ""},
		{"delete without a path", "DELETE", "/api/hosts/%d/files", ""},
		{"usage without a path", "GET", "/api/hosts/%d/files/usage", ""},
		{"usage of a relative path", "GET", "/api/hosts/%d/files/usage?path=pi-apps", ""},
		{"write without a path", "PUT", "/api/hosts/%d/files/content", `{"path":"","content":"x"}`},
		{"write a relative path", "PUT", "/api/hosts/%d/files/content", `{"path":"etc/hosts","content":"x"}`},
		{"mkdir with a relative path", "POST", "/api/hosts/%d/files/mkdir", `{"path":"tmp/x"}`},
		{"rename to a relative path", "POST", "/api/hosts/%d/files/rename", `{"path":"/tmp/a","to":"b"}`},
		{"rename from nothing", "POST", "/api/hosts/%d/files/rename", `{"path":"","to":"/tmp/b"}`},
		{"an unknown field", "POST", "/api/hosts/%d/files/mkdir", `{"path":"/tmp/x","mode":"755"}`},
		{"chmod with a relative path", "POST", "/api/hosts/%d/files/chmod", `{"path":"tmp/x","mode":"755"}`},
		{"chmod with no mode", "POST", "/api/hosts/%d/files/chmod", `{"path":"/tmp/x","mode":""}`},
		{"chmod with a symbolic mode", "POST", "/api/hosts/%d/files/chmod", `{"path":"/tmp/x","mode":"u+x"}`},
		{"chmod with a mode that is not octal", "POST", "/api/hosts/%d/files/chmod", `{"path":"/tmp/x","mode":"888"}`},
		{"chmod with a mode carrying a command", "POST", "/api/hosts/%d/files/chmod", `{"path":"/tmp/x","mode":"755;reboot"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := do(t, h, tc.method, fmt.Sprintf(tc.path, id), tc.body)
			if w.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400 (body %s)", w.Code, w.Body)
			}
			if decode[apiError](t, w).Error == "" {
				t.Error("a 400 should say what was wrong with the request")
			}
		})
	}
}

// The service screens name a unit in the query string or the body. Services and
// timers are what HostMan manages; anything else is the request's problem
// rather than the host's, and is refused before a connection is opened.
func TestServiceRequestsValidateTheUnitName(t *testing.T) {
	_, h := testServer(t, "")
	id := newHost(t, h)

	cases := []struct{ name, method, path, body string }{
		{"no name", "GET", "/api/hosts/%d/services/unit", ""},
		{"a socket", "GET", "/api/hosts/%d/services/unit?name=photos.socket", ""},
		{"a shell fragment", "GET", "/api/hosts/%d/services/unit?name=photos.service;reboot", ""},
		{"a path", "GET", "/api/hosts/%d/services/unit?name=../../etc/passwd", ""},
		{"logs without a name", "GET", "/api/hosts/%d/services/logs", ""},
		{"logs for a mount", "GET", "/api/hosts/%d/services/logs?name=x.mount", ""},
		{"an action with no unit", "POST", "/api/hosts/%d/services/action", `{"name":"","action":"start"}`},
		{"deleting with no name", "DELETE", "/api/hosts/%d/services", ""},
		{"deleting a target", "DELETE", "/api/hosts/%d/services?name=multi-user.target", ""},
		{"creating with no name", "POST", "/api/hosts/%d/services", `{"name":"","content":"[Unit]"}`},
		{"creating a template", "POST", "/api/hosts/%d/services", `{"name":"t@.service","content":"[Unit]"}`},
		{"creating an empty unit", "POST", "/api/hosts/%d/services", `{"name":"a.service","content":"  "}`},
		{"an unknown field", "POST", "/api/hosts/%d/services/action", `{"name":"a.service","action":"start","force":true}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := do(t, h, tc.method, fmt.Sprintf(tc.path, id), tc.body)
			if w.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400 (body %s)", w.Code, w.Body)
			}
			if decode[apiError](t, w).Error == "" {
				t.Error("a 400 should say what was wrong with the request")
			}
		})
	}
}

// HostMan asks systemd to start, stop and restart things. Masking a unit,
// isolating a target or powering the machine off from here are not on offer,
// and the API is where that stops rather than the UI.
func TestServiceActionsAreLimitedToWhatTheUIOffers(t *testing.T) {
	_, h := testServer(t, "")
	id := newHost(t, h)

	for _, action := range []string{"mask", "isolate", "kill", "poweroff", "edit", ""} {
		body := fmt.Sprintf(`{"name":"photos.service","action":%q}`, action)
		w := do(t, h, "POST", fmt.Sprintf("/api/hosts/%d/services/action", id), body)
		if w.Code != http.StatusBadRequest {
			t.Errorf("%q = %d, want 400 — it should never reach the host", action, w.Code)
		}
	}
}

// Restarting is the only power state there is: a host can be brought back from
// a reboot, and HostMan cannot bring one back from off.
func TestThereIsNoShutdownRoute(t *testing.T) {
	_, h := testServer(t, "")
	id := newHost(t, h)

	for _, path := range []string{"/api/hosts/%d/power", "/api/hosts/%d/shutdown"} {
		w := do(t, h, "POST", fmt.Sprintf(path, id), `{"action":"shutdown"}`)
		if w.Code != http.StatusNotFound {
			t.Errorf("POST %s = %d, want 404 — there is no such endpoint", path, w.Code)
		}
	}
}

// A host that does not exist is a 404 whatever is asked of it, and is answered
// without dialling anything.
func TestHostOperationsOnAMissingHost(t *testing.T) {
	_, h := testServer(t, "")
	cases := []struct{ method, path, body string }{
		{"POST", "/api/hosts/999/reboot", ""},
		{"GET", "/api/hosts/999/boot", ""},
		{"POST", "/api/hosts/999/boot/journal", ""},
		{"GET", "/api/hosts/999/cron", ""},
		{"PUT", "/api/hosts/999/cron", `{"user":"","content":""}`},
		{"GET", "/api/hosts/999/services", ""},
		{"POST", "/api/hosts/999/services", `{"name":"photos.service","content":"[Unit]"}`},
		{"DELETE", "/api/hosts/999/services?name=photos.service", ""},
		{"GET", "/api/hosts/999/services/unit?name=photos.service", ""},
		{"GET", "/api/hosts/999/services/logs?name=photos.service", ""},
		{"POST", "/api/hosts/999/services/action", `{"name":"photos.service","action":"restart"}`},
		{"POST", "/api/hosts/999/services/reload", ""},
		{"GET", "/api/hosts/999/files?path=/etc", ""},
		{"GET", "/api/hosts/999/files/content?path=/etc/hosts", ""},
		{"PUT", "/api/hosts/999/files/content", `{"path":"/tmp/x","content":"x"}`},
		{"POST", "/api/hosts/999/files/mkdir", `{"path":"/tmp/x"}`},
		{"POST", "/api/hosts/999/files/rename", `{"path":"/tmp/a","to":"/tmp/b"}`},
		{"DELETE", "/api/hosts/999/files?path=/tmp/x", ""},
	}
	for _, tc := range cases {
		w := do(t, h, tc.method, tc.path, tc.body)
		if w.Code != http.StatusNotFound {
			t.Errorf("%s %s = %d, want 404 (body %s)", tc.method, tc.path, w.Code, w.Body)
		}
	}
}

// Managing a host is behind the PIN like everything else.
func TestHostOperationsNeedTheSession(t *testing.T) {
	_, h := testServer(t, "1234")
	for _, path := range []string{
		"/api/hosts/1/files?path=/etc",
		"/api/hosts/1/files/content?path=/etc/hosts",
		"/api/hosts/1/cron",
		"/api/hosts/1/services",
		"/api/hosts/1/services/unit?name=photos.service",
		"/api/hosts/1/services/logs?name=photos.service",
		"/api/hosts/1/boot",
	} {
		if w := do(t, h, "GET", path, ""); w.Code != http.StatusUnauthorized {
			t.Errorf("GET %s without a session = %d, want 401", path, w.Code)
		}
	}
	if w := do(t, h, "POST", "/api/hosts/1/reboot", ""); w.Code != http.StatusUnauthorized {
		t.Errorf("reboot without a session = %d, want 401", w.Code)
	}
	if w := do(t, h, "POST", "/api/hosts/1/boot/journal", ""); w.Code != http.StatusUnauthorized {
		t.Errorf("turning on persistent logging without a session = %d, want 401", w.Code)
	}
	body := `{"name":"photos.service","action":"restart"}`
	if w := do(t, h, "POST", "/api/hosts/1/services/action", body); w.Code != http.StatusUnauthorized {
		t.Errorf("restarting a service without a session = %d, want 401", w.Code)
	}
	create := `{"name":"photos.service","content":"[Unit]"}`
	if w := do(t, h, "POST", "/api/hosts/1/services", create); w.Code != http.StatusUnauthorized {
		t.Errorf("creating a service without a session = %d, want 401", w.Code)
	}
	if w := do(t, h, "DELETE", "/api/hosts/1/services?name=photos.service", ""); w.Code != http.StatusUnauthorized {
		t.Errorf("deleting a service without a session = %d, want 401", w.Code)
	}
}
