package api

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/chinmay28/hostman/server/internal/forward"
	"github.com/chinmay28/hostman/server/internal/store"
)

func TestAppForwardSettings(t *testing.T) {
	s, h := testServer(t, "")
	s.Port = 8899

	w := do(t, h, "POST", "/api/apps",
		`{"name":"photoframe","installCommand":"curl x | sudo bash","forwardPort":18080,"forwardTargetPort":80}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}
	app := decode[store.App](t, w)
	if app.ForwardPort != 18080 || app.ForwardTargetPort != 80 {
		t.Fatalf("created forward = %d -> %d", app.ForwardPort, app.ForwardTargetPort)
	}

	// A PATCH that doesn't mention forwarding leaves it alone.
	w = do(t, h, "PATCH", "/api/apps/1", `{"description":"e-ink frame"}`)
	if got := decode[store.App](t, w); got.ForwardPort != 18080 || got.ForwardTargetPort != 80 {
		t.Fatalf("forward after unrelated edit = %d -> %d", got.ForwardPort, got.ForwardTargetPort)
	}

	// Turning it off.
	w = do(t, h, "PATCH", "/api/apps/1", `{"forwardPort":0,"forwardTargetPort":0}`)
	if got := decode[store.App](t, w); w.Code != http.StatusOK || got.ForwardPort != 0 {
		t.Fatalf("turn off: %d %s", w.Code, w.Body.String())
	}
}

func TestAppForwardValidation(t *testing.T) {
	s, h := testServer(t, "")
	s.Port = 8899
	if w := do(t, h, "POST", "/api/apps",
		`{"name":"first","installCommand":"true","forwardPort":18080,"forwardTargetPort":80}`); w.Code != http.StatusCreated {
		t.Fatalf("setup: %d %s", w.Code, w.Body.String())
	}

	cases := map[string]struct{ body, want string }{
		"no app port":       {`"forwardPort":18081`, "app port"},
		"no HostMan port":   {`"forwardTargetPort":80`, "HostMan port"},
		"out of range":      {`"forwardPort":70000,"forwardTargetPort":80`, "between 1 and 65535"},
		"HostMan's own":     {`"forwardPort":8899,"forwardTargetPort":80`, "HostMan's own web port"},
		"taken by an app":   {`"forwardPort":18080,"forwardTargetPort":81`, "already forwarded for first"},
		"negative app port": {`"forwardPort":18082,"forwardTargetPort":-1`, "app port"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			w := do(t, h, "POST", "/api/apps", `{"name":"second","installCommand":"true",`+tc.body+`}`)
			if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), tc.want) {
				t.Fatalf("got %d %s, want 400 mentioning %q", w.Code, w.Body.String(), tc.want)
			}
		})
	}

	// The app that owns a port can keep it when saved again.
	if w := do(t, h, "PATCH", "/api/apps/1", `{"forwardPort":18080,"forwardTargetPort":8080}`); w.Code != http.StatusOK {
		t.Fatalf("re-saving its own port: %d %s", w.Code, w.Body.String())
	}
}

func TestListForwards(t *testing.T) {
	s, h := testServer(t, "")

	// Without a manager the endpoint still answers, with nothing.
	if got := decode[[]forward.Status](t, do(t, h, "GET", "/api/forwards", "")); len(got) != 0 {
		t.Fatalf("no manager: %+v", got)
	}

	s.Forwards = forward.NewManager(forward.DBSource(s.DB), nil, s.Log)
	t.Cleanup(s.Forwards.Close)
	do(t, h, "POST", "/api/apps", `{"name":"photoframe","installCommand":"true","forwardPort":18080,"forwardTargetPort":80}`)
	s.Forwards.Reconcile(context.Background())

	got := decode[[]forward.Status](t, do(t, h, "GET", "/api/forwards", ""))
	if len(got) != 1 || got[0].State != forward.StateWaiting || got[0].ListenPort != 18080 {
		t.Fatalf("forwards = %+v, want photoframe waiting for an installation", got)
	}
}
