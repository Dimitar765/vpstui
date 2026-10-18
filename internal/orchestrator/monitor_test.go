package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"vpstui/internal/apitypes"
	"vpstui/internal/docker"
	"vpstui/internal/models"
	"vpstui/internal/store"
)

// canned ps JSON in the same shape `docker ps --format json` emits, decoded
// the way docker.parsePs does, so the fake feeds realistic wire data.
const cannedPs = `{"ID":"a1b2c3d4e5f6","Names":"web-1","Image":"nginx:1.27","State":"running","Status":"Up 2 hours","CreatedAt":"2026-10-18 10:00:00 +0000 UTC","Labels":"com.docker.compose.project=vpstui-x","Networks":["bridge"]}
{"ID":"f6e5d4c3b2a1","Names":"cache","Image":"redis:7","State":"exited","Status":"Exited (0)","CreatedAt":"2026-10-18 09:00:00 +0000 UTC","Labels":"","Networks":null}`
const cannedStats = `{"ID":"a1b2c3d4e5f6","Name":"web-1","CPUPerc":"12.50%","MemUsage":"20MiB / 1GiB","MemPerc":"1.95%"}`

type psRow struct {
	ID, Names, Image, State, Status, CreatedAt, Labels string
	Networks                                           []string
}
type statsRow struct {
	ID, Name, CPUPerc, MemUsage, MemPerc string
}

// fakeClient implements Client. Snapshot blocks until release is closed so
// tests can pile up events mid-snapshot.
type fakeClient struct {
	mu        sync.Mutex
	snapCalls int
	sweeps    int
	fail      error
	release   chan struct{}
	events    chan string // closed by the test to end the stream
}

func (f *fakeClient) Snapshot(ctx context.Context) ([]models.Container, []models.ContainerStats, models.HostStats, error) {
	f.mu.Lock()
	f.snapCalls++
	f.mu.Unlock()
	if f.release != nil {
		select {
		case <-f.release:
		case <-ctx.Done():
			return nil, nil, models.HostStats{}, ctx.Err()
		}
	}
	if f.fail != nil {
		return nil, nil, models.HostStats{}, f.fail
	}
	var rows []psRow
	dec := json.NewDecoder(strings.NewReader(cannedPs))
	for dec.More() {
		var r psRow
		if err := dec.Decode(&r); err != nil {
			panic(err)
		}
		rows = append(rows, r)
	}
	var cs []models.Container
	for _, r := range rows {
		cs = append(cs, models.Container{ID: r.ID, Names: r.Names, Image: r.Image, State: r.State, Status: r.Status, Labels: r.Labels})
	}
	var srows []statsRow
	sdec := json.NewDecoder(strings.NewReader(cannedStats))
	for sdec.More() {
		var r statsRow
		if err := sdec.Decode(&r); err != nil {
			panic(err)
		}
		srows = append(srows, r)
	}
	var stats []models.ContainerStats
	for _, r := range srows {
		stats = append(stats, models.ContainerStats{ID: r.ID, Name: r.Name, CPUPerc: r.CPUPerc, MemUsage: r.MemUsage, MemPerc: r.MemPerc})
	}
	return cs, stats, models.HostStats{Load1: 0.4, MemTotalMB: 16000, MemUsedMB: 4000, NumCPU: 8, UptimeSec: 7200}, nil
}

func (f *fakeClient) StreamEvents(ctx context.Context) (io.ReadCloser, error) {
	if f.events == nil {
		return nil, errors.New("no events")
	}
	return chanReader(f.events), nil
}

type chanReader chan string

func (c chanReader) Read(p []byte) (int, error) {
	line, ok := <-c
	if !ok {
		return 0, io.EOF
	}
	return copy(p, line+"\n"), nil
}
func (c chanReader) Close() error { return nil }

func (f *fakeClient) Start(context.Context, string) error   { return nil }
func (f *fakeClient) Stop(context.Context, string) error    { return nil }
func (f *fakeClient) Restart(context.Context, string) error { return nil }
func (f *fakeClient) Rm(context.Context, string) error      { return nil }

func (f *fakeClient) SweepErrors(context.Context, string, int) (string, error) {
	f.mu.Lock()
	f.sweeps++
	f.mu.Unlock()
	return "a1b2c3d4e5f6\tweb-1\t2026-10-18T10:00:01.000000000Z something ERROR: connection refused to db:5432\n", nil
}

func (f *fakeClient) calls() (snaps, sweeps int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.snapCalls, f.sweeps
}

func newTestOrch(t *testing.T) (*Orchestrator, *store.DB) {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "o.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := db.Migrate(); err != nil {
		t.Fatal(err)
	}
	pool := NewPool(db)
	o := NewOrchestrator(db, pool, nil, log.New(io.Discard, "", 0))
	t.Cleanup(o.Stop)
	return o, db
}

func addServer(t *testing.T, db *store.DB, interval int) store.Server {
	t.Helper()
	s := store.Server{ID: "srv1", Name: "loop", Address: "127.0.0.1", Port: 22, User: "dimitar",
		AuthType: "key", BaseDir: "/opt/vpstui", MonitorIntervalSec: interval,
		ProxyHTTPPort: 8080, ProxyHTTPSPort: 8443}
	if err := db.CreateServer(s); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestFleetSnapshotShape(t *testing.T) {
	o, db := newTestOrch(t)
	addServer(t, db, 1)
	fake := &fakeClient{}
	o.SetClientFactory(func(context.Context, store.Server) (Client, error) { return fake, nil })
	if err := o.Start(context.Background()); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		fl := o.Fleet()
		if len(fl) == 1 && fl[0].Status == apitypes.StatusOnline {
			fs := fl[0]
			if fs.HostStats == nil || !fs.HasHostStats {
				t.Fatal("host stats missing")
			}
			if fs.HostStats.Load1 != 0.4 || fs.HostStats.MemTotalMB != 16000 {
				t.Fatalf("host stats wrong: %+v", fs.HostStats)
			}
			if len(fs.Containers) != 2 {
				t.Fatalf("containers: %+v", fs.Containers)
			}
			web := fs.Containers[0]
			if web.Name != "web-1" || web.Image != "nginx:1.27" || web.CPUPct == nil || *web.CPUPct != 12.5 {
				t.Fatalf("container row: %+v", web)
			}
			if web.MemPct == nil || *web.MemPct != 1.95 || web.MemUsage != "20MiB / 1GiB" {
				t.Fatalf("container mem: %+v", web)
			}
			if fs.Containers[1].CPUPct != nil {
				t.Fatal("container without stats must leave cpuPct absent")
			}
			if fs.FetchedAt == nil || fs.LastSeenAt == nil {
				t.Fatal("timestamps missing")
			}
			// Online status persisted.
			if row, _ := db.ServerByID("srv1"); row.Status != apitypes.StatusOnline || row.LastSeenAt == nil {
				t.Fatal("status not persisted")
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("server never came online: %+v", o.Fleet())
}

func TestEventsAcceleratorCoalesces(t *testing.T) {
	o, db := newTestOrch(t)
	addServer(t, db, 1)
	release := make(chan struct{})
	events := make(chan string)
	fake := &fakeClient{release: release, events: events}
	o.SetClientFactory(func(context.Context, store.Server) (Client, error) { return fake, nil })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := o.Start(ctx); err != nil {
		t.Fatal(err)
	}

	// First snapshot starts immediately and blocks inside the fake.
	deadline := time.Now().Add(2 * time.Second)
	for {
		if s, _ := fake.calls(); s == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("first snapshot never started")
		}
		time.Sleep(5 * time.Millisecond)
	}

	// A burst of docker events while one snapshot is in flight must collapse
	// into exactly one follow-up snapshot, never a queue.
	for range 10 {
		events <- "2026-10-18T10:00:00.000000000Z container die a1b2c3d4e5f6 (web)"
	}

	close(release) // let the blocked snapshot finish
	deadline = time.Now().Add(3 * time.Second)
	for {
		snaps, _ := fake.calls()
		if snaps == 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("coalescing broken: %d snapshots", snaps)
		}
		time.Sleep(5 * time.Millisecond)
	}
	// Give the loop a beat to prove nothing extra fires from the burst.
	time.Sleep(300 * time.Millisecond)
	if snaps, _ := fake.calls(); snaps != 2 {
		t.Fatalf("burst produced extra snapshots: %d", snaps)
	}
}

// Regression: the handlers reconcile with short-lived request contexts; a
// monitor spawned by such a Reapply must NOT die when that context expires
// (was: "dial … operation was canceled" → offline until daemon restart).
func TestReapplyRequestContextDoesNotKillMonitors(t *testing.T) {
	o, db := newTestOrch(t)
	fake := &fakeClient{}
	o.SetClientFactory(func(context.Context, store.Server) (Client, error) { return fake, nil })
	if err := o.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	addServer(t, db, 1)
	reqCtx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if err := o.Reapply(reqCtx); err != nil {
		t.Fatal(err)
	}
	time.Sleep(60 * time.Millisecond) // request context is now dead

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if fl := o.Fleet(); len(fl) == 1 && fl[0].Status == apitypes.StatusOnline {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("monitor died with the request context: %+v", o.Fleet())
}

func TestOfflineStatusAndBackoff(t *testing.T) {
	o, db := newTestOrch(t)
	addServer(t, db, 1)
	events := make(chan string)
	fake := &fakeClient{fail: errors.New("dial: connection refused"), events: events}
	o.SetClientFactory(func(context.Context, store.Server) (Client, error) { return fake, nil })
	if err := o.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if row, _ := db.ServerByID("srv1"); row.Status == apitypes.StatusOffline {
			fl := o.Fleet()
			if fl[0].HasHostStats || len(fl[0].Containers) != 0 {
				t.Fatal("offline server must not publish stale stats")
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("server never marked offline: %+v", o.Fleet())
}

func TestContainerRemoveRefusesRunning(t *testing.T) {
	o, db := newTestOrch(t)
	addServer(t, db, 1)
	fake := &fakeClient{}
	o.SetClientFactory(func(context.Context, store.Server) (Client, error) { return fake, nil })
	if err := o.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if len(o.snapshotContainers("srv1")) > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	err := o.ContainerAction(context.Background(), "srv1", "a1b2c3d4e5f6", "remove")
	if err == nil || !strings.Contains(err.Error(), "running") {
		t.Fatalf("remove running: %v", err)
	}
	if err := o.ContainerAction(context.Background(), "srv1", "f6e5d4c3b2a1", "remove"); err != nil {
		t.Fatalf("remove exited container: %v", err)
	}
	if err := o.ContainerAction(context.Background(), "srv1", "x", "explode"); err == nil {
		t.Fatal("bad action accepted")
	}
}

func TestFleetErrorsGrouping(t *testing.T) {
	o, db := newTestOrch(t)
	addServer(t, db, 1)
	fake := &fakeClient{}
	o.SetClientFactory(func(context.Context, store.Server) (Client, error) { return fake, nil })
	if err := o.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		resp := o.FleetErrors(200)
		if len(resp.Groups) == 1 {
			g := resp.Groups[0]
			if g.Count != 1 || g.Sev != "error" || len(g.Hosts) != 1 || g.Hosts[0] != "loop" {
				t.Fatalf("group: %+v", g)
			}
			if g.Sample == nil || !strings.Contains(g.Sample.Text, "connection refused") {
				t.Fatalf("sample: %+v", g.Sample)
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("error group never appeared: %+v", o.FleetErrors(200).Groups)
}

// Keep the docker import honest: ParseEvent is what drainEvents uses.
var _ = docker.ParseEvent
