package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

const testVM = "6c0a3a1e-5a7f-4c1c-9d3a-1f0b2c3d4e5f"

// An action reaches VBoxManage on somebody's machine, so what is wrong with a
// request is refused here, before anything connects.
func TestVMRequestsAreValidatedBeforeConnecting(t *testing.T) {
	_, h := testServer(t, "")
	id := newHost(t, h)

	cases := []struct{ name, body string }{
		{"an action nothing offers", `{"action":"destroy","id":"` + testVM + `"}`},
		{"no action", `{"id":"` + testVM + `"}`},
		{"no machine", `{"action":"start"}`},
		{"a name for a machine", `{"action":"start","id":"ubuntu"}`},
		{"an id carrying a command", `{"action":"start","id":"` + testVM + `; reboot"}`},
		{"an unknown field", `{"action":"start","id":"` + testVM + `","type":"gui"}`},
		{"not json", `start`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := do(t, h, "POST", fmt.Sprintf("/api/hosts/%d/vms/action", id), tc.body)
			if w.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400 (body %s)", w.Code, w.Body)
			}
			if decode[apiError](t, w).Error == "" {
				t.Error("a 400 should say what was wrong with the request")
			}
		})
	}

	// The refusal names what is offered, so a client can fix its request.
	w := do(t, h, "POST", fmt.Sprintf("/api/hosts/%d/vms/action", id), `{"action":"destroy","id":"`+testVM+`"}`)
	if msg := decode[apiError](t, w).Error; !strings.Contains(msg, "start, stop, poweroff") {
		t.Errorf("error = %q, want it to list the actions", msg)
	}
}

// A host that does not exist is a 404 whatever is asked about its machines,
// rather than an attempt to connect to nothing.
func TestVMRequestsNeedAHost(t *testing.T) {
	_, h := testServer(t, "")
	for _, tc := range []struct{ method, path, body string }{
		{"GET", "/api/hosts/999/vms", ""},
		{"POST", "/api/hosts/999/vms/action", `{"action":"start","id":"` + testVM + `"}`},
	} {
		w := do(t, h, tc.method, tc.path, tc.body)
		if w.Code != http.StatusNotFound {
			t.Errorf("%s %s = %d, want 404 (body %s)", tc.method, tc.path, w.Code, w.Body)
		}
	}
}

// The host record carries whether VirtualBox is there, so the UI can decide
// whether to offer the screen without asking the host again.
func TestHostSaysWhetherItHasVirtualBox(t *testing.T) {
	_, h := testServer(t, "")
	id := newHost(t, h)
	w := do(t, h, "GET", fmt.Sprintf("/api/hosts/%d", id), "")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", w.Code, w.Body)
	}
	var view struct {
		VirtualBox *bool `json:"virtualbox"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	if view.VirtualBox == nil {
		t.Fatalf("a host should always say whether it has VirtualBox: %s", w.Body)
	}
	if *view.VirtualBox {
		t.Error("a host never probed should not claim to have VirtualBox")
	}
}
