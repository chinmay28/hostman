package forward

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"sort"
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/chinmay28/hostman/server/internal/store"
)

// Tunnel opens connections from a host. *sshx.Client is one.
type Tunnel interface {
	DialTCP(ctx context.Context, addr string) (net.Conn, error)
	Close() error
}

// Connector opens a tunnel to a host.
type Connector func(ctx context.Context, h *store.Host) (Tunnel, error)

// Source lists what Plan needs.
type Source func(ctx context.Context) ([]*store.App, []*store.Installation, []*store.Host, error)

const (
	// reconcileInterval bounds how long a new installation, a changed host or
	// a port that was busy waits before its forward catches up. Saving an
	// app's settings doesn't wait: the API kicks the manager.
	reconcileInterval = 15 * time.Second
	// dialTimeout covers connecting to the host and opening the channel.
	dialTimeout = 15 * time.Second
)

// Manager runs the forwards Plan asks for and keeps them in step with it.
type Manager struct {
	source  Source
	connect Connector
	listen  func(port int) (net.Listener, error)
	log     *slog.Logger

	mu      sync.Mutex
	running map[int64]*forwarder // by app ID
	other   map[int64]Status     // forwards not running, and why
	kick    chan struct{}
}

// NewManager builds a manager. It does nothing until Run or Reconcile.
func NewManager(source Source, connect Connector, log *slog.Logger) *Manager {
	return &Manager{
		source:  source,
		connect: connect,
		listen: func(port int) (net.Listener, error) {
			// Every interface: being reachable from the network is the point.
			return net.Listen("tcp", ":"+strconv.Itoa(port))
		},
		log:     log,
		running: map[int64]*forwarder{},
		other:   map[int64]Status{},
		kick:    make(chan struct{}, 1),
	}
}

// DBSource reads apps, installations and hosts from the database.
func DBSource(db *store.DB) Source {
	return func(ctx context.Context) ([]*store.App, []*store.Installation, []*store.Host, error) {
		apps, err := db.ListApps(ctx)
		if err != nil {
			return nil, nil, nil, err
		}
		installs, err := db.ListInstallations(ctx)
		if err != nil {
			return nil, nil, nil, err
		}
		hosts, err := db.ListHosts(ctx)
		if err != nil {
			return nil, nil, nil, err
		}
		return apps, installs, hosts, nil
	}
}

// Run reconciles now, then on a timer and whenever kicked, until ctx ends.
// Every forward is stopped on the way out.
func (m *Manager) Run(ctx context.Context) {
	defer m.Close()
	t := time.NewTicker(reconcileInterval)
	defer t.Stop()
	for {
		m.Reconcile(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-m.kick:
		}
	}
}

// Kick asks Run to reconcile soon, for example after an app's settings change.
func (m *Manager) Kick() {
	select {
	case m.kick <- struct{}{}:
	default:
	}
}

// Reconcile starts, restarts and stops forwards to match the current plan.
func (m *Manager) Reconcile(ctx context.Context) {
	apps, installs, hosts, err := m.source(ctx)
	if err != nil {
		m.log.Error("forward: read plan", "err", err)
		return
	}
	rules, statuses := Plan(apps, installs, hosts)

	m.mu.Lock()
	defer m.mu.Unlock()

	want := map[int64]Rule{}
	for _, r := range rules {
		want[r.AppID] = r
	}
	for id, f := range m.running {
		if r, ok := want[id]; !ok || r.key() != f.rule.key() {
			f.stop()
			delete(m.running, id)
			m.log.Info("forward stopped", "app", f.rule.AppName, "port", f.rule.ListenPort)
		}
	}

	m.other = map[int64]Status{}
	for _, st := range statuses {
		m.other[st.AppID] = st
	}
	for _, r := range rules {
		if _, ok := m.running[r.AppID]; ok {
			continue
		}
		ln, err := m.listen(r.ListenPort)
		if err != nil {
			// Tried again on the next reconcile: the port may be freed.
			m.other[r.AppID] = Status{
				AppID: r.AppID, ListenPort: r.ListenPort, TargetPort: r.TargetPort,
				HostID: r.Host.ID, HostName: r.Host.Name,
				State: StateError, Detail: listenError(r.ListenPort, err),
			}
			continue
		}
		f := newForwarder(r, ln, m.connect, m.log)
		m.running[r.AppID] = f
		go f.serve()
		m.log.Info("forward started", "app", r.AppName, "port", r.ListenPort,
			"host", r.Host.Name, "target", r.TargetPort)
	}
}

func listenError(port int, err error) string {
	switch {
	case errors.Is(err, syscall.EADDRINUSE):
		return fmt.Sprintf("port %d is already in use on HostMan's machine", port)
	case errors.Is(err, syscall.EACCES):
		return fmt.Sprintf("HostMan isn't allowed to listen on port %d; use a port above 1024", port)
	}
	return fmt.Sprintf("can't listen on port %d: %v", port, err)
}

// Statuses reports every forward, running or not, by app ID.
func (m *Manager) Statuses() []Status {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Status, 0, len(m.running)+len(m.other))
	for _, f := range m.running {
		out = append(out, f.status())
	}
	for _, st := range m.other {
		out = append(out, st)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].AppID < out[j].AppID })
	return out
}

// Close stops every forward.
func (m *Manager) Close() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for id, f := range m.running {
		f.stop()
		delete(m.running, id)
	}
}

// forwarder relays one listening port to one app.
type forwarder struct {
	rule    Rule
	ln      net.Listener
	connect Connector
	log     *slog.Logger

	mu      sync.Mutex
	tunnel  Tunnel
	conns   map[net.Conn]struct{}
	lastErr string
	stopped bool
}

func newForwarder(r Rule, ln net.Listener, connect Connector, log *slog.Logger) *forwarder {
	return &forwarder{rule: r, ln: ln, connect: connect, log: log, conns: map[net.Conn]struct{}{}}
}

func (f *forwarder) serve() {
	for {
		c, err := f.ln.Accept()
		if err != nil {
			return // listener closed by stop
		}
		go f.handle(c)
	}
}

func (f *forwarder) handle(client net.Conn) {
	ctx, cancel := context.WithTimeout(context.Background(), dialTimeout)
	remote, err := f.dial(ctx)
	cancel()
	if err != nil {
		f.mu.Lock()
		f.lastErr = err.Error()
		f.mu.Unlock()
		f.log.Warn("forward: connect", "app", f.rule.AppName, "err", err)
		client.Close()
		return
	}
	if !f.track(client, remote) {
		client.Close()
		remote.Close()
		return
	}
	pipe(client, remote)
	f.untrack(client, remote)
}

// dial opens a connection to the app through the host's tunnel. A tunnel
// that has gone stale (the host rebooted, Wi-Fi dropped) fails the first
// dial; it is replaced and the dial tried once more.
func (f *forwarder) dial(ctx context.Context) (net.Conn, error) {
	addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(f.rule.TargetPort))
	var lastErr error
	for attempt := 0; attempt < 2; attempt++ {
		t, err := f.getTunnel(ctx)
		if err != nil {
			return nil, fmt.Errorf("connect to %s: %w", f.rule.Host.Name, err)
		}
		conn, err := t.DialTCP(ctx, addr)
		if err == nil {
			f.mu.Lock()
			f.lastErr = ""
			f.mu.Unlock()
			return conn, nil
		}
		lastErr = err
		f.dropTunnel(t)
	}
	return nil, fmt.Errorf("port %d on %s: %w", f.rule.TargetPort, f.rule.Host.Name, lastErr)
}

func (f *forwarder) getTunnel(ctx context.Context) (Tunnel, error) {
	f.mu.Lock()
	if f.tunnel != nil {
		t := f.tunnel
		f.mu.Unlock()
		return t, nil
	}
	f.mu.Unlock()

	host := f.rule.Host
	t, err := f.connect(ctx, &host)
	if err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.stopped {
		t.Close()
		return nil, errors.New("forward stopped")
	}
	if f.tunnel != nil { // another connection won the race; use its tunnel
		t.Close()
		return f.tunnel, nil
	}
	f.tunnel = t
	return t, nil
}

func (f *forwarder) dropTunnel(t Tunnel) {
	f.mu.Lock()
	if f.tunnel == t {
		f.tunnel = nil
	}
	f.mu.Unlock()
	t.Close()
}

func (f *forwarder) track(conns ...net.Conn) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.stopped {
		return false
	}
	for _, c := range conns {
		f.conns[c] = struct{}{}
	}
	return true
}

func (f *forwarder) untrack(conns ...net.Conn) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range conns {
		delete(f.conns, c)
	}
}

func (f *forwarder) status() Status {
	f.mu.Lock()
	defer f.mu.Unlock()
	return Status{
		AppID: f.rule.AppID, ListenPort: f.rule.ListenPort, TargetPort: f.rule.TargetPort,
		HostID: f.rule.Host.ID, HostName: f.rule.Host.Name,
		State: StateListening, Detail: f.lastErr,
		Active: len(f.conns) / 2, // each relayed connection is a pair
	}
}

// stop closes the listener, the tunnel and every relayed connection.
func (f *forwarder) stop() {
	f.mu.Lock()
	f.stopped = true
	conns := f.conns
	f.conns = map[net.Conn]struct{}{}
	t := f.tunnel
	f.tunnel = nil
	f.mu.Unlock()

	f.ln.Close()
	for c := range conns {
		c.Close()
	}
	if t != nil {
		t.Close()
	}
}

// pipe copies both ways until both directions are done. When one side stops
// sending, the other is told (a half-close) so request/response protocols
// finish cleanly; then both are closed.
func pipe(a, b net.Conn) {
	var wg sync.WaitGroup
	wg.Add(2)
	copyHalf := func(dst, src net.Conn) {
		defer wg.Done()
		_, _ = io.Copy(dst, src)
		if cw, ok := dst.(interface{ CloseWrite() error }); ok {
			_ = cw.CloseWrite()
		} else {
			dst.Close()
		}
	}
	go copyHalf(a, b)
	go copyHalf(b, a)
	wg.Wait()
	a.Close()
	b.Close()
}
