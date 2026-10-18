package deploy

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"vpstui/internal/apitypes"
	"vpstui/internal/orchestrator"
	"vpstui/internal/store"
)

func newTestDB(t *testing.T) *store.DB {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := db.Migrate(); err != nil {
		t.Fatal(err)
	}
	return db
}

// TestRecoverInterrupted: queued and running deployments from before a
// restart come back failed with the log note; terminal ones untouched.
func TestRecoverInterrupted(t *testing.T) {
	db := newTestDB(t)
	dataDir := t.TempDir()

	mk := func(id, status string) store.Deployment {
		dep := store.Deployment{ID: id, AppID: "a1", Status: status, Trigger: "manual",
			Release: "r", LogFile: filepath.Join(dataDir, id+".log"), CreatedAt: time.Now().UTC()}
		if err := db.CreateDeployment(dep); err != nil {
			t.Fatal(err)
		}
		return dep
	}
	now := time.Now().UTC()
	if err := db.CreateProject(store.Project{ID: "p1", Name: "p", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateEnvironment(store.Environment{ID: "env1", ProjectID: "p1", Name: "prod"}); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateServer(store.Server{ID: "srv1", Name: "s", Address: "127.0.0.1", Port: 22,
		User: "u", AuthType: "agent", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateApp(store.App{ID: "a1", EnvironmentID: "env1", ServerID: "srv1", Name: "a",
		SourceType: "image", SourceConfig: map[string]any{"image": "nginx"}, EnvVars: map[string]string{},
		Domains: []string{}, AutoDeploy: true, Status: apitypes.AppUndeployed, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	q := mk("d-queued", apitypes.DepQueued)
	r := mk("d-running", apitypes.DepRunning)
	ok := mk("d-done", apitypes.DepSuccess)

	eng := NewEngine(db, orchestrator.NewPool(db), nil, dataDir, nil)
	if err := eng.RecoverInterrupted(); err != nil {
		t.Fatal(err)
	}
	for _, dep := range []store.Deployment{q, r} {
		got, err := db.DeploymentByID(dep.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got.Status != apitypes.DepFailed {
			t.Fatalf("%s status = %s, want failed", dep.ID, got.Status)
		}
		if got.FinishedAt == nil {
			t.Fatalf("%s: finished_at not stamped", dep.ID)
		}
		b, _ := os.ReadFile(dep.LogFile)
		if !strings.Contains(string(b), "interrupted by restart") {
			t.Fatalf("%s log note missing: %q", dep.ID, b)
		}
	}
	got, _ := db.DeploymentByID(ok.ID)
	if got.Status != apitypes.DepSuccess {
		t.Fatalf("terminal deployment disturbed: %s", got.Status)
	}
}

// TestAppLockSerializesPerApp: same app reuses one mutex, different apps
// don't share.
func TestAppLockSerializesPerApp(t *testing.T) {
	eng := NewEngine(nil, nil, nil, t.TempDir(), nil)
	a1, a1b := eng.appLock("a1"), eng.appLock("a1")
	if a1 != a1b {
		t.Fatal("same app must map to the same mutex")
	}
	if a1 == eng.appLock("a2") {
		t.Fatal("different apps must not share a mutex")
	}

	if !a1.TryLock() {
		t.Fatal("fresh mutex should be lockable")
	}
	if eng.appLock("a1").TryLock() {
		t.Fatal("locked app mutex must block the second worker")
	}
	a1.Unlock()
}
