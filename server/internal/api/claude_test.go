package api

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/chinmay28/deployer/server/internal/claude"
)

// A Claude endpoint that could not be reached without a host on the other end
// would be untestable here, so what these cover is the layer above it: the
// handles, the codes, and the stream's shape. That a session itself works is
// proved in the claude package against a process that never leaves the test.

func openClaude(t *testing.T, s *Server, hostID int64) (string, *claude.Loopback) {
	t.Helper()
	sess, p := s.Claude.AdoptLoopback(hostID, "pi", claude.Options{Model: "sonnet"})
	t.Cleanup(func() { p.Close() })
	return sess.ID(), p
}

func TestClaudeHandlesAreOpaqueAndUnknownOnesAre404(t *testing.T) {
	_, h := testServer(t, "")
	for _, tc := range []struct{ name, method, path, body string }{
		{"reading one", "GET", "/api/claude/nosuch", ""},
		{"watching one", "GET", "/api/claude/nosuch/stream", ""},
		{"messaging one", "POST", "/api/claude/nosuch/message", `{"text":"hi"}`},
		{"answering one", "POST", "/api/claude/nosuch/answer", `{"requestId":"r","allow":true}`},
		{"changing its model", "POST", "/api/claude/nosuch/model", `{"model":"opus"}`},
		{"changing its mode", "POST", "/api/claude/nosuch/mode", `{"mode":"plan"}`},
		{"interrupting one", "POST", "/api/claude/nosuch/interrupt", ""},
		{"ending one", "DELETE", "/api/claude/nosuch", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if w := do(t, h, tc.method, tc.path, tc.body); w.Code != http.StatusNotFound {
				t.Fatalf("status = %d, want 404 (body %s)", w.Code, w.Body)
			}
		})
	}
}

func TestClaudeOnAMissingHostIs404(t *testing.T) {
	_, h := testServer(t, "")
	for _, tc := range []struct{ method, path, body string }{
		{"GET", "/api/hosts/999/claude", ""},
		{"POST", "/api/hosts/999/claude/install", ""},
		{"POST", "/api/hosts/999/claude/login", `{}`},
		{"POST", "/api/hosts/999/claude/login/code", `{"code":"abc"}`},
		{"POST", "/api/hosts/999/claude/key", `{"key":"sk-ant-x"}`},
		{"GET", "/api/hosts/999/claude/sessions", ""},
		{"POST", "/api/hosts/999/claude/sessions", `{}`},
	} {
		if w := do(t, h, tc.method, tc.path, tc.body); w.Code != http.StatusNotFound {
			t.Fatalf("%s %s = %d, want 404 (body %s)", tc.method, tc.path, w.Code, w.Body)
		}
	}
}

func TestListingClaudeSessionsOnAQuietHostIsAnEmptyList(t *testing.T) {
	_, h := testServer(t, "")
	id := newHost(t, h)
	w := do(t, h, "GET", fmt.Sprintf("/api/hosts/%d/claude/sessions", id), "")
	if w.Code != http.StatusOK || strings.TrimSpace(w.Body.String()) != "[]" {
		t.Fatalf("status = %d, body %s", w.Code, w.Body)
	}
}

func TestClaudeMessageReachesTheProcessAndTheHistory(t *testing.T) {
	s, h := testServer(t, "")
	id, p := openClaude(t, s, 1)

	w := do(t, h, "POST", "/api/claude/"+id+"/message", `{"text":"hello there"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", w.Code, w.Body)
	}
	info := decode[claude.Info](t, w)
	if !info.Busy || info.Name != "hello there" {
		t.Fatalf("info = %+v", info)
	}
	if line := p.Next(time.Second); !strings.Contains(line, `"content":"hello there"`) {
		t.Fatalf("the process got %q", line)
	}
	if w := do(t, h, "POST", "/api/claude/"+id+"/message", `{"text":"  "}`); w.Code != http.StatusBadGateway && w.Code != http.StatusBadRequest {
		t.Fatalf("an empty message = %d", w.Code)
	}
}

func TestClaudeAnswerCodes(t *testing.T) {
	s, h := testServer(t, "")
	id, p := openClaude(t, s, 1)
	p.Emit(`{"type":"control_request","request_id":"req_1","request":{"subtype":"can_use_tool","tool_name":"Bash","input":{"command":"ls"}}}`)
	deadline := time.Now().Add(2 * time.Second)
	for {
		sess, _ := s.Claude.Get(id)
		if sess.Info().Pending == 1 || time.Now().After(deadline) {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	if w := do(t, h, "POST", "/api/claude/"+id+"/answer", `{"allow":true}`); w.Code != http.StatusBadRequest {
		t.Fatalf("answer without an id = %d", w.Code)
	}
	w := do(t, h, "POST", "/api/claude/"+id+"/answer", `{"requestId":"req_1","allow":false,"reason":"not now"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", w.Code, w.Body)
	}
	if line := p.Next(time.Second); !strings.Contains(line, `"deny"`) || !strings.Contains(line, "not now") {
		t.Fatalf("the process got %q", line)
	}
	// Answered twice — from a second phone — is a conflict, not a mystery.
	if w := do(t, h, "POST", "/api/claude/"+id+"/answer", `{"requestId":"req_1","allow":true}`); w.Code != http.StatusConflict {
		t.Fatalf("second answer = %d, want 409 (body %s)", w.Code, w.Body)
	}
}

func TestClaudeModelChangeWaitsForTheCLI(t *testing.T) {
	s, h := testServer(t, "")
	id, p := openClaude(t, s, 1)
	go func() {
		line := p.Next(2 * time.Second)
		if i := strings.Index(line, `"request_id":"`); i >= 0 {
			rest := line[i+len(`"request_id":"`):]
			rid := rest[:strings.IndexByte(rest, '"')]
			p.Emit(`{"type":"control_response","response":{"subtype":"success","request_id":"` + rid + `"}}`)
		}
	}()
	w := do(t, h, "POST", "/api/claude/"+id+"/model", `{"model":"opus"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", w.Code, w.Body)
	}
	if decode[claude.Info](t, w).Model != "opus" {
		t.Fatal("the model did not change")
	}
	if w := do(t, h, "POST", "/api/claude/"+id+"/mode", `{"mode":"auto"}`); w.Code != http.StatusBadGateway {
		t.Fatalf("a mode HostMan does not offer = %d", w.Code)
	}
}

// The stream opens with the session, carries the history from the offset
// asked for, follows every batch with the session again, and ends with the
// exit when the process is gone.
func TestClaudeStreamShape(t *testing.T) {
	s, h := testServer(t, "")
	id, p := openClaude(t, s, 1)
	p.Emit(`{"type":"system","subtype":"init","model":"claude-sonnet-5","permissionMode":"default","cwd":"/home/pi"}`)
	p.Emit(`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"Hi."}]}}`)
	deadline := time.Now().Add(2 * time.Second)
	for {
		sess, _ := s.Claude.Get(id)
		if sess.Info().Offset == 2 || time.Now().After(deadline) {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	r := httptest.NewRequest("GET", "/api/claude/"+id+"/stream?from=1", nil).WithContext(ctx)
	w := httptest.NewRecorder()
	go func() {
		time.Sleep(100 * time.Millisecond)
		p.Exit(0, "")
	}()
	h.ServeHTTP(w, r)

	body := w.Body.String()
	if ct := w.Header().Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("content type = %q", ct)
	}
	if !strings.HasPrefix(body, "event: session\n") {
		t.Fatalf("stream does not open with the session:\n%s", body)
	}
	if !strings.Contains(body, `"from":1`) || strings.Contains(body, `"kind":"init"`) || !strings.Contains(body, `"kind":"assistant"`) {
		t.Fatalf("stream did not start from the offset asked for:\n%s", body)
	}
	if !strings.Contains(body, `"kind":"exit"`) || !strings.Contains(body, "event: exit\n") {
		t.Fatalf("stream did not end with the exit:\n%s", body)
	}
	if strings.Count(body, "event: session\n") < 2 {
		t.Fatalf("the session was not sent again after the entries:\n%s", body)
	}
}

func TestDeletingAHostEndsItsClaudeSessions(t *testing.T) {
	s, h := testServer(t, "")
	hostID := newHost(t, h)
	id, _ := openClaude(t, s, hostID)
	if w := do(t, h, "DELETE", fmt.Sprintf("/api/hosts/%d", hostID), ""); w.Code != http.StatusNoContent {
		t.Fatalf("delete host = %d", w.Code)
	}
	if w := do(t, h, "GET", "/api/claude/"+id, ""); w.Code != http.StatusNotFound {
		t.Fatalf("session after the host went = %d, want 404", w.Code)
	}
}

func TestClaudeKeyAndCodeAreCheckedBeforeTheHostIsAsked(t *testing.T) {
	_, h := testServer(t, "")
	id := newHost(t, h)
	// Neither of these reaches SSH: the shape is wrong, so the answer is a 400
	// at once rather than a connection attempt to a host that is not there.
	if w := do(t, h, "POST", fmt.Sprintf("/api/hosts/%d/claude/key", id), `{"key":"hunter2"}`); w.Code != http.StatusBadRequest {
		t.Fatalf("bad key = %d (body %s)", w.Code, w.Body)
	}
	if w := do(t, h, "POST", fmt.Sprintf("/api/hosts/%d/claude/login/code", id), `{"code":"rm -rf /"}`); w.Code != http.StatusBadRequest {
		t.Fatalf("bad code = %d (body %s)", w.Code, w.Body)
	}
}
