package forward

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chinmay28/hostman/server/internal/store"
)

func TestPlan(t *testing.T) {
	pi := &store.Host{ID: 1, Name: "photoframe", Address: "192.168.2.227", Port: 22, Username: "pi"}
	nas := &store.Host{ID: 2, Name: "nas", Address: "nas.local", Port: 22, Username: "admin"}
	apps := []*store.App{
		{ID: 1, Name: "photoframe", ForwardPort: 18080, ForwardTargetPort: 80},
		{ID: 2, Name: "unforwarded"},
		{ID: 3, Name: "nowhere", ForwardPort: 18081, ForwardTargetPort: 8080},
		{ID: 4, Name: "twice", ForwardPort: 18082, ForwardTargetPort: 8080},
		{ID: 5, Name: "clash", ForwardPort: 18080, ForwardTargetPort: 9000},
		{ID: 6, Name: "orphan", ForwardPort: 18083, ForwardTargetPort: 80},
	}
	installs := []*store.Installation{
		{AppID: 1, HostID: 1, HostName: "photoframe"},
		{AppID: 2, HostID: 1, HostName: "photoframe"},
		{AppID: 4, HostID: 1, HostName: "photoframe"},
		{AppID: 4, HostID: 2, HostName: "nas"},
		{AppID: 5, HostID: 2, HostName: "nas"},
		{AppID: 6, HostID: 99, HostName: "gone"},
	}
	rules, statuses := Plan(apps, installs, []*store.Host{pi, nas})

	if len(rules) != 1 {
		t.Fatalf("rules = %+v, want one for photoframe", rules)
	}
	if r := rules[0]; r.AppID != 1 || r.ListenPort != 18080 || r.TargetPort != 80 || r.Host.Address != "192.168.2.227" {
		t.Errorf("rule = %+v", r)
	}

	want := map[int64]struct{ state, detail string }{
		3: {StateWaiting, "not installed"},
		4: {StateError, "installed on 2 hosts (nas, photoframe)"},
		5: {StateError, "already forwarded for photoframe"},
		6: {StateError, "no longer known"},
	}
	if len(statuses) != len(want) {
		t.Fatalf("statuses = %+v", statuses)
	}
	for _, st := range statuses {
		w, ok := want[st.AppID]
		if !ok {
			t.Errorf("unexpected status for app %d: %+v", st.AppID, st)
			continue
		}
		if st.State != w.state || !strings.Contains(st.Detail, w.detail) {
			t.Errorf("app %d: state %q detail %q, want %q containing %q", st.AppID, st.State, st.Detail, w.state, w.detail)
		}
	}
}

// localTunnel stands in for an SSH tunnel: it dials on this machine, which is
// what "127.0.0.1:<port> on the host" means when the host is this machine.
type localTunnel struct {
	closed atomic.Bool
	fail   atomic.Bool // fail every dial, like a tunnel whose host went away
}

func (t *localTunnel) DialTCP(ctx context.Context, addr string) (net.Conn, error) {
	if t.closed.Load() {
		return nil, errors.New("tunnel closed")
	}
	if t.fail.Load() {
		return nil, errors.New("ssh: connection lost")
	}
	var d net.Dialer
	return d.DialContext(ctx, "tcp", addr)
}

func (t *localTunnel) Close() error { t.closed.Store(true); return nil }

type env struct {
	m        *Manager
	apps     []*store.App
	installs []*store.Installation
	hosts    []*store.Host

	mu      sync.Mutex
	tunnels []*localTunnel
	connErr error
}

func newEnv(t *testing.T) *env {
	t.Helper()
	e := &env{hosts: []*store.Host{{ID: 1, Name: "photoframe", Address: "127.0.0.1"}}}
	source := func(context.Context) ([]*store.App, []*store.Installation, []*store.Host, error) {
		e.mu.Lock()
		defer e.mu.Unlock()
		return e.apps, e.installs, e.hosts, nil
	}
	connect := func(context.Context, *store.Host) (Tunnel, error) {
		e.mu.Lock()
		defer e.mu.Unlock()
		if e.connErr != nil {
			return nil, e.connErr
		}
		lt := &localTunnel{}
		e.tunnels = append(e.tunnels, lt)
		return lt, nil
	}
	e.m = NewManager(source, connect, slog.New(slog.NewTextHandler(io.Discard, nil)))
	// Tests listen on loopback only.
	e.m.listen = func(port int) (net.Listener, error) {
		return net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(port))
	}
	t.Cleanup(e.m.Close)
	return e
}

func (e *env) setApp(id int64, listen, target int, installed bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.apps = []*store.App{{ID: id, Name: "photoframe", ForwardPort: listen, ForwardTargetPort: target}}
	e.installs = nil
	if installed {
		e.installs = []*store.Installation{{AppID: id, HostID: 1, HostName: "photoframe"}}
	}
}

func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

func appServer(t *testing.T, body string) int {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, body+r.URL.Path)
	}))
	t.Cleanup(srv.Close)
	u, _ := url.Parse(srv.URL)
	p, _ := strconv.Atoi(u.Port())
	return p
}

func get(t *testing.T, port int, path string) (string, error) {
	t.Helper()
	// A new TCP connection per request, so each one goes through the forward's
	// dial rather than riding an earlier one.
	c := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{DisableKeepAlives: true}}
	resp, err := c.Get(fmt.Sprintf("http://127.0.0.1:%d%s", port, path))
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	return string(b), err
}

func status(t *testing.T, m *Manager, appID int64) Status {
	t.Helper()
	for _, st := range m.Statuses() {
		if st.AppID == appID {
			return st
		}
	}
	t.Fatalf("no status for app %d in %+v", appID, m.Statuses())
	return Status{}
}

func TestForwardRelaysHTTP(t *testing.T) {
	e := newEnv(t)
	target := appServer(t, "photoframe")
	listen := freePort(t)
	e.setApp(1, listen, target, true)
	e.m.Reconcile(context.Background())

	if st := status(t, e.m, 1); st.State != StateListening || st.HostName != "photoframe" {
		t.Fatalf("status = %+v", st)
	}
	for _, path := range []string{"/", "/api/status", "/app.js"} {
		body, err := get(t, listen, path)
		if err != nil || body != "photoframe"+path {
			t.Fatalf("GET %s via forward = %q, %v", path, body, err)
		}
	}
	e.mu.Lock()
	n := len(e.tunnels)
	e.mu.Unlock()
	if n != 1 {
		t.Errorf("opened %d tunnels for three requests, want 1 reused", n)
	}
}

func TestForwardReplacesDeadTunnel(t *testing.T) {
	e := newEnv(t)
	target := appServer(t, "ok")
	listen := freePort(t)
	e.setApp(1, listen, target, true)
	e.m.Reconcile(context.Background())

	if _, err := get(t, listen, "/"); err != nil {
		t.Fatal(err)
	}
	e.mu.Lock()
	e.tunnels[0].fail.Store(true) // the host rebooted under the tunnel
	e.mu.Unlock()

	if body, err := get(t, listen, "/again"); err != nil || body != "ok/again" {
		t.Fatalf("after tunnel died: %q, %v", body, err)
	}
	e.mu.Lock()
	n := len(e.tunnels)
	e.mu.Unlock()
	if n != 2 {
		t.Errorf("tunnels = %d, want the dead one replaced by a second", n)
	}
}

func TestForwardReportsUnreachableHost(t *testing.T) {
	e := newEnv(t)
	listen := freePort(t)
	e.setApp(1, listen, 80, true)
	e.connErr = errors.New("connect to 192.168.2.227:22: no route to host")
	e.m.Reconcile(context.Background())

	if _, err := get(t, listen, "/"); err == nil {
		t.Fatal("request through an unreachable host succeeded")
	}
	st := status(t, e.m, 1)
	if st.State != StateListening || !strings.Contains(st.Detail, "no route to host") {
		t.Fatalf("status = %+v, want listening with the connect error", st)
	}
}

func TestReconcileFollowsSettings(t *testing.T) {
	e := newEnv(t)
	target := appServer(t, "v")
	first, second := freePort(t), freePort(t)

	e.setApp(1, first, target, true)
	e.m.Reconcile(context.Background())
	if _, err := get(t, first, "/"); err != nil {
		t.Fatal(err)
	}

	// Port changed in the app's settings: the old port closes, the new opens.
	e.setApp(1, second, target, true)
	e.m.Reconcile(context.Background())
	if _, err := get(t, second, "/"); err != nil {
		t.Fatalf("new port: %v", err)
	}
	if _, err := get(t, first, "/"); err == nil {
		t.Fatal("old port still forwarding")
	}

	// Uninstalled: nothing listens, and the status says why.
	e.setApp(1, second, target, false)
	e.m.Reconcile(context.Background())
	if _, err := get(t, second, "/"); err == nil {
		t.Fatal("still forwarding with no installation")
	}
	if st := status(t, e.m, 1); st.State != StateWaiting {
		t.Fatalf("status = %+v, want waiting", st)
	}

	// Forwarding turned off: no status at all.
	e.setApp(1, 0, 0, true)
	e.m.Reconcile(context.Background())
	if len(e.m.Statuses()) != 0 {
		t.Fatalf("statuses = %+v, want none", e.m.Statuses())
	}
}

func TestReconcileReportsBusyPortAndRetries(t *testing.T) {
	e := newEnv(t)
	target := appServer(t, "v")
	busy, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := busy.Addr().(*net.TCPAddr).Port
	e.setApp(1, port, target, true)
	e.m.Reconcile(context.Background())

	st := status(t, e.m, 1)
	if st.State != StateError || !strings.Contains(st.Detail, "already in use") {
		t.Fatalf("status = %+v, want port-in-use error", st)
	}

	busy.Close()
	e.m.Reconcile(context.Background())
	if st := status(t, e.m, 1); st.State != StateListening {
		t.Fatalf("after the port was freed: %+v", st)
	}
	if _, err := get(t, port, "/"); err != nil {
		t.Fatal(err)
	}
}

func TestRunKickAndShutdown(t *testing.T) {
	e := newEnv(t)
	target := appServer(t, "v")
	listen := freePort(t)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { e.m.Run(ctx); close(done) }()

	e.setApp(1, listen, target, true)
	e.m.Kick()
	deadline := time.Now().Add(3 * time.Second)
	for {
		if _, err := get(t, listen, "/"); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("kick did not start the forward")
		}
		time.Sleep(20 * time.Millisecond)
	}

	cancel()
	<-done
	if _, err := get(t, listen, "/"); err == nil {
		t.Fatal("forward still running after shutdown")
	}
}
