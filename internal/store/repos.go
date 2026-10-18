package store

import (
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// ErrNotFound is returned by Get-style methods when no row matches.
var ErrNotFound = errors.New("not found")

func now() int64 { return time.Now().Unix() }

// ---------------------------------------------------------------- users

type User struct {
	ID           string
	Email        string
	PasswordHash string
	CreatedAt    time.Time
}

func (d *DB) CountUsers() (int, error) {
	var n int
	err := d.db.QueryRow(`SELECT COUNT(*) FROM users`).Scan(&n)
	return n, err
}

func (d *DB) CreateUser(id, email, passwordHash string) error {
	_, err := d.db.Exec(`INSERT INTO users(id, email, password_hash, created_at) VALUES(?,?,?,?)`,
		id, email, passwordHash, now())
	return err
}

func (d *DB) UserByEmail(email string) (User, error) {
	row := d.db.QueryRow(`SELECT id, email, password_hash, created_at FROM users WHERE email = ?`, email)
	return scanUser(row)
}

func (d *DB) UserByID(id string) (User, error) {
	row := d.db.QueryRow(`SELECT id, email, password_hash, created_at FROM users WHERE id = ?`, id)
	return scanUser(row)
}

// AdminUser returns the single admin (users/teams are deferred). ErrNotFound
// before setup has run.
func (d *DB) AdminUser() (User, error) {
	row := d.db.QueryRow(`SELECT id, email, password_hash, created_at FROM users ORDER BY created_at LIMIT 1`)
	return scanUser(row)
}

func scanUser(row *sql.Row) (User, error) {
	var u User
	var created int64
	err := row.Scan(&u.ID, &u.Email, &u.PasswordHash, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, ErrNotFound
	}
	if err != nil {
		return User{}, err
	}
	u.CreatedAt = time.Unix(created, 0)
	return u, nil
}

// ---------------------------------------------------------------- sessions

type Session struct {
	TokenHash string
	UserID    string
	ExpiresAt time.Time
}

func (d *DB) CreateSession(tokenHash, userID string, expiresAt time.Time) error {
	_, err := d.db.Exec(`INSERT INTO sessions(token_hash, user_id, created_at, expires_at) VALUES(?,?,?,?)`,
		tokenHash, userID, now(), expiresAt.Unix())
	return err
}

// Session returns the live session with this hash; expired sessions read as
// not found.
func (d *DB) Session(tokenHash string) (Session, error) {
	var s Session
	var exp int64
	err := d.db.QueryRow(`SELECT token_hash, user_id, expires_at FROM sessions WHERE token_hash = ?`, tokenHash).
		Scan(&s.TokenHash, &s.UserID, &exp)
	if errors.Is(err, sql.ErrNoRows) {
		return Session{}, ErrNotFound
	}
	if err != nil {
		return Session{}, err
	}
	s.ExpiresAt = time.Unix(exp, 0)
	if time.Now().After(s.ExpiresAt) {
		_ = d.DeleteSession(tokenHash)
		return Session{}, ErrNotFound
	}
	return s, nil
}

func (d *DB) DeleteSession(tokenHash string) error {
	_, err := d.db.Exec(`DELETE FROM sessions WHERE token_hash = ?`, tokenHash)
	return err
}

// ---------------------------------------------------------------- api keys

type APIKey struct {
	ID         string
	Name       string
	TokenHash  string
	CreatedAt  time.Time
	LastUsedAt *time.Time
}

func (d *DB) CreateKey(id, name, tokenHash string) error {
	_, err := d.db.Exec(`INSERT INTO api_keys(id, name, token_hash, created_at) VALUES(?,?,?,?)`,
		id, name, tokenHash, now())
	return err
}

func (d *DB) Keys() ([]APIKey, error) {
	rows, err := d.db.Query(`SELECT id, name, token_hash, created_at, last_used_at FROM api_keys ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []APIKey
	for rows.Next() {
		var k APIKey
		var created int64
		var last sql.NullInt64
		if err := rows.Scan(&k.ID, &k.Name, &k.TokenHash, &created, &last); err != nil {
			return nil, err
		}
		k.CreatedAt = time.Unix(created, 0)
		if last.Valid {
			t := time.Unix(last.Int64, 0)
			k.LastUsedAt = &t
		}
		out = append(out, k)
	}
	return out, rows.Err()
}

// KeyByTokenHash looks a key up by its stored sha256 — the auth path. The
// caller knows a row exists only if the hash matches, so no timing leak
// beyond the index lookup itself.
func (d *DB) KeyByTokenHash(hash string) (APIKey, error) {
	var k APIKey
	var created int64
	var last sql.NullInt64
	err := d.db.QueryRow(`SELECT id, name, token_hash, created_at, last_used_at FROM api_keys WHERE token_hash = ?`, hash).
		Scan(&k.ID, &k.Name, &k.TokenHash, &created, &last)
	if errors.Is(err, sql.ErrNoRows) {
		return APIKey{}, ErrNotFound
	}
	if err != nil {
		return APIKey{}, err
	}
	k.CreatedAt = time.Unix(created, 0)
	if last.Valid {
		t := time.Unix(last.Int64, 0)
		k.LastUsedAt = &t
	}
	return k, nil
}

func (d *DB) KeyByID(id string) (APIKey, error) {
	var k APIKey
	var created int64
	var last sql.NullInt64
	err := d.db.QueryRow(`SELECT id, name, token_hash, created_at, last_used_at FROM api_keys WHERE id = ?`, id).
		Scan(&k.ID, &k.Name, &k.TokenHash, &created, &last)
	if errors.Is(err, sql.ErrNoRows) {
		return APIKey{}, ErrNotFound
	}
	if err != nil {
		return APIKey{}, err
	}
	k.CreatedAt = time.Unix(created, 0)
	if last.Valid {
		t := time.Unix(last.Int64, 0)
		k.LastUsedAt = &t
	}
	return k, nil
}

// TouchKey records a key use, but only if the stored stamp is older than a
// minute — a polling client must not turn every request into a write.
func (d *DB) TouchKey(id string) error {
	_, err := d.db.Exec(`UPDATE api_keys SET last_used_at = ? WHERE id = ? AND (last_used_at IS NULL OR last_used_at < ?)`,
		now(), id, now()-60)
	return err
}

func (d *DB) DeleteKey(id string) error {
	res, err := d.db.Exec(`DELETE FROM api_keys WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// ---------------------------------------------------------------- servers

// Server is one managed VPS row. Credentials are never here: the API layer
// puts them into internal/secret under "<ID>:password"/"<ID>:passphrase".
type Server struct {
	ID                 string
	Name               string
	Address            string
	Port               int
	User               string
	AuthType           string // key | password | agent
	KeyPath            string
	BaseDir            string
	MonitorIntervalSec int
	ProxyHTTPPort      int
	ProxyHTTPSPort     int
	CreatedAt          time.Time
	LastSeenAt         *time.Time
	Status             string
}

const serverCols = `id, name, address, port, user, auth_type, key_path, base_dir,
  monitor_interval_sec, proxy_http_port, proxy_https_port, created_at, last_seen_at, status`

func (d *DB) CreateServer(s Server) error {
	_, err := d.db.Exec(`INSERT INTO servers(`+serverCols+`) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		s.ID, s.Name, s.Address, s.Port, s.User, s.AuthType, s.KeyPath, s.BaseDir,
		s.MonitorIntervalSec, s.ProxyHTTPPort, s.ProxyHTTPSPort, now(), nil, "unknown")
	return err
}

func (d *DB) UpdateServer(s Server) error {
	res, err := d.db.Exec(`UPDATE servers SET name=?, address=?, port=?, user=?, auth_type=?, key_path=?,
      base_dir=?, monitor_interval_sec=?, proxy_http_port=?, proxy_https_port=? WHERE id=?`,
		s.Name, s.Address, s.Port, s.User, s.AuthType, s.KeyPath, s.BaseDir,
		s.MonitorIntervalSec, s.ProxyHTTPPort, s.ProxyHTTPSPort, s.ID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func (d *DB) Servers() ([]Server, error) {
	rows, err := d.db.Query(`SELECT ` + serverCols + ` FROM servers ORDER BY created_at, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Server
	for rows.Next() {
		s, err := scanServer(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

func (d *DB) ServerByID(id string) (Server, error) {
	row := d.db.QueryRow(`SELECT `+serverCols+` FROM servers WHERE id = ?`, id)
	s, err := scanServer(row)
	if err != nil {
		return Server{}, err
	}
	return s, nil
}

func (d *DB) DeleteServer(id string) error {
	res, err := d.db.Exec(`DELETE FROM servers WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// SetServerStatus records the monitor's view of a server.
func (d *DB) SetServerStatus(id, status string, lastSeen *time.Time) error {
	var ls any
	if lastSeen != nil {
		ls = lastSeen.Unix()
	}
	_, err := d.db.Exec(`UPDATE servers SET status = ?, last_seen_at = ? WHERE id = ?`, status, ls, id)
	return err
}

type rowScanner interface{ Scan(dest ...any) error }

func scanServer(r rowScanner) (Server, error) {
	var s Server
	var created int64
	var last sql.NullInt64
	err := r.Scan(&s.ID, &s.Name, &s.Address, &s.Port, &s.User, &s.AuthType, &s.KeyPath, &s.BaseDir,
		&s.MonitorIntervalSec, &s.ProxyHTTPPort, &s.ProxyHTTPSPort, &created, &last, &s.Status)
	if errors.Is(err, sql.ErrNoRows) {
		return Server{}, ErrNotFound
	}
	if err != nil {
		return Server{}, err
	}
	s.CreatedAt = time.Unix(created, 0)
	if last.Valid {
		t := time.Unix(last.Int64, 0)
		s.LastSeenAt = &t
	}
	return s, nil
}

// ---------------------------------------------------------------- samples

type MetricSample struct {
	TS         time.Time
	ServerID   string
	Load1      float64
	Load5      float64
	Load15     float64
	MemUsedMB  int
	MemTotalMB int
}

type ContainerSample struct {
	TS          time.Time
	ServerID    string
	ContainerID string
	Name        string
	CPUPct      float64
	MemPct      float64
	State       string
}

func (d *DB) InsertMetricSample(s MetricSample) error {
	_, err := d.db.Exec(`INSERT OR REPLACE INTO metric_samples(ts, server_id, load1, load5, load15, mem_used_mb, mem_total_mb)
      VALUES(?,?,?,?,?,?,?)`,
		s.TS.Unix(), s.ServerID, s.Load1, s.Load5, s.Load15, s.MemUsedMB, s.MemTotalMB)
	return err
}

func (d *DB) InsertContainerSamples(samples []ContainerSample) error {
	if len(samples) == 0 {
		return nil
	}
	tx, err := d.db.Begin()
	if err != nil {
		return err
	}
	for _, s := range samples {
		if _, err := tx.Exec(`INSERT OR REPLACE INTO container_samples(ts, server_id, container_id, name, cpu_pct, mem_pct, state)
          VALUES(?,?,?,?,?,?,?)`,
			s.TS.Unix(), s.ServerID, s.ContainerID, s.Name, s.CPUPct, s.MemPct, s.State); err != nil {
			tx.Rollback()
			return fmt.Errorf("insert container sample: %w", err)
		}
	}
	return tx.Commit()
}

// PruneSamples removes metric and container samples older than cutoff.
func (d *DB) PruneSamples(cutoff time.Time) error {
	for _, q := range []string{
		`DELETE FROM metric_samples WHERE ts < ?`,
		`DELETE FROM container_samples WHERE ts < ?`,
	} {
		if _, err := d.db.Exec(q, cutoff.Unix()); err != nil {
			return err
		}
	}
	return nil
}
