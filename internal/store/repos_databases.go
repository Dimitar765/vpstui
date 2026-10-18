package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

// ---------------------------------------------------------------- databases

// Database is a managed database instance. Config is the free-form JSON
// config column: known keys are "publish" (bool — publish the port on
// 127.0.0.1), "port" (host port when published) and nothing else; secrets
// always live in internal/secret under db:<id>:*.
type Database struct {
	ID            string
	EnvironmentID string
	ServerID      string
	DbType        string // postgres | mysql | redis | mongo
	Name          string
	Config        map[string]any
	Status        string
	CreatedAt     time.Time
}

func (d *DB) CreateDatabase(r Database) error {
	cfg, err := json.Marshal(r.Config)
	if err != nil {
		return err
	}
	if r.Config == nil {
		cfg = []byte("{}")
	}
	_, err = d.db.Exec(`INSERT INTO databases(id, environment_id, server_id, db_type, name, config, status, created_at)
		VALUES(?,?,?,?,?,?,?,?)`,
		r.ID, r.EnvironmentID, r.ServerID, r.DbType, r.Name, string(cfg), r.Status, now())
	return err
}

func scanDatabase(sc func(dest ...any) error) (Database, error) {
	var r Database
	var cfg string
	var ts int64
	if err := sc(&r.ID, &r.EnvironmentID, &r.ServerID, &r.DbType, &r.Name, &cfg, &r.Status, &ts); err != nil {
		return Database{}, err
	}
	r.Config = map[string]any{}
	_ = json.Unmarshal([]byte(cfg), &r.Config)
	r.CreatedAt = time.Unix(ts, 0).UTC()
	return r, nil
}

const dbCols = `id, environment_id, server_id, db_type, name, config, status, created_at`

func (d *DB) Databases() ([]Database, error) {
	rows, err := d.db.Query(`SELECT ` + dbCols + ` FROM databases ORDER BY created_at, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Database
	for rows.Next() {
		r, err := scanDatabase(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (d *DB) DatabaseByID(id string) (Database, error) {
	r, err := scanDatabase(d.db.QueryRow(`SELECT `+dbCols+` FROM databases WHERE id = ?`, id).Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return Database{}, ErrNotFound
	}
	return r, err
}

func (d *DB) SetDatabaseStatus(id, status string) error {
	_, err := d.db.Exec(`UPDATE databases SET status = ? WHERE id = ?`, status, id)
	return err
}

// DeleteDatabase removes the row; backup_schedules cascades. Remote teardown
// happens in deploy before this is called.
func (d *DB) DeleteDatabase(id string) error {
	return deleteByID(d, "databases", id)
}

// ---------------------------------------------------------------- backup schedules

// BackupSchedule is the per-database cron backup policy.
type BackupSchedule struct {
	DatabaseID string
	CronSpec   string
	Retention  int
	LastRunAt  *time.Time
}

func (d *DB) UpsertBackupSchedule(s BackupSchedule) error {
	_, err := d.db.Exec(`INSERT INTO backup_schedules(database_id, cron_spec, retention) VALUES(?,?,?)
		ON CONFLICT(database_id) DO UPDATE SET cron_spec = excluded.cron_spec, retention = excluded.retention`,
		s.DatabaseID, s.CronSpec, s.Retention)
	return err
}

func (d *DB) BackupScheduleByDatabase(databaseID string) (BackupSchedule, error) {
	var s BackupSchedule
	var last *int64
	err := d.db.QueryRow(
		`SELECT database_id, cron_spec, retention, last_run_at FROM backup_schedules WHERE database_id = ?`,
		databaseID).Scan(&s.DatabaseID, &s.CronSpec, &s.Retention, &last)
	if errors.Is(err, sql.ErrNoRows) {
		return BackupSchedule{}, ErrNotFound
	}
	if err != nil {
		return BackupSchedule{}, err
	}
	if last != nil {
		t := time.Unix(*last, 0).UTC()
		s.LastRunAt = &t
	}
	return s, nil
}

// BackupSchedules returns every schedule (scheduler reload).
func (d *DB) BackupSchedules() ([]BackupSchedule, error) {
	rows, err := d.db.Query(
		`SELECT database_id, cron_spec, retention, last_run_at FROM backup_schedules ORDER BY database_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []BackupSchedule
	for rows.Next() {
		var s BackupSchedule
		var last *int64
		if err := rows.Scan(&s.DatabaseID, &s.CronSpec, &s.Retention, &last); err != nil {
			return nil, err
		}
		if last != nil {
			t := time.Unix(*last, 0).UTC()
			s.LastRunAt = &t
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

func (d *DB) SetBackupLastRun(databaseID string, at time.Time) error {
	_, err := d.db.Exec(`UPDATE backup_schedules SET last_run_at = ? WHERE database_id = ?`,
		at.Unix(), databaseID)
	return err
}
