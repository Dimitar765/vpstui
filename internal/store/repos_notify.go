package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// repos_notify hosts the S7 domains: notification channels, alert rules —
// plus UpdateUserPassword, the store write path for the auth password-change
// fold-in (repos.go itself is outside this phase's file ownership; the method
// is hosted beside the S7 code it serves).

// ---------------------------------------------------------------- channels

// NotificationChannel is one notification_channels row. Config is the
// free-form per-kind JSON column; Events lists the subscribed event strings.
type NotificationChannel struct {
	ID      string
	Kind    string // discord | slack | telegram | webhook | email
	Config  map[string]any
	Events  []string
	Enabled bool
}

const channelCols = `id, kind, config, events, enabled`

func scanChannel(r rowScanner) (NotificationChannel, error) {
	var c NotificationChannel
	var cfg, events string
	var en int
	if err := r.Scan(&c.ID, &c.Kind, &cfg, &events, &en); err != nil {
		return NotificationChannel{}, err
	}
	if err := json.Unmarshal([]byte(cfg), &c.Config); err != nil {
		return NotificationChannel{}, fmt.Errorf("channel %s config: %w", c.ID, err)
	}
	if err := json.Unmarshal([]byte(events), &c.Events); err != nil {
		return NotificationChannel{}, fmt.Errorf("channel %s events: %w", c.ID, err)
	}
	c.Enabled = en == 1
	return c, nil
}

func (c NotificationChannel) args() []any {
	cfg, _ := json.Marshal(c.Config)
	if c.Config == nil {
		cfg, _ = json.Marshal(map[string]any{})
	}
	events, _ := json.Marshal(c.Events)
	if c.Events == nil {
		events, _ = json.Marshal([]string{})
	}
	return []any{c.ID, c.Kind, string(cfg), string(events), c.Enabled}
}

func (d *DB) Channels() ([]NotificationChannel, error) {
	rows, err := d.db.Query("SELECT " + channelCols + " FROM notification_channels ORDER BY id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []NotificationChannel{}
	for rows.Next() {
		c, err := scanChannel(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (d *DB) ChannelByID(id string) (NotificationChannel, error) {
	c, err := scanChannel(d.db.QueryRow("SELECT "+channelCols+" FROM notification_channels WHERE id = ?", id))
	if errors.Is(err, sql.ErrNoRows) {
		return NotificationChannel{}, ErrNotFound
	}
	return c, err
}

func (d *DB) CreateChannel(c NotificationChannel) error {
	_, err := d.db.Exec("INSERT INTO notification_channels (id, kind, config, events, enabled) VALUES (?,?,?,?,?)",
		c.args()...)
	return err
}

func (d *DB) UpdateChannel(c NotificationChannel) error {
	a := c.args()
	res, err := d.db.Exec("UPDATE notification_channels SET kind=?, config=?, events=?, enabled=? WHERE id=?",
		a[1], a[2], a[3], a[4], c.ID)
	if err != nil {
		return err
	}
	return requireRow(res)
}

func (d *DB) DeleteChannel(id string) error {
	res, err := d.db.Exec("DELETE FROM notification_channels WHERE id = ?", id)
	if err != nil {
		return err
	}
	return requireRow(res)
}

// ---------------------------------------------------------------- alerts

// AlertRule is one alert_rules row. Empty ServerID matches every server;
// empty ContainerPattern marks a host-level rule.
type AlertRule struct {
	ID               string
	ServerID         string // "" = all servers
	ContainerPattern string // "" = host-level; regexp on container name
	Metric           string // load1 | mem_pct | container_cpu_pct | container_mem_pct | container_down
	Threshold        float64
	DurationSec      int
	Enabled          bool
	State            string // ok | firing
	LastFiredAt      *time.Time
}

const alertCols = `id, server_id, container_pattern, metric, threshold, duration_sec, enabled, state, last_fired_at`

func scanAlert(r rowScanner) (AlertRule, error) {
	var a AlertRule
	var serverID, pattern sql.NullString
	var threshold sql.NullFloat64
	var en int
	var fired sql.NullInt64
	if err := r.Scan(&a.ID, &serverID, &pattern, &a.Metric, &threshold, &a.DurationSec, &en, &a.State, &fired); err != nil {
		return AlertRule{}, err
	}
	a.ServerID, a.ContainerPattern = serverID.String, pattern.String
	a.Threshold = threshold.Float64
	a.Enabled = en == 1
	if fired.Valid {
		t := time.Unix(fired.Int64, 0).UTC()
		a.LastFiredAt = &t
	}
	return a, nil
}

func (a AlertRule) args() []any {
	var serverID, pattern, threshold any
	if a.ServerID != "" {
		serverID = a.ServerID
	}
	if a.ContainerPattern != "" {
		pattern = a.ContainerPattern
	}
	if a.Threshold != 0 {
		threshold = a.Threshold
	}
	var lastFired any
	if a.LastFiredAt != nil {
		lastFired = a.LastFiredAt.Unix()
	}
	return []any{a.ID, serverID, pattern, a.Metric, threshold, a.DurationSec, a.Enabled, a.State, lastFired}
}

func (d *DB) AlertRules() ([]AlertRule, error) {
	rows, err := d.db.Query("SELECT " + alertCols + " FROM alert_rules ORDER BY id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AlertRule{}
	for rows.Next() {
		a, err := scanAlert(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func (d *DB) AlertRuleByID(id string) (AlertRule, error) {
	a, err := scanAlert(d.db.QueryRow("SELECT "+alertCols+" FROM alert_rules WHERE id = ?", id))
	if errors.Is(err, sql.ErrNoRows) {
		return AlertRule{}, ErrNotFound
	}
	return a, err
}

func (d *DB) CreateAlertRule(a AlertRule) error {
	if a.State == "" {
		a.State = "ok"
	}
	_, err := d.db.Exec("INSERT INTO alert_rules (id, server_id, container_pattern, metric, threshold, duration_sec, enabled, state, last_fired_at) VALUES (?,?,?,?,?,?,?,?,?)",
		a.args()...)
	return err
}

func (d *DB) UpdateAlertRule(a AlertRule) error {
	v := a.args()
	res, err := d.db.Exec(`UPDATE alert_rules SET server_id=?, container_pattern=?, metric=?, threshold=?,
		duration_sec=?, enabled=? WHERE id=?`,
		v[1], v[2], a.Metric, v[4], a.DurationSec, a.Enabled, a.ID)
	if err != nil {
		return err
	}
	return requireRow(res)
}

// SetAlertState records a firing/ok transition; firedAt is stamped on fire
// and cleared on resolve (nil).
func (d *DB) SetAlertState(id, state string, firedAt *time.Time) error {
	var lastFired any
	if firedAt != nil {
		lastFired = firedAt.Unix()
	}
	res, err := d.db.Exec("UPDATE alert_rules SET state=?, last_fired_at=? WHERE id=?", state, lastFired, id)
	if err != nil {
		return err
	}
	return requireRow(res)
}

// DisableAlertRule turns a rule off after a load-time failure (bad regexp).
func (d *DB) DisableAlertRule(id string) error {
	res, err := d.db.Exec("UPDATE alert_rules SET enabled=0 WHERE id=?", id)
	if err != nil {
		return err
	}
	return requireRow(res)
}

func (d *DB) DeleteAlertRule(id string) error {
	res, err := d.db.Exec("DELETE FROM alert_rules WHERE id = ?", id)
	if err != nil {
		return err
	}
	return requireRow(res)
}

// ---------------------------------------------------------------- users

// UpdateUserPassword replaces the admin's bcrypt hash (password change).
// Sessions are intentionally untouched: changing the password keeps existing
// logins alive.
func (d *DB) UpdateUserPassword(id, passwordHash string) error {
	res, err := d.db.Exec("UPDATE users SET password_hash=? WHERE id=?", passwordHash, id)
	if err != nil {
		return err
	}
	return requireRow(res)
}

// requireRow maps a 0-rows-affected UPDATE/DELETE onto ErrNotFound so
// handlers can 404 uniformly.
func requireRow(res sql.Result) error {
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}
