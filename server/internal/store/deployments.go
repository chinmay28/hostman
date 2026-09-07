package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"time"
)

// Deployment statuses.
const (
	DeployRunning     = "running"
	DeploySucceeded   = "succeeded"
	DeployFailed      = "failed"
	DeployCanceled    = "canceled"
	DeployInterrupted = "interrupted" // the server stopped while it was running
)

// What a deployment ran: the app's install command, or the one that takes it
// back off the host again.
const (
	KindInstall   = "install"
	KindUninstall = "uninstall"
)

// Deployment is one run of an app's install command on a host.
type Deployment struct {
	ID      int64             `json:"id"`
	AppID   int64             `json:"appId"`
	HostID  int64             `json:"hostId"`
	Command string            `json:"command"`
	Params  map[string]string `json:"params"`
	// Kind is "install" or "uninstall": which of the app's two commands this
	// run was. Empty on the way in means an install.
	Kind       string     `json:"kind"`
	Status     string     `json:"status"`
	ExitCode   *int       `json:"exitCode"`
	Error      string     `json:"error"`
	Log        string     `json:"log,omitempty"`
	StartedAt  time.Time  `json:"startedAt"`
	FinishedAt *time.Time `json:"finishedAt"`
	// DetachedLog is where the command writes on the host when it has to
	// outlive HostMan. Empty for ordinary deployments.
	DetachedLog string `json:"-"`

	// Denormalized for list views.
	AppName  string `json:"appName,omitempty"`
	HostName string `json:"hostName,omitempty"`
}

// Done reports whether the deployment has stopped running.
func (d *Deployment) Done() bool { return d.Status != DeployRunning }

const deploymentColumns = `d.id, d.app_id, d.host_id, d.command, d.params, d.kind, d.status,
	d.exit_code, d.error, d.started_at, d.finished_at, d.detached_log`

func scanDeployment(row interface{ Scan(...any) error }, withLog bool) (*Deployment, error) {
	var d Deployment
	var params, started string
	var finished sql.NullString
	var appName, hostName sql.NullString
	// Every query joins the app and host names; the log is only worth carrying
	// when a single deployment was asked for.
	targets := []any{&d.ID, &d.AppID, &d.HostID, &d.Command, &params, &d.Kind, &d.Status,
		&d.ExitCode, &d.Error, &started, &finished, &d.DetachedLog, &appName, &hostName}
	if withLog {
		targets = append(targets, &d.Log)
	}
	if err := row.Scan(targets...); err != nil {
		if err == sql.ErrNoRows {
			return nil, ErrNotFound
		}
		return nil, err
	}
	d.Params = map[string]string{}
	_ = json.Unmarshal([]byte(params), &d.Params)
	if t, err := parseSQLiteTime(started); err == nil {
		d.StartedAt = t
	}
	if finished.Valid {
		if t, err := parseSQLiteTime(finished.String); err == nil {
			d.FinishedAt = &t
		}
	}
	d.AppName, d.HostName = appName.String, hostName.String
	return &d, nil
}

// CreateDeployment records a deployment that is about to start.
func (d *DB) CreateDeployment(ctx context.Context, dep *Deployment) (*Deployment, error) {
	params, err := json.Marshal(orEmptyMap(dep.Params))
	if err != nil {
		return nil, err
	}
	kind := dep.Kind
	if kind == "" {
		kind = KindInstall
	}
	res, err := d.sql.ExecContext(ctx, `
		INSERT INTO deployments (app_id, host_id, command, params, kind, status, started_at, detached_log)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		dep.AppID, dep.HostID, dep.Command, string(params), kind, DeployRunning,
		time.Now().UTC().Format(time.RFC3339Nano), dep.DetachedLog)
	if err != nil {
		return nil, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, err
	}
	return d.GetDeployment(ctx, id)
}

// FinishDeployment records the outcome and the captured log.
func (d *DB) FinishDeployment(ctx context.Context, id int64, status string, exitCode *int, errMsg, log string) error {
	res, err := d.sql.ExecContext(ctx, `
		UPDATE deployments SET status = ?, exit_code = ?, error = ?, log = ?, finished_at = ?
		WHERE id = ?`,
		status, exitCode, errMsg, log, time.Now().UTC().Format(time.RFC3339Nano), id)
	if err != nil {
		return err
	}
	return requireRow(res)
}

// GetDeployment returns one deployment including its log.
func (d *DB) GetDeployment(ctx context.Context, id int64) (*Deployment, error) {
	row := d.sql.QueryRowContext(ctx, `SELECT `+deploymentColumns+`, a.name, h.name, d.log
		FROM deployments d
		JOIN apps a ON a.id = d.app_id
		JOIN hosts h ON h.id = d.host_id
		WHERE d.id = ?`, id)
	return scanDeployment(row, true)
}

// DeploymentFilter narrows a deployment listing.
type DeploymentFilter struct {
	AppID  int64
	HostID int64
	Limit  int
}

// ListDeployments returns deployments newest first, without their logs.
func (d *DB) ListDeployments(ctx context.Context, f DeploymentFilter) ([]*Deployment, error) {
	where := []string{}
	args := []any{}
	if f.AppID > 0 {
		where = append(where, "d.app_id = ?")
		args = append(args, f.AppID)
	}
	if f.HostID > 0 {
		where = append(where, "d.host_id = ?")
		args = append(args, f.HostID)
	}
	limit := f.Limit
	if limit <= 0 || limit > 500 {
		limit = 50
	}
	q := `SELECT ` + deploymentColumns + `, a.name, h.name
		FROM deployments d
		JOIN apps a ON a.id = d.app_id
		JOIN hosts h ON h.id = d.host_id`
	if len(where) > 0 {
		q += " WHERE " + strings.Join(where, " AND ")
	}
	q += " ORDER BY d.started_at DESC, d.id DESC LIMIT ?"
	args = append(args, limit)

	rows, err := d.sql.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Deployment{}
	for rows.Next() {
		dep, err := scanDeployment(rows, false)
		if err != nil {
			return nil, err
		}
		out = append(out, dep)
	}
	return out, rows.Err()
}

// InterruptRunningDeployments marks deployments that were in flight when the
// server stopped. Their remote commands may well have completed; the record is
// honest that HostMan stopped watching. Detached deployments are left alone:
// they were designed to outlive HostMan and are resumed instead.
func (d *DB) InterruptRunningDeployments(ctx context.Context) (int64, error) {
	res, err := d.sql.ExecContext(ctx, `
		UPDATE deployments
		SET status = ?, error = 'HostMan restarted while this deployment was running',
			finished_at = ?
		WHERE status = ? AND detached_log = ''`,
		DeployInterrupted, time.Now().UTC().Format(time.RFC3339Nano), DeployRunning)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// Installation is an app that has been deployed to a host at least once.
type Installation struct {
	ID               int64             `json:"id"`
	AppID            int64             `json:"appId"`
	HostID           int64             `json:"hostId"`
	Params           map[string]string `json:"params"`
	LastDeploymentID *int64            `json:"lastDeploymentId"`
	HealthStatus     string            `json:"healthStatus"`
	HealthDetail     string            `json:"healthDetail"`
	HealthCheckedAt  *time.Time        `json:"healthCheckedAt"`
	InstalledAt      time.Time         `json:"installedAt"`
	UpdatedAt        time.Time         `json:"updatedAt"`

	// Denormalized for list views.
	AppName      string `json:"appName,omitempty"`
	HostName     string `json:"hostName,omitempty"`
	HostAddress  string `json:"hostAddress,omitempty"`
	HealthType   string `json:"healthType,omitempty"`
	HealthTarget string `json:"healthTarget,omitempty"`
	LastStatus   string `json:"lastStatus,omitempty"`

	// Ports the app answers on, worked out from the health check and the
	// parameters rather than stored. Filled in on the way out; see
	// deploy.InstallationPorts.
	Ports []int `json:"ports,omitempty"`

	// URL is where to open the app in a browser, from the same two sources and
	// on the same terms: empty when neither says. See deploy.InstallationURL.
	URL string `json:"url,omitempty"`

	// Version is the version last deployed here, read off the parameters that
	// named it. Empty for an app whose command installs whatever is current.
	// See deploy.InstallationVersion.
	Version string `json:"version,omitempty"`
}

const installationColumns = `i.id, i.app_id, i.host_id, i.params, i.last_deployment_id,
	i.health_status, i.health_detail, i.health_checked_at, i.installed_at, i.updated_at,
	a.name, h.name, h.address, a.health_type, a.health_target,
	COALESCE((SELECT status FROM deployments WHERE id = i.last_deployment_id), '')`

const installationJoin = `FROM installations i
	JOIN apps a ON a.id = i.app_id
	JOIN hosts h ON h.id = i.host_id`

func scanInstallation(row interface{ Scan(...any) error }) (*Installation, error) {
	var in Installation
	var params, installed, updated string
	var checked sql.NullString
	err := row.Scan(&in.ID, &in.AppID, &in.HostID, &params, &in.LastDeploymentID,
		&in.HealthStatus, &in.HealthDetail, &checked, &installed, &updated,
		&in.AppName, &in.HostName, &in.HostAddress, &in.HealthType, &in.HealthTarget,
		&in.LastStatus)
	if err == sql.ErrNoRows {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	in.Params = map[string]string{}
	_ = json.Unmarshal([]byte(params), &in.Params)
	if t, err := parseSQLiteTime(installed); err == nil {
		in.InstalledAt = t
	}
	if t, err := parseSQLiteTime(updated); err == nil {
		in.UpdatedAt = t
	}
	if checked.Valid {
		if t, err := parseSQLiteTime(checked.String); err == nil {
			in.HealthCheckedAt = &t
		}
	}
	return &in, nil
}

// ListInstallations returns every app/host pair HostMan has deployed.
func (d *DB) ListInstallations(ctx context.Context) ([]*Installation, error) {
	rows, err := d.sql.QueryContext(ctx,
		`SELECT `+installationColumns+` `+installationJoin+` ORDER BY a.name, h.name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Installation{}
	for rows.Next() {
		in, err := scanInstallation(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, in)
	}
	return out, rows.Err()
}

// GetInstallation returns one installation by id.
func (d *DB) GetInstallation(ctx context.Context, id int64) (*Installation, error) {
	return scanInstallation(d.sql.QueryRowContext(ctx,
		`SELECT `+installationColumns+` `+installationJoin+` WHERE i.id = ?`, id))
}

// UpsertInstallation records a successful deployment against an app/host pair,
// remembering the parameters so the next deploy can prefill them.
func (d *DB) UpsertInstallation(ctx context.Context, appID, hostID int64, params map[string]string, deploymentID int64) error {
	encoded, err := json.Marshal(orEmptyMap(params))
	if err != nil {
		return err
	}
	_, err = d.sql.ExecContext(ctx, `
		INSERT INTO installations (app_id, host_id, params, last_deployment_id, updated_at)
		VALUES (?, ?, ?, ?, datetime('now'))
		ON CONFLICT(app_id, host_id) DO UPDATE SET
			params = excluded.params,
			last_deployment_id = excluded.last_deployment_id,
			updated_at = excluded.updated_at`,
		appID, hostID, string(encoded), deploymentID)
	return err
}

// DeleteInstallation forgets an installation. It does not uninstall anything on
// the host.
func (d *DB) DeleteInstallation(ctx context.Context, id int64) error {
	res, err := d.sql.ExecContext(ctx, `DELETE FROM installations WHERE id = ?`, id)
	if err != nil {
		return err
	}
	return requireRow(res)
}

// ForgetInstallation drops the record of an app on a host, by the pair rather
// than by id — which is what a finished uninstall has to hand. Deleting an
// installation that is already gone is not an error: somebody forgetting it
// while the uninstall ran wanted the same end state.
func (d *DB) ForgetInstallation(ctx context.Context, appID, hostID int64) error {
	_, err := d.sql.ExecContext(ctx,
		`DELETE FROM installations WHERE app_id = ? AND host_id = ?`, appID, hostID)
	return err
}

// SetInstallationHealth records the result of a health check.
func (d *DB) SetInstallationHealth(ctx context.Context, id int64, status, detail string) error {
	_, err := d.sql.ExecContext(ctx, `
		UPDATE installations SET health_status = ?, health_detail = ?, health_checked_at = ?
		WHERE id = ?`,
		status, detail, time.Now().UTC().Format(time.RFC3339Nano), id)
	return err
}

// FindInstallation looks up an installation by app and host.
func (d *DB) FindInstallation(ctx context.Context, appID, hostID int64) (*Installation, error) {
	return scanInstallation(d.sql.QueryRowContext(ctx,
		`SELECT `+installationColumns+` `+installationJoin+` WHERE i.app_id = ? AND i.host_id = ?`,
		appID, hostID))
}

func orEmptyMap(m map[string]string) map[string]string {
	if m == nil {
		return map[string]string{}
	}
	return m
}

// ResumableDeployments returns detached deployments still marked running: they
// kept going on the host while HostMan was restarting.
func (d *DB) ResumableDeployments(ctx context.Context) ([]*Deployment, error) {
	rows, err := d.sql.QueryContext(ctx, `SELECT `+deploymentColumns+`, a.name, h.name
		FROM deployments d
		JOIN apps a ON a.id = d.app_id
		JOIN hosts h ON h.id = d.host_id
		WHERE d.status = ? AND d.detached_log != ''`, DeployRunning)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Deployment{}
	for rows.Next() {
		dep, err := scanDeployment(rows, false)
		if err != nil {
			return nil, err
		}
		out = append(out, dep)
	}
	return out, rows.Err()
}

// SetDetachedLog records where a deployment writes its output on the host.
func (d *DB) SetDetachedLog(ctx context.Context, id int64, path string) error {
	_, err := d.sql.ExecContext(ctx, `UPDATE deployments SET detached_log = ? WHERE id = ?`, path, id)
	return err
}

// HasRunningDeployment reports whether this app is mid-deployment on this host.
func (d *DB) HasRunningDeployment(ctx context.Context, appID, hostID int64) (bool, error) {
	var count int
	err := d.sql.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM deployments WHERE app_id = ? AND host_id = ? AND status = ?`,
		appID, hostID, DeployRunning).Scan(&count)
	return count > 0, err
}
