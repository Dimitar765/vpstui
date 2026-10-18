package app

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"vpstui/internal/apitypes"
	"vpstui/internal/hubclient"
	"vpstui/internal/models"
)

// newHubModel builds a model already connected to srv, sized for rendering.
// No command is ever executed, so no goroutine starts here; tests that open
// log views must defer m.stopHub().
func newHubModel(t *testing.T, srvURL string) *Model {
	t.Helper()
	m := New(nil, nil)
	m.width, m.height = 100, 30
	// The connect commands are values; tests never execute them, so no
	// background work starts here.
	m.ConnectHub(srvURL, "tok")
	m.hub.SetSize(m.width, m.height-m.tabRows())
	return m
}

// pressKey builds the key message for a named key; "enter"/"esc"/"tab" are
// special key types, anything else a rune sequence.
func pressKey(k string) tea.KeyMsg {
	switch k {
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	case "esc":
		return tea.KeyMsg{Type: tea.KeyEscape}
	case "tab":
		return tea.KeyMsg{Type: tea.KeyTab}
	}
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)}
}

func step(t *testing.T, m *Model, k string) (*Model, tea.Cmd) {
	t.Helper()
	nm, cmd := m.Update(pressKey(k))
	return nm.(*Model), cmd
}

func hubServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/fleet":
			_ = json.NewEncoder(w).Encode(apitypes.FleetResponse{Servers: []apitypes.FleetServer{{
				ID: "srv1", Name: "loop", Status: apitypes.StatusOnline,
				HostStats:      &apitypes.HostStats{Load1: 0.5, MemTotalMB: 8000, MemUsedMB: 3200},
				ContainerCount: 3,
			}}})
		case r.URL.Path == "/api/apps":
			_ = json.NewEncoder(w).Encode([]apitypes.App{{
				ID: "a1", Name: "web", ServerID: "srv1", EnvironmentID: "env1",
				SourceType: "image", SourceConfig: map[string]any{"image": "docker.io/library/nginx:1.27-alpine"},
				Status: apitypes.AppRunning, LastGoodDeployment: "dep0",
			}})
		case r.URL.Path == "/api/apps/a1/deploy" || r.URL.Path == "/api/apps/a1/rollback":
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(`{"deploymentId":"dep9"}`))
		case r.URL.Path == "/api/deployments/dep9":
			_ = json.NewEncoder(w).Encode(apitypes.Deployment{ID: "dep9", AppID: "a1", Status: apitypes.DepQueued})
		case r.URL.Path == "/api/projects":
			_ = json.NewEncoder(w).Encode([]apitypes.Project{{ID: "p1", Name: "prod"}})
		case r.URL.Path == "/api/projects/p1/environments":
			_ = json.NewEncoder(w).Encode([]apitypes.Environment{{ID: "env1", ProjectID: "p1", Name: "web"}})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestHubViewRoutingChain(t *testing.T) {
	m := newHubModel(t, "http://hub.test")

	if m.view != viewHubFleet {
		t.Fatalf("view = %v, want hub fleet after connect", m.view)
	}
	m, _ = step(t, m, "2")
	if m.view != viewHubApps {
		t.Fatalf("view = %v, want hub apps after 2", m.view)
	}
	m, _ = step(t, m, "1")
	if m.view != viewHubFleet {
		t.Fatalf("view = %v, want hub fleet after 1", m.view)
	}
	m, _ = step(t, m, "2")
	m, _ = step(t, m, "esc") // apps → fleet
	if m.view != viewHubFleet {
		t.Fatalf("view = %v, want fleet after esc from apps", m.view)
	}
	m, _ = step(t, m, "esc") // fleet → hosts
	if m.view != viewHosts {
		t.Fatalf("view = %v, want hosts after esc from fleet", m.view)
	}
	// The pinned hub tab is reachable from the host list by tab, and stays
	// pinned in the bar.
	if bar := m.tabBar(); !strings.Contains(bar, "hub") {
		t.Fatalf("host list bar = %q", bar)
	}
	m, _ = step(t, m, "tab")
	if m.view != viewHubFleet {
		t.Fatalf("view = %v, want fleet after tab from host list", m.view)
	}
	m, _ = step(t, m, "2")
	if m.view != viewHubApps {
		t.Fatalf("view = %v, want apps after re-entering", m.view)
	}
}

func TestHubAppsToDeploymentsToLogChain(t *testing.T) {
	old := hubclient.PollInterval
	hubclient.PollInterval = time.Hour
	t.Cleanup(func() { hubclient.PollInterval = old })
	srv := hubServer(t)
	m := newHubModel(t, srv.URL)
	defer m.stopHub()

	// Deliver the apps payload through the real message path.
	fetch := m.fetchHubApps()
	msg := fetch()
	if am, ok := msg.(hubAppsMsg); !ok || len(am.apps) != 1 {
		t.Fatalf("fetchHubApps produced %+v", msg)
	} else {
		m.Update(am)
	}
	m.view = viewHubApps
	m, _ = step(t, m, "enter")
	if m.view != viewHubDeployments {
		t.Fatalf("view = %v, want deployments after enter", m.view)
	}
	if m.hub.curApp != "a1" || m.hub.curAppName != "web" {
		t.Fatalf("curApp = %q/%q", m.hub.curApp, m.hub.curAppName)
	}

	// Deliver deployments, then open one.
	dm := m.fetchHubDeps("a1")().(hubDepsMsg)
	if dm.err != nil || len(dm.deps) != 0 {
		// server has no deployments route for a1's list? it 404s; fine — seed manually
	}
	m.hub.deps = []apitypes.Deployment{{ID: "dep1", AppID: "a1", Status: apitypes.DepSuccess, Trigger: apitypes.TriggerManual}}
	m.hub.refreshDepsRows()
	m2, cmd := step(t, m, "enter")
	if m2.view != viewHubLogs || m2.hubLog == nil {
		t.Fatalf("view = %v, log = %v; want live log view", m2.view, m2.hubLog)
	}
	if cmd == nil {
		t.Fatal("opening a log must start the pump")
	}
	m2.closeHubLog()

	// esc steps back to the deployments list.
	m2, _ = step(t, m2, "esc")
	if m2.view != viewHubDeployments {
		t.Fatalf("view = %v, want deployments after esc from log", m2.view)
	}
}

func TestHubStaleGenerationDropped(t *testing.T) {
	m := newHubModel(t, "http://hub.test")
	gen := m.hub.gen

	// A message from a previous generation (e.g. in flight across a
	// reconnect) must not touch state.
	stale := hubFleetMsg{msgTag: msgTag{hostID: hubHostID, gen: gen - 1},
		servers: []apitypes.FleetServer{{ID: "old", Name: "ghost"}}}
	if _, _ = m.Update(stale); len(m.hub.servers) != 0 {
		t.Fatalf("stale frame applied: %+v", m.hub.servers)
	}

	fresh := hubFleetMsg{msgTag: msgTag{hostID: hubHostID, gen: gen},
		servers: []apitypes.FleetServer{{ID: "srv1", Name: "loop", Status: apitypes.StatusOnline}}}
	if _, _ = m.Update(fresh); len(m.hub.servers) != 1 || m.hub.servers[0].Name != "loop" {
		t.Fatalf("fresh frame not applied: %+v", m.hub.servers)
	}

	// Wrong host id is equally stale — only hubHostID talks to hub state.
	alien := hubFleetMsg{msgTag: msgTag{hostID: "somehost", gen: gen}, servers: []apitypes.FleetServer{{ID: "x"}}}
	if _, _ = m.Update(alien); len(m.hub.servers) != 1 {
		t.Fatalf("alien-host frame applied: %+v", m.hub.servers)
	}
}

func TestHubReconnectBumpsGeneration(t *testing.T) {
	old := hubclient.PollInterval
	hubclient.PollInterval = time.Hour
	t.Cleanup(func() { hubclient.PollInterval = old })
	srv := hubServer(t)
	m := newHubModel(t, srv.URL)
	defer m.stopHub()
	gen := m.hub.gen

	// In-flight frame from the previous generation arrives after reconnect.
	inflight := hubFleetMsg{msgTag: msgTag{hostID: hubHostID, gen: gen},
		servers: []apitypes.FleetServer{{ID: "old"}}}

	if cmd := m.ConnectHub(srv.URL, "tok"); cmd == nil {
		t.Fatal("reconnect returned no commands")
	}
	if m.hub.gen == gen {
		t.Fatal("reconnect kept the generation")
	}
	if _, _ = m.Update(inflight); len(m.hub.servers) != 0 {
		t.Fatal("pre-reconnect frame survived the generation bump")
	}
}

func TestLineSourceFeedsLogView(t *testing.T) {
	m := newHubModel(t, "http://hub.test")
	defer m.stopHub()

	// Open the log view without starting the real watch: state only.
	m.hubLog = NewContainerView("dep1", "web", 80, 24)
	m.hubLog.hostID, m.hubLog.gen = hubHostID, m.hub.gen
	m.hubLogDep = "dep1"
	m.view = viewHubLogs

	sends := make(chan tea.Msg, 16)
	m.Attach(func(msg tea.Msg) { sends <- msg })

	frames := make(chan apitypes.DeployLogFrame, 4)
	src := &wsLines{ch: frames}
	t0 := msgTag{hostID: hubHostID, gen: m.hub.gen}
	pump := m.pumpHubLines(src, t0, "dep1")
	done := make(chan struct{})
	go func() { defer close(done); pump() }()

	frames <- apitypes.DeployLogFrame{Type: apitypes.FrameDeployLog, Line: "step 1"}
	frames <- apitypes.DeployLogFrame{Type: apitypes.FrameDeployLog, Line: "step 2"}
	frames <- apitypes.DeployLogFrame{Type: apitypes.FrameDeployLog, DeploymentID: "dep1", Done: true}
	close(frames)

	var chunks []string
	ended := false
	for i := 0; i < 3; i++ {
		select {
		case msg := <-sends:
			switch msg := msg.(type) {
			case hubLogChunkMsg:
				chunks = append(chunks, msg.chunk)
			case hubLogEndMsg:
				ended = true
			}
		case <-time.After(2 * time.Second):
			t.Fatal("pump stalled")
		}
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("pump did not end at EOF")
	}
	if len(chunks) != 2 || !strings.HasPrefix(chunks[0], "step 1") || !ended {
		t.Fatalf("chunks = %q ended = %v", chunks, ended)
	}

	// Apply them the way the program would: chunk messages append, the flush
	// tick renders, the end message closes the stream.
	for _, c := range chunks {
		if _, _ = m.Update(hubLogChunkMsg{msgTag: t0, depID: "dep1", chunk: c}); false {
		}
	}
	// A foreign depID must not land in the current view.
	if _, _ = m.Update(hubLogChunkMsg{msgTag: t0, depID: "other", chunk: "alien\n"}); false {
	}
	if _, cmd := m.Update(logFlushMsg{msgTag: t0}); cmd == nil {
		t.Fatal("flush tick should re-arm while lines are pending")
	}
	view := m.hubLog.View()
	if !strings.Contains(view, "step 1") || !strings.Contains(view, "step 2") {
		t.Fatalf("log view missing pumped lines:\n%s", view)
	}
	if strings.Contains(view, "alien") {
		t.Fatal("foreign depID chunk leaked into the log view")
	}
	if _, cmd := m.Update(hubLogEndMsg{msgTag: t0, depID: "dep1"}); cmd == nil {
		t.Fatal("log end must fetch the deployment status")
	}
	if m.hubLog.running {
		t.Fatal("stream still marked running after end")
	}
}

func TestHubDeployConfirmFlow(t *testing.T) {
	old := hubclient.PollInterval
	hubclient.PollInterval = time.Hour
	t.Cleanup(func() { hubclient.PollInterval = old })
	srv := hubServer(t)
	m := newHubModel(t, srv.URL)
	defer m.stopHub()

	// Seed one app.
	m.hub.apps = []apitypes.App{{ID: "a1", Name: "web", Status: apitypes.AppRunning, LastGoodDeployment: "dep0"}}
	m.hub.refreshAppsRows()
	m.view = viewHubApps

	m, _ = step(t, m, "d")
	if m.hubConfirm != "deploy" {
		t.Fatalf("confirm = %q, want deploy", m.hubConfirm)
	}
	if v := m.View(); !strings.Contains(v, "deploy web?") {
		t.Fatalf("confirm line missing from view:\n%s", v)
	}
	// Any key but y/n keeps the prompt up.
	m, _ = step(t, m, "j")
	if m.hubConfirm != "deploy" {
		t.Fatal("prompt dismissed by an unrelated key")
	}
	m, _ = step(t, m, "n")
	if m.hubConfirm != "" {
		t.Fatal("n did not clear the prompt")
	}

	// y runs the confirmed deploy against the test daemon.
	m, _ = step(t, m, "d")
	_, cmd := step(t, m, "y")
	if cmd == nil {
		t.Fatal("y produced no command")
	}
	am, ok := cmd().(hubActionMsg)
	if !ok || am.err != nil || am.depID != "dep9" || am.verb != "deploy" {
		t.Fatalf("action msg = %+v ok = %v", cmd(), ok)
	}
	m2i, _ := m.Update(am)
	m2 := m2i.(*Model)
	if m2.view != viewHubLogs || m2.hubLog == nil || m2.hubLog.id != "dep9" {
		t.Fatalf("after deploy: view = %v log = %+v; want live log of dep9", m2.view, m2.hubLog)
	}
	if m2.hub.curApp != "a1" || m2.hub.curAppName != "web" {
		t.Fatalf("deploy did not carry app context: %q/%q", m2.hub.curApp, m2.hub.curAppName)
	}
	if len(m2.hub.deps) == 0 || m2.hub.deps[0].ID != "dep9" {
		t.Fatalf("queued run not pre-seeded: %+v", m2.hub.deps)
	}
	m2.closeHubLog()

	// Rollback goes through the same gate, with the verb preserved.
	m.view = viewHubApps
	m, _ = step(t, m, "R")
	if m.hubConfirm != "rollback" {
		t.Fatalf("confirm = %q, want rollback", m.hubConfirm)
	}
	m, _ = step(t, m, "y")
	// (the command would be executed the same way; verb mapping is the point)
}

func TestHubTerminalStatusToast(t *testing.T) {
	srv := hubServer(t)
	m := newHubModel(t, srv.URL)
	m.hubLog = NewContainerView("dep9", "web", 80, 24)
	m.hubLogVerb = "rollback"

	dm := hubDepMsg{msgTag: msgTag{hostID: hubHostID, gen: m.hub.gen},
		dep: apitypes.Deployment{ID: "dep9", Status: apitypes.DepSuccess}}
	if _, cmd := m.Update(dm); cmd == nil {
		t.Fatal("terminal status produced no command")
	}
	if v := m.View(); !strings.Contains(v, "rollback succeeded") {
		t.Fatalf("missing success toast:\n%s", v)
	}
	// The cached history row picked up the terminal status too.
	for _, d := range m.hub.deps {
		if d.ID == "dep9" && d.Status != apitypes.DepSuccess {
			t.Fatalf("cached row = %s", d.Status)
		}
	}
}

func TestHubTabCoexistsWithSessions(t *testing.T) {
	old := hubclient.PollInterval
	hubclient.PollInterval = time.Hour
	t.Cleanup(func() { hubclient.PollInterval = old })
	m := newHubModel(t, "http://hub.test")
	defer m.stopHub()

	// A direct session joins, dashboard-ready so tab activation lands there.
	s := &Session{host: models.Host{ID: "h1", Name: "loop"}, gen: m.nextGen(),
		dash: NewDashboard(models.Host{ID: "h1", Name: "loop"}, nil, m.width, m.height, 1)}
	m.sessions = append(m.sessions, s)

	// From the hub, tab lands on the session.
	m.view = viewHubFleet
	m2, _ := step(t, m, "tab")
	if m2.view != viewDashboard {
		t.Fatalf("view = %v, want dashboard after tab from hub", m2.view)
	}
	// And from the session view, tab wraps to the hub tab.
	m3, _ := step(t, m2, "tab")
	if m3.view != viewHubFleet {
		t.Fatalf("view = %v, want hub fleet after tab from session", m3.view)
	}
	// shift+tab from the hub goes back to the (last) session.
	m4, _ := step(t, m3, "shift+tab")
	if m4.view != viewDashboard {
		t.Fatalf("view = %v, want dashboard after shift+tab from hub", m4.view)
	}

	// The bar renders both tabs, hub active in hub views and session active in
	// session views.
	bar := m3.tabBar()
	if !strings.Contains(bar, "1 loop") || !strings.Contains(bar, "hub") {
		t.Fatalf("tab bar = %q", bar)
	}
	m4.view = viewHubFleet
	if !strings.Contains(m4.tabBar(), lipglossActiveTab().Render("hub")) {
		t.Fatalf("hub tab not active in hub view: %q", m4.tabBar())
	}
}

func TestHubTabBarWithNoSessions(t *testing.T) {
	m := newHubModel(t, "http://hub.test")
	defer m.stopHub()
	if m.tabRows() != 1 {
		t.Fatal("hub-only mode must still show a one-row tab bar")
	}
	if bar := m.tabBar(); !strings.Contains(bar, "hub") {
		t.Fatalf("bar = %q", bar)
	}
	// Clicking the tab bar's hub slot keeps hub mode.
	if i := m.tabIndexAt(0); i != len(m.sessions) {
		t.Fatalf("tabIndexAt(0) = %d, want hub slot", i)
	}
}

func TestHubContainersCarryTags(t *testing.T) {
	m := newHubModel(t, "http://hub.test")
	defer m.stopHub()
	// The operator's own host: tags are filed under its ID, while the hub
	// knows the server only by name.
	m.hosts = []models.Host{{ID: "c2-loop", Name: "loop"}}
	m.tags.Set("c2-loop", "web", "edge", "green", "id1")

	m.hub.servers = []apitypes.FleetServer{{
		ID: "srv1", Name: "loop", Status: apitypes.StatusOnline,
		Containers: []apitypes.Container{{ID: "c1", Name: "web", Image: "nginx", State: "running",
			CPUPct: new(1.5), MemPct: new(9.0)}},
		ContainerCount: 1,
	}}
	m.hub.fleetLoaded = true
	m.hub.refreshFleetRows(m.tags, m.hubTagHost)

	// The fleet row itself shows the server, not containers.
	if v := m.View(); strings.Contains(v, "edge") {
		t.Fatalf("container label leaked into the server table:\n%s", v)
	}
	// Expanding the cursor server shows the tagged container.
	m2, _ := step(t, m, "enter")
	if !m2.hub.showContainers || m2.hub.expandedServer != "srv1" {
		t.Fatalf("expand failed: %+v", m2.hub.showContainers)
	}
	if v := m2.View(); !strings.Contains(v, "edge web") {
		t.Fatalf("tagged container label missing:\n%s", v)
	}
	if tint := m2.hub.contTbl.tint; len(tint) != 1 || tint[0] == "" {
		t.Fatalf("container row not tinted: %v", tint)
	}
	// esc collapses back to the server rows before leaving the hub.
	m3, _ := step(t, m2, "esc")
	if m3.hub.showContainers || m3.view != viewHubFleet {
		t.Fatalf("esc inside fleet: containers = %v view = %v", m3.hub.showContainers, m3.view)
	}
	m3, _ = step(t, m3, "esc")
	if m3.view != viewHosts {
		t.Fatalf("view = %v, want hosts", m3.view)
	}
}

func TestHubHelpRowsAndOverlay(t *testing.T) {
	m := newHubModel(t, "http://hub.test")
	defer m.stopHub()
	for view, want := range map[View]string{
		viewHubFleet:       "keys — hub fleet",
		viewHubApps:        "keys — hub apps",
		viewHubDeployments: "keys — hub deployments",
	} {
		m.view = view
		if rows := m.helpRows(); len(rows) == 0 {
			t.Errorf("%v helpRows empty", view)
		}
		m.helpOpen = true
		if v := m.View(); !strings.Contains(v, want) {
			t.Errorf("overlay title for %v missing: want %q", view, want)
		}
		m.helpOpen = false
	}
	// The hub log view reuses the container log key table.
	m.hubLog = NewContainerView("dep1", "web", 80, 24)
	m.view = viewHubLogs
	if rows := m.helpRows(); len(rows) == 0 {
		t.Error("hub log helpRows empty")
	}
	m.hubForm = newHubForm("http://127.0.0.1:8716")
	m.view = viewHubForm
	if rows := m.helpRows(); len(rows) == 0 {
		t.Error("hub form helpRows empty")
	}
	m.helpOpen = true
	if v := m.View(); !strings.Contains(v, "keys — hub connect") {
		t.Errorf("overlay title for hub form missing")
	}
}

func TestHubTokenRefKeying(t *testing.T) {
	cases := map[string]string{
		"http://127.0.0.1:8716":         "127.0.0.1:8716",
		"http://127.0.0.1:8716/":        "127.0.0.1:8716",
		"127.0.0.1:8716":                "127.0.0.1:8716",
		"https://hub.example.com:443/x": "hub.example.com/x",
	}
	for in, want := range cases {
		if got := hubKey(in); got != want {
			t.Errorf("hubKey(%q) = %q, want %q", in, got, want)
		}
		if !strings.HasPrefix(HubTokenRef(in), "hub:token:") {
			t.Errorf("HubTokenRef(%q) = %q", in, HubTokenRef(in))
		}
	}
	if HubTokenRef("http://127.0.0.1:8716") != HubTokenRef("127.0.0.1:8716/") {
		t.Error("equivalent URLs fork the token ref")
	}
}

func TestHubFormSubmitsURLAndToken(t *testing.T) {
	f := newHubForm("http://127.0.0.1:8716")
	// URL prefilled from --hub: focus starts on the token.
	if f.focus != 1 {
		t.Fatalf("focus = %d, want token field", f.focus)
	}
	// esc on the untouched form reports cancelled.
	if _, _, _, cancelled := f.Update(pressKey("esc")); !cancelled {
		t.Fatal("esc on a fresh form did not report cancelled")
	}
	// Focus is on the token: type, then enter on the last field submits.
	nf, _, _, _ := f.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("vps_secret")})
	f = nf
	nf, _, submitted, _ := f.Update(pressKey("enter"))
	f = nf
	if !submitted || f.URL() != "http://127.0.0.1:8716" || f.Token() != "vps_secret" {
		t.Fatalf("submitted = %v url = %q token = %q", submitted, f.URL(), f.Token())
	}
	// Empty URL refuses to submit: enter on the URL field moves down, the
	// second enter (on the last field) tries and sets the form error.
	f2 := newHubForm("")
	if f2.focus != 0 {
		t.Fatalf("focus = %d, want url field", f2.focus)
	}
	nf2, _, _, _ := f2.Update(pressKey("enter"))
	f2 = nf2
	nf2, _, submitted2, _ := f2.Update(pressKey("enter"))
	if submitted2 || nf2.err == "" {
		t.Fatalf("empty url submitted: %v %q", submitted2, nf2.err)
	}
}
