package store

import (
	"path/filepath"
	"testing"
	"time"
)

func openTest(t *testing.T) *DB {
	t.Helper()
	db, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if err := db.Migrate(); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

func TestMigrateFromEmptyFile(t *testing.T) {
	db := openTest(t)
	// Idempotent: a second Migrate is a no-op, and the schema actually works.
	if err := db.Migrate(); err != nil {
		t.Fatalf("second migrate: %v", err)
	}
	if err := db.CreateUser("u1", "a@b.c", "hash"); err != nil {
		t.Fatalf("create user: %v", err)
	}
	n, err := db.CountUsers()
	if err != nil || n != 1 {
		t.Fatalf("count users: %v %d", err, n)
	}
}

func TestUserSessionFlow(t *testing.T) {
	db := openTest(t)
	if _, err := db.UserByEmail("a@b.c"); err != ErrNotFound {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
	if err := db.CreateUser("u1", "a@b.c", "hash"); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateUser("u2", "a@b.c", "h"); err == nil {
		t.Fatal("duplicate email must fail")
	}
	if err := db.CreateSession("hash1", "u1", time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if s, err := db.Session("hash1"); err != nil || s.UserID != "u1" {
		t.Fatalf("session: %v %+v", err, s)
	}
	// Expired sessions read as not found.
	if err := db.CreateSession("hash2", "u1", time.Now().Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Session("hash2"); err != ErrNotFound {
		t.Fatalf("expired session: %v", err)
	}
}

func TestKeys(t *testing.T) {
	db := openTest(t)
	if err := db.CreateKey("k1", "ci", "h1"); err != nil {
		t.Fatal(err)
	}
	k, err := db.KeyByTokenHash("h1")
	if err != nil || k.ID != "k1" {
		t.Fatalf("by hash: %v %+v", err, k)
	}
	if k.LastUsedAt != nil {
		t.Fatal("fresh key has no last_used_at")
	}
	if err := db.TouchKey("k1"); err != nil {
		t.Fatal(err)
	}
	if k, _ := db.KeyByID("k1"); k.LastUsedAt == nil {
		t.Fatal("touch recorded")
	}
	// Touch inside the minute window must not write again (same second-level
	// stamp is the observable).
	if err := db.TouchKey("k1"); err != nil {
		t.Fatal(err)
	}
	if err := db.DeleteKey("k1"); err != nil {
		t.Fatal(err)
	}
	if err := db.DeleteKey("k1"); err != ErrNotFound {
		t.Fatalf("double delete: %v", err)
	}
}

func TestServerCRUD(t *testing.T) {
	db := openTest(t)
	row := Server{ID: "s1", Name: "box", Address: "127.0.0.1", Port: 22, User: "dimitar",
		AuthType: "key", BaseDir: "/opt/vpstui", MonitorIntervalSec: 15,
		ProxyHTTPPort: 80, ProxyHTTPSPort: 443}
	if err := db.CreateServer(row); err != nil {
		t.Fatal(err)
	}
	got, err := db.ServerByID("s1")
	if err != nil || got.Status != "unknown" || got.BaseDir != "/opt/vpstui" {
		t.Fatalf("get: %v %+v", err, got)
	}
	row.Name = "box2"
	if err := db.UpdateServer(row); err != nil {
		t.Fatal(err)
	}
	if got, _ = db.ServerByID("s1"); got.Name != "box2" {
		t.Fatal("update applied")
	}
	now := time.Now()
	if err := db.SetServerStatus("s1", "online", &now); err != nil {
		t.Fatal(err)
	}
	if got, _ = db.ServerByID("s1"); got.Status != "online" || got.LastSeenAt == nil {
		t.Fatal("status update applied")
	}
	if err := db.DeleteServer("s1"); err != nil {
		t.Fatal(err)
	}
	if err := db.DeleteServer("s1"); err != ErrNotFound {
		t.Fatalf("double delete: %v", err)
	}
}

func TestSamplesAndPrune(t *testing.T) {
	db := openTest(t)
	ts := time.Now()
	if err := db.InsertMetricSample(MetricSample{TS: ts, ServerID: "s1", Load1: 0.5, MemUsedMB: 100, MemTotalMB: 1000}); err != nil {
		t.Fatal(err)
	}
	old := ts.Add(-8 * 24 * time.Hour)
	if err := db.InsertMetricSample(MetricSample{TS: old, ServerID: "s1", Load1: 1}); err != nil {
		t.Fatal(err)
	}
	if err := db.InsertContainerSamples([]ContainerSample{{
		TS: ts, ServerID: "s1", ContainerID: "abc", Name: "web", CPUPct: 2.5, MemPct: 10, State: "running",
	}}); err != nil {
		t.Fatal(err)
	}
	if err := db.PruneSamples(ts.Add(-7 * 24 * time.Hour)); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := db.db.QueryRow(`SELECT COUNT(*) FROM metric_samples`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("prune metric: %v %d", err, n)
	}
	if err := db.db.QueryRow(`SELECT COUNT(*) FROM container_samples`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("container samples kept: %v %d", err, n)
	}
}
