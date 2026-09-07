package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"
)

// Health check kinds an app can declare.
const (
	HealthNone    = "none"
	HealthHTTP    = "http"
	HealthSystemd = "systemd"
)

// Health results recorded against an installation.
const (
	HealthUnknown   = "unknown"
	HealthPassing   = "passing"
	HealthFailing   = "failing"
	HealthUnchecked = "unchecked"
)

// Param is one value the user fills in before a deploy. Values are substituted
// into the install command wherever {{name}} appears.
type Param struct {
	Name     string `json:"name"`
	Label    string `json:"label"`
	Default  string `json:"default"`
	Help     string `json:"help"`
	Required bool   `json:"required"`
}

// App is something HostMan knows how to install, defined by a one-line
// install command.
type App struct {
	ID             int64  `json:"id"`
	Name           string `json:"name"`
	Description    string `json:"description"`
	InstallCommand string `json:"installCommand"`
	// UninstallCommand takes the app back off a host. Optional: an app that
	// declares none can only be forgotten, never removed.
	UninstallCommand string    `json:"uninstallCommand"`
	Params           []Param   `json:"params"`
	HealthType       string    `json:"healthType"`
	HealthTarget     string    `json:"healthTarget"`
	CreatedAt        time.Time `json:"createdAt"`
	// SelfUpdate marks the app that installs HostMan itself. Deploying it to
	// the home host restarts the process running the deployment, so it runs
	// detached and is picked back up afterwards.
	SelfUpdate bool `json:"selfUpdate"`
}

const appColumns = `id, name, description, install_command, uninstall_command, params,
	health_type, health_target, created_at, self_update`

func scanApp(row interface{ Scan(...any) error }) (*App, error) {
	var a App
	var params, created string
	err := row.Scan(&a.ID, &a.Name, &a.Description, &a.InstallCommand, &a.UninstallCommand,
		&params, &a.HealthType, &a.HealthTarget, &created, &a.SelfUpdate)
	if err == sql.ErrNoRows {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	a.Params = []Param{}
	_ = json.Unmarshal([]byte(params), &a.Params)
	if t, err := parseSQLiteTime(created); err == nil {
		a.CreatedAt = t
	}
	return &a, nil
}

// ListApps returns all apps by name.
func (d *DB) ListApps(ctx context.Context) ([]*App, error) {
	rows, err := d.sql.QueryContext(ctx, `SELECT `+appColumns+` FROM apps ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	apps := []*App{}
	for rows.Next() {
		a, err := scanApp(rows)
		if err != nil {
			return nil, err
		}
		apps = append(apps, a)
	}
	return apps, rows.Err()
}

// GetApp returns one app by id.
func (d *DB) GetApp(ctx context.Context, id int64) (*App, error) {
	return scanApp(d.sql.QueryRowContext(ctx, `SELECT `+appColumns+` FROM apps WHERE id = ?`, id))
}

// CreateApp inserts an app.
func (d *DB) CreateApp(ctx context.Context, a *App) (*App, error) {
	params, err := json.Marshal(orEmptyParams(a.Params))
	if err != nil {
		return nil, err
	}
	res, err := d.sql.ExecContext(ctx, `
		INSERT INTO apps (name, description, install_command, uninstall_command, params,
			health_type, health_target, self_update)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		a.Name, a.Description, a.InstallCommand, a.UninstallCommand, string(params),
		a.HealthType, a.HealthTarget, a.SelfUpdate)
	if err != nil {
		return nil, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, err
	}
	return d.GetApp(ctx, id)
}

// UpdateApp writes an app's editable fields.
func (d *DB) UpdateApp(ctx context.Context, a *App) error {
	params, err := json.Marshal(orEmptyParams(a.Params))
	if err != nil {
		return err
	}
	res, err := d.sql.ExecContext(ctx, `
		UPDATE apps SET name = ?, description = ?, install_command = ?, uninstall_command = ?,
			params = ?, health_type = ?, health_target = ?
		WHERE id = ?`,
		a.Name, a.Description, a.InstallCommand, a.UninstallCommand, string(params),
		a.HealthType, a.HealthTarget, a.ID)
	if err != nil {
		return err
	}
	return requireRow(res)
}

// DeleteApp removes an app along with its deployments and installations. It
// does not uninstall anything on a host; see Runner.Uninstall for that.
func (d *DB) DeleteApp(ctx context.Context, id int64) error {
	res, err := d.sql.ExecContext(ctx, `DELETE FROM apps WHERE id = ?`, id)
	if err != nil {
		return err
	}
	return requireRow(res)
}

func orEmptyParams(p []Param) []Param {
	if p == nil {
		return []Param{}
	}
	return p
}

// SelfUpdateApp returns the app that installs HostMan itself, if it exists.
func (d *DB) SelfUpdateApp(ctx context.Context) (*App, error) {
	return scanApp(d.sql.QueryRowContext(ctx,
		`SELECT `+appColumns+` FROM apps WHERE self_update = 1 ORDER BY id LIMIT 1`))
}
