package forward

import (
	"context"
	"io"
	"log/slog"
	"net"
	"os/user"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/chinmay28/hostman/server/internal/hosts"
	"github.com/chinmay28/hostman/server/internal/sshx"
	"github.com/chinmay28/hostman/server/internal/store"
	"github.com/chinmay28/hostman/server/internal/testutil"
)

// The whole path for real: an app saved with a forward, installed on a host
// that is reached over SSH, and an HTTP request through HostMan's port that
// arrives at the app by way of the SSH connection.
func TestForwardOverRealSSH(t *testing.T) {
	testutil.RequireSSHD(t)
	ctx := context.Background()

	db, err := store.Open(filepath.Join(t.TempDir(), "hostman.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	id, err := sshx.EnsureIdentity(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	sshPort := testutil.StartSSHD(t, id.AuthorizedKey())
	me, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	host, err := db.CreateHost(ctx, &store.Host{Name: "photoframe", Address: "127.0.0.1", Port: sshPort, Username: me.Username})
	if err != nil {
		t.Fatal(err)
	}

	target := appServer(t, "photoframe")
	listen := freePort(t)
	app, err := db.CreateApp(ctx, &store.App{
		Name: "photoframe", InstallCommand: "true", ForwardPort: listen, ForwardTargetPort: target,
	})
	if err != nil {
		t.Fatal(err)
	}
	dep, err := db.CreateDeployment(ctx, &store.Deployment{AppID: app.ID, HostID: host.ID, Command: "true"})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertInstallation(ctx, app.ID, host.ID, map[string]string{}, dep.ID); err != nil {
		t.Fatal(err)
	}

	svc := hosts.NewService(db, id, nil)
	m := NewManager(DBSource(db), func(ctx context.Context, h *store.Host) (Tunnel, error) {
		return svc.Connect(ctx, h)
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	m.listen = func(port int) (net.Listener, error) {
		return net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(port))
	}
	t.Cleanup(m.Close)
	m.Reconcile(ctx)

	if st := status(t, m, app.ID); st.State != StateListening {
		t.Fatalf("status = %+v", st)
	}
	body, err := get(t, listen, "/api/device")
	if err != nil || body != "photoframe/api/device" {
		t.Fatalf("GET through the SSH forward = %q, %v", body, err)
	}
	if st := status(t, m, app.ID); st.Detail != "" {
		t.Errorf("a working forward reports %q", st.Detail)
	}
}
