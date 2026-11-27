package store

import (
	"context"
	"database/sql"
	"time"
)

// PowerSchedule is when one host sleeps and wakes by itself. The store keeps
// the numbers and does not judge them; internal/powersched says what they mean
// and which are allowed.
type PowerSchedule struct {
	HostID int64
	// SleepAt and WakeAt are minutes after midnight.
	SleepAt int
	WakeAt  int
	// Days is a bit set of weekdays, bit 0 for Sunday.
	Days int
	// LastAction is what the most recent run did — "sleep" or "wake" — or
	// empty where the schedule has not run since it was set.
	LastAction string
	// LastAt is when that run was, or nil.
	LastAt *time.Time
	// LastError is why that run failed, or empty where it did not.
	LastError string
}

const powerScheduleColumns = `host_id, sleep_at, wake_at, days, last_action, last_at, last_error`

func scanPowerSchedule(row interface{ Scan(...any) error }) (*PowerSchedule, error) {
	var p PowerSchedule
	var lastAt sql.NullString
	err := row.Scan(&p.HostID, &p.SleepAt, &p.WakeAt, &p.Days, &p.LastAction, &lastAt, &p.LastError)
	if err == sql.ErrNoRows {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if lastAt.Valid {
		if t, err := parseSQLiteTime(lastAt.String); err == nil {
			p.LastAt = &t
		}
	}
	return &p, nil
}

// GetPowerSchedule returns a host's schedule, or ErrNotFound where it has
// none — which is also the answer for a host that does not exist.
func (d *DB) GetPowerSchedule(ctx context.Context, hostID int64) (*PowerSchedule, error) {
	return scanPowerSchedule(d.sql.QueryRowContext(ctx,
		`SELECT `+powerScheduleColumns+` FROM power_schedules WHERE host_id = ?`, hostID))
}

// ListPowerSchedules returns every schedule, in host order.
func (d *DB) ListPowerSchedules(ctx context.Context) ([]*PowerSchedule, error) {
	rows, err := d.sql.QueryContext(ctx, `SELECT `+powerScheduleColumns+` FROM power_schedules ORDER BY host_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*PowerSchedule{}
	for rows.Next() {
		p, err := scanPowerSchedule(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// SetPowerSchedule gives a host a schedule, or replaces the one it has. Only
// the times and days are taken from p. What the old schedule last did is
// dropped: it says nothing about the new one.
func (d *DB) SetPowerSchedule(ctx context.Context, p *PowerSchedule) error {
	_, err := d.sql.ExecContext(ctx, `
		INSERT INTO power_schedules (host_id, sleep_at, wake_at, days) VALUES (?, ?, ?, ?)
		ON CONFLICT(host_id) DO UPDATE SET
			sleep_at = excluded.sleep_at, wake_at = excluded.wake_at, days = excluded.days,
			last_action = '', last_at = NULL, last_error = '', updated_at = datetime('now')`,
		p.HostID, p.SleepAt, p.WakeAt, p.Days)
	return err
}

// DeletePowerSchedule takes a host's schedule away. ErrNotFound where it had
// none.
func (d *DB) DeletePowerSchedule(ctx context.Context, hostID int64) error {
	res, err := d.sql.ExecContext(ctx, `DELETE FROM power_schedules WHERE host_id = ?`, hostID)
	if err != nil {
		return err
	}
	return requireRow(res)
}

// RecordPowerScheduleRun notes what a schedule just did and, where it failed,
// why. It does nothing for a schedule removed in the meantime.
func (d *DB) RecordPowerScheduleRun(ctx context.Context, hostID int64, action string, at time.Time, failure string) error {
	_, err := d.sql.ExecContext(ctx,
		`UPDATE power_schedules SET last_action = ?, last_at = ?, last_error = ? WHERE host_id = ?`,
		action, at.UTC().Format(time.RFC3339), failure, hostID)
	return err
}
