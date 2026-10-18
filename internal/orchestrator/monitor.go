package orchestrator

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"vpstui/internal/apitypes"
	"vpstui/internal/docker"
	"vpstui/internal/models"
	"vpstui/internal/store"
)

// Broadcaster receives fan-out frames. *server.Hub implements it; the
// interface lives here so the orchestrator never imports the HTTP layer.
type Broadcaster interface {
	Broadcast(frame any)
}

// Notifier is the notification seam (S7); *notify.Dispatcher implements it.
// The interface lives here so the orchestrator never imports the notify
// package.
type Notifier interface {
	Dispatch(event string, payload any)
}

// Orchestrator runs one monitor goroutine per managed server and keeps the
// latest fleet snapshot in memory for the API layer.
type Orchestrator struct {
	db   *store.DB
	pool *Pool
	bc   Broadcaster
	nf   Notifier
	log  *log.Logger

	// clientFn produces the per-server monitoring client. Overridable in tests.
	clientFn func(ctx context.Context, s store.Server) (Client, error)

	mu   sync.Mutex
	mons map[string]*mon

	// baseCtx is the daemon-lifetime parent for monitor goroutines. Captured
	// by Start; Reapply's own ctx argument bounds only the synchronous
	// reconcile (store read), never monitor lifetimes — handlers pass
	// short-lived request contexts.
	baseCtx context.Context

	wg sync.WaitGroup
}

// NewOrchestrator wires the orchestrator to its store, pool and (optional)
// broadcaster.
func NewOrchestrator(db *store.DB, pool *Pool, bc Broadcaster, logger *log.Logger) *Orchestrator {
	if logger == nil {
		logger = log.Default()
	}
	o := &Orchestrator{db: db, pool: pool, bc: bc, log: logger, mons: map[string]*mon{},
		baseCtx: context.Background()}
	o.clientFn = pool.Client
	return o
}

// SetClientFactory overrides per-server client creation. Test seam: the
// daemon always uses the pool.
func (o *Orchestrator) SetClientFactory(fn func(ctx context.Context, s store.Server) (Client, error)) {
	o.clientFn = fn
}

// SetNotifier installs the notification dispatcher (S7). Safe to call once
// at boot, before Start.
func (o *Orchestrator) SetNotifier(n Notifier) { o.nf = n }

// notify dispatches a notification event, tolerating an unwired notifier
// (tests, S2-only binaries).
func (o *Orchestrator) notify(event string, payload map[string]any) {
	if o.nf == nil {
		return
	}
	o.nf.Dispatch(event, payload)
}

// Start launches a monitor per server row. ctx is the daemon-lifetime parent
// for every monitor goroutine (past and future Reapply calls). Call Reapply
// after registry changes; never call Start twice.
func (o *Orchestrator) Start(ctx context.Context) error {
	o.baseCtx = ctx
	return o.Reapply(ctx)
}

// Reapply reconciles the running monitors with the server table: starts new
// rows, stops removed ones, restarts rows whose settings changed.
func (o *Orchestrator) Reapply(ctx context.Context) error {
	servers, err := o.db.Servers()
	if err != nil {
		return err
	}
	want := map[string]store.Server{}
	for _, s := range servers {
		want[s.ID] = s
	}

	o.mu.Lock()
	// Stop monitors that vanished or changed.
	for id, m := range o.mons {
		s, ok := want[id]
		if ok && m.sameSettings(s) {
			continue
		}
		m.cancel()
		delete(o.mons, id)
	}
	// Start missing ones. Monitor lifetimes hang off the daemon-lifetime
	// base context, never off the caller's (request-scoped) ctx.
	for id, s := range want {
		if _, running := o.mons[id]; running {
			continue
		}
		mctx, cancel := context.WithCancel(o.baseCtx)
		m := &mon{o: o, srv: s, cancel: cancel, errs: errCorpus{}, now: time.Now,
			alerts: newAlertEngine(o)}
		o.mons[id] = m
		o.wg.Add(1)
		go func() {
			defer o.wg.Done()
			m.run(mctx)
		}()
	}
	o.mu.Unlock()
	return nil
}

// Stop cancels every monitor and waits for them to finish.
func (o *Orchestrator) Stop() {
	o.mu.Lock()
	for _, m := range o.mons {
		m.cancel()
	}
	o.mu.Unlock()
	o.wg.Wait()
	o.pool.Close()
}

// Fleet assembles the current fleet snapshot: the registry joined with the
// latest in-memory monitor state. Servers is never null.
func (o *Orchestrator) Fleet() []apitypes.FleetServer {
	servers, err := o.db.Servers()
	if err != nil {
		o.log.Printf("fleet: list servers: %v", err)
		servers = nil
	}
	out := make([]apitypes.FleetServer, 0, len(servers))
	o.mu.Lock()
	defer o.mu.Unlock()
	for _, s := range servers {
		fs := fleetRow(s)
		if m, ok := o.mons[s.ID]; ok {
			m.fill(fs)
		}
		out = append(out, *fs)
	}
	return out
}

// FleetErrors builds the cross-host error aggregate: each server's retained
// samples grouped by signature, bounded to maxGroups. Output is fully
// ordered, so an unchanged corpus serialises identically.
func (o *Orchestrator) FleetErrors(maxGroups int) apitypes.ErrorsResponse {
	servers, err := o.db.Servers()
	if err != nil {
		servers = nil
	}
	var lines []errLine
	hosts := make([]apitypes.ErrorHost, 0, len(servers))
	connected := 0
	o.mu.Lock()
	for _, s := range servers {
		h := apitypes.ErrorHost{Name: s.Name, Status: s.Status}
		if m, ok := o.mons[s.ID]; ok {
			m.mu.Lock()
			lines = append(lines, m.errs.lines...)
			h.Swept = !m.errs.sweptAt.IsZero()
			h.SweptAt = m.errs.sweptAt
			h.LineCount = len(m.errs.lines)
			m.mu.Unlock()
		}
		if s.Status == apitypes.StatusOnline {
			connected++
		}
		hosts = append(hosts, h)
	}
	o.mu.Unlock()
	sort.Slice(hosts, func(i, j int) bool { return hosts[i].Name < hosts[j].Name })
	groups := groupErrors(lines, maxGroups)
	if groups == nil {
		groups = []apitypes.ErrorGroup{}
	}
	return apitypes.ErrorsResponse{
		Swept:     len(hosts),
		Connected: connected,
		FetchedAt: time.Now(),
		LineCount: len(lines),
		Hosts:     hosts,
		Groups:    groups,
	}
}

// PoolRemove drops the server's pooled connection (dial settings changed or
// the row is going away).
func (o *Orchestrator) PoolRemove(serverID string) { o.pool.Remove(serverID) }

// ContainerAction runs start|stop|restart|remove for one container on one
// server. remove refuses running containers, exactly like the TUI's `D`.
func (o *Orchestrator) ContainerAction(ctx context.Context, serverID, cid, action string) error {
	var s store.Server
	var cl Client
	o.mu.Lock()
	if m, ok := o.mons[serverID]; ok {
		// Route through the monitor's client: the pooled executor in prod,
		// the injected fake in tests.
		if c, err := m.client(ctx); err == nil {
			cl = c
		}
		s = m.srv
	}
	o.mu.Unlock()
	if s.ID == "" {
		var err error
		s, err = o.db.ServerByID(serverID)
		if err != nil {
			return fmt.Errorf("server %s: %w", serverID, err)
		}
	}
	if cl == nil {
		c, err := o.pool.Executor(ctx, s)
		if err != nil {
			return err
		}
		cl = c
	}
	var err error
	switch action {
	case "start":
		err = cl.Start(ctx, cid)
	case "stop":
		err = cl.Stop(ctx, cid)
	case "restart":
		err = cl.Restart(ctx, cid)
	case "remove":
		// A running container must be stopped first, mirroring the TUI.
		for _, c := range o.snapshotContainers(serverID) {
			if strings.HasPrefix(c.ID, cid) && c.State == "running" {
				return errors.New("container is running — stop it before removing")
			}
		}
		err = cl.Rm(ctx, cid)
	default:
		return fmt.Errorf("unknown action %q", action)
	}
	if isConnLost(err) {
		o.pool.Remove(serverID)
	}
	return err
}

// snapshotContainers returns the cached container list for a server, if any.
func (o *Orchestrator) snapshotContainers(serverID string) []models.Container {
	o.mu.Lock()
	defer o.mu.Unlock()
	m, ok := o.mons[serverID]
	if !ok {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.containers
}

// ---------------------------------------------------------------- monitor

const (
	// Reconnect backoff: 5 s doubling to a 60 s ceiling, forever.
	backoffBase = 5 * time.Second
	backoffMax  = 60 * time.Second
	// The error sweep is far heavier than a snapshot; run it on its own
	// slower cadence.
	errSweepEvery  = time.Minute
	errSweepSince  = "2h"
	errSweepTail   = 500
	errCorpusCap   = 500
	pruneEvery     = time.Hour
	pruneOlderThan = 7 * 24 * time.Hour
)

// mon is one server's polling loop. The snapshot discipline is the TUI's
// refresh chain: one snapshot in flight, events only set a pending flag, so
// a burst of docker events can never queue a burst of SSH sweeps.
type mon struct {
	o      *Orchestrator
	srv    store.Server
	cancel context.CancelFunc
	alerts *alertEngine

	mu         sync.Mutex
	containers []models.Container
	stats      []models.ContainerStats
	host       models.HostStats
	hasHost    bool
	fetchedAt  time.Time
	errs       errCorpus
	lastPrune  time.Time

	// now is swappable for tests.
	now func() time.Time
}

func (m *mon) sameSettings(s store.Server) bool {
	return m.srv.MonitorIntervalSec == s.MonitorIntervalSec &&
		m.srv.Address == s.Address && m.srv.Port == s.Port && m.srv.User == s.User &&
		m.srv.AuthType == s.AuthType && m.srv.KeyPath == s.KeyPath
}

// kick is the refresh chain's accelerator: buffered size 1, so any number of
// events during an in-flight snapshot collapse into one following snapshot.
func (m *mon) kick(ch chan struct{}) {
	select {
	case ch <- struct{}{}:
	default:
	}
}

func (m *mon) run(ctx context.Context) {
	kicks := make(chan struct{}, 1)
	go m.watchEvents(ctx, kicks)

	backoff := time.Duration(0)
	nextErr := time.Time{}    // sweep on the first successful snapshot too
	timer := time.NewTimer(0) // first snapshot immediately
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-kicks:
			// Events landed while we were idle or mid-snapshot: refresh now.
		case <-timer.C:
		}
		err := m.snapshot(ctx, &nextErr)
		if err != nil {
			if backoff == 0 {
				backoff = backoffBase
			} else {
				backoff *= 2
				if backoff > backoffMax {
					backoff = backoffMax
				}
			}
			m.setOffline(err)
			m.o.pool.Remove(m.srv.ID)
		} else {
			backoff = 0
			m.setOnline()
		}
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		d := m.interval()
		if backoff > 0 {
			d = backoff
		}
		timer.Reset(d)
	}
}

func (m *mon) interval() time.Duration {
	if m.srv.MonitorIntervalSec <= 0 {
		return 15 * time.Second
	}
	return time.Duration(m.srv.MonitorIntervalSec) * time.Second
}

// watchEvents streams docker events and turns each one into a kick, copying
// the TUI's events-as-accelerator discipline. On stream end it rearms on the
// next poll cycle rather than hot-looping.
func (m *mon) watchEvents(ctx context.Context, kicks chan struct{}) {
	for {
		cl, err := m.client(ctx)
		if err != nil {
			return // the snapshot loop is already surfacing this
		}
		rc, err := cl.StreamEvents(ctx)
		if err != nil {
			return
		}
		m.drainEvents(rc, kicks)
		if ctx.Err() != nil {
			return
		}
		// Stream ended (docker events stops on errors/EOF): wait one poll
		// interval so a failing host cannot spin.
		select {
		case <-ctx.Done():
			return
		case <-time.After(m.interval()):
		}
	}
}

func (m *mon) drainEvents(rc io.ReadCloser, kicks chan struct{}) {
	defer rc.Close()
	sc := bufio.NewScanner(rc)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		if _, ok := docker.ParseEvent(sc.Text()); ok {
			m.kick(kicks)
		}
	}
}

func (m *mon) client(ctx context.Context) (Client, error) {
	return m.o.clientFn(ctx, m.currentRow())
}

// currentRow re-reads the server row so status writes and dials use fresh data.
func (m *mon) currentRow() store.Server {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.srv
}

// snapshot performs one monitoring pass: snapshot, persist samples, maybe
// sweep errors, maybe prune. Errors do not abort the pass unless the
// snapshot itself failed.
func (m *mon) snapshot(ctx context.Context, nextErr *time.Time) error {
	cl, err := m.client(ctx)
	if err != nil {
		return err
	}
	containers, stats, host, err := cl.Snapshot(ctx)
	if err != nil {
		return err
	}
	now := m.now()
	ts := now.Truncate(time.Second)
	m.mu.Lock()
	prevContainers := m.containers
	m.containers, m.stats, m.host, m.hasHost = containers, stats, host, true
	m.fetchedAt = now
	m.mu.Unlock()

	m.persist(ts, host, containers, stats)
	m.evaluateAlerts(prevContainers, containers, stats, host, now)
	m.setOnline()
	m.o.broadcastFleet()

	// The sweep is the slow part of a pass; a healthy server is online and
	// published before it starts.
	if !now.Before(*nextErr) {
		*nextErr = now.Add(errSweepEvery)
		if payload, err := cl.SweepErrors(ctx, errSweepSince, errSweepTail); err == nil {
			m.retainErrors(payload, now)
		} else {
			m.o.log.Printf("monitor %s: error sweep: %v", m.srv.Name, err)
		}
	}
	if m.lastPrune.IsZero() || now.Sub(m.lastPrune) >= pruneEvery {
		m.lastPrune = now
		if err := m.o.db.PruneSamples(now.Add(-pruneOlderThan)); err != nil {
			m.o.log.Printf("monitor %s: prune samples: %v", m.srv.Name, err)
		}
	}
	return nil
}

func (m *mon) persist(ts time.Time, host models.HostStats, containers []models.Container, stats []models.ContainerStats) {
	s := m.currentRow()
	if err := m.o.db.InsertMetricSample(store.MetricSample{
		TS: ts, ServerID: s.ID,
		Load1: host.Load1, Load5: host.Load5, Load15: host.Load15,
		MemUsedMB: host.MemUsedMB, MemTotalMB: host.MemTotalMB,
	}); err != nil {
		m.o.log.Printf("monitor %s: insert metric sample: %v", s.Name, err)
	}
	statByID := make(map[string]models.ContainerStats, len(stats))
	for _, st := range stats {
		statByID[strings.ToLower(strings.TrimSpace(st.ID))] = st
	}
	samples := make([]store.ContainerSample, 0, len(containers))
	for _, c := range containers {
		st, ok := statByID[strings.ToLower(strings.TrimSpace(c.ID))]
		if !ok {
			continue
		}
		samples = append(samples, store.ContainerSample{
			TS: ts, ServerID: s.ID, ContainerID: c.ID, Name: c.ShortName(),
			CPUPct: atof(st.CPUPerc), MemPct: atof(st.MemPerc), State: c.State,
		})
	}
	if err := m.o.db.InsertContainerSamples(samples); err != nil {
		m.o.log.Printf("monitor %s: insert container samples: %v", s.Name, err)
	}
}

// atof parses docker stats percentage strings ("12.34%") defensively.
func atof(s string) float64 {
	s = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(s), "%"))
	v, _ := strconv.ParseFloat(s, 64)
	return v
}

func (m *mon) retainErrors(payload string, at time.Time) {
	s := m.currentRow()
	lines := parseErrSweep(payload, s.ID, s.Name, at)
	m.mu.Lock()
	m.errs.add(lines)
	m.mu.Unlock()
}

func (m *mon) setOnline() {
	m.mu.Lock()
	m.srv.Status = apitypes.StatusOnline
	now := m.now()
	m.srv.LastSeenAt = &now
	s := m.srv
	m.mu.Unlock()
	if err := m.o.db.SetServerStatus(s.ID, s.Status, s.LastSeenAt); err != nil {
		m.o.log.Printf("monitor %s: set status: %v", s.Name, err)
	}
}

func (m *mon) setOffline(cause error) {
	m.mu.Lock()
	prev := m.srv.Status
	m.srv.Status = apitypes.StatusOffline
	m.hasHost = false
	m.containers = nil
	m.stats = nil
	m.fetchedAt = time.Time{}
	s := m.srv
	m.mu.Unlock()
	if err := m.o.db.SetServerStatus(s.ID, s.Status, s.LastSeenAt); err != nil {
		m.o.log.Printf("monitor %s: set status: %v", s.Name, err)
	}
	m.o.log.Printf("monitor %s: offline: %v", s.Name, cause)
	// Fire server.offline only on the online→offline transition; the
	// reconnect backoff re-enters this path every retry and must not spam.
	if prev != apitypes.StatusOffline {
		m.o.notify(apitypes.EventServerOffline, map[string]any{
			"serverId": s.ID, "serverName": s.Name, "error": cause.Error(),
		})
	}
	m.o.broadcastFleet()
}

// fill projects the monitor state onto a fleet row.
func (m *mon) fill(fs *apitypes.FleetServer) {
	m.mu.Lock()
	defer m.mu.Unlock()
	fs.Status = m.srv.Status
	fs.LastSeenAt = m.srv.LastSeenAt
	fs.Containers = make([]apitypes.Container, 0, len(m.containers))
	for _, c := range m.containers {
		fs.Containers = append(fs.Containers, toContainer(c, m.stats))
	}
	fs.ContainerCount = len(fs.Containers)
	if m.hasHost {
		fs.HasHostStats = true
		h := m.host
		fs.HostStats = &apitypes.HostStats{
			Load1: h.Load1, Load5: h.Load5, Load15: h.Load15,
			MemTotalMB: h.MemTotalMB, MemUsedMB: h.MemUsedMB,
			NumCPU: h.NumCPU, UptimeSec: h.UptimeSec,
		}
	}
	if !m.fetchedAt.IsZero() {
		t := m.fetchedAt
		fs.FetchedAt = &t
	}
	fs.Perf = perfSnapshot(m.containers, m.stats)
}

func fleetRow(s store.Server) *apitypes.FleetServer {
	return &apitypes.FleetServer{
		ID: s.ID, Name: s.Name, Address: s.Address, Port: s.Port, User: s.User,
		AuthType: s.AuthType, KeyPath: s.KeyPath, BaseDir: s.BaseDir,
		ProxyHTTPPort: s.ProxyHTTPPort, ProxyHTTPSPort: s.ProxyHTTPSPort,
		MonitorIntervalSec: s.MonitorIntervalSec,
		Status:             s.Status, LastSeenAt: s.LastSeenAt,
		Containers: []apitypes.Container{},
	}
}

// toContainer maps one snapshot container onto the shared DTO, deriving the
// numeric cpuPct/memPct fields the GUI charts on, from the docker stats text.
func toContainer(c models.Container, stats []models.ContainerStats) apitypes.Container {
	out := apitypes.Container{
		ID: c.ID, Name: c.ShortName(),
		Image: c.Image, State: c.State, Status: c.Status,
	}
	for _, st := range stats {
		if !strings.EqualFold(strings.TrimSpace(st.ID), strings.TrimSpace(c.ID)) {
			continue
		}
		out.CPUPerc = st.CPUPerc
		out.MemUsage = st.MemUsage
		out.MemPerc = st.MemPerc
		if v, ok := parsePct(st.CPUPerc); ok {
			out.CPUPct = &v
		}
		if v, ok := parsePct(st.MemPerc); ok {
			out.MemPct = &v
		}
		break
	}
	return out
}

func parsePct(s string) (float64, bool) {
	s = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(s), "%"))
	v, err := strconv.ParseFloat(s, 64)
	return v, err == nil
}

func (o *Orchestrator) broadcastFleet() {
	if o.bc == nil {
		return
	}
	o.bc.Broadcast(apitypes.FleetUpdateFrame{Type: apitypes.FrameFleetUpdate, Servers: o.Fleet()})
}

func (o *Orchestrator) broadcastAlert(state, ruleID, detail string) {
	if o.bc == nil {
		return
	}
	o.bc.Broadcast(apitypes.AlertFrame{Type: apitypes.FrameAlert, RuleID: ruleID, State: state, Detail: detail})
}

// ---------------------------------------------------------------- alerts

// Alert evaluation (S7). Rules are re-read from alert_rules on every pass,
// so CRUD applies without a reload signal. Sustain state lives in memory:
// a breach must hold continuously for duration_sec before the rule fires,
// and it resolves on the first pass back under threshold. Each rule's
// container_pattern is pre-compiled at load; a bad pattern disables the
// rule in the store (once, logged) instead of erroring every pass.

// alertSample is one pass's readings.
type alertSample struct {
	host    models.HostStats
	hasHost bool
	// prev/cur bracket the pass; container_down needs the previous snapshot
	// to know which containers vanished.
	prev, cur []models.Container
	stats     []models.ContainerStats
}

// compiledRule is a rule's shape cached between passes.
type compiledRule struct {
	pattern   string // as compiled; "" = host-level
	threshold float64
	duration  time.Duration
	re        *regexp.Regexp
}

// alertEngine carries one server's sustain map and the compiled-rule cache.
type alertEngine struct {
	o *Orchestrator

	mu          sync.Mutex
	firstBreach map[string]time.Time
	compiled    map[string]compiledRule
	bad         map[string]bool // rules whose pattern failed to compile
}

func newAlertEngine(o *Orchestrator) *alertEngine {
	return &alertEngine{o: o,
		firstBreach: map[string]time.Time{},
		compiled:    map[string]compiledRule{},
		bad:         map[string]bool{}}
}

func (m *mon) evaluateAlerts(prev, cur []models.Container, stats []models.ContainerStats, host models.HostStats, now time.Time) {
	if m.alerts == nil {
		return
	}
	m.alerts.evaluate(m.currentRow(), alertSample{
		host: host, hasHost: true, prev: prev, cur: cur, stats: stats,
	}, now)
}

// evaluate runs every enabled rule scoped to this server against the pass.
func (a *alertEngine) evaluate(srv store.Server, s alertSample, now time.Time) {
	rules, err := a.o.db.AlertRules()
	if err != nil {
		a.o.log.Printf("alerts %s: list rules: %v", srv.Name, err)
		return
	}
	for _, r := range rules {
		if !r.Enabled || (r.ServerID != "" && r.ServerID != srv.ID) {
			continue
		}
		c, ok := a.compile(r)
		if !ok {
			continue
		}
		value, detail, breach, observed := alertValue(r, srv, s, c)
		if observed {
			a.transition(r, c, breach, value, detail, srv, now)
		}
	}
}

// compile returns the rule's cached shape, (re)compiling after any
// definition change. A recompile resets the sustain map — an edited rule is
// a new argument, not a continuation of the old one. A pattern that fails
// to compile disables the rule in the store exactly once.
func (a *alertEngine) compile(r store.AlertRule) (compiledRule, bool) {
	dur := time.Duration(r.DurationSec) * time.Second
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.bad[r.ID] {
		return compiledRule{}, false
	}
	if c, ok := a.compiled[r.ID]; ok &&
		c.pattern == r.ContainerPattern && c.threshold == r.Threshold && c.duration == dur {
		return c, true
	}
	var re *regexp.Regexp
	if r.ContainerPattern != "" {
		var err error
		if re, err = regexp.Compile(r.ContainerPattern); err != nil {
			a.bad[r.ID] = true
			a.o.log.Printf("alert %s: bad container_pattern %q: %v — rule disabled", r.ID, r.ContainerPattern, err)
			if derr := a.o.db.DisableAlertRule(r.ID); derr != nil {
				a.o.log.Printf("alert %s: disable: %v", r.ID, derr)
			}
			return compiledRule{}, false
		}
	}
	delete(a.firstBreach, r.ID)
	c := compiledRule{pattern: r.ContainerPattern, threshold: r.Threshold, duration: dur, re: re}
	a.compiled[r.ID] = c
	return c, true
}

// alertValue reads one rule against the pass: the metric's value, a human
// detail line, whether the threshold is exceeded this pass, and whether the
// metric was observed at all. An unobserved container metric (no matching
// container) must neither fire nor resolve — absence of data is not "back
// under threshold".
func alertValue(r store.AlertRule, srv store.Server, s alertSample, c compiledRule) (value float64, detail string, breach, observed bool) {
	switch r.Metric {
	case apitypes.MetricLoad1:
		value = s.host.Load1
		return value, loadDetail(r, srv, value), value > r.Threshold, true
	case apitypes.MetricMemPct:
		if s.host.MemTotalMB <= 0 {
			return 0, "", false, false
		}
		value = float64(s.host.MemUsedMB) / float64(s.host.MemTotalMB) * 100
		return value, memDetail(r, srv, value), value > r.Threshold, true
	case apitypes.MetricContainerCPUPct, apitypes.MetricContainerMemPct:
		// The worst matching container carries the rule; the detail names it.
		name, worst := "", 0.0
		for _, cs := range s.stats {
			if !c.re.MatchString(cs.Name) {
				continue
			}
			pct := cs.CPUPerc
			if r.Metric == apitypes.MetricContainerMemPct {
				pct = cs.MemPerc
			}
			if v := atof(pct); v > worst {
				worst, name = v, cs.Name
			}
		}
		if name == "" {
			return 0, "", false, false // nothing matches: no data this pass
		}
		return worst, containerPctDetail(r, srv, name, worst), worst > r.Threshold, true
	case apitypes.MetricContainerDown:
		// Seen running in the previous pass, absent or exited now. Always
		// observed: a matching container that stays up is a calm pass, and
		// one that was removed resolves on the pass after the fire.
		var down []string
		var up []string
		for _, p := range s.prev {
			if p.State != "running" || !c.re.MatchString(p.ShortName()) {
				continue
			}
			stillUp := false
			for _, cu := range s.cur {
				if strings.EqualFold(strings.TrimSpace(cu.ID), strings.TrimSpace(p.ID)) {
					stillUp = cu.State == "running"
					break
				}
			}
			if stillUp {
				up = append(up, p.ShortName())
			} else {
				down = append(down, p.ShortName())
			}
		}
		if len(down) > 0 {
			return float64(len(down)), fmt.Sprintf("%s down on %s", strings.Join(down, ", "), srv.Name), true, true
		}
		if len(up) > 0 {
			return 0, fmt.Sprintf("%s back up on %s", strings.Join(up, ", "), srv.Name), false, true
		}
		return 0, "", false, true
	}
	return 0, "", false, false
}

// thresholdDir words the detail line by outcome: firing passes read
// "x > threshold", resolving passes "back under threshold".
func thresholdDir(r store.AlertRule, breach bool) string {
	if breach {
		return ">"
	}
	return "back under"
}

func loadDetail(r store.AlertRule, srv store.Server, v float64) string {
	return fmt.Sprintf("load1=%.2f %s %g on %s", v, thresholdDir(r, v > r.Threshold), r.Threshold, srv.Name)
}

func memDetail(r store.AlertRule, srv store.Server, v float64) string {
	return fmt.Sprintf("mem_pct=%.1f%% %s %g%% on %s", v, thresholdDir(r, v > r.Threshold), r.Threshold, srv.Name)
}

func containerPctDetail(r store.AlertRule, srv store.Server, name string, v float64) string {
	return fmt.Sprintf("%s %s=%.2f%% %s %g%% on %s", name, r.Metric, v, thresholdDir(r, v > r.Threshold), r.Threshold, srv.Name)
}

// transition applies the sustain map: a breach held for the rule's duration
// fires (duration 0 fires on the first breaching pass); a resolved breach
// clears state. Firing and resolving are one-shot per transition.
func (a *alertEngine) transition(r store.AlertRule, c compiledRule, breach bool, value float64, detail string, srv store.Server, now time.Time) {
	a.mu.Lock()
	if breach {
		if a.firstBreach[r.ID].IsZero() {
			a.firstBreach[r.ID] = now
		}
		if now.Sub(a.firstBreach[r.ID]) < c.duration {
			a.mu.Unlock()
			return
		}
		a.mu.Unlock()
		if r.State != apitypes.AlertStateFiring {
			t := now.UTC()
			if err := a.o.db.SetAlertState(r.ID, apitypes.AlertStateFiring, &t); err != nil {
				a.o.log.Printf("alert %s: set firing: %v", r.ID, err)
				return
			}
			a.o.broadcastAlert(apitypes.AlertStateFiring, r.ID, detail)
			a.o.notify(apitypes.EventAlertFired, map[string]any{
				"ruleId": r.ID, "metric": r.Metric,
				"serverId": srv.ID, "serverName": srv.Name,
				"value": value, "threshold": r.Threshold, "detail": detail,
			})
		}
		return
	}
	delete(a.firstBreach, r.ID)
	a.mu.Unlock()
	if r.State == apitypes.AlertStateFiring {
		if err := a.o.db.SetAlertState(r.ID, apitypes.AlertStateOK, nil); err != nil {
			a.o.log.Printf("alert %s: set ok: %v", r.ID, err)
			return
		}
		a.o.broadcastAlert(apitypes.AlertStateOK, r.ID, detail)
		a.o.notify(apitypes.EventAlertResolved, map[string]any{
			"ruleId": r.ID, "metric": r.Metric,
			"serverId": srv.ID, "serverName": srv.Name,
			"value": value, "threshold": r.Threshold, "detail": detail,
		})
	}
}

// ---------------------------------------------------------------- perf

// perfSnapshot ranks the latest sample of every container by cpu desc (ties
// by name). This is the fold-in's documented fallback: the TUI's windowed
// cgroup probe is not reachable through the pooled connection without new
// remote plumbing, so the section is stamped source "snapshot".
func perfSnapshot(containers []models.Container, stats []models.ContainerStats) *apitypes.ServerPerf {
	if len(containers) == 0 {
		return nil
	}
	rows := make([]apitypes.PerfRow, 0, len(containers))
	for _, c := range containers {
		tc := toContainer(c, stats)
		row := apitypes.PerfRow{ID: tc.ID, Name: tc.Name, MemUsage: tc.MemUsage, State: tc.State}
		if tc.CPUPct != nil {
			row.CPU = *tc.CPUPct
		}
		if tc.MemPct != nil {
			row.MemPct = *tc.MemPct
		}
		rows = append(rows, row)
	}
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].CPU != rows[j].CPU {
			return rows[i].CPU > rows[j].CPU
		}
		return rows[i].Name < rows[j].Name
	})
	return &apitypes.ServerPerf{Source: apitypes.PerfSourceSnapshot, Rows: rows}
}
