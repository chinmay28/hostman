package hosts

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os/user"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chinmay28/hostman/server/internal/metrics"
	"github.com/chinmay28/hostman/server/internal/selfhost"
	"github.com/chinmay28/hostman/server/internal/sshx"
	"github.com/chinmay28/hostman/server/internal/store"
	"github.com/chinmay28/hostman/server/internal/testutil"
)

// These tests drive the real SSH path against a throwaway sshd on localhost:
// key auth, host-key pinning, and the /proc probe against a live kernel. They
// skip where sshd is unavailable rather than silently passing.
func testEnv(t *testing.T) (*store.DB, *Service, *store.Host) {
	t.Helper()
	testutil.RequireSSHD(t)

	db, err := store.Open(filepath.Join(t.TempDir(), "hostman.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	id, err := sshx.EnsureIdentity(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	port := testutil.StartSSHD(t, id.AuthorizedKey())

	me, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	h, err := db.CreateHost(context.Background(), &store.Host{
		Name: "localhost", Address: "127.0.0.1", Port: port, Username: me.Username,
	})
	if err != nil {
		t.Fatal(err)
	}
	return db, NewService(db, id, nil), h
}

func TestProbeAgainstRealHost(t *testing.T) {
	db, svc, h := testEnv(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	probe, err := svc.Probe(ctx, h)
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}

	if probe.Facts.Hostname == "" || probe.Facts.Arch == "" || probe.Facts.Kernel == "" {
		t.Errorf("facts incomplete: %+v", probe.Facts)
	}
	if !strings.Contains(strings.ToLower(probe.Facts.OS), "linux") &&
		!strings.Contains(strings.ToLower(probe.Facts.OS), "ubuntu") &&
		!strings.Contains(strings.ToLower(probe.Facts.OS), "debian") {
		t.Errorf("os = %q, want something recognizable", probe.Facts.OS)
	}
	if probe.Sample.MemTotal <= 0 || probe.Sample.MemUsed <= 0 {
		t.Errorf("memory = %d/%d, want positive values", probe.Sample.MemUsed, probe.Sample.MemTotal)
	}
	if probe.Sample.MemUsed > probe.Sample.MemTotal {
		t.Errorf("memUsed %d exceeds memTotal %d", probe.Sample.MemUsed, probe.Sample.MemTotal)
	}
	if probe.Sample.CPUPct < 0 || probe.Sample.CPUPct > 100 {
		t.Errorf("cpuPct = %v, out of range", probe.Sample.CPUPct)
	}
	if probe.Sample.UptimeS <= 0 {
		t.Errorf("uptimeS = %d, want positive", probe.Sample.UptimeS)
	}
	if len(probe.Sample.Disks) == 0 {
		t.Error("no filesystems reported")
	}
	for _, d := range probe.Sample.Disks {
		if d.TotalBytes <= 0 || d.UsedBytes > d.TotalBytes {
			t.Errorf("implausible disk %+v", d)
		}
	}

	// The same round trip walks /proc, so a live kernel always has processes to
	// report — the probe's own shell among them — and every figure must be
	// one the UI can show.
	if probe.Processes == nil {
		t.Fatal("no process snapshot from a live host")
	}
	if len(probe.Processes.TopMem) == 0 {
		t.Error("not one process reported resident memory")
	}
	for _, p := range append(append([]metrics.Process{}, probe.Processes.TopCPU...), probe.Processes.TopMem...) {
		if p.PID <= 0 || p.Name == "" {
			t.Errorf("unnamed process %+v", p)
		}
		if p.CPUPct < 0 || p.CPUPct > 100 || p.MemPct < 0 || p.MemPct > 100 {
			t.Errorf("process out of range: %+v", p)
		}
		if p.MemBytes > probe.Sample.MemTotal {
			t.Errorf("%s holds %d bytes of %d in the machine", p.Name, p.MemBytes, probe.Sample.MemTotal)
		}
	}
	// A snapshot is kept in memory rather than in the database, which is where
	// the metrics endpoint reads it from.
	if svc.Processes(h.ID) == nil {
		t.Error("the snapshot was not remembered for the host")
	}
	svc.Forget(h.ID)
	if svc.Processes(h.ID) != nil {
		t.Error("a removed host kept its snapshot")
	}

	// The probe result must land in the database.
	stored, err := db.GetHost(ctx, h.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != store.StatusOnline {
		t.Errorf("status = %q, want online", stored.Status)
	}
	if stored.LastSeenAt == nil {
		t.Error("lastSeenAt not recorded")
	}
	if stored.HostKey == "" {
		t.Error("host key was not pinned on first connect")
	}
	latest, err := db.LatestSamples(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if latest[h.ID] == nil {
		t.Fatal("sample was not stored")
	}
}

func TestPinnedHostKeyMismatchIsRejected(t *testing.T) {
	db, svc, h := testEnv(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if _, err := svc.Probe(ctx, h); err != nil {
		t.Fatalf("first Probe: %v", err)
	}
	stored, _ := db.GetHost(ctx, h.ID)
	if stored.HostKey == "" {
		t.Fatal("expected a pinned host key")
	}

	// Simulate the host presenting a different key (reinstall, or a MITM).
	other := "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIJ9Z8pCBmC3Cq5V0f3n5Y7v0Yl8kQxKq1nqZ0000000 impostor"
	if err := db.SetHostKey(ctx, h.ID, other); err != nil {
		t.Fatal(err)
	}
	stored, _ = db.GetHost(ctx, h.ID)

	_, err := svc.Probe(ctx, stored)
	if !errors.Is(err, sshx.ErrHostKeyChanged) {
		t.Fatalf("Probe error = %v, want ErrHostKeyChanged", err)
	}
	after, _ := db.GetHost(ctx, h.ID)
	if after.Status != store.StatusError {
		t.Errorf("status = %q, want error after a host key mismatch", after.Status)
	}
}

func TestUnreachableHostIsMarkedOffline(t *testing.T) {
	testutil.RequireSSHD(t)
	db, err := store.Open(filepath.Join(t.TempDir(), "hostman.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	id, err := sshx.EnsureIdentity(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	svc := NewService(db, id, nil)

	ctx := context.Background()
	h, err := db.CreateHost(ctx, &store.Host{
		Name: "ghost", Address: "127.0.0.1", Port: testutil.FreePort(t), Username: "nobody",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Probe(ctx, h); err == nil {
		t.Fatal("expected a connection error")
	}
	stored, _ := db.GetHost(ctx, h.ID)
	if stored.Status != store.StatusOffline {
		t.Errorf("status = %q, want offline", stored.Status)
	}
	if stored.LastError == "" {
		t.Error("lastError not recorded")
	}
}

func TestTestReportsHints(t *testing.T) {
	_, svc, h := testEnv(t)
	res := svc.Test(context.Background(), h)
	if !res.OK {
		t.Fatalf("Test failed: %+v", res)
	}
	if res.Hostname == "" {
		t.Error("hostname missing from a successful test")
	}
	if !res.SudoOK && len(res.Hints) == 0 {
		t.Error("sudo unavailable but no hint offered")
	}
}

// The machine id is what makes detection work regardless of the address used:
// the throwaway sshd runs on this very machine, so a probe of 127.0.0.1 must
// come back tagged as the home host.
func TestProbeDetectsTheHomeHost(t *testing.T) {
	db, _, h := testEnv(t)
	ctx := context.Background()

	id, err := sshx.EnsureIdentity(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	local := selfhost.MachineID()
	if local == "" {
		t.Skip("this machine has no /etc/machine-id to compare against")
	}
	svc := NewService(db, id, selfhost.New(db, selfhost.Config{}, discardLogger()))

	probe, err := svc.Probe(ctx, h)
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if probe.Facts.MachineID != local {
		t.Fatalf("probed machine id = %q, want this machine's %q", probe.Facts.MachineID, local)
	}
	if !probe.Facts.IsSelf {
		t.Error("probing this machine should identify it as the home host")
	}
	stored, err := db.GetHost(ctx, h.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !stored.IsSelf {
		t.Error("the home host tag was not persisted")
	}
	if selfHost, err := db.SelfHost(ctx); err != nil || selfHost.ID != h.ID {
		t.Errorf("SelfHost = %v, %v; want host %d", selfHost, err, h.ID)
	}
}

// A different machine must not be mistaken for this one.
func TestProbeDoesNotTagOtherMachinesAsHome(t *testing.T) {
	db, _, h := testEnv(t)
	ctx := context.Background()
	id, err := sshx.EnsureIdentity(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	elsewhere := selfhost.New(db, selfhost.Config{}, discardLogger())
	elsewhere.SetMachineIDForTest("a-different-machine")

	svc := NewService(db, id, elsewhere)
	probe, err := svc.Probe(ctx, h)
	if err != nil {
		t.Fatal(err)
	}
	if probe.Facts.IsSelf {
		t.Error("a machine with a different id must not be tagged as home")
	}
	if _, err := db.SelfHost(ctx); err == nil {
		t.Error("no host should be flagged as self")
	}
}

func discardLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }
