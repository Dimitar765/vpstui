package orchestrator

import (
	"bytes"
	"errors"
	"sync"
	"testing"
	"time"

	"vpstui/internal/apitypes"
	"vpstui/internal/models"
	"vpstui/internal/store"
)

// ---------------------------------------------------------------- recorders

type recBroadcaster struct {
	mu     sync.Mutex
	alerts []apitypes.AlertFrame
}

func (r *recBroadcaster) Broadcast(f any) {
	if af, ok := f.(apitypes.AlertFrame); ok {
		r.mu.Lock()
		r.alerts = append(r.alerts, af)
		r.mu.Unlock()
	}
}

func (r *recBroadcaster) got() []apitypes.AlertFrame {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]apitypes.AlertFrame{}, r.alerts...)
}

type recNotifier struct {
	mu     sync.Mutex
	events []string
}

func (r *recNotifier) Dispatch(event string, _ any) {
	r.mu.Lock()
	r.events = append(r.events, event)
	r.mu.Unlock()
}

func (r *recNotifier) got() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string{}, r.events...)
}

// harness wires a real store, a recorder broadcaster/notifier and a fresh
// alert engine; rule rows go straight into alert_rules.
type alertHarness struct {
	t   *testing.T
	o   *Orchestrator
	db  *store.DB
	srv store.Server
	eng *alertEngine
	bc  *recBroadcaster
	nf  *recNotifier
	t0  time.Time
}

func newAlertHarness(t *testing.T) *alertHarness {
	o, db := newTestOrch(t)
	srv := addServer(t, db, 15)
	h := &alertHarness{t: t, o: o, db: db, srv: srv,
		eng: newAlertEngine(o), bc: &recBroadcaster{}, nf: &recNotifier{},
		t0: time.Now().UTC().Truncate(time.Second)}
	o.bc = h.bc
	o.nf = h.nf
	return h
}

func (h *alertHarness) rule(id, metric string, threshold float64, durSec int, pattern string) store.AlertRule {
	h.t.Helper()
	r := store.AlertRule{ID: id, Metric: metric, Threshold: threshold,
		DurationSec: durSec, Enabled: true, State: apitypes.AlertStateOK, ServerID: h.srv.ID}
	if pattern != "" {
		r.ContainerPattern = pattern
	}
	if err := h.db.CreateAlertRule(r); err != nil {
		h.t.Fatal(err)
	}
	return r
}

func (h *alertHarness) stored(id string) store.AlertRule {
	h.t.Helper()
	r, err := h.db.AlertRuleByID(id)
	if err != nil {
		h.t.Fatalf("rule %s: %v", id, err)
	}
	return r
}

func hostStats(load1 float64) models.HostStats {
	return models.HostStats{Load1: load1, Load5: load1, Load15: load1,
		MemTotalMB: 1000, MemUsedMB: 10}
}

// TestSustainFiresAndResolves walks a load1 rule through no-fire, fire,
// no-refire, resolve with a fake clock.
func TestSustainFiresAndResolves(t *testing.T) {
	h := newAlertHarness(t)
	r := h.rule("r1", apitypes.MetricLoad1, 0.5, 300, "")
	s := alertSample{host: hostStats(1.5), hasHost: true}

	// Breach, but sustained for 0 s of the 300 s sustain: no fire.
	h.eng.evaluate(h.srv, s, h.t0)
	if st := h.stored(r.ID).State; st != apitypes.AlertStateOK {
		t.Fatalf("state after first breach = %s", st)
	}
	// Still short of the duration.
	h.eng.evaluate(h.srv, s, h.t0.Add(100*time.Second))
	if st := h.stored(r.ID).State; st != apitypes.AlertStateOK {
		t.Fatalf("state at 100s = %s", st)
	}
	// Past the duration: fires.
	h.eng.evaluate(h.srv, s, h.t0.Add(301*time.Second))
	if st := h.stored(r.ID).State; st != apitypes.AlertStateFiring {
		t.Fatalf("state at 301s = %s", st)
	}
	if lf := h.stored(r.ID).LastFiredAt; lf == nil {
		t.Fatal("last_fired_at not stamped")
	}
	if frames := h.bc.got(); len(frames) != 1 || frames[0].State != apitypes.AlertStateFiring || frames[0].RuleID != r.ID {
		t.Fatalf("frames after fire = %+v", h.bc.got())
	}
	if ev := h.nf.got(); len(ev) != 1 || ev[0] != apitypes.EventAlertFired {
		t.Fatalf("notifications after fire = %v", h.nf.got())
	}
	// Sustained and already firing: no duplicate.
	h.eng.evaluate(h.srv, s, h.t0.Add(600*time.Second))
	if frames := h.bc.got(); len(frames) != 1 {
		t.Fatalf("duplicate fire: %+v", h.bc.got())
	}
	// Back under threshold: resolves.
	low := alertSample{host: hostStats(0.1), hasHost: true}
	h.eng.evaluate(h.srv, low, h.t0.Add(610*time.Second))
	if st := h.stored(r.ID).State; st != apitypes.AlertStateOK {
		t.Fatalf("state after calm = %s", st)
	}
	if lf := h.stored(r.ID).LastFiredAt; lf != nil {
		t.Fatal("last_fired_at not cleared on resolve")
	}
	frames := h.bc.got()
	if last := frames[len(frames)-1]; last.State != apitypes.AlertStateOK {
		t.Fatalf("last frame = %+v", last)
	}
	if ev := h.nf.got(); len(ev) != 2 || ev[1] != apitypes.EventAlertResolved {
		t.Fatalf("notifications after resolve = %v", ev)
	}
}

// TestSustainZeroDurationFiresImmediately: duration 0 fires on the first
// breaching pass (the live-check `load1 > 0` rule).
func TestSustainZeroDurationFiresImmediately(t *testing.T) {
	h := newAlertHarness(t)
	r := h.rule("r0", apitypes.MetricLoad1, 0, 0, "")
	h.eng.evaluate(h.srv, alertSample{host: hostStats(0.03), hasHost: true}, h.t0)
	if st := h.stored(r.ID).State; st != apitypes.AlertStateFiring {
		t.Fatalf("state = %s", st)
	}
	if frames := h.bc.got(); len(frames) != 1 {
		t.Fatalf("frames = %+v", h.bc.got())
	}
}

// TestContainerCPUMetric: pattern-scoped cpu rule takes the worst matching
// container and ignores non-matches.
func TestContainerCPUMetric(t *testing.T) {
	h := newAlertHarness(t)
	r := h.rule("rc", apitypes.MetricContainerCPUPct, 90, 0, "^web")
	stats := []models.ContainerStats{
		{ID: "a", Name: "web-1", CPUPerc: "95.00%", MemPerc: "10.00%"},
		{ID: "b", Name: "cache", CPUPerc: "99.00%", MemPerc: "99.00%"},
		{ID: "c", Name: "web-2", CPUPerc: "12.00%", MemPerc: "5.00%"},
	}
	h.eng.evaluate(h.srv, alertSample{hasHost: true, stats: stats}, h.t0)
	if st := h.stored(r.ID).State; st != apitypes.AlertStateFiring {
		t.Fatalf("state = %s", st)
	}
	for _, f := range h.bc.got() {
		if f.State == apitypes.AlertStateFiring && (!bytes.Contains([]byte(f.Detail), []byte("web-1")) || bytes.Contains([]byte(f.Detail), []byte("cache"))) {
			t.Fatalf("detail names wrong container: %q", f.Detail)
		}
	}
	// No matching container: no data, no breach, no transition noise.
	h.eng.evaluate(h.srv, alertSample{hasHost: true}, h.t0.Add(time.Second))
	if frames := h.bc.got(); len(frames) != 1 {
		t.Fatalf("empty stats changed frames: %+v", h.bc.got())
	}
}

// TestContainerDownMetric: running in the previous pass, gone/exited now.
func TestContainerDownMetric(t *testing.T) {
	h := newAlertHarness(t)
	r := h.rule("rd", apitypes.MetricContainerDown, 0, 0, "web")
	up := []models.Container{{ID: "a", Names: "web-1", State: "running"}, {ID: "b", Names: "db", State: "running"}}
	exited := []models.Container{{ID: "a", Names: "web-1", State: "exited"}, {ID: "b", Names: "db", State: "running"}}

	// First pass with no history: nothing was running before, no breach.
	h.eng.evaluate(h.srv, alertSample{hasHost: true, cur: up}, h.t0)
	if st := h.stored(r.ID).State; st != apitypes.AlertStateOK {
		t.Fatalf("state with empty history = %s", st)
	}
	// web-1 exits while db stays up: fires, naming web-1 only.
	h.eng.evaluate(h.srv, alertSample{hasHost: true, prev: up, cur: exited}, h.t0.Add(time.Second))
	if st := h.stored(r.ID).State; st != apitypes.AlertStateFiring {
		t.Fatalf("state = %s", st)
	}
	if d := h.bc.got()[0].Detail; !bytes.Contains([]byte(d), []byte("web-1")) || bytes.Contains([]byte(d), []byte("db")) {
		t.Fatalf("detail = %q", d)
	}
	// web-1 back: resolves.
	h.eng.evaluate(h.srv, alertSample{hasHost: true, prev: exited, cur: up}, h.t0.Add(2*time.Second))
	if st := h.stored(r.ID).State; st != apitypes.AlertStateOK {
		t.Fatalf("state after recovery = %s", st)
	}
}

// TestBadPatternDisablesRule: a rule that can never compile is disabled in
// the store exactly once and skipped thereafter.
func TestBadPatternDisablesRule(t *testing.T) {
	h := newAlertHarness(t)
	r := h.rule("rb", apitypes.MetricContainerDown, 0, 0, "(") // invalid regexp
	up := []models.Container{{ID: "a", Names: "web-1", State: "running"}}
	h.eng.evaluate(h.srv, alertSample{hasHost: true, cur: up}, h.t0)
	if h.stored(r.ID).Enabled {
		t.Fatal("bad-pattern rule still enabled")
	}
	h.eng.evaluate(h.srv, alertSample{hasHost: true, cur: up}, h.t0.Add(time.Second))
	if frames := h.bc.got(); len(frames) != 0 {
		t.Fatalf("frames = %+v", frames)
	}
}

// TestMemPctHostMetric: mem_pct derives from host counters and resolves
// when usage falls back.
func TestMemPctHostMetric(t *testing.T) {
	h := newAlertHarness(t)
	r := h.rule("rm", apitypes.MetricMemPct, 50, 0, "")
	high := alertSample{host: models.HostStats{MemTotalMB: 1000, MemUsedMB: 600}, hasHost: true}
	h.eng.evaluate(h.srv, high, h.t0)
	if st := h.stored(r.ID).State; st != apitypes.AlertStateFiring {
		t.Fatalf("state = %s", st)
	}
	low := alertSample{host: models.HostStats{MemTotalMB: 1000, MemUsedMB: 400}, hasHost: true}
	h.eng.evaluate(h.srv, low, h.t0.Add(time.Second))
	if st := h.stored(r.ID).State; st != apitypes.AlertStateOK {
		t.Fatalf("state after drop = %s", st)
	}
	// Zero total memory: no denominator, no breach.
	r2 := h.rule("rz", apitypes.MetricMemPct, 0, 0, "")
	h.eng.evaluate(h.srv, alertSample{host: models.HostStats{MemUsedMB: 5}, hasHost: true}, h.t0.Add(2*time.Second))
	if st := h.stored(r2.ID).State; st != apitypes.AlertStateOK {
		t.Fatalf("zero-total state = %s", st)
	}
}

// TestRuleScopeFilter: a rule pinned to another server never fires here.
func TestRuleScopeFilter(t *testing.T) {
	h := newAlertHarness(t)
	if err := h.db.CreateServer(store.Server{ID: "srv-other", Name: "far", Address: "10.0.0.1",
		Port: 22, User: "u", AuthType: "key", BaseDir: "/opt/vpstui", MonitorIntervalSec: 15,
		ProxyHTTPPort: 80, ProxyHTTPSPort: 443, CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	r := store.AlertRule{ID: "rother", Metric: apitypes.MetricLoad1, Threshold: 0,
		DurationSec: 0, Enabled: true, State: apitypes.AlertStateOK, ServerID: "srv-other"}
	if err := h.db.CreateAlertRule(r); err != nil {
		t.Fatal(err)
	}
	h.eng.evaluate(h.srv, alertSample{host: hostStats(5), hasHost: true}, h.t0)
	if st := h.stored(r.ID).State; st != apitypes.AlertStateOK {
		t.Fatalf("foreign rule fired: %s", st)
	}
}

// TestServerOfflineNotifiesOnce drives the online→offline transition guard.
func TestServerOfflineNotifiesOnce(t *testing.T) {
	h := newAlertHarness(t)
	m := &mon{o: h.o, srv: h.srv, errs: errCorpus{}, now: time.Now, alerts: newAlertEngine(h.o)}

	m.setOffline(errors.New("dial: refused"))
	if ev := h.nf.got(); len(ev) != 1 || ev[0] != apitypes.EventServerOffline {
		t.Fatalf("first offline events = %v", h.nf.got())
	}
	// The reconnect backoff re-enters setOffline every retry: no spam.
	m.setOffline(errors.New("dial: refused again"))
	if ev := h.nf.got(); len(ev) != 1 {
		t.Fatalf("retry spammed: %v", h.nf.got())
	}
	// Recovery then another drop: notifies again.
	m.setOnline()
	m.setOffline(errors.New("flap"))
	if ev := h.nf.got(); len(ev) != 2 {
		t.Fatalf("after flap events = %v", h.nf.got())
	}
}
