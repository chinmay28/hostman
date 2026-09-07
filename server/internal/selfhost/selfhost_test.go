package selfhost

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chinmay28/deployer/server/internal/store"
)

func testDB(t *testing.T) *store.DB {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "deployer.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func testManager(t *testing.T, db *store.DB) *Manager {
	t.Helper()
	m := New(db, Config{SSHUser: "chinmay", Port: "8899", Repo: "chinmay28/hostman", Ref: "main"},
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	m.machineID = "abc123"
	return m
}

func TestMachineIDReadsTheFirstAvailableFile(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "missing")
	present := filepath.Join(dir, "machine-id")
	if err := os.WriteFile(present, []byte("2f1c9d\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	original := machineIDPaths
	t.Cleanup(func() { machineIDPaths = original })

	machineIDPaths = []string{missing, present}
	if got := MachineID(); got != "2f1c9d" {
		t.Errorf("MachineID = %q, want 2f1c9d (trimmed)", got)
	}

	machineIDPaths = []string{missing}
	if got := MachineID(); got != "" {
		t.Errorf("MachineID = %q, want empty when nothing is readable", got)
	}
}

// A machine without an id must not be mistaken for every other machine
// without one.
func TestIsSelfNeedsBothSides(t *testing.T) {
	m := testManager(t, testDB(t))
	if !m.IsSelf("abc123") {
		t.Error("the matching id should be recognised")
	}
	if m.IsSelf("other") {
		t.Error("a different id must not match")
	}
	if m.IsSelf("") {
		t.Error("an unknown remote id must not match")
	}
	m.machineID = ""
	if m.IsSelf("abc123") || m.IsSelf("") {
		t.Error("without a local id nothing should match")
	}
}

func TestEnsureRegistersHomeHostAndUpdaterApp(t *testing.T) {
	db := testDB(t)
	m := testManager(t, db)
	ctx := context.Background()

	m.Ensure(ctx)

	host, err := db.SelfHost(ctx)
	if err != nil {
		t.Fatalf("home host not registered: %v", err)
	}
	if host.Address != "127.0.0.1" || host.Username != "chinmay" || host.Port != 22 {
		t.Errorf("home host = %+v", host)
	}
	if !host.IsSelf {
		t.Error("home host should be tagged as self")
	}

	app, err := db.SelfUpdateApp(ctx)
	if err != nil {
		t.Fatalf("updater app not created: %v", err)
	}
	if !strings.Contains(app.InstallCommand, "chinmay28/hostman") {
		t.Errorf("install command = %q, want it to build from the configured repo", app.InstallCommand)
	}
	if !strings.Contains(app.InstallCommand, "{{ref}}") {
		t.Errorf("install command = %q, want the version to be a parameter", app.InstallCommand)
	}
	// Script and build must come from the same ref, or you get a v2 installer
	// building main.
	if strings.Count(app.InstallCommand, "{{ref}}") != 2 {
		t.Errorf("install command = %q, want the ref used for both the script and the build",
			app.InstallCommand)
	}
	if app.HealthTarget != "http://{{host}}:8899/api/health" {
		t.Errorf("healthTarget = %q, want the port HostMan listens on", app.HealthTarget)
	}
	if len(app.Params) != 1 || app.Params[0].Name != "ref" || app.Params[0].Default != "main" {
		t.Errorf("params = %+v", app.Params)
	}

	// Running again changes nothing.
	m.Ensure(ctx)
	hosts, _ := db.ListHosts(ctx)
	apps, _ := db.ListApps(ctx)
	if len(hosts) != 1 || len(apps) != 1 {
		t.Errorf("second Ensure created duplicates: %d hosts, %d apps", len(hosts), len(apps))
	}
}

// Deleting the home host or the updater app is a decision; a restart must not
// undo it.
func TestEnsureDoesNotResurrectDeletedEntries(t *testing.T) {
	db := testDB(t)
	m := testManager(t, db)
	ctx := context.Background()

	m.Ensure(ctx)
	host, _ := db.SelfHost(ctx)
	app, _ := db.SelfUpdateApp(ctx)
	if err := db.DeleteHost(ctx, host.ID); err != nil {
		t.Fatal(err)
	}
	if err := db.DeleteApp(ctx, app.ID); err != nil {
		t.Fatal(err)
	}

	m.Ensure(ctx)

	if hosts, _ := db.ListHosts(ctx); len(hosts) != 0 {
		t.Errorf("home host came back: %+v", hosts)
	}
	if apps, _ := db.ListApps(ctx); len(apps) != 0 {
		t.Errorf("updater app came back: %+v", apps)
	}
}

// If the user already added this machine themselves, don't add a second entry.
func TestEnsureLeavesAnExistingEntryAlone(t *testing.T) {
	db := testDB(t)
	m := testManager(t, db)
	ctx := context.Background()

	mine, err := db.CreateHost(ctx, &store.Host{
		Name: localName(), Address: "nakedpi.local", Port: 22, Username: "pi",
	})
	if err != nil {
		t.Fatal(err)
	}

	m.Ensure(ctx)

	hosts, _ := db.ListHosts(ctx)
	if len(hosts) != 1 {
		t.Fatalf("got %d hosts, want just the one already there", len(hosts))
	}
	if hosts[0].ID != mine.ID || hosts[0].Address != "nakedpi.local" {
		t.Errorf("existing host was modified: %+v", hosts[0])
	}
}

// A host already flagged as self means registration has happened before.
func TestEnsureSkipsWhenSelfHostExists(t *testing.T) {
	db := testDB(t)
	m := testManager(t, db)
	ctx := context.Background()

	existing, err := db.CreateHost(ctx, &store.Host{
		Name: "somethingelse", Address: "10.0.0.4", Port: 22, Username: "pi",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.SetHostSelf(ctx, existing.ID, true, "abc123"); err != nil {
		t.Fatal(err)
	}

	m.Ensure(ctx)

	if hosts, _ := db.ListHosts(ctx); len(hosts) != 1 {
		t.Errorf("got %d hosts, want no new entry", len(hosts))
	}
}
