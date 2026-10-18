package app

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"vpstui/internal/apitypes"
	"vpstui/internal/config"
	"vpstui/internal/hubclient"
)

// hubHostID is the msgTag hostID for every hub message. The rest of the model
// tags messages with the SSH host they belong to; hub messages all belong to
// the one daemon connection, and the generation field (below) is what makes
// them stale or fresh.
const hubHostID = "hub"

// HubTokenRef is the keyring reference under which a hub's API token is
// stored, keyed by its URL: secret.Set(HubTokenRef(u), token).
func HubTokenRef(baseURL string) string { return "hub:token:" + hubKey(baseURL) }

// hubKey normalizes a hub URL into the stable key tokens are filed under:
// host (default ports elided) plus any path, so a trailing slash or an
// explicit http:// cannot fork the token store.
func hubKey(baseURL string) string {
	s := strings.TrimSpace(baseURL)
	u, err := url.Parse(s)
	if err != nil || u.Host == "" {
		return strings.TrimRight(s, "/")
	}
	host := strings.ToLower(u.Host)
	switch u.Scheme {
	case "http":
		host = strings.TrimSuffix(host, ":80")
	case "https":
		host = strings.TrimSuffix(host, ":443")
	}
	return host + strings.TrimRight(u.Path, "/")
}

// ---------------------------------------------------------------- messages

// hubMsg is implemented by every hub message so route can apply the
// generation check once. The tag is minted when the hub connection is made;
// anything arriving from an older generation is dropped on arrival — the same
// discipline session messages get from their host/gen pair.
type hubMsg interface {
	tea.Msg
	hubTag() msgTag
}

type hubFleetMsg struct {
	msgTag
	servers []apitypes.FleetServer
	err     error
}

func (m hubFleetMsg) hubTag() msgTag { return m.msgTag }

type hubAlertMsg struct {
	msgTag
	frame apitypes.AlertFrame
}

func (m hubAlertMsg) hubTag() msgTag { return m.msgTag }

type hubAppsMsg struct {
	msgTag
	apps []apitypes.App
	err  error
}

func (m hubAppsMsg) hubTag() msgTag { return m.msgTag }

// hubMetaMsg carries the environment-id → "project/env" label map for the
// apps table. A failed lookup is not an error on screen: the column falls
// back to raw ids.
type hubMetaMsg struct {
	msgTag
	envNames map[string]string
}

func (m hubMetaMsg) hubTag() msgTag { return m.msgTag }

type hubDepsMsg struct {
	msgTag
	appID string
	deps  []apitypes.Deployment
	err   error
}

func (m hubDepsMsg) hubTag() msgTag { return m.msgTag }

// hubDepMsg reports one deployment's current state — fetched when a watched
// stream ends, so the terminal toast names the real outcome.
type hubDepMsg struct {
	msgTag
	dep apitypes.Deployment
	err error
}

func (m hubDepMsg) hubTag() msgTag { return m.msgTag }

// hubActionMsg reports the daemon's acceptance of a deploy or rollback: the
// queued deployment id, which the caller turns into a live log view.
type hubActionMsg struct {
	msgTag
	verb    string // "deploy" | "rollback"
	appID   string
	appName string
	depID   string
	err     error
}

func (m hubActionMsg) hubTag() msgTag { return m.msgTag }

type hubLogChunkMsg struct {
	msgTag
	depID string
	chunk string
}

func (m hubLogChunkMsg) hubTag() msgTag { return m.msgTag }

type hubLogEndMsg struct {
	msgTag
	depID string
	err   error
}

func (m hubLogEndMsg) hubTag() msgTag { return m.msgTag }

// ---------------------------------------------------------------- state

// alertInfo is one alert rule's last known transition state, kept for the
// fleet view's alert badges.
type alertInfo struct {
	firing bool
	detail string
}

// hubState is the model's view of one daemon connection: the client, its
// context (canceled on reconnect or quit), and the cached data the three hub
// tables render. Owned by the UI goroutine like every other Model field;
// hubclient watches run in their own goroutines and reach it only via m.send.
type hubState struct {
	url    string
	client *hubclient.Client
	ctx    context.Context // root for every fetch and watch; cancel unwinds all
	cancel context.CancelFunc
	gen    uint64

	servers     []apitypes.FleetServer
	apps        []apitypes.App
	envNames    map[string]string
	alerts      map[string]alertInfo
	fleetLoaded bool
	fleetErr    string
	appsLoaded  bool
	appsErr     string
	depsErr     string

	curApp     string // app whose deployments/logs are on screen
	curAppName string
	deps       []apitypes.Deployment

	showContainers bool // fleet view: expand the cursor server's containers
	expandedServer string

	fleetTbl ctable
	contTbl  ctable
	appsTbl  ctable
	depsTbl  ctable
}

func newHubState(url string, c *hubclient.Client, ctx context.Context, cancel context.CancelFunc, gen uint64) *hubState {
	return &hubState{
		url: url, client: c, ctx: ctx, cancel: cancel, gen: gen,
		envNames: map[string]string{},
		alerts:   map[string]alertInfo{},
	}
}

// SetSize refits the hub tables to the content area.
func (h *hubState) SetSize(w, contentH int) {
	w = max(w-2, 20)
	hh := max(contentH-6, 3)
	h.fleetTbl.SetColumns(fleetColsFor(w))
	h.fleetTbl.SetSize(w, hh)
	h.contTbl.SetColumns(contColsFor(w))
	h.contTbl.SetSize(w, hh)
	h.appsTbl.SetColumns(appsColsFor(w))
	h.appsTbl.SetSize(w, hh)
	h.depsTbl.SetColumns(depsColsFor(w))
	h.depsTbl.SetSize(w, hh)
}

func (h *hubState) serverName(id string) string {
	for _, s := range h.servers {
		if s.ID == id {
			return s.Name
		}
	}
	return id
}

func (h *hubState) envLabel(id string) string {
	if n, ok := h.envNames[id]; ok {
		return n
	}
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

// cursorApp is the app the apps-table cursor points at.
func (h *hubState) cursorApp() (apitypes.App, bool) {
	i := h.appsTbl.Cursor()
	if i < 0 || i >= len(h.apps) {
		return apitypes.App{}, false
	}
	return h.apps[i], true
}

func (h *hubState) cursorDeployment() (apitypes.Deployment, bool) {
	i := h.depsTbl.Cursor()
	if i < 0 || i >= len(h.deps) {
		return apitypes.Deployment{}, false
	}
	return h.deps[i], true
}

// firingAlerts counts rules currently firing; when server is non-empty only
// those whose detail mentions it count. Alert frames carry a rule id and
// free-text detail but no server id (S7), so attribution matches the server
// name inside the detail — best effort until the frame grows a field.
func (h *hubState) firingAlerts(server string) int {
	n := 0
	for _, a := range h.alerts {
		if !a.firing {
			continue
		}
		if server == "" || strings.Contains(a.detail, server) {
			n++
		}
	}
	return n
}

// ---------------------------------------------------------------- row building

func fleetColsFor(total int) []ctableCol {
	fixed := 12 + 6 + 6 + 5 + 9
	name := max(total-fixed-5*cellGap, 8)
	return []ctableCol{
		{title: "SERVER", width: name, kind: cellPlain},
		{title: "STATUS", width: 12, kind: cellHubStatus},
		{title: "LOAD", width: 6, kind: cellPlain},
		{title: "MEM", width: 6, kind: cellPct},
		{title: "CTRS", width: 5, kind: cellPlain},
		{title: "ALERTS", width: 9, kind: cellAlert},
	}
}

func contColsFor(total int) []ctableCol {
	fixed := 18 + 12 + 8 + 10
	name := max(total-fixed-4*cellGap, 10)
	return []ctableCol{
		{title: "NAME", width: name, kind: cellTag},
		{title: "IMAGE", width: 18, kind: cellPlain},
		{title: "STATE", width: 12, kind: cellState},
		{title: "CPU", width: 8, kind: cellPct},
		{title: "MEM", width: 10, kind: cellPct},
	}
}

func appsColsFor(total int) []ctableCol {
	fixed := 14 + 12 + 10 + 12 + 10
	name := max(total-fixed-5*cellGap, 8)
	return []ctableCol{
		{title: "APP", width: name, kind: cellPlain},
		{title: "PROJECT", width: 14, kind: cellPlain},
		{title: "SERVER", width: 12, kind: cellPlain},
		{title: "SOURCE", width: 10, kind: cellPlain},
		{title: "STATUS", width: 12, kind: cellHubStatus},
		{title: "UPDATED", width: 10, kind: cellPlain},
	}
}

func depsColsFor(total int) []ctableCol {
	fixed := 12 + 9 + 12 + 10 + 10
	rel := max(total-fixed-5*cellGap, 8)
	return []ctableCol{
		{title: "STATUS", width: 12, kind: cellHubStatus},
		{title: "TRIGGER", width: 9, kind: cellPlain},
		{title: "RELEASE", width: rel, kind: cellPlain},
		{title: "CREATED", width: 10, kind: cellPlain},
		{title: "FINISHED", width: 10, kind: cellPlain},
	}
}

// hubStatusCell colors a server/app/deployment status dot. The palette
// matches the fleet's semantics: green live, red down/failed, muted
// undeployed/canceled, amber everything else.
func hubStatusCell(s string) string {
	switch s {
	case apitypes.StatusOnline, apitypes.AppRunning, apitypes.DepSuccess:
		return styleOK.Render("● " + s)
	case apitypes.StatusOffline, apitypes.DepFailed:
		return styleErr.Render("● " + s)
	case apitypes.StatusUnknown, apitypes.AppUndeployed, apitypes.DepCanceled:
		return styleMuted.Render("○ " + s)
	}
	return styleWarn.Render("● " + s)
}

// refreshFleetRows rebuilds the fleet (or expanded container) table rows from
// the cached snapshot. Tags match on host name + container name: the resolver
// maps a hub server name to the operator's own host id (the key tags.json
// entries are filed under), so a label applied in direct mode follows the
// container into hub mode.
func (h *hubState) refreshFleetRows(tags *TagStore, tagHost func(serverName string) string) {
	if h.showContainers {
		rows, tint := h.containerRows(tags, tagHost)
		h.contTbl.SetRows(rows, nil)
		h.contTbl.SetTint(tint)
		return
	}
	rows := make([][]string, 0, len(h.servers))
	for _, s := range h.servers {
		load, mem := "—", ""
		if s.HostStats != nil {
			load = strconv.FormatFloat(s.HostStats.Load1, 'f', 2, 64)
			if s.HostStats.MemTotalMB > 0 {
				mem = fmt.Sprintf("%.0f%%", 100*float64(s.HostStats.MemUsedMB)/float64(s.HostStats.MemTotalMB))
			}
		}
		alerts := ""
		if n := h.firingAlerts(s.Name); n > 0 {
			alerts = fmt.Sprintf("⚠ %d", n)
		}
		rows = append(rows, []string{
			s.Name, s.Status, load, mem,
			strconv.Itoa(s.ContainerCount), alerts,
		})
	}
	h.fleetTbl.SetRows(rows, nil)
}

func (h *hubState) containerRows(tags *TagStore, tagHost func(string) string) ([][]string, []string) {
	var s *apitypes.FleetServer
	for i := range h.servers {
		if h.servers[i].ID == h.expandedServer {
			s = &h.servers[i]
			break
		}
	}
	if s == nil {
		return nil, nil
	}
	rows := make([][]string, 0, len(s.Containers))
	tint := make([]string, 0, len(s.Containers))
	host := s.Name
	if tagHost != nil {
		host = tagHost(s.Name)
	}
	for _, c := range s.Containers {
		// Hub containers match tags by host name + container name, resolved
		// to the direct-mode host id the labels are stored under.
		display := c.Name
		t := ""
		if tags != nil {
			if d := tags.Display(host, c.Name); d != "" {
				display = d
			}
			t = tags.Tint(host, c.Name)
		}
		cpu, mem := "", ""
		if c.CPUPct != nil {
			cpu = fmt.Sprintf("%.1f%%", *c.CPUPct)
		}
		if c.MemPct != nil {
			mem = fmt.Sprintf("%.1f%%", *c.MemPct)
		}
		rows = append(rows, []string{display, c.Image, c.State, cpu, mem})
		tint = append(tint, t)
	}
	return rows, tint
}

func (h *hubState) refreshAppsRows() {
	rows := make([][]string, 0, len(h.apps))
	for _, a := range h.apps {
		src := a.SourceType
		if img, ok := a.SourceConfig["image"].(string); ok && img != "" {
			if i := strings.LastIndex(img, "/"); i >= 0 {
				img = img[i+1:]
			}
			src += " " + img
		}
		rows = append(rows, []string{
			a.Name, h.envLabel(a.EnvironmentID), h.serverName(a.ServerID),
			src, a.Status, hubAgo(a.UpdatedAt),
		})
	}
	h.appsTbl.SetRows(rows, nil)
}

func (h *hubState) refreshDepsRows() {
	rows := make([][]string, 0, len(h.deps))
	for _, d := range h.deps {
		fin := ""
		if d.FinishedAt != nil {
			fin = hubAgo(*d.FinishedAt)
		} else if d.Status == apitypes.DepQueued || d.Status == apitypes.DepRunning {
			fin = "…"
		}
		rel := d.Release
		if rel == "" {
			rel = "—"
		}
		rows = append(rows, []string{
			d.Status, d.Trigger, rel, hubAgo(d.CreatedAt), fin,
		})
	}
	h.depsTbl.SetRows(rows, nil)
}

func hubAgo(t time.Time) string {
	if t.IsZero() {
		return "—"
	}
	d := time.Since(t)
	switch {
	case d < 0:
		return "now"
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
	return fmt.Sprintf("%dd", int(d.Hours()/24))
}

// ---------------------------------------------------------------- lifecycle

// StartHub connects the model to a hub daemon; exported for main's --hub
// flag. Without a token it opens the hub form first — the same form the `H`
// key opens from the host list — and the form's submit path calls ConnectHub.
func (m *Model) StartHub(url, token string) tea.Cmd {
	if strings.TrimSpace(token) == "" {
		m.hubForm = newHubForm(url)
		m.view = viewHubForm
		return nil
	}
	return m.ConnectHub(url, token)
}

// SetStartHub arms hub mode for --hub startup: the command (or the pending
// form, which needs no command) is batched into Init so it runs on the UI
// goroutine with everything else.
func (m *Model) SetStartHub(url, token string) {
	m.startCmd = m.StartHub(url, token)
}

// ConnectHub drops any previous hub connection (its generation goes stale and
// every in-flight message from it is dropped on arrival) and starts the fetch
// and watch chains for the new one.
func (m *Model) ConnectHub(url, token string) tea.Cmd {
	if m.hub != nil {
		m.hub.cancel()
		m.hub = nil
	}
	m.closeHubLog()
	c := hubclient.New(url, token)
	ctx, cancel := context.WithCancel(context.Background())
	m.hub = newHubState(url, c, ctx, cancel, m.nextGen())
	m.view = viewHubFleet
	return tea.Batch(
		m.fetchHubFleet(), m.fetchHubApps(), m.fetchHubMeta(),
		m.watchHubFleetCmd(), m.watchHubAlertsCmd(),
	)
}

// stopHub unwinds the daemon connection: watches and polls die with the
// context, and the generation bump on the next ConnectHub drops anything
// already in flight.
func (m *Model) stopHub() {
	m.closeHubLog()
	if m.hub != nil {
		m.hub.cancel()
		m.hub = nil
	}
}

// fetchHubFleet is the one-shot REST refresh; the watch keeps the table live
// between presses of f.
func (m *Model) fetchHubFleet() tea.Cmd {
	h := m.hub
	if h == nil {
		return nil
	}
	t := msgTag{hostID: hubHostID, gen: h.gen}
	return func() tea.Msg {
		servers, err := h.client.Fleet(h.ctx)
		return hubFleetMsg{msgTag: t, servers: servers, err: err}
	}
}

func (m *Model) fetchHubApps() tea.Cmd {
	h := m.hub
	if h == nil {
		return nil
	}
	t := msgTag{hostID: hubHostID, gen: h.gen}
	return func() tea.Msg {
		apps, err := h.client.Apps(h.ctx)
		return hubAppsMsg{msgTag: t, apps: apps, err: err}
	}
}

func (m *Model) fetchHubMeta() tea.Cmd {
	h := m.hub
	if h == nil {
		return nil
	}
	t := msgTag{hostID: hubHostID, gen: h.gen}
	return func() tea.Msg {
		names := map[string]string{}
		projs, err := h.client.Projects(h.ctx)
		if err == nil {
			for _, p := range projs {
				pd, derr := h.client.Project(h.ctx, p.ID)
				if derr != nil {
					continue
				}
				for _, e := range pd.Environments {
					names[e.ID] = pd.Name + "/" + e.Name
				}
			}
		}
		return hubMetaMsg{msgTag: t, envNames: names}
	}
}

func (m *Model) fetchHubDeps(appID string) tea.Cmd {
	h := m.hub
	if h == nil || appID == "" {
		return nil
	}
	t := msgTag{hostID: hubHostID, gen: h.gen}
	return func() tea.Msg {
		deps, err := h.client.Deployments(h.ctx, appID)
		return hubDepsMsg{msgTag: t, appID: appID, deps: deps, err: err}
	}
}

// fetchHubDep asks for one deployment's current state; the arrival decides
// the terminal toast.
func (m *Model) fetchHubDep(depID string) tea.Cmd {
	h := m.hub
	if h == nil || depID == "" {
		return nil
	}
	t := msgTag{hostID: hubHostID, gen: h.gen}
	return func() tea.Msg {
		dep, err := h.client.Deployment(h.ctx, depID)
		return hubDepMsg{msgTag: t, dep: dep, err: err}
	}
}

// watchHubFleetCmd starts the fleet watch for this generation. Frames — WS
// pushes or poll fallbacks — become hubFleetMsgs on m.send; the watch itself
// dies with the hub context.
func (m *Model) watchHubFleetCmd() tea.Cmd {
	h := m.hub
	if h == nil {
		return nil
	}
	t := msgTag{hostID: hubHostID, gen: h.gen}
	send := m.send
	return func() tea.Msg {
		ch, stop, err := h.client.WatchFleet(h.ctx)
		if err != nil {
			return hubFleetMsg{msgTag: t, err: err}
		}
		go func() {
			defer stop()
			for f := range ch {
				if send != nil {
					send(hubFleetMsg{msgTag: t, servers: f.Servers})
				}
			}
		}()
		return nil
	}
}

// watchHubAlertsCmd starts the alert-transition watch for this generation.
func (m *Model) watchHubAlertsCmd() tea.Cmd {
	h := m.hub
	if h == nil {
		return nil
	}
	t := msgTag{hostID: hubHostID, gen: h.gen}
	send := m.send
	return func() tea.Msg {
		ch, stop, err := h.client.WatchAlerts(h.ctx)
		if err != nil {
			return nil // alerts are additive; the fleet view works without them
		}
		go func() {
			defer stop()
			for f := range ch {
				if send != nil {
					send(hubAlertMsg{msgTag: t, frame: f})
				}
			}
		}()
		return nil
	}
}

// refreshHubView re-fetches whatever the current hub screen shows; it backs
// `f`, view switches, and hub re-entry after tabbing away.
func (m *Model) refreshHubView() tea.Cmd {
	if m.hub == nil {
		return nil
	}
	switch m.view {
	case viewHubFleet:
		return m.fetchHubFleet()
	case viewHubApps:
		return tea.Batch(m.fetchHubApps(), m.fetchHubMeta())
	case viewHubDeployments:
		return m.fetchHubDeps(m.hub.curApp)
	}
	return nil
}

// activateHub makes the pinned hub tab current.
func (m *Model) activateHub() tea.Cmd {
	if m.hub == nil {
		return nil
	}
	m.view = viewHubFleet
	return m.refreshHubView()
}

// hubTabs is the number of tab-bar slots the hub adds (0 or 1, pinned last).
func (m *Model) hubTabs() int {
	if m.hub != nil {
		return 1
	}
	return 0
}

func isHubView(v View) bool {
	switch v {
	case viewHubFleet, viewHubApps, viewHubDeployments, viewHubLogs:
		return true
	}
	return false
}

// cycleTab walks the session tabs plus the pinned hub tab in the given
// direction (1 forward, -1 back). sessionKeys hands its tab keys here so one
// ordering covers every surface.
func (m *Model) cycleTab(dir int) tea.Cmd {
	total := len(m.sessions) + m.hubTabs()
	if total == 0 {
		return nil
	}
	cur := m.active
	if isHubView(m.view) {
		cur = len(m.sessions)
	}
	next := ((cur+dir)%total + total) % total
	if next == len(m.sessions) {
		return m.activateHub()
	}
	return m.activate(next)
}

// ---------------------------------------------------------------- deploy log

// lineSource yields the next raw log line, or io.EOF when the stream is over.
// ContainerView's buffer/scroll machinery is agnostic about where lines come
// from: direct mode feeds it through the SSH log-stream reader, hub mode
// through wsLines over the deployment-log watch.
type lineSource interface {
	Next(ctx context.Context) (string, error)
}

// wsLines adapts the hub deployment-log channel to lineSource: line frames
// pass through, the terminal Done frame and the channel close both read as
// io.EOF, and a canceled context surfaces as ctx.Err.
type wsLines struct {
	ch <-chan apitypes.DeployLogFrame
}

func (w *wsLines) Next(ctx context.Context) (string, error) {
	for {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case f, ok := <-w.ch:
			if !ok {
				return "", io.EOF
			}
			if f.Line != "" {
				return f.Line, nil
			}
			if f.Done {
				return "", io.EOF
			}
		}
	}
}

// openHubLog switches to the live deployment log for d, reusing
// ContainerView's buffer/filter/scroll machinery with a WS-backed lineSource.
// The returned cmd starts the pump goroutine; the view outlives it via the
// generation + depID staleness checks in routeHub.
func (m *Model) openHubLog(verb string, d apitypes.Deployment) tea.Cmd {
	h := m.hub
	if h == nil {
		return nil
	}
	m.closeHubLog()
	m.hubLog = NewContainerView(d.ID, h.curAppName, m.width, m.height-m.tabRows())
	m.hubLog.hostID, m.hubLog.gen = hubHostID, h.gen
	m.hubLog.wrap = m.cfg.LogWrap
	m.hubLogDep = d.ID
	m.hubLogVerb = verb
	m.view = viewHubLogs
	t := msgTag{hostID: hubHostID, gen: h.gen}
	ch, stop, err := h.client.WatchDeployment(h.ctx, d.ID)
	if err != nil {
		stop()
		return func() tea.Msg { return hubLogEndMsg{msgTag: t, depID: d.ID, err: err} }
	}
	m.hubLogStop = stop
	return m.pumpHubLines(&wsLines{ch: ch}, t, d.ID)
}

// pumpHubLines drains the line source into the UI goroutine: one chunk message
// per line, one end message at EOF. It runs inside the cmd goroutine — the
// same arrangement as the direct-mode streamLogs — and ends when the stream
// does or when stop (or the hub context) cancels it out.
func (m *Model) pumpHubLines(src lineSource, t msgTag, depID string) tea.Cmd {
	send := m.send
	return func() tea.Msg {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		for {
			line, err := src.Next(ctx)
			if err != nil {
				if send != nil {
					if err == io.EOF || ctx.Err() != nil {
						send(hubLogEndMsg{msgTag: t, depID: depID})
					} else {
						send(hubLogEndMsg{msgTag: t, depID: depID, err: err})
					}
				}
				return nil
			}
			if send != nil {
				send(hubLogChunkMsg{msgTag: t, depID: depID, chunk: line + "\n"})
			}
		}
	}
}

// closeHubLog tears down the live log view and its watch. Safe to call when
// nothing is open.
func (m *Model) closeHubLog() {
	if m.hubLogStop != nil {
		m.hubLogStop()
		m.hubLogStop = nil
	}
	if m.hubLog != nil {
		m.hubLog.Stop()
		m.hubLog = nil
	}
	m.hubLogDep = ""
}

// leaveHubLog steps back from a deployment log to its deployments list.
func (m *Model) leaveHubLog() (tea.Model, tea.Cmd) {
	m.closeHubLog()
	m.hubLogVerb = ""
	m.view = viewHubDeployments
	if m.hub != nil && m.hub.curApp != "" {
		return m, m.fetchHubDeps(m.hub.curApp)
	}
	return m, nil
}

// ---------------------------------------------------------------- actions

// hubDeploy fires the confirmed deploy/rollback; the acceptance comes back as
// hubActionMsg, which opens the live log of the queued deployment.
func (m *Model) hubDeploy(verb string) tea.Cmd {
	h := m.hub
	if h == nil {
		return nil
	}
	app, ok := h.cursorApp()
	if !ok {
		return m.toast("no app selected", sevWarn)
	}
	if verb == "rollback" && app.LastGoodDeployment == "" {
		return m.toast(app.Name+": no known-good deployment to roll back to", sevWarn)
	}
	t := msgTag{hostID: hubHostID, gen: h.gen}
	return func() tea.Msg {
		var id string
		var err error
		if verb == "rollback" {
			id, err = h.client.Rollback(h.ctx, app.ID)
		} else {
			id, err = h.client.Deploy(h.ctx, app.ID)
		}
		return hubActionMsg{msgTag: t, verb: verb, appID: app.ID, appName: app.Name, depID: id, err: err}
	}
}

// ---------------------------------------------------------------- routing

func (m *Model) routeHub(msg hubMsg) (tea.Model, tea.Cmd) {
	t := msg.hubTag()
	if m.hub == nil || t.hostID != hubHostID || t.gen != m.hub.gen {
		return m, nil // stale hub generation — dropped on reconnect
	}
	switch msg := msg.(type) {
	case hubFleetMsg:
		if msg.err != nil {
			m.hub.fleetErr = msg.err.Error()
			return m, nil
		}
		m.hub.fleetErr = ""
		m.hub.fleetLoaded = true
		m.hub.servers = msg.servers
		if m.hub.showContainers && m.hub.serverIndex(m.hub.expandedServer) < 0 {
			m.hub.showContainers = false // the expanded server went away
		}
		m.hub.refreshFleetRows(m.tags, m.hubTagHost)
		return m, nil

	case hubAlertMsg:
		m.hub.alerts[msg.frame.RuleID] = alertInfo{
			firing: msg.frame.State == "firing", detail: msg.frame.Detail,
		}
		m.hub.refreshFleetRows(m.tags, m.hubTagHost)
		return m, nil

	case hubAppsMsg:
		if msg.err != nil {
			m.hub.appsErr = msg.err.Error()
			return m, nil
		}
		m.hub.appsErr = ""
		m.hub.appsLoaded = true
		m.hub.apps = msg.apps
		m.hub.refreshAppsRows()
		return m, nil

	case hubMetaMsg:
		if len(msg.envNames) > 0 {
			m.hub.envNames = msg.envNames
			m.hub.refreshAppsRows()
		}
		return m, nil

	case hubDepsMsg:
		if msg.appID != m.hub.curApp {
			return m, nil // a deployments view the user already left
		}
		if msg.err != nil {
			m.hub.depsErr = msg.err.Error()
			return m, nil
		}
		m.hub.depsErr = ""
		m.hub.deps = msg.deps
		m.hub.refreshDepsRows()
		return m, nil

	case hubDepMsg:
		return m.onHubDep(msg)

	case hubActionMsg:
		if msg.err != nil {
			return m, m.toast(msg.verb+" failed: "+msg.err.Error(), sevError)
		}
		// The action carries its app context: deployments opened straight from
		// the apps view must know whose history they show.
		m.hub.curApp, m.hub.curAppName = msg.appID, msg.appName
		dep := apitypes.Deployment{ID: msg.depID, AppID: msg.appID, Status: apitypes.DepQueued}
		if msg.verb == "rollback" {
			dep.Trigger = apitypes.TriggerRollback
		} else {
			dep.Trigger = apitypes.TriggerManual
		}
		dep.CreatedAt = time.Now()
		// Pre-seed the list so the deployments screen shows the queued run
		// even before the next fetch.
		m.hub.deps = append([]apitypes.Deployment{dep}, m.hub.deps...)
		m.hub.refreshDepsRows()
		return m, tea.Batch(
			m.toast(msg.verb+" queued for "+m.hub.curAppName, sevInfo),
			m.openHubLog(msg.verb, dep),
		)

	case hubLogChunkMsg:
		if m.hubLog != nil && m.hubLog.id == msg.depID {
			m.hubLog.Append(msg.chunk)
		}
		return m, nil

	case hubLogEndMsg:
		if m.hubLog == nil || m.hubLog.id != msg.depID {
			return m, nil // the view moved on; the old pump's verdict is moot
		}
		m.hubLog.StreamDone(msg.err)
		return m, m.fetchHubDep(msg.depID)
	}
	return m, nil
}

// onHubDep lands the terminal status of a watched deployment: the cached rows
// update and the operator gets one toast naming the outcome.
func (m *Model) onHubDep(msg hubDepMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		if m.hubLog != nil && m.hubLog.id == msg.dep.ID {
			m.hubLog.SetNote("status check failed: " + msg.err.Error())
		}
		return m, nil
	}
	for i := range m.hub.deps {
		if m.hub.deps[i].ID == msg.dep.ID {
			m.hub.deps[i] = msg.dep
		}
	}
	m.hub.refreshDepsRows()
	var cmd tea.Cmd
	if m.hubLog != nil && m.hubLog.id == msg.dep.ID {
		m.hubLog.SetNote("status: " + msg.dep.Status)
		switch msg.dep.Status {
		case apitypes.DepSuccess:
			cmd = m.toast(m.hubLogVerbLabel()+" succeeded", sevInfo)
		case apitypes.DepFailed:
			cmd = m.toast(m.hubLogVerbLabel()+" failed", sevError)
		case apitypes.DepCanceled:
			cmd = m.toast(m.hubLogVerbLabel()+" canceled", sevWarn)
		}
	}
	return m, tea.Batch(cmd, m.fetchHubApps())
}

func (m *Model) hubLogVerbLabel() string {
	if m.hubLogVerb == "rollback" {
		return "rollback"
	}
	return "deployment"
}

// routeHubLogFlush services the hub log view's SetContent tick. Same batching
// discipline as the direct-mode logFlushMsg: one viewport repaint per tick.
func (m *Model) routeHubLogFlush(t msgTag) (tea.Model, tea.Cmd) {
	if m.hub == nil || t.gen != m.hub.gen || m.hubLog == nil {
		return m, nil
	}
	m.hubLog.flush()
	if m.hubLog.running || m.hubLog.dirty {
		return m, tickLogs(hubHostID, t.gen)
	}
	return m, nil
}

// ---------------------------------------------------------------- keys

// updateHub handles keys on the hub screens. The shared hub keys (1/2, f, tab,
// esc) run first, then the per-screen ones.
func (m *Model) updateHub(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	key := msg.String()

	if m.hubConfirm != "" {
		switch key {
		case "y", "Y":
			verb := m.hubConfirm
			m.hubConfirm = ""
			return m, m.hubDeploy(verb)
		case "n", "N", "esc":
			m.hubConfirm = ""
			return m, nil
		}
		return m, nil // the confirmation owns every key while it is up
	}

	switch key {
	case "tab":
		return m, m.cycleTab(1)
	case "shift+tab":
		return m, m.cycleTab(-1)
	case "1":
		m.view = viewHubFleet
		return m, m.refreshHubView()
	case "2":
		m.view = viewHubApps
		return m, m.refreshHubView()
	case "f":
		return m, m.refreshHubView()
	case "esc":
		return m.hubEscape()
	}

	h := m.hub
	if h == nil {
		m.view = viewHosts
		return m, nil
	}

	switch m.view {
	case viewHubFleet:
		if key == "enter" && !h.showContainers && len(h.servers) > 0 {
			if i := h.fleetTbl.Cursor(); i >= 0 && i < len(h.servers) {
				h.expandedServer = h.servers[i].ID
				h.showContainers = true
				h.refreshFleetRows(m.tags, m.hubTagHost)
				return m, nil
			}
		}
		if key == "d" || key == "R" {
			return m, nil // deploys need an app context; the fleet has none
		}
		var cmd tea.Cmd
		if h.showContainers {
			h.contTbl, cmd = h.contTbl.Update(msg)
		} else {
			h.fleetTbl, cmd = h.fleetTbl.Update(msg)
		}
		return m, cmd

	case viewHubApps:
		switch key {
		case "d", "R":
			if app, ok := h.cursorApp(); ok {
				m.hubConfirm, m.hubConfirmName = hubConfirmVerb(key), app.Name
			}
			return m, nil
		case "enter":
			if app, ok := h.cursorApp(); ok {
				h.curApp, h.curAppName = app.ID, app.Name
				h.deps = nil
				h.depsErr = ""
				h.refreshDepsRows()
				m.view = viewHubDeployments
				return m, m.fetchHubDeps(app.ID)
			}
			return m, nil
		}
		var cmd tea.Cmd
		h.appsTbl, cmd = h.appsTbl.Update(msg)
		return m, cmd

	case viewHubDeployments:
		switch key {
		case "d", "R":
			if h.curApp != "" {
				m.hubConfirm, m.hubConfirmName = hubConfirmVerb(key), h.curAppName
			}
			return m, nil
		case "enter":
			if d, ok := h.cursorDeployment(); ok {
				return m, m.openHubLog("", d)
			}
			return m, nil
		}
		var cmd tea.Cmd
		h.depsTbl, cmd = h.depsTbl.Update(msg)
		return m, cmd

	case viewHubLogs:
		if key == "esc" || key == "q" {
			return m.leaveHubLog()
		}
		var cmd tea.Cmd
		hl := m.hubLog
		hl, cmd = hl.Update(msg)
		m.hubLog = hl
		if _, changed := hl.TakeWrapToggle(); changed {
			m.cfg.LogWrap = hl.wrap
			return m, tea.Batch(cmd, persistCmd("saving settings", func() error {
				return config.SaveSettings(m.settingsSnapshot(hl.wrap))
			}))
		}
		return m, cmd
	}
	return m, nil
}

// hubConfirmVerb maps the confirm key to the action name the confirm line and
// the daemon call both use.
func hubConfirmVerb(key string) string {
	if key == "R" {
		return "rollback"
	}
	return "deploy"
}

// hubEscape steps back up the hub chain: log → deployments → apps → fleet →
// host list, with the fleet's expanded container list one more step inside.
func (m *Model) hubEscape() (tea.Model, tea.Cmd) {
	switch m.view {
	case viewHubLogs:
		return m.leaveHubLog()
	case viewHubDeployments:
		m.view = viewHubApps
	case viewHubApps:
		m.view = viewHubFleet
	case viewHubFleet:
		if m.hub != nil && m.hub.showContainers {
			m.hub.showContainers = false
			m.hub.refreshFleetRows(m.tags, m.hubTagHost)
			return m, nil
		}
		m.view = viewHosts
		m.status = ""
	}
	return m, nil
}

// hubTagHost maps a hub server name to the local host id whose tags.json
// entries should apply, falling back to the name itself when no configured
// host matches.
func (m *Model) hubTagHost(serverName string) string {
	for _, h := range m.hosts {
		if h.Name == serverName {
			return h.ID
		}
	}
	return serverName
}

// serverIndex finds a cached fleet row by server id, -1 when gone.
func (h *hubState) serverIndex(id string) int {
	for i := range h.servers {
		if h.servers[i].ID == id {
			return i
		}
	}
	return -1
}

// ---------------------------------------------------------------- rendering

func (m *Model) hubBody() string {
	if m.hub == nil {
		m.view = viewHosts
		return ""
	}
	w := m.width
	// Keep the tables fitted even when the view opened between resizes.
	m.hub.SetSize(w, m.height-m.tabRows())
	var b strings.Builder
	var footer []keyHelp
	switch m.view {
	case viewHubFleet:
		b.WriteString(m.hub.renderFleet(w))
		footer = hubFleetRows(m.hub.showContainers)
	case viewHubApps:
		b.WriteString(m.hub.renderApps(w))
		footer = hubAppsRows()
	case viewHubDeployments:
		b.WriteString(m.hub.renderDeps(w))
		footer = hubDepsRows()
	case viewHubLogs:
		if m.hubLog != nil {
			return m.tabBar() + "\n" + m.hubLog.View()
		}
		return m.tabBar()
	}
	if m.hubConfirm != "" {
		b.WriteString("\n" + styleWarn.Render(
			m.hubConfirm+" "+m.hubConfirmName+"? (y/n)") + "\n")
	}
	return m.tabBar() + "\n" + pinFooter(b.String(), helpBar(footer, 5), w, m.height)
}

func (h *hubState) renderFleet(w int) string {
	var b strings.Builder
	b.WriteString(styleTitle.Render("hub — fleet") + " " + styleMuted.Render(h.url))
	if n := h.firingAlerts(""); n > 0 {
		b.WriteString("  " + styleErr.Render(fmt.Sprintf("⚠ %d alert%s firing", n, plural(n))))
	}
	b.WriteString("\n\n")
	switch {
	case h.fleetErr != "":
		b.WriteString(styleErr.Render("fleet failed: "+h.fleetErr) + "\n")
	case !h.fleetLoaded:
		b.WriteString(styleMuted.Render("loading fleet…") + "\n")
	case h.showContainers:
		name := h.expandedServer
		if i := h.serverIndex(h.expandedServer); i >= 0 {
			name = h.servers[i].Name
		}
		if len(h.contTbl.rows) == 0 {
			b.WriteString(styleMuted.Render("no containers on "+name) + "\n")
		} else {
			b.WriteString(styleMuted.Render("containers on "+name+" — esc back") + "\n")
			b.WriteString(h.contTbl.View() + "\n")
		}
	case len(h.servers) == 0:
		b.WriteString(styleMuted.Render("no servers registered on this hub yet") + "\n")
	default:
		b.WriteString(h.fleetTbl.View() + "\n")
	}
	return b.String()
}

func (h *hubState) renderApps(w int) string {
	var b strings.Builder
	b.WriteString(styleTitle.Render("hub — apps") + " " + styleMuted.Render(h.url) + "\n\n")
	switch {
	case h.appsErr != "":
		b.WriteString(styleErr.Render("apps failed: "+h.appsErr) + "\n")
	case !h.appsLoaded:
		b.WriteString(styleMuted.Render("loading apps…") + "\n")
	case len(h.apps) == 0:
		b.WriteString(styleMuted.Render("no apps yet — create one on the hub (GUI or curl), then press f") + "\n")
	default:
		b.WriteString(h.appsTbl.View() + "\n")
	}
	return b.String()
}

func (h *hubState) renderDeps(w int) string {
	var b strings.Builder
	b.WriteString(styleTitle.Render("hub — deployments") + " " +
		styleKey.Render(h.curAppName) + " " + styleMuted.Render(h.url) + "\n\n")
	switch {
	case h.depsErr != "":
		b.WriteString(styleErr.Render("deployments failed: "+h.depsErr) + "\n")
	case len(h.deps) == 0:
		b.WriteString(styleMuted.Render("no deployments yet — press d to deploy") + "\n")
	default:
		b.WriteString(h.depsTbl.View() + "\n")
	}
	return b.String()
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// ---------------------------------------------------------------- help rows

func hubFleetRows(containers bool) []keyHelp {
	if containers {
		return []keyHelp{
			{"j/k", "scroll"}, {"enter/esc", "back"}, {"1", "fleet"}, {"2", "apps"},
			{"f", "refresh"}, {"tab", "sessions"}, {"?", "more"},
		}
	}
	return []keyHelp{
		{"j/k", "servers"}, {"enter", "containers"}, {"1", "fleet"}, {"2", "apps"},
		{"f", "refresh"}, {"tab", "sessions"}, {"?", "more"},
	}
}

func hubAppsRows() []keyHelp {
	return []keyHelp{
		{"j/k", "apps"}, {"enter", "deployments"}, {"d", "deploy"},
		{"R", "rollback"}, {"1/2", "fleet/apps"}, {"f", "refresh"}, {"?", "more"},
	}
}

func hubDepsRows() []keyHelp {
	return []keyHelp{
		{"j/k", "runs"}, {"enter", "live log"}, {"d", "deploy"}, {"R", "rollback"},
		{"f", "refresh"}, {"esc", "back"}, {"?", "more"},
	}
}

// hubFormRows is dispatched from help.go like every other screen's table.
func hubFormRows() []keyHelp {
	return []keyHelp{
		{"up/down", "fields"}, {"enter", "next / connect"}, {"esc", "cancel"},
	}
}
