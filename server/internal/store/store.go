// Package store owns the SQLite database: schema migrations and all queries.
package store

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"
)

// migrations are append-only. Never edit a migration that has shipped; add a
// new one instead.
var migrations = []string{
	`CREATE TABLE settings (
		key        TEXT PRIMARY KEY,
		value      TEXT NOT NULL,
		updated_at TEXT NOT NULL DEFAULT (datetime('now'))
	)`,
	`CREATE TABLE hosts (
		id           INTEGER PRIMARY KEY AUTOINCREMENT,
		name         TEXT NOT NULL UNIQUE,
		address      TEXT NOT NULL,
		port         INTEGER NOT NULL DEFAULT 22,
		username     TEXT NOT NULL,
		host_key     TEXT NOT NULL DEFAULT '',
		status       TEXT NOT NULL DEFAULT 'unknown',
		last_error   TEXT NOT NULL DEFAULT '',
		last_seen_at TEXT,
		hostname     TEXT NOT NULL DEFAULT '',
		os           TEXT NOT NULL DEFAULT '',
		kernel       TEXT NOT NULL DEFAULT '',
		arch         TEXT NOT NULL DEFAULT '',
		sudo_ok      INTEGER NOT NULL DEFAULT 0,
		created_at   TEXT NOT NULL DEFAULT (datetime('now'))
	)`,
	`CREATE TABLE metric_samples (
		id         INTEGER PRIMARY KEY AUTOINCREMENT,
		host_id    INTEGER NOT NULL REFERENCES hosts(id) ON DELETE CASCADE,
		taken_at   TEXT NOT NULL,
		cpu_pct    REAL NOT NULL,
		mem_used   INTEGER NOT NULL,
		mem_total  INTEGER NOT NULL,
		swap_used  INTEGER NOT NULL DEFAULT 0,
		swap_total INTEGER NOT NULL DEFAULT 0,
		load1      REAL NOT NULL,
		uptime_s   INTEGER NOT NULL DEFAULT 0,
		temp_c     REAL,
		disks      TEXT NOT NULL DEFAULT '[]'
	)`,
	`CREATE INDEX idx_metric_samples_host_time ON metric_samples(host_id, taken_at)`,
	`CREATE TABLE apps (
		id              INTEGER PRIMARY KEY AUTOINCREMENT,
		name            TEXT NOT NULL UNIQUE,
		description     TEXT NOT NULL DEFAULT '',
		install_command TEXT NOT NULL,
		params          TEXT NOT NULL DEFAULT '[]',
		health_type     TEXT NOT NULL DEFAULT 'none',
		health_target   TEXT NOT NULL DEFAULT '',
		created_at      TEXT NOT NULL DEFAULT (datetime('now'))
	)`,
	`CREATE TABLE deployments (
		id          INTEGER PRIMARY KEY AUTOINCREMENT,
		app_id      INTEGER NOT NULL REFERENCES apps(id) ON DELETE CASCADE,
		host_id     INTEGER NOT NULL REFERENCES hosts(id) ON DELETE CASCADE,
		command     TEXT NOT NULL,
		params      TEXT NOT NULL DEFAULT '{}',
		status      TEXT NOT NULL,
		exit_code   INTEGER,
		error       TEXT NOT NULL DEFAULT '',
		log         TEXT NOT NULL DEFAULT '',
		started_at  TEXT NOT NULL,
		finished_at TEXT
	)`,
	`CREATE INDEX idx_deployments_app_host ON deployments(app_id, host_id, started_at)`,
	`CREATE TABLE installations (
		id                 INTEGER PRIMARY KEY AUTOINCREMENT,
		app_id             INTEGER NOT NULL REFERENCES apps(id) ON DELETE CASCADE,
		host_id            INTEGER NOT NULL REFERENCES hosts(id) ON DELETE CASCADE,
		params             TEXT NOT NULL DEFAULT '{}',
		last_deployment_id INTEGER REFERENCES deployments(id) ON DELETE SET NULL,
		health_status      TEXT NOT NULL DEFAULT 'unknown',
		health_detail      TEXT NOT NULL DEFAULT '',
		health_checked_at  TEXT,
		installed_at       TEXT NOT NULL DEFAULT (datetime('now')),
		updated_at         TEXT NOT NULL DEFAULT (datetime('now')),
		UNIQUE(app_id, host_id)
	)`,
	// The machine HostMan itself runs on, once identified, is marked so the UI
	// can label it and so self-updates can be handled differently.
	`ALTER TABLE hosts ADD COLUMN is_self INTEGER NOT NULL DEFAULT 0`,
	`ALTER TABLE hosts ADD COLUMN machine_id TEXT NOT NULL DEFAULT ''`,
	// An app that installs HostMan itself: deploying it to the home host
	// restarts the very process running the deployment.
	`ALTER TABLE apps ADD COLUMN self_update INTEGER NOT NULL DEFAULT 0`,
	// Where a detached deployment writes its output on the host. Non-empty
	// means the command outlives HostMan and can be picked back up.
	`ALTER TABLE deployments ADD COLUMN detached_log TEXT NOT NULL DEFAULT ''`,
	// How an app takes itself back off a host. Optional: an app that does not
	// say cannot be uninstalled, only forgotten.
	`ALTER TABLE apps ADD COLUMN uninstall_command TEXT NOT NULL DEFAULT ''`,
	// Which of an app's two commands a deployment ran. Everything recorded
	// before this column existed was an install.
	`ALTER TABLE deployments ADD COLUMN kind TEXT NOT NULL DEFAULT 'install'`,
}

// DB wraps the SQLite handle.
type DB struct {
	sql *sql.DB
}

// Open opens (creating if needed) the database at path and applies migrations.
func Open(path string) (*DB, error) {
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, fmt.Errorf("create data dir: %w", err)
		}
	}
	dsn := path + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)"
	sqlDB, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	// SQLite writes serialize anyway; a small pool keeps lock contention simple.
	sqlDB.SetMaxOpenConns(4)

	db := &DB{sql: sqlDB}
	if err := db.migrate(context.Background()); err != nil {
		sqlDB.Close()
		return nil, err
	}
	return db, nil
}

func (d *DB) Close() error { return d.sql.Close() }

// SQL exposes the underlying handle for packages that need it directly.
func (d *DB) SQL() *sql.DB { return d.sql }

func (d *DB) migrate(ctx context.Context) error {
	if _, err := d.sql.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (version INTEGER PRIMARY KEY)`); err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}
	var applied int
	if err := d.sql.QueryRowContext(ctx, `SELECT COALESCE(MAX(version), 0) FROM schema_migrations`).Scan(&applied); err != nil {
		return fmt.Errorf("read schema version: %w", err)
	}
	for i := applied; i < len(migrations); i++ {
		tx, err := d.sql.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, migrations[i]); err != nil {
			tx.Rollback()
			return fmt.Errorf("migration %d: %w", i+1, err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO schema_migrations (version) VALUES (?)`, i+1); err != nil {
			tx.Rollback()
			return fmt.Errorf("record migration %d: %w", i+1, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit migration %d: %w", i+1, err)
		}
	}
	return nil
}

// GetSetting returns the value for key, or ok=false if unset.
func (d *DB) GetSetting(ctx context.Context, key string) (string, bool, error) {
	var v string
	err := d.sql.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, key).Scan(&v)
	if err == sql.ErrNoRows {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return v, true, nil
}

// SetSetting upserts key to value.
func (d *DB) SetSetting(ctx context.Context, key, value string) error {
	_, err := d.sql.ExecContext(ctx, `
		INSERT INTO settings (key, value, updated_at) VALUES (?, ?, datetime('now'))
		ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`,
		key, value)
	return err
}
