package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

// A pull or an action reaches ollama on somebody's machine, so what is wrong
// with a request is refused here, before anything connects.
func TestOllamaRequestsAreValidatedBeforeConnecting(t *testing.T) {
	_, h := testServer(t, "")
	id := newHost(t, h)

	cases := []struct{ name, path, body string }{
		{"a pull of nothing", "/api/hosts/%d/ollama/pull", `{}`},
		{"a pull of an option", "/api/hosts/%d/ollama/pull", `{"model":"--insecure"}`},
		{"a pull with a space in it", "/api/hosts/%d/ollama/pull", `{"model":"llama 3"}`},
		{"a pull carrying a command", "/api/hosts/%d/ollama/pull", `{"model":"llama3; reboot"}`},
		{"a pull with an unknown field", "/api/hosts/%d/ollama/pull", `{"model":"llama3","insecure":true}`},
		{"a pull that is not json", "/api/hosts/%d/ollama/pull", `llama3`},

		{"an action nothing offers", "/api/hosts/%d/ollama/action", `{"action":"serve"}`},
		{"the ollama verb rather than ours", "/api/hosts/%d/ollama/action", `{"action":"rm","model":"llama3"}`},
		{"no action", "/api/hosts/%d/ollama/action", `{"model":"llama3"}`},
		{"removing nothing", "/api/hosts/%d/ollama/action", `{"action":"remove"}`},
		{"removing a quoted name", "/api/hosts/%d/ollama/action", `{"action":"remove","model":"\"llama3\""}`},
		{"stopping a name carrying a command", "/api/hosts/%d/ollama/action", `{"action":"stop","model":"llama3; reboot"}`},
		{"an unknown field", "/api/hosts/%d/ollama/action", `{"action":"stop","model":"llama3","force":true}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := do(t, h, "POST", fmt.Sprintf(tc.path, id), tc.body)
			if w.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400 (body %s)", w.Code, w.Body)
			}
			if decode[apiError](t, w).Error == "" {
				t.Error("a 400 should say what was wrong with the request")
			}
		})
	}

	// The refusal names what is offered, so a client can fix its request.
	w := do(t, h, "POST", fmt.Sprintf("/api/hosts/%d/ollama/action", id), `{"action":"serve"}`)
	if msg := decode[apiError](t, w).Error; !strings.Contains(msg, "remove, stop, start") {
		t.Errorf("error = %q, want it to list the actions", msg)
	}
	w = do(t, h, "POST", fmt.Sprintf("/api/hosts/%d/ollama/pull", id), `{"model":"llama 3"}`)
	if msg := decode[apiError](t, w).Error; !strings.Contains(msg, "name:tag") {
		t.Errorf("error = %q, want it to say how a model is named", msg)
	}
}

// A host that does not exist is a 404 whatever is asked about its models,
// rather than an attempt to connect to nothing.
func TestOllamaRequestsNeedAHost(t *testing.T) {
	_, h := testServer(t, "")
	for _, tc := range []struct{ method, path, body string }{
		{"GET", "/api/hosts/999/ollama", ""},
		{"POST", "/api/hosts/999/ollama/pull", `{"model":"llama3.2"}`},
		{"POST", "/api/hosts/999/ollama/action", `{"action":"start"}`},
	} {
		w := do(t, h, tc.method, tc.path, tc.body)
		if w.Code != http.StatusNotFound {
			t.Errorf("%s %s = %d, want 404 (body %s)", tc.method, tc.path, w.Code, w.Body)
		}
	}
}

// The host record carries whether ollama is there, so the UI can decide
// whether to offer the screen without asking the host again.
func TestHostSaysWhetherItHasOllama(t *testing.T) {
	_, h := testServer(t, "")
	id := newHost(t, h)
	w := do(t, h, "GET", fmt.Sprintf("/api/hosts/%d", id), "")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", w.Code, w.Body)
	}
	var view struct {
		Ollama *bool `json:"ollama"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	if view.Ollama == nil {
		t.Fatalf("a host should always say whether it has ollama: %s", w.Body)
	}
	if *view.Ollama {
		t.Error("a host never probed should not claim to have ollama")
	}
}

// A line typed into a new shell is one line: a second would be a second
// command run behind the person's back, and a control byte a key pressed in
// a terminal they are not looking at yet.
func TestShellOpenRefusesACommandThatIsNotOneLine(t *testing.T) {
	_, h := testServer(t, "")
	id := newHost(t, h)
	for name, body := range map[string]string{
		"two lines":         `{"cols":80,"rows":24,"command":"ollama run llama3\nrm -rf /"}`,
		"a control byte":    `{"cols":80,"rows":24,"command":"ollama run llama3\u0003"}`,
		"a carriage return": `{"cols":80,"rows":24,"command":"ollama run llama3\r"}`,
		"far too long":      `{"cols":80,"rows":24,"command":"` + strings.Repeat("a", 600) + `"}`,
	} {
		t.Run(name, func(t *testing.T) {
			w := do(t, h, "POST", fmt.Sprintf("/api/hosts/%d/shell", id), body)
			if w.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400 (body %s)", w.Code, w.Body)
			}
		})
	}
	if err := checkShellCommand("ollama run llama3.2:3b"); err != nil {
		t.Errorf("a plain command was refused: %v", err)
	}
	if err := checkShellCommand(""); err != nil {
		t.Errorf("no command was refused: %v", err)
	}
}
