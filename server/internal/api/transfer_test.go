package api

import (
	"fmt"
	"net/http"
	"testing"
)

// The checks made before a job is started: paths, the destination host, and
// the session. What a job does on a host is the hostops package's business.

func TestTransferRequestsValidateBeforeStarting(t *testing.T) {
	_, h := testServer(t, "")
	id := newHost(t, h)

	cases := []struct{ name, path, body string }{
		{"copy without a path", "/api/hosts/%d/files/copy", `{"path":"","to":"/tmp"}`},
		{"copy with a relative path", "/api/hosts/%d/files/copy", `{"path":"x","to":"/tmp"}`},
		{"copy with a relative destination", "/api/hosts/%d/files/copy", `{"path":"/media/x","to":"tmp"}`},
		{"copy into itself", "/api/hosts/%d/files/copy", `{"path":"/media/x","to":"/media/x/y"}`},
		{"copy of the root", "/api/hosts/%d/files/copy", `{"path":"/","to":"/tmp"}`},
		{"move without a destination", "/api/hosts/%d/files/move", `{"path":"/media/x"}`},
		{"move where it already is", "/api/hosts/%d/files/move", `{"path":"/media/x","to":"/media"}`},
		{"send without a host", "/api/hosts/%d/files/send", `{"path":"/media/x","to":"/tmp"}`},
		{"send to itself", "/api/hosts/%d/files/send", `{"path":"/media/x","to":"/tmp","hostId":%d}`},
		{"an unknown field", "/api/hosts/%d/files/copy", `{"path":"/media/x","to":"/tmp","force":true}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := tc.body
			if tc.name == "send to itself" {
				body = fmt.Sprintf(tc.body, id)
			}
			w := do(t, h, "POST", fmt.Sprintf(tc.path, id), body)
			if w.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400 (body %s)", w.Code, w.Body)
			}
			if decode[apiError](t, w).Error == "" {
				t.Error("a 400 should say what was wrong with the request")
			}
		})
	}
	if w := do(t, h, "GET", fmt.Sprintf("/api/hosts/%d/files/jobs", id), ""); w.Code != http.StatusOK || w.Body.String() != "[]\n" {
		t.Errorf("after refused requests the job list = %d %q, want an empty array", w.Code, w.Body)
	}
}

func TestTransferRequestsNeedTheHosts(t *testing.T) {
	_, h := testServer(t, "")
	id := newHost(t, h)

	for _, tc := range []struct{ method, path, body string }{
		{"POST", "/api/hosts/999/files/copy", `{"path":"/media/x","to":"/tmp"}`},
		{"POST", "/api/hosts/999/files/move", `{"path":"/media/x","to":"/tmp"}`},
		{"POST", "/api/hosts/999/files/send", `{"path":"/media/x","to":"/tmp","hostId":1}`},
		{"GET", "/api/hosts/999/files/jobs", ""},
		{"POST", fmt.Sprintf("/api/hosts/%d/files/send", id), `{"path":"/media/x","to":"/tmp","hostId":999}`},
		{"GET", "/api/files/jobs/nope", ""},
		{"POST", "/api/files/jobs/nope/cancel", ""},
		{"DELETE", "/api/files/jobs/nope", ""},
	} {
		if w := do(t, h, tc.method, tc.path, tc.body); w.Code != http.StatusNotFound {
			t.Errorf("%s %s = %d, want 404 (body %s)", tc.method, tc.path, w.Code, w.Body)
		}
	}
}

// A request that passes the checks becomes a job at once, answered 202, and
// can be found by its id and under its host. This one cannot connect — the
// host is a closed port on localhost — so it ends as a failed job rather than
// hanging around.
func TestCopyStartsAJob(t *testing.T) {
	_, h := testServer(t, "")
	w := do(t, h, "POST", "/api/hosts", `{"name":"pi","address":"127.0.0.1","port":1,"username":"chinmay"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("create host: %d %s", w.Code, w.Body)
	}
	id := decode[hostView](t, w).ID

	w = do(t, h, "POST", fmt.Sprintf("/api/hosts/%d/files/copy", id), `{"path":"/media/x","to":"/tmp"}`)
	if w.Code != http.StatusAccepted {
		t.Fatalf("copy = %d, want 202 (body %s)", w.Code, w.Body)
	}
	job := decode[map[string]any](t, w)
	jid, _ := job["id"].(string)
	if jid == "" || job["kind"] != "copy" || job["path"] != "/media/x" || job["to"] != "/tmp" {
		t.Fatalf("job = %v", job)
	}
	if w := do(t, h, "GET", "/api/files/jobs/"+jid, ""); w.Code != http.StatusOK {
		t.Errorf("GET job = %d", w.Code)
	}
	if w := do(t, h, "GET", fmt.Sprintf("/api/hosts/%d/files/jobs", id), ""); w.Code != http.StatusOK || len(decode[[]map[string]any](t, w)) != 1 {
		t.Errorf("the host's job list = %d %s, want the one job", w.Code, w.Body)
	}
	// Cancelling is always allowed; it answers with the job as it now is.
	if w := do(t, h, "POST", "/api/files/jobs/"+jid+"/cancel", ""); w.Code != http.StatusOK {
		t.Errorf("cancel = %d %s", w.Code, w.Body)
	}
}

func TestTransfersNeedTheSession(t *testing.T) {
	_, h := testServer(t, "1234")
	for _, tc := range []struct{ method, path, body string }{
		{"POST", "/api/hosts/1/files/copy", `{"path":"/media/x","to":"/tmp"}`},
		{"POST", "/api/hosts/1/files/move", `{"path":"/media/x","to":"/tmp"}`},
		{"POST", "/api/hosts/1/files/send", `{"path":"/media/x","to":"/tmp","hostId":2}`},
		{"GET", "/api/hosts/1/files/jobs", ""},
		{"GET", "/api/files/jobs/x", ""},
		{"POST", "/api/files/jobs/x/cancel", ""},
		{"DELETE", "/api/files/jobs/x", ""},
	} {
		if w := do(t, h, tc.method, tc.path, tc.body); w.Code != http.StatusUnauthorized {
			t.Errorf("%s %s without a session = %d, want 401", tc.method, tc.path, w.Code)
		}
	}
}
