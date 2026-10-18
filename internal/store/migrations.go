package store

import (
	"embed"
	"fmt"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// Migrate applies every embedded migration newer than the database's
// PRAGMA user_version, each inside its own transaction. Re-running on a
// migrated database is a no-op.
func (d *DB) Migrate() error {
	var version int
	if err := d.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		return fmt.Errorf("read user_version: %w", err)
	}
	entries, err := migrationsFS.ReadDir("migrations")
	if err != nil {
		return fmt.Errorf("list migrations: %w", err)
	}
	for _, e := range entries {
		name := e.Name()
		var n int
		if _, err := fmt.Sscanf(name, "%d_", &n); err != nil {
			return fmt.Errorf("migration %s: bad name", name)
		}
		if n <= version {
			continue
		}
		body, err := migrationsFS.ReadFile("migrations/" + name)
		if err != nil {
			return fmt.Errorf("read migration %s: %w", name, err)
		}
		tx, err := d.db.Begin()
		if err != nil {
			return fmt.Errorf("begin migration %s: %w", name, err)
		}
		if _, err := tx.Exec(string(body)); err != nil {
			tx.Rollback()
			return fmt.Errorf("apply migration %s: %w", name, err)
		}
		// PRAGMA cannot be parameterised; n comes from the file name digits.
		if _, err := tx.Exec(fmt.Sprintf("PRAGMA user_version = %d", n)); err != nil {
			tx.Rollback()
			return fmt.Errorf("bump user_version to %d: %w", n, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit migration %s: %w", name, err)
		}
	}
	return nil
}
