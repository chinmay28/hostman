package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// ErrNotFound is returned when a row does not exist.
var ErrNotFound = errors.New("not found")

// Host status values.
const (
	StatusUnknown = "unknown"
	StatusOnline  = "online"
	StatusOffline = "offline"
	StatusError   = "error"
)

// Host is a machine HostMan can reach over SSH.
type Host struct {
	ID         int64      `json:"id"`
	Name       string     `json:"name"`
	Address    string     `json:"address"`
	Port       int        `json:"port"`
	Username   string     `json:"username"`
	HostKey    string     `json:"-"` // trusted public key, pinned on first connect
	Status     string     `json:"status"`
	LastError  string     `json:"lastError"`
	LastSeenAt *time.Time `json:"lastSeenAt"`
	Hostname   string     `json:"hostname"`
	OS         string     `json:"os"`
	Kernel     string     `json:"kernel"`
	Arch       string     `json:"arch"`
	SudoOK     bool       `json:"sudoOk"`
	CreatedAt  time.Time  `json:"createdAt"`
	// IsSelf marks the machine HostMan itself is running on.
	IsSelf    bool   `json:"isSelf"`
	MachineID string `json:"-"`
}

const hostColumns = `id, name, address, port, username, host_key, status, last_error,
	last_seen_at, hostname, os, kernel, arch, sudo_ok, created_at, is_self, machine_id`

func scanHost(row interface{ Scan(...any) error }) (*Host, error) {
	var h Host
	var lastSeen sql.NullString
	var created string
	err := row.Scan(&h.ID, &h.Name, &h.Address, &h.Port, &h.Username, &h.HostKey,
		&h.Status, &h.LastError, &lastSeen, &h.Hostname, &h.OS, &h.Kernel, &h.Arch,
		&h.SudoOK, &created, &h.IsSelf, &h.MachineID)
	if err == sql.ErrNoRows {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if lastSeen.Valid {
		if t, err := parseSQLiteTime(lastSeen.String); err == nil {
			h.LastSeenAt = &t
		}
	}
	if t, err := parseSQLiteTime(created); err == nil {
		h.CreatedAt = t
	}
	return &h, nil
}

func parseSQLiteTime(s string) (time.Time, error) {
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02 15:04:05"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC(), nil
		}
	}
	return time.Time{}, errors.New("unrecognized time format: " + s)
}

// ListHosts returns all hosts, newest last.
func (d *DB) ListHosts(ctx context.Context) ([]*Host, error) {
	rows, err := d.sql.QueryContext(ctx, `SELECT `+hostColumns+` FROM hosts ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	hosts := []*Host{}
	for rows.Next() {
		h, err := scanHost(rows)
		if err != nil {
			return nil, err
		}
		hosts = append(hosts, h)
	}
	return hosts, rows.Err()
}

// GetHost returns one host by id.
func (d *DB) GetHost(ctx context.Context, id int64) (*Host, error) {
	return scanHost(d.sql.QueryRowContext(ctx, `SELECT `+hostColumns+` FROM hosts WHERE id = ?`, id))
}

// CreateHost inserts a host and returns it with its assigned id.
func (d *DB) CreateHost(ctx context.Context, h *Host) (*Host, error) {
	res, err := d.sql.ExecContext(ctx,
		`INSERT INTO hosts (name, address, port, username) VALUES (?, ?, ?, ?)`,
		h.Name, h.Address, h.Port, h.Username)
	if err != nil {
		return nil, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, err
	}
	return d.GetHost(ctx, id)
}

// UpdateHost writes the user-editable fields of a host.
func (d *DB) UpdateHost(ctx context.Context, h *Host) error {
	res, err := d.sql.ExecContext(ctx,
		`UPDATE hosts SET name = ?, address = ?, port = ?, username = ? WHERE id = ?`,
		h.Name, h.Address, h.Port, h.Username, h.ID)
	if err != nil {
		return err
	}
	return requireRow(res)
}

// DeleteHost removes a host and its metric history.
func (d *DB) DeleteHost(ctx context.Context, id int64) error {
	res, err := d.sql.ExecContext(ctx, `DELETE FROM hosts WHERE id = ?`, id)
	if err != nil {
		return err
	}
	return requireRow(res)
}

// SetHostKey pins the host's SSH public key after a first successful connect.
func (d *DB) SetHostKey(ctx context.Context, id int64, key string) error {
	_, err := d.sql.ExecContext(ctx, `UPDATE hosts SET host_key = ? WHERE id = ?`, key, id)
	return err
}

// HostFacts are the slow-changing details read from a host on connect.
type HostFacts struct {
	Hostname string
	OS       string
	Kernel   string
	Arch     string
	SudoOK   bool
	// MachineID is /etc/machine-id, which is how HostMan recognises the
	// machine it is running on regardless of the address used to reach it.
	MachineID string
	IsSelf    bool
}

// MarkHostOnline records a successful connection along with the host's facts.
func (d *DB) MarkHostOnline(ctx context.Context, id int64, f HostFacts) error {
	_, err := d.sql.ExecContext(ctx, `
		UPDATE hosts SET status = ?, last_error = '', last_seen_at = datetime('now'),
			hostname = ?, os = ?, kernel = ?, arch = ?, sudo_ok = ?, machine_id = ?, is_self = ?
		WHERE id = ?`,
		StatusOnline, f.Hostname, f.OS, f.Kernel, f.Arch, f.SudoOK, f.MachineID, f.IsSelf, id)
	return err
}

// MarkHostFailed records a failed connection attempt.
func (d *DB) MarkHostFailed(ctx context.Context, id int64, status, reason string) error {
	_, err := d.sql.ExecContext(ctx,
		`UPDATE hosts SET status = ?, last_error = ? WHERE id = ?`, status, reason, id)
	return err
}

func requireRow(res interface{ RowsAffected() (int64, error) }) error {
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// SelfHost returns the host HostMan is running on, if one has been identified.
func (d *DB) SelfHost(ctx context.Context) (*Host, error) {
	return scanHost(d.sql.QueryRowContext(ctx,
		`SELECT `+hostColumns+` FROM hosts WHERE is_self = 1 ORDER BY id LIMIT 1`))
}

// SetHostSelf marks (or unmarks) a host as the machine HostMan runs on.
func (d *DB) SetHostSelf(ctx context.Context, id int64, isSelf bool, machineID string) error {
	_, err := d.sql.ExecContext(ctx,
		`UPDATE hosts SET is_self = ?, machine_id = ? WHERE id = ?`, isSelf, machineID, id)
	return err
}
