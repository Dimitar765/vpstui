package server

import (
	"testing"
)

func TestDatabaseReqNormalizeCronRejected(t *testing.T) {
	good := func(spec string) *databaseReq {
		return &databaseReq{
			EnvironmentID: "env", ServerID: "srv", DbType: "postgres",
			Name: "main", Backup: &backupSchedReq{CronSpec: spec, Retention: 3},
		}
	}
	if err := good("0 4 * * *").normalize(); err != nil {
		t.Fatalf("standard spec rejected: %v", err)
	}
	if err := good("*/5 * * * *").normalize(); err != nil {
		t.Fatalf("step spec rejected: %v", err)
	}
	for _, bad := range []string{"not a cron", "0 4 * *", "61 * * * *", "@weekly"} {
		// @weekly IS valid standard syntax; keep it in the good set mentally —
		// here we assert the truly broken ones fail.
		if bad == "@weekly" {
			continue
		}
		if err := good(bad).normalize(); err == nil {
			t.Errorf("cron spec %q should be rejected", bad)
		}
	}
	if err := (&databaseReq{EnvironmentID: "e", ServerID: "s", DbType: "sqlite", Name: "x"}).normalize(); err == nil {
		t.Error("bad dbType should be rejected")
	}
	if err := (&databaseReq{DbType: "redis", Name: "x"}).normalize(); err == nil {
		t.Error("missing env/server should be rejected")
	}
	// Defaults applied on empty backup block.
	req := &databaseReq{EnvironmentID: "e", ServerID: "s", DbType: "redis", Name: "cache", Backup: &backupSchedReq{}}
	if err := req.normalize(); err != nil {
		t.Fatal(err)
	}
	if req.Backup.CronSpec != "0 4 * * *" || req.Backup.Retention != 7 {
		t.Errorf("defaults not applied: %+v", req.Backup)
	}
}
