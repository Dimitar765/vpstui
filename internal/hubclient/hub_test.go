package hubclient

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"

	"vpstui/internal/apitypes"
)

func TestFleetDecodesServers(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/fleet" {
			http.NotFound(w, r)
			return
		}
		if got := r.Header.Get("Authorization"); got != "Bearer tok123" {
			t.Errorf("Authorization = %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(apitypes.FleetResponse{Servers: []apitypes.FleetServer{{
			ID: "srv1", Name: "loop", Status: apitypes.StatusOnline,
			HostStats:      &apitypes.HostStats{Load1: 0.4, MemTotalMB: 8000, MemUsedMB: 3000},
			Containers:     []apitypes.Container{{Name: "web", State: "running"}},
			ContainerCount: 1,
		}}})
	}))
	defer srv.Close()

	c := New(srv.URL, "tok123")
	servers, err := c.Fleet(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(servers) != 1 || servers[0].Name != "loop" || servers[0].Status != apitypes.StatusOnline {
		t.Fatalf("servers = %+v", servers)
	}
	if servers[0].HostStats.Load1 != 0.4 || len(servers[0].Containers) != 1 {
		t.Fatalf("nested DTOs lost: %+v", servers[0])
	}
}

func TestRESTErrorSurfacesServerMessage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"error":"no known-good deployment to roll back to"}`))
	}))
	defer srv.Close()

	c := New(srv.URL, "")
	if _, err := c.Deploy(context.Background(), "app1"); err == nil ||
		err.Error() != "POST /api/apps/app1/deploy: 409 no known-good deployment to roll back to" {
		t.Fatalf("err = %v", err)
	}
}

func TestDeployAndDeployments(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/apps/a1/deploy":
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(`{"deploymentId":"dep1"}`))
		case r.Method == http.MethodGet && r.URL.Path == "/api/apps/a1/deployments":
			_ = json.NewEncoder(w).Encode([]apitypes.Deployment{{ID: "dep1", Status: apitypes.DepSuccess}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	c := New(srv.URL, "")
	id, err := c.Deploy(context.Background(), "a1")
	if err != nil || id != "dep1" {
		t.Fatalf("Deploy = %q, %v", id, err)
	}
	deps, err := c.Deployments(context.Background(), "a1")
	if err != nil || len(deps) != 1 || deps[0].Status != apitypes.DepSuccess {
		t.Fatalf("Deployments = %+v, %v", deps, err)
	}
}

// wsHub is a test daemon: it upgrades /api/ws and /api/deployments/{id}/logs
// and pushes scripted frames.
type wsHub struct {
	*httptest.Server
	wsHits  atomic.Int32 // /api/ws upgrade attempts
	logHits atomic.Int32
}

func newWSHub(t *testing.T, fleetFrames func(n int) []string, logFrames func() []string) *wsHub {
	t.Helper()
	h := &wsHub{}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/ws", func(w http.ResponseWriter, r *http.Request) {
		h.wsHits.Add(1)
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close(websocket.StatusNormalClosure, "")
		n := 0
		for _, f := range fleetFrames(n) {
			n++
			wctx, cancel := context.WithTimeout(r.Context(), time.Second)
			if err := conn.Write(wctx, websocket.MessageText, []byte(f)); err != nil {
				cancel()
				return
			}
			cancel()
		}
		// Hold the connection open until the client goes away.
		for {
			if _, _, err := conn.Read(r.Context()); err != nil {
				return
			}
		}
	})
	mux.HandleFunc("GET /api/deployments/dep1/logs", func(w http.ResponseWriter, r *http.Request) {
		h.logHits.Add(1)
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close(websocket.StatusNormalClosure, "")
		for _, f := range logFrames() {
			wctx, cancel := context.WithTimeout(r.Context(), time.Second)
			if err := conn.Write(wctx, websocket.MessageText, []byte(f)); err != nil {
				cancel()
				return
			}
			cancel()
		}
		for {
			if _, _, err := conn.Read(r.Context()); err != nil {
				return
			}
		}
	})
	h.Server = httptest.NewServer(mux)
	t.Cleanup(h.Close)
	return h
}

func TestWatchFleetReceivesWSPushes(t *testing.T) {
	frames := []string{
		`{"type":"deploy.log","deploymentId":"x","line":"noise"}`, // filtered out
		`{"type":"fleet.update","servers":[{"id":"s1","name":"loop","status":"online"}]}`,
	}
	h := newWSHub(t, func(int) []string { return frames }, nil)

	c := New(h.URL, "tok")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch, stop, err := c.WatchFleet(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer stop()

	select {
	case f, ok := <-ch:
		if !ok {
			t.Fatal("channel closed without frames")
		}
		if len(f.Servers) != 1 || f.Servers[0].Name != "loop" || f.Servers[0].Status != apitypes.StatusOnline {
			t.Fatalf("frame = %+v", f)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no fleet frame within 2s")
	}
	// Only the fleet.update frame came through; the deploy frame was skipped.
	select {
	case f := <-ch:
		t.Fatalf("unexpected extra frame %+v", f)
	case <-time.After(100 * time.Millisecond):
	}
}

func TestWatchFleetPollsWhenWSFails(t *testing.T) {
	old := PollInterval
	PollInterval = 25 * time.Millisecond
	t.Cleanup(func() { PollInterval = old })

	var hits atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/ws", func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r) // WS upgrade always fails
	})
	mux.HandleFunc("GET /api/fleet", func(w http.ResponseWriter, r *http.Request) {
		n := hits.Add(1)
		_ = json.NewEncoder(w).Encode(apitypes.FleetResponse{Servers: []apitypes.FleetServer{
			{ID: "s1", Name: "loop", ContainerCount: int(n)},
		}})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := New(srv.URL, "")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch, stop, err := c.WatchFleet(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer stop()

	// Two consecutive polls must arrive, with fresh data each time.
	var last int
	deadline := time.Now().Add(3 * time.Second)
	for last < 2 && time.Now().Before(deadline) {
		select {
		case f, ok := <-ch:
			if !ok {
				t.Fatal("channel closed during polling")
			}
			if n := f.Servers[0].ContainerCount; n <= last {
				t.Fatalf("stale poll frame: %d after %d", n, last)
			} else {
				last = n
			}
		case <-time.After(time.Second):
			t.Fatalf("poll fallback stalled after %d frames", last)
		}
	}
	if last < 2 {
		t.Fatalf("only %d poll frames", last)
	}
	if h := hits.Load(); h < 2 {
		t.Fatalf("fleet endpoint hit %d times", h)
	}
}

func TestWatchDeploymentStreamsThenTerminal(t *testing.T) {
	old := PollInterval
	PollInterval = 25 * time.Millisecond
	t.Cleanup(func() { PollInterval = old })

	logFrames := []string{
		`{"type":"deploy.log","deploymentId":"dep1","line":"step 1"}`,
		`{"type":"deploy.log","deploymentId":"dep1","line":"step 2"}`,
		`{"type":"deploy.status","deploymentId":"dep1","status":"success"}`,
	}
	h := newWSHub(t, nil, func() []string { return logFrames })
	c := New(h.URL, "")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch, stop, err := c.WatchDeployment(ctx, "dep1")
	if err != nil {
		t.Fatal(err)
	}
	defer stop()

	var lines []string
	sawDone := false
	deadline := time.After(3 * time.Second)
	for !sawDone {
		select {
		case f, ok := <-ch:
			if !ok {
				t.Fatal("channel closed before terminal frame")
			}
			if f.Done {
				sawDone = true
			} else {
				lines = append(lines, f.Line)
			}
		case <-deadline:
			t.Fatal("timed out waiting for terminal frame")
		}
	}
	if len(lines) != 2 || lines[0] != "step 1" || lines[1] != "step 2" {
		t.Fatalf("lines = %q", lines)
	}
	// The channel closes right after the Done frame.
	if _, ok := <-ch; ok {
		t.Fatal("channel still open after Done")
	}
}

func TestWatchDeploymentFallsBackToStatusPolls(t *testing.T) {
	old := PollInterval
	PollInterval = 25 * time.Millisecond
	t.Cleanup(func() { PollInterval = old })

	var polls atomic.Int32
	mux := http.NewServeMux()
	// No WS route at all: every dial fails.
	mux.HandleFunc("GET /api/deployments/dep1", func(w http.ResponseWriter, r *http.Request) {
		status := apitypes.DepRunning
		if polls.Add(1) >= 2 {
			status = apitypes.DepSuccess // terminal on the second poll
		}
		_ = json.NewEncoder(w).Encode(apitypes.Deployment{ID: "dep1", Status: status})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := New(srv.URL, "")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch, stop, err := c.WatchDeployment(ctx, "dep1")
	if err != nil {
		t.Fatal(err)
	}
	defer stop()

	// No lines ever streamed; the watch must terminate with a Done frame.
	select {
	case f, ok := <-ch:
		if !ok || !f.Done {
			t.Fatalf("frame = %+v ok = %v, want Done", f, ok)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no Done frame from poll fallback")
	}
	if _, ok := <-ch; ok {
		t.Fatal("watch did not end after terminal status")
	}
	if polls.Load() < 2 {
		t.Fatalf("status polled %d times", polls.Load())
	}
}

func TestWatchStopIsLeakFree(t *testing.T) {
	old := PollInterval
	PollInterval = 20 * time.Millisecond
	t.Cleanup(func() { PollInterval = old })

	h := newWSHub(t, func(int) []string {
		return []string{`{"type":"fleet.update","servers":[]}`}
	}, func() []string {
		return []string{`{"type":"deploy.status","deploymentId":"dep1","status":"failed"}`}
	})
	c := New(h.URL, "")

	before := runtime.NumGoroutine()
	for range 5 {
		ctx := context.Background()
		fch, fstop, err := c.WatchFleet(ctx)
		if err != nil {
			t.Fatal(err)
		}
		<-fch
		fstop()
		if _, ok := <-fch; ok {
			t.Fatal("fleet channel open after stop")
		}
		dch, dstop, err := c.WatchDeployment(ctx, "dep1")
		if err != nil {
			t.Fatal(err)
		}
		<-dch // terminal frame (failed)
		if _, ok := <-dch; ok {
			t.Fatal("deployment watch still open after terminal frame")
		}
		dstop() // stop after natural end must not hang or panic
	}
	// Watch goroutines plus their websocket readers are gone; allow the
	// runtime a moment to reap, then require the count back to baseline.
	deadline := time.Now().Add(2 * time.Second)
	for runtime.NumGoroutine() > before+2 {
		if time.Now().After(deadline) {
			t.Fatalf("goroutines leaked: before=%d after=%d", before, runtime.NumGoroutine())
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestWatchContextCancelEndsWatch(t *testing.T) {
	old := PollInterval
	PollInterval = 20 * time.Millisecond
	t.Cleanup(func() { PollInterval = old })

	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/ws", func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := New(srv.URL, "")
	ctx, cancel := context.WithCancel(context.Background())
	ch, stop, err := c.WatchFleet(ctx)
	if err != nil {
		t.Fatal(err)
	}
	cancel() // context-driven stop: no explicit stop call
	select {
	case _, ok := <-ch:
		if ok {
			t.Fatal("unexpected frame after cancel")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("watch outlived its context")
	}
	stop()
}
