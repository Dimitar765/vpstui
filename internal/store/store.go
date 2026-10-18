// Package store is the vpstui-server's SQLite state layer. Pure-Go driver
// (modernc.org/sqlite) so the daemon cross-compiles like the rest of the
// tree; schema lives in embedded migrations applied in order and tracked by
// PRAGMA user_version. Secrets never live here — server credentials ride
// internal/secret under the daemon's own XDG dir.
package store

import (
	"database/sql"
	"fmt"

	_ "modernc.org/sqlite"
)

// DB wraps the sqlite handle. All repos are methods on it.
type DB struct {
	db *sql.DB
}

// Open opens (creating if absent) the SQLite database at path with WAL
// journaling, a 5 s busy timeout and foreign keys on.
func Open(path string) (*DB, error) {
	dsn := fmt.Sprintf("file:%s?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)", path)
	d, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite %s: %w", path, err)
	}
	// modernc sqlite serialises writers; a single conn avoids SQLITE_BUSY
	// storms and matches the daemon's one-process access pattern.
	d.SetMaxOpenConns(1)
	if err := d.Ping(); err != nil {
		d.Close()
		return nil, fmt.Errorf("ping sqlite %s: %w", path, err)
	}
	return &DB{db: d}, nil
}

// Close closes the underlying handle.
func (d *DB) Close() error { return d.db.Close() }
