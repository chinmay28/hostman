// Package selfhost is how HostMan recognises the machine it is running on,
// registers it as a host, and knows how to update itself.
package selfhost

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/user"
	"strings"

	"github.com/chinmay28/deployer/server/internal/store"
)

// Settings keys recording one-time setup, so that deleting the home host or the
// updater app does not bring them straight back on the next restart.
const (
	settingHomeHostAdded  = "home_host_added"
	settingUpdaterAppMade = "updater_app_created"
)

// machineIDPaths are where systemd and dbus keep the machine's stable id.
var machineIDPaths = []string{"/etc/machine-id", "/var/lib/dbus/machine-id"}

// MachineID reads this machine's id. It is stable across reboots and unique per
// installation, which makes it a far better identity than a hostname or an
// address — the same machine can be reached as localhost, as an IP, or as
// nakedpi.local, and this recognises all three.
func MachineID() string {
	for _, path := range machineIDPaths {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		if id := strings.TrimSpace(string(data)); id != "" {
			return id
		}
	}
	return ""
}

// Config describes how HostMan reaches itself over SSH.
type Config struct {
	// SSHUser is the account on this machine that HostMan connects as. It
	// needs HostMan's public key and passwordless sudo, exactly like any
	// other host.
	SSHUser string
	// Port is the port HostMan is listening on, used for its health check.
	Port string
	// Repo and Ref are where a self-update builds from.
	Repo string
	Ref  string
}

// Manager owns the home host and the app that updates HostMan.
type Manager struct {
	db        *store.DB
	machineID string
	cfg       Config
	log       *slog.Logger
}

// New builds a Manager. A blank SSHUser falls back to the account HostMan runs
// as, which is usually wrong but at least gives the user something to correct.
func New(db *store.DB, cfg Config, log *slog.Logger) *Manager {
	if cfg.SSHUser == "" {
		if u, err := user.Current(); err == nil {
			cfg.SSHUser = u.Username
		}
	}
	return &Manager{db: db, machineID: MachineID(), cfg: cfg, log: log}
}

// MachineID returns the id this HostMan instance identifies itself by.
func (m *Manager) MachineID() string { return m.machineID }

// SetMachineIDForTest pretends HostMan is running on a different machine.
func (m *Manager) SetMachineIDForTest(id string) { m.machineID = id }

// IsSelf reports whether a probed machine id belongs to this machine.
func (m *Manager) IsSelf(machineID string) bool {
	return m.machineID != "" && machineID != "" && machineID == m.machineID
}

// Ensure registers the home host and the self-update app on first run. Both are
// one-time: if you delete them, they stay deleted.
func (m *Manager) Ensure(ctx context.Context) {
	if err := m.ensureHomeHost(ctx); err != nil {
		m.log.Error("selfhost: register home host", "err", err)
	}
	if err := m.EnsureUpdaterApp(ctx); err != nil {
		m.log.Error("selfhost: create updater app", "err", err)
	}
}

func (m *Manager) ensureHomeHost(ctx context.Context) error {
	if done, _, err := m.db.GetSetting(ctx, settingHomeHostAdded); err != nil || done != "" {
		return err
	}
	// Somebody may have added this machine by hand before it was recognised.
	if _, err := m.db.SelfHost(ctx); err == nil {
		return m.db.SetSetting(ctx, settingHomeHostAdded, "1")
	}

	name := localName()
	// A name collision means the user already has an entry for this machine
	// under its own name; leave it alone and let the probe tag it.
	if hosts, err := m.db.ListHosts(ctx); err == nil {
		for _, h := range hosts {
			if strings.EqualFold(h.Name, name) {
				return m.db.SetSetting(ctx, settingHomeHostAdded, "1")
			}
		}
	}

	host, err := m.db.CreateHost(ctx, &store.Host{
		Name:     name,
		Address:  "127.0.0.1",
		Port:     22,
		Username: m.cfg.SSHUser,
	})
	if err != nil {
		return err
	}
	// The flag is provisional until a probe confirms the machine id; setting it
	// now means the UI labels it correctly from the start.
	if err := m.db.SetHostSelf(ctx, host.ID, true, m.machineID); err != nil {
		return err
	}
	m.log.Info("registered this machine as a host", "name", name, "user", m.cfg.SSHUser)
	return m.db.SetSetting(ctx, settingHomeHostAdded, "1")
}

// UpdaterAppName is what the self-update app is called.
const UpdaterAppName = "HostMan"

// EnsureUpdaterApp creates the app that installs HostMan, once.
func (m *Manager) EnsureUpdaterApp(ctx context.Context) error {
	if done, _, err := m.db.GetSetting(ctx, settingUpdaterAppMade); err != nil || done != "" {
		return err
	}
	if _, err := m.db.SelfUpdateApp(ctx); err == nil {
		return m.db.SetSetting(ctx, settingUpdaterAppMade, "1")
	}

	repo := strings.TrimSuffix(m.cfg.Repo, ".git")
	repo = strings.TrimPrefix(repo, "https://github.com/")
	if repo == "" {
		repo = "chinmay28/hostman"
	}
	// The script and the build come from the same ref, so pinning a tag pins
	// both halves of the upgrade.
	command := fmt.Sprintf(
		"curl -fsSL https://raw.githubusercontent.com/%s/{{ref}}/scripts/quickstart.sh | sudo DEPLOYER_REF={{ref}} bash",
		repo)

	port := m.cfg.Port
	if port == "" {
		port = "8899"
	}
	if _, err := m.db.CreateApp(ctx, &store.App{
		Name:           UpdaterAppName,
		Description:    "HostMan itself. Updating restarts the service.",
		InstallCommand: command,
		Params: []store.Param{{
			Name:    "ref",
			Label:   "Version",
			Default: m.refOrMain(),
			Help:    "Branch, tag or commit to build from",
		}},
		HealthType:   store.HealthHTTP,
		HealthTarget: "http://{{host}}:" + port + "/api/health",
		SelfUpdate:   true,
	}); err != nil {
		return err
	}
	return m.db.SetSetting(ctx, settingUpdaterAppMade, "1")
}

func (m *Manager) refOrMain() string {
	if m.cfg.Ref != "" {
		return m.cfg.Ref
	}
	return "main"
}

// localName is the name the home host is registered under.
func localName() string {
	name, err := os.Hostname()
	if err != nil || name == "" {
		return "this machine"
	}
	// Hostnames are sometimes fully qualified; the short form reads better.
	if short, _, found := strings.Cut(name, "."); found && short != "" {
		return short
	}
	return name
}
