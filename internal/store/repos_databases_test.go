package store

import (
	"errors"
	"testing"
	"time"
)

func seedDatabaseParents(t *testing.T, db *DB) {
	t.Helper()
	if err := db.CreateProject(Project{ID: "p1", Name: "proj", CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateEnvironment(Environment{ID: "env1", ProjectID: "p1", Name: "prod"}); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateServer(Server{ID: "srv1", Name: "box", Address: "127.0.0.1", Port: 22,
		User: "u", AuthType: "key", BaseDir: "/opt/vpstui", MonitorIntervalSec: 15,
		ProxyHTTPPort: 80, ProxyHTTPSPort: 443}); err != nil {
		t.Fatal(err)
	}
}

func TestDatabaseCRUD(t *testing.T) {
	db := openTest(t)
	seedDatabaseParents(t, db)
	row := Database{
		ID: "db1", EnvironmentID: "env1", ServerID: "srv1", DbType: "postgres",
		Name: "main", Config: map[string]any{"publish": true, "port": 55432.0},
		Status: "undeployed", CreatedAt: time.Now().UTC().Round(0),
	}
	if err := db.CreateDatabase(row); err != nil {
		t.Fatalf("create: %v", err)
	}
	got, err := db.DatabaseByID("db1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.DbType != "postgres" || got.Config["publish"] != true {
		t.Errorf("round-trip mismatch: %+v", got)
	}
	if _, err := db.DatabaseByID("nope"); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing row: %v, want ErrNotFound", err)
	}
	if err := db.SetDatabaseStatus("db1", "running"); err != nil {
		t.Fatal(err)
	}
	if got, _ = db.DatabaseByID("db1"); got.Status != "running" {
		t.Errorf("status = %s", got.Status)
	}
	all, err := db.Databases()
	if err != nil || len(all) != 1 {
		t.Fatalf("list: %v %d", err, len(all))
	}
}

func TestBackupScheduleCRUD(t *testing.T) {
	db := openTest(t)
	seedDatabaseParents(t, db)
	if err := db.CreateDatabase(Database{ID: "db1", EnvironmentID: "env1", ServerID: "srv1",
		DbType: "redis", Name: "c", Config: map[string]any{}, CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertBackupSchedule(BackupSchedule{DatabaseID: "db1", CronSpec: "0 4 * * *", Retention: 7}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	// Upsert again replaces (no PK conflict error).
	if err := db.UpsertBackupSchedule(BackupSchedule{DatabaseID: "db1", CronSpec: "*/10 * * * *", Retention: 2}); err != nil {
		t.Fatalf("re-upsert: %v", err)
	}
	sc, err := db.BackupScheduleByDatabase("db1")
	if err != nil || sc.CronSpec != "*/10 * * * *" || sc.Retention != 2 {
		t.Fatalf("schedule: %+v %v", sc, err)
	}
	now := time.Now().UTC().Round(0)
	if err := db.SetBackupLastRun("db1", now); err != nil {
		t.Fatal(err)
	}
	sc, _ = db.BackupScheduleByDatabase("db1")
	if sc.LastRunAt == nil || !sc.LastRunAt.Truncate(time.Second).Equal(now.Truncate(time.Second)) {
		t.Errorf("last_run_at = %v, want ~%v", sc.LastRunAt, now)
	}
	all, err := db.BackupSchedules()
	if err != nil || len(all) != 1 {
		t.Fatalf("list: %v %d", err, len(all))
	}
	// Deleting the database cascades the schedule.
	if err := db.DeleteDatabase("db1"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.BackupScheduleByDatabase("db1"); !errors.Is(err, ErrNotFound) {
		t.Errorf("schedule not cascaded: %v", err)
	}
}
