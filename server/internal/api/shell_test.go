package api

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/chinmay28/deployer/server/internal/shell"
)

// A shell endpoint that could not be reached without a host on the other end
// would be untestable here, so what these cover is the layer above it: the
// handles, the encoding, the codes, and the stream's shape. That the shell
// itself works is proved in the shell package, against a real sshd.

// openShell puts a live session into the manager without any SSH, and returns
// its id together with the terminal driving it. The host id matches the one
// newHost creates, so a test can remove the host out from under the shell.
func openShell(t *testing.T, s *Server, hostID int64) (string, *shell.Loopback) {
	t.Helper()
	sess, term := s.Shells.AdoptLoopback(hostID, "pi", 80, 24)
	t.Cleanup(func() { term.Close() })
	return sess.ID(), term
}

func TestShellHandlesAreOpaqueAndUnknownOnesAre404(t *testing.T) {
	_, h := testServer(t, "")
	cases := []struct{ name, method, path, body string }{
		{"reading one", "GET", "/api/shell/nosuchsession", ""},
		{"watching one", "GET", "/api/shell/nosuchsession/stream", ""},
		{"typing at one", "POST", "/api/shell/nosuchsession/input", `{"data":"bHM="}`},
		{"resizing one", "POST", "/api/shell/nosuchsession/resize", `{"cols":80,"rows":24}`},
		{"closing one", "DELETE", "/api/shell/nosuchsession", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := do(t, h, tc.method, tc.path, tc.body)
			if w.Code != http.StatusNotFound {
				t.Fatalf("status = %d, want 404 (body %s)", w.Code, w.Body)
			}
		})
	}
}

// A host that does not exist is a 404 rather than an attempt to connect to it.
func TestShellsOnAMissingHostAre404(t *testing.T) {
	_, h := testServer(t, "")
	for _, tc := range []struct{ method, path, body string }{
		{"GET", "/api/hosts/999/shell", ""},
		{"POST", "/api/hosts/999/shell", `{"cols":80,"rows":24}`},
	} {
		w := do(t, h, tc.method, tc.path, tc.body)
		if w.Code != http.StatusNotFound {
			t.Fatalf("%s %s = %d, want 404 (body %s)", tc.method, tc.path, w.Code, w.Body)
		}
	}
}

// A host with no shells answers with an empty list rather than null, so the
// screen can map over it without a guard.
func TestListingShellsOnAQuietHostIsAnEmptyList(t *testing.T) {
	_, h := testServer(t, "")
	id := newHost(t, h)
	w := do(t, h, "GET", fmt.Sprintf("/api/hosts/%d/shell", id), "")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", w.Code, w.Body)
	}
	if got := strings.TrimSpace(w.Body.String()); got != "[]" {
		t.Fatalf("body = %s, want []", got)
	}
}

func TestShellInputMustBeBase64(t *testing.T) {
	s, h := testServer(t, "")
	id, _ := openShell(t, s, 1)

	w := do(t, h, "POST", "/api/shell/"+id+"/input", `{"data":"not base64!"}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body %s)", w.Code, w.Body)
	}
	if got := decode[apiError](t, w).Error; !strings.Contains(got, "base64") {
		t.Fatalf("error = %q, want it to say what the encoding is", got)
	}
}

// The bytes that matter most at a terminal are not text, so the round trip has
// to carry them exactly: Ctrl-C, an escape, and an arrow key's sequence.
func TestShellInputCarriesControlBytes(t *testing.T) {
	s, h := testServer(t, "")
	id, term := openShell(t, s, 1)

	keys := []byte{0x03, 0x1b, 0x1b, '[', 'A', '\t', '\r'}
	body := fmt.Sprintf(`{"data":%q}`, base64.StdEncoding.EncodeToString(keys))
	w := do(t, h, "POST", "/api/shell/"+id+"/input", body)
	if w.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204 (body %s)", w.Code, w.Body)
	}
	if got := term.Keystrokes(); string(got) != string(keys) {
		t.Fatalf("the shell got % x, want % x", got, keys)
	}
}

func TestShellResizeAnswersWithTheSizeItTook(t *testing.T) {
	s, h := testServer(t, "")
	id, term := openShell(t, s, 1)

	w := do(t, h, "POST", "/api/shell/"+id+"/resize", `{"cols":132,"rows":43}`)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", w.Code, w.Body)
	}
	info := decode[shell.Info](t, w)
	if info.Cols != 132 || info.Rows != 43 {
		t.Fatalf("session says %dx%d, want 132x43", info.Cols, info.Rows)
	}
	if cols, rows := term.Size(); cols != 132 || rows != 43 {
		t.Fatalf("the terminal is %dx%d, want 132x43", cols, rows)
	}
}

// An absurd geometry is clamped rather than refused: a screen that measured
// itself wrong should get a small terminal, not a broken one.
func TestShellResizeClampsRatherThanRefusing(t *testing.T) {
	s, h := testServer(t, "")
	id, _ := openShell(t, s, 1)

	w := do(t, h, "POST", "/api/shell/"+id+"/resize", `{"cols":0,"rows":-4}`)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", w.Code, w.Body)
	}
	if info := decode[shell.Info](t, w); info.Cols < 1 || info.Rows < 1 {
		t.Fatalf("session says %dx%d", info.Cols, info.Rows)
	}
}

// The stream leads with the session, so a client that reconnected into a shell
// somebody has since resized can correct itself before drawing anything.
func TestShellStreamLeadsWithTheSessionThenTheScreen(t *testing.T) {
	s, h := testServer(t, "")
	id, term := openShell(t, s, 1)
	term.Print("pi@raspberrypi:~$ ")

	body := streamUntil(t, h, "/api/shell/"+id+"/stream", "event: out")
	if !strings.HasPrefix(body, "event: session\n") {
		t.Fatalf("the stream opened with %q", firstEvent(body))
	}
	want := base64.StdEncoding.EncodeToString([]byte("pi@raspberrypi:~$ "))
	if !strings.Contains(body, want) {
		t.Fatalf("the screen did not arrive base64-encoded in %q", body)
	}
}

// Reconnecting from an offset replays what was missed and not what was seen,
// which is what makes locking a phone mid-command survivable.
func TestShellStreamResumesFromAnOffset(t *testing.T) {
	s, h := testServer(t, "")
	id, term := openShell(t, s, 1)
	term.Print("seen already")
	seen := int64(len("seen already"))
	term.Print("missed while away")

	body := streamUntil(t, h, fmt.Sprintf("/api/shell/%s/stream?from=%d", id, seen), "event: out")
	if got := base64.StdEncoding.EncodeToString([]byte("seen already")); strings.Contains(body, got) {
		t.Fatalf("the stream replayed what the client had already seen: %q", body)
	}
	want := base64.StdEncoding.EncodeToString([]byte("missed while away"))
	if !strings.Contains(body, want) {
		t.Fatalf("the stream did not carry what was missed: %q", body)
	}
}

// A shell that has exited ends its stream with a reason rather than hanging on
// a connection that will never say anything again.
func TestShellStreamEndsWhenTheShellDoes(t *testing.T) {
	s, h := testServer(t, "")
	id, term := openShell(t, s, 1)
	term.Exit(0)

	body := streamUntil(t, h, "/api/shell/"+id+"/stream", "event: exit")
	if !strings.Contains(body, "the shell exited") {
		t.Fatalf("the stream ended with %q", body)
	}
}

// Typing at a shell that has exited is a conflict, not a server error: it is a
// thing that has happened, and the screen can say so.
func TestTypingAtAnExitedShellIsAConflict(t *testing.T) {
	s, h := testServer(t, "")
	id, term := openShell(t, s, 1)
	term.Exit(0)
	waitFor(t, func() bool { return !mustSession(t, s, id).Running() })

	w := do(t, h, "POST", "/api/shell/"+id+"/input", `{"data":"bHM="}`)
	if w.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409 (body %s)", w.Code, w.Body)
	}
}

// Removing a host ends the shells on it: a terminal typing at a machine
// HostMan no longer admits to knowing is the worst of both.
func TestRemovingAHostClosesItsShells(t *testing.T) {
	s, h := testServer(t, "")
	hostID := newHost(t, h)
	id, _ := openShell(t, s, hostID)

	if w := do(t, h, "DELETE", fmt.Sprintf("/api/hosts/%d", hostID), ""); w.Code != http.StatusNoContent {
		t.Fatalf("delete host = %d, body %s", w.Code, w.Body)
	}
	if w := do(t, h, "GET", "/api/shell/"+id, ""); w.Code != http.StatusNotFound {
		t.Fatalf("the shell survived its host: %d", w.Code)
	}
}

// --- helpers ---

func mustSession(t *testing.T, s *Server, id string) *shell.Session {
	t.Helper()
	sess, err := s.Shells.Get(id)
	if err != nil {
		t.Fatalf("get session: %v", err)
	}
	return sess
}

func waitFor(t *testing.T, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if ok() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("timed out waiting for the session to settle")
}

// streamUntil reads an SSE response until the marker has been seen, then hangs
// up — which is what a phone does, and the only way to end a stream that is
// meant to stay open.
//
// It goes through a real server rather than a recorder: a recorder's body is
// only safe to read once the handler has returned, and a stream that returns is
// no longer the thing under test.
func streamUntil(t *testing.T, h http.Handler, path, marker string) string {
	t.Helper()
	srv := httptest.NewServer(h)
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", srv.URL+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("open the stream: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("stream status = %d", resp.StatusCode)
	}

	var seen strings.Builder
	buf := make([]byte, 4<<10)
	for {
		n, err := resp.Body.Read(buf)
		seen.Write(buf[:n])
		if strings.Contains(seen.String(), marker) {
			return seen.String()
		}
		if err != nil {
			t.Fatalf("%q never appeared in the stream: %q (%v)", marker, seen.String(), err)
		}
	}
}

func firstEvent(body string) string {
	if i := strings.Index(body, "\n"); i >= 0 {
		return body[:i]
	}
	return body
}
