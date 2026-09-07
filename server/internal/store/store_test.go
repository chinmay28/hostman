package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func testDB(t *testing.T) *DB {
	t.Helper()
	db, err := Open(filepath.Join(t.TempDir(), "nested", "hostman.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func TestMigrationsAreIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hostman.db")
	db, err := Open(path)
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	if err := db.SetSetting(context.Background(), "k", "v"); err != nil {
		t.Fatalf("SetSetting: %v", err)
	}
	db.Close()

	db2, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer db2.Close()
	v, ok, err := db2.GetSetting(context.Background(), "k")
	if err != nil || !ok || v != "v" {
		t.Fatalf("GetSetting after reopen = %q, %v, %v", v, ok, err)
	}
}

func TestSettingsUpsert(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()

	if _, ok, err := db.GetSetting(ctx, "missing"); err != nil || ok {
		t.Fatalf("GetSetting(missing) = ok %v, err %v; want false, nil", ok, err)
	}
	if err := db.SetSetting(ctx, "pin", "1234"); err != nil {
		t.Fatal(err)
	}
	if err := db.SetSetting(ctx, "pin", "5678"); err != nil {
		t.Fatal(err)
	}
	v, ok, err := db.GetSetting(ctx, "pin")
	if err != nil || !ok || v != "5678" {
		t.Fatalf("GetSetting = %q, %v, %v; want 5678", v, ok, err)
	}
}

func TestHostCRUD(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()

	h, err := db.CreateHost(ctx, &Host{Name: "pi", Address: "nakedpi.local", Port: 22, Username: "chinmay"})
	if err != nil {
		t.Fatalf("CreateHost: %v", err)
	}
	if h.ID == 0 || h.Status != StatusUnknown {
		t.Fatalf("created host = %+v", h)
	}
	if h.CreatedAt.IsZero() {
		t.Error("createdAt not parsed")
	}

	if _, err := db.CreateHost(ctx, &Host{Name: "pi", Address: "other", Port: 22, Username: "x"}); err == nil {
		t.Error("expected a unique-name violation")
	}

	h.Address = "192.168.2.123"
	if err := db.UpdateHost(ctx, h); err != nil {
		t.Fatalf("UpdateHost: %v", err)
	}
	got, err := db.GetHost(ctx, h.ID)
	if err != nil || got.Address != "192.168.2.123" {
		t.Fatalf("GetHost = %+v, %v", got, err)
	}

	if _, err := db.GetHost(ctx, 9999); !errors.Is(err, ErrNotFound) {
		t.Errorf("GetHost(missing) = %v, want ErrNotFound", err)
	}
	if err := db.DeleteHost(ctx, 9999); !errors.Is(err, ErrNotFound) {
		t.Errorf("DeleteHost(missing) = %v, want ErrNotFound", err)
	}
	if err := db.DeleteHost(ctx, h.ID); err != nil {
		t.Fatalf("DeleteHost: %v", err)
	}
	if list, err := db.ListHosts(ctx); err != nil || len(list) != 0 {
		t.Fatalf("ListHosts after delete = %v, %v", list, err)
	}
}

func TestHostStatusTransitions(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	h, err := db.CreateHost(ctx, &Host{Name: "pi", Address: "a", Port: 22, Username: "u"})
	if err != nil {
		t.Fatal(err)
	}

	facts := HostFacts{Hostname: "nakedpi", OS: "Debian 12", Kernel: "6.6", Arch: "aarch64", SudoOK: true}
	if err := db.MarkHostOnline(ctx, h.ID, facts); err != nil {
		t.Fatal(err)
	}
	got, _ := db.GetHost(ctx, h.ID)
	if got.Status != StatusOnline || !got.SudoOK || got.Hostname != "nakedpi" {
		t.Fatalf("after MarkHostOnline: %+v", got)
	}
	if got.LastSeenAt == nil {
		t.Fatal("lastSeenAt not set")
	}

	if err := db.MarkHostFailed(ctx, h.ID, StatusOffline, "connection refused"); err != nil {
		t.Fatal(err)
	}
	got, _ = db.GetHost(ctx, h.ID)
	if got.Status != StatusOffline || got.LastError != "connection refused" {
		t.Fatalf("after MarkHostFailed: %+v", got)
	}
	// Facts from the last successful connect stay visible while offline.
	if got.Hostname != "nakedpi" {
		t.Errorf("facts cleared on failure: %+v", got)
	}
}

func TestSummarySince(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	h, err := db.CreateHost(ctx, &Host{Name: "pi", Address: "a", Port: 22, Username: "u"})
	if err != nil {
		t.Fatal(err)
	}

	now := time.Now().UTC()
	// Two samples inside the day and one older than it, which must not count.
	samples := []*Sample{
		{HostID: h.ID, TakenAt: now.Add(-2 * time.Hour), CPUPct: 10, MemUsed: 2000, MemTotal: 8000},
		{HostID: h.ID, TakenAt: now, CPUPct: 30, MemUsed: 6000, MemTotal: 8000},
		{HostID: h.ID, TakenAt: now.Add(-48 * time.Hour), CPUPct: 99, MemUsed: 7999, MemTotal: 8000},
	}
	for _, s := range samples {
		if err := db.InsertSample(ctx, s); err != nil {
			t.Fatal(err)
		}
	}

	got, err := db.SummarySince(ctx, h.ID, now.Add(-24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if got.Samples != 2 {
		t.Fatalf("samples = %d, want 2 — the 48h-old one is outside the window", got.Samples)
	}
	if got.CPUPct != (Stat{Min: 10, Max: 30, Avg: 20}) {
		t.Errorf("cpu = %+v, want 10/30/20", got.CPUPct)
	}
	if got.MemPct != (Stat{Min: 25, Max: 75, Avg: 50}) {
		t.Errorf("mem pct = %+v, want 25/75/50", got.MemPct)
	}
	if got.MemUsed != (Stat{Min: 2000, Max: 6000, Avg: 4000}) {
		t.Errorf("mem used = %+v, want 2000/6000/4000", got.MemUsed)
	}
	if got.MemTotal != 8000 {
		t.Errorf("mem total = %d, want 8000", got.MemTotal)
	}

	// A host with no history in the window summarizes to zeroes, not an error:
	// every aggregate but the count comes back NULL.
	empty, err := db.SummarySince(ctx, h.ID, now.Add(time.Hour))
	if err != nil {
		t.Fatalf("SummarySince over an empty window: %v", err)
	}
	if empty.Samples != 0 || empty.CPUPct != (Stat{}) || empty.MemUsed != (Stat{}) {
		t.Errorf("empty window = %+v, want zeroes", empty)
	}
}

func TestSamplesRoundTripAndPrune(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	h, err := db.CreateHost(ctx, &Host{Name: "pi", Address: "a", Port: 22, Username: "u"})
	if err != nil {
		t.Fatal(err)
	}

	temp := 48.5
	now := time.Now().UTC()
	old := &Sample{HostID: h.ID, TakenAt: now.Add(-48 * time.Hour), CPUPct: 10, MemTotal: 100, MemUsed: 50}
	recent := &Sample{
		HostID: h.ID, TakenAt: now, CPUPct: 25.5, MemUsed: 2048, MemTotal: 8192,
		Load1: 0.5, UptimeS: 3600, TempC: &temp,
		Disks: []Disk{{Mount: "/", Device: "/dev/sda1", TotalBytes: 100, UsedBytes: 40}},
	}
	for _, s := range []*Sample{old, recent} {
		if err := db.InsertSample(ctx, s); err != nil {
			t.Fatalf("InsertSample: %v", err)
		}
	}

	latest, err := db.LatestSamples(ctx)
	if err != nil {
		t.Fatal(err)
	}
	got := latest[h.ID]
	if got == nil || got.CPUPct != 25.5 {
		t.Fatalf("LatestSamples = %+v, want the recent one", got)
	}
	if got.TempC == nil || *got.TempC != 48.5 {
		t.Errorf("tempC round trip = %v", got.TempC)
	}
	if len(got.Disks) != 1 || got.Disks[0].Mount != "/" {
		t.Errorf("disks round trip = %+v", got.Disks)
	}

	within, err := db.SamplesSince(ctx, h.ID, now.Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(within) != 1 {
		t.Fatalf("SamplesSince(1h) returned %d samples, want 1", len(within))
	}

	n, err := db.PruneSamples(ctx, now.Add(-24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("PruneSamples removed %d, want 1", n)
	}

	// Deleting a host must take its telemetry with it.
	if err := db.DeleteHost(ctx, h.ID); err != nil {
		t.Fatal(err)
	}
	left, err := db.SamplesSince(ctx, h.ID, now.Add(-72*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(left) != 0 {
		t.Errorf("samples survived host deletion: %d rows", len(left))
	}
}

// An app's uninstall command survives the round trip, and the deployment kind
// defaults to an install — which is what every row written before the column
// existed is.
func TestAppUninstallCommandAndDeploymentKind(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()

	app, err := db.CreateApp(ctx, &App{
		Name:             "photos",
		InstallCommand:   "install --port {{port}}",
		UninstallCommand: "uninstall --port {{port}}",
	})
	if err != nil {
		t.Fatalf("CreateApp: %v", err)
	}
	if app.UninstallCommand != "uninstall --port {{port}}" {
		t.Errorf("uninstall command = %q", app.UninstallCommand)
	}

	app.UninstallCommand = "purge"
	if err := db.UpdateApp(ctx, app); err != nil {
		t.Fatalf("UpdateApp: %v", err)
	}
	reread, err := db.GetApp(ctx, app.ID)
	if err != nil {
		t.Fatalf("GetApp: %v", err)
	}
	if reread.UninstallCommand != "purge" {
		t.Errorf("uninstall command after update = %q, want %q", reread.UninstallCommand, "purge")
	}

	host, err := db.CreateHost(ctx, &Host{Name: "pi", Address: "pi.local", Port: 22, Username: "pi"})
	if err != nil {
		t.Fatalf("CreateHost: %v", err)
	}
	install, err := db.CreateDeployment(ctx, &Deployment{AppID: app.ID, HostID: host.ID, Command: "install"})
	if err != nil {
		t.Fatalf("CreateDeployment: %v", err)
	}
	if install.Kind != KindInstall {
		t.Errorf("kind = %q, want %q for a deployment that did not say", install.Kind, KindInstall)
	}
	removal, err := db.CreateDeployment(ctx, &Deployment{
		AppID: app.ID, HostID: host.ID, Command: "purge", Kind: KindUninstall,
	})
	if err != nil {
		t.Fatalf("CreateDeployment: %v", err)
	}
	if removal.Kind != KindUninstall {
		t.Errorf("kind = %q, want %q", removal.Kind, KindUninstall)
	}
	// Listings carry it too: history has to say which of the two a row was.
	list, err := db.ListDeployments(ctx, DeploymentFilter{AppID: app.ID})
	if err != nil {
		t.Fatalf("ListDeployments: %v", err)
	}
	if len(list) != 2 || list[0].Kind != KindUninstall {
		t.Fatalf("listed deployments = %+v", list)
	}
}

// ForgetInstallation works from the app/host pair a finished uninstall has, and
// says nothing when there is no longer a record to remove.
func TestForgetInstallationByPair(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()

	app, err := db.CreateApp(ctx, &App{Name: "photos", InstallCommand: "install"})
	if err != nil {
		t.Fatal(err)
	}
	host, err := db.CreateHost(ctx, &Host{Name: "pi", Address: "pi.local", Port: 22, Username: "pi"})
	if err != nil {
		t.Fatal(err)
	}
	dep, err := db.CreateDeployment(ctx, &Deployment{AppID: app.ID, HostID: host.ID, Command: "install"})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertInstallation(ctx, app.ID, host.ID, nil, dep.ID); err != nil {
		t.Fatal(err)
	}
	if err := db.ForgetInstallation(ctx, app.ID, host.ID); err != nil {
		t.Fatalf("ForgetInstallation: %v", err)
	}
	if _, err := db.FindInstallation(ctx, app.ID, host.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("installation still there: %v", err)
	}
	// Somebody forgetting it by hand while the uninstall ran wanted the same
	// end state, so a second call is not an error.
	if err := db.ForgetInstallation(ctx, app.ID, host.ID); err != nil {
		t.Errorf("second ForgetInstallation: %v", err)
	}
	// The deployment record is the log of what ran; it stays.
	if _, err := db.GetDeployment(ctx, dep.ID); err != nil {
		t.Errorf("deployment gone after forgetting the installation: %v", err)
	}
}
