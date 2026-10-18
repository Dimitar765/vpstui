package app

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"vpstui/internal/config"
	"vpstui/internal/docker"
	"vpstui/internal/models"
)

// mkC1Dash builds a dashboard with the given containers already fetched.
func mkC1Dash(cs ...models.Container) *Dashboard {
	d := NewDashboard(host("h"), nil, 100, 30, 1)
	d.containers = cs
	d.rebuild()
	return d
}

func composeC(name, proj string) models.Container {
	c := models.Container{ID: name, Names: name, Image: "img:" + name, State: "running", Status: "Up"}
	if proj != "" {
		c.Labels = "com.docker.compose.project=" + proj
	}
	return c
}

// --- item 3: filter applies before grouping/sorting ---

func TestFilterBeforeGroupOrdering(t *testing.T) {
	d := mkC1Dash(
		composeC("solo", ""),
		composeC("web-a", "web"),
		composeC("db-a", "db"),
		composeC("web-b", "web"),
	)
	// Type the filter through the real input path: / opens, runes filter live,
	// enter commits and closes the input.
	_, _ = d.Update(key("/"))
	if !d.Filtering() {
		t.Fatal("/ did not open the filter input")
	}
	for _, r := range "web" {
		_, _ = d.Update(key(string(r)))
	}
	_, _ = d.Update(key("enter"))
	if d.Filtering() || d.filter != "web" {
		t.Fatalf("filter state after enter: filtering=%v filter=%q", d.Filtering(), d.filter)
	}
	// Rows: header(web), web-a, web-b. The db group and the standalone row are
	// gone — a group header hides with its children.
	if len(d.rowCIdx) != 3 {
		t.Fatalf("rows=%d (%v), want 3", len(d.rowCIdx), d.rowProj)
	}
	if d.rowCIdx[0] != -1 || d.rowProj[0] != "web" {
		t.Fatalf("first row not the web header: cIdx=%v proj=%v", d.rowCIdx, d.rowProj)
	}
	v := d.View()
	if strings.Contains(v, "compose:db") || strings.Contains(v, "solo") {
		t.Error("filtered-out rows still rendered")
	}
	if !strings.Contains(v, "filter: web") {
		t.Error("status line missing filter")
	}
	// esc clears the filter and restores the full table, then leaves the screen.
	_, _ = d.Update(tea.KeyMsg{Type: tea.KeyEscape})
	if d.filter != "" || len(d.rowCIdx) != 6 {
		t.Fatalf("esc did not clear: filter=%q rows=%d", d.filter, len(d.rowCIdx))
	}
	if d.TakeBack() {
		t.Error("first esc must only clear the filter, not leave the screen")
	}
	// No-match empty state.
	_, _ = d.Update(key("/"))
	for _, r := range "zzz" {
		_, _ = d.Update(key(string(r)))
	}
	_, _ = d.Update(key("enter"))
	if v := d.View(); !strings.Contains(v, "nothing matches 'zzz' — esc clears") {
		t.Errorf("no-match empty state missing:\n%s", v)
	}
}

// --- item 4: multi-select mark / clear / fan-out ---

func TestMultiSelectFanOutOneConfirmation(t *testing.T) {
	d := mkC1Dash(
		composeC("a", ""),
		composeC("b", ""),
		composeC("c", ""),
	)
	d.tbl.SetCursor(0)
	_, _ = d.Update(key("v"))
	d.tbl.SetCursor(2)
	_, _ = d.Update(key("v"))
	if len(d.marked) != 2 {
		t.Fatalf("marked=%v, want a and c", d.marked)
	}
	if row := d.containerRow(d.containers[0]); !strings.HasPrefix(row[0], "* ") {
		t.Errorf("marked row missing * gutter: %q", row[0])
	}
	// x stops the marked set: ONE confirmation naming the count.
	_, _ = d.Update(key("x"))
	if d.confirm != "stop" {
		t.Fatalf("confirm=%q", d.confirm)
	}
	if v := d.View(); !strings.Contains(v, "stop 2 marked containers") {
		t.Errorf("confirmation does not name the marked count:\n%s", v)
	}
	_, cmd := d.Update(key("y"))
	if cmd == nil {
		t.Fatal("y returned no command")
	}
	msg := cmd()
	batch, ok := msg.(tea.BatchMsg)
	if !ok {
		t.Fatalf("expected a batch of per-container commands, got %T", msg)
	}
	if len(batch) != 2 {
		t.Fatalf("fan-out ran %d commands, want one per marked container", len(batch))
	}
	// D stays single-row even with marks present.
	d2 := mkC1Dash(composeC("a", ""), func() models.Container { c := composeC("b", ""); c.State = "exited"; return c }())
	d2.marked[d2.containers[0].ID] = true
	d2.tbl.SetCursor(1)
	_, _ = d2.Update(key("D"))
	if d2.confirm != "rm" {
		t.Fatalf("D confirm=%q, want rm", d2.confirm)
	}
	if v := d2.View(); strings.Contains(v, "marked containers") {
		t.Errorf("D must stay single-row: %s", "confirmation batched")
	}
	// V clears everything.
	_, _ = d.Update(key("V"))
	if len(d.marked) != 0 {
		t.Fatalf("V left marks behind: %v", d.marked)
	}
}

// --- item 2: palette fuzzy match + action dispatch ---

func c1Model(t *testing.T) *Model {
	t.Helper()
	m := New([]models.Host{host("alpha")}, nil)
	s := &Session{host: host("alpha"), gen: 1}
	s.dock = docker.New(nil)
	s.dash = NewDashboard(s.host, nil, 100, 30, s.gen)
	s.dash.containers = []models.Container{composeC("nginx-1", "web"), composeC("pg-1", "db")}
	s.dash.rebuild()
	m.sessions = []*Session{s}
	m.active = 0
	m.view = viewDashboard
	upd, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	return upd.(*Model)
}

// runAll executes a command the way the bubbletea runtime would: a BatchMsg's
// sub-commands are run one by one, not by calling the batch func alone.
func runAll(t *testing.T, cmd tea.Cmd) {
	t.Helper()
	if cmd == nil {
		return
	}
	switch msg := cmd().(type) {
	case tea.BatchMsg:
		for _, c := range msg {
			runAll(t, c)
		}
	}
}

func TestPaletteFuzzyMatchAndDispatch(t *testing.T) {
	m := c1Model(t)
	if _, cmd := m.Update(key(":")); !m.pal.open || cmd == nil {
		t.Fatal(": did not open the palette")
	}
	// "ngi" fuzzy-matches the logs verb for the running container.
	for _, r := range "ngi" {
		_, _ = m.Update(key(string(r)))
	}
	if len(m.pal.matches) == 0 {
		t.Fatalf("no matches for 'ngi': %v", m.pal.matches)
	}
	if m.pal.matches[0].label != "logs nginx-1" {
		t.Fatalf("top match %q, want logs nginx-1", m.pal.matches[0].label)
	}
	_, cmd := m.Update(key("enter"))
	if m.pal.open {
		t.Error("enter left the palette open")
	}
	if m.view != viewContainer {
		t.Fatalf("logs verb landed on view %v, want viewContainer", m.view)
	}
	if cmd == nil {
		t.Error("logs verb did not start the stream")
	}
	if m.sessions[0].cv == nil || m.sessions[0].cv.name != "nginx-1" {
		t.Fatalf("log view not opened on nginx-1: %+v", m.sessions[0].cv)
	}

	// sort name dispatches onto the active dashboard.
	m2 := c1Model(t)
	_, _ = m2.Update(key(":"))
	for _, r := range "sort na" {
		_, _ = m2.Update(key(string(r)))
	}
	if len(m2.pal.matches) == 0 || m2.pal.matches[0].label != "sort name" {
		t.Fatalf("sort matches: %+v", m2.pal.matches)
	}
	_, _ = m2.Update(key("enter"))
	if got := m2.sessions[0].dash.sortMode; got != sortName {
		t.Fatalf("sort verb set mode %d, want sortName", got)
	}

	// esc closes without running anything.
	m3 := c1Model(t)
	_, _ = m3.Update(key(":"))
	_, _ = m3.Update(tea.KeyMsg{Type: tea.KeyEscape})
	if m3.pal.open || m3.view != viewDashboard {
		t.Fatalf("esc: open=%v view=%v", m3.pal.open, m3.view)
	}
	// While open, the palette owns typing: session hotkeys must not fire.
	m4 := c1Model(t)
	_, _ = m4.Update(key(":"))
	_, _ = m4.Update(key("2"))
	if !m4.pal.open {
		t.Error("session hotkey stole keys from the open palette")
	}
}

// --- item 6: toast queue ttl / severity ---

func TestToastQueueExpiryAndCap(t *testing.T) {
	m := New(nil, nil)
	cmd := m.toast("first", sevInfo)
	if len(m.toasts) != 1 || m.toasts[0].sev != sevInfo {
		t.Fatalf("toast not queued: %+v", m.toasts)
	}
	if cmd == nil {
		t.Fatal("toast did not schedule an expiry tick")
	}
	m.toast("bad", sevError)
	m.toast("warn", sevWarn)
	if len(m.toasts) != 3 {
		t.Fatalf("queue holds %d, want 3", len(m.toasts))
	}
	// Expiry is per-toast: firing one id drops only that entry.
	_, _ = m.Update(toastExpireMsg{id: m.toasts[0].id})
	if len(m.toasts) != 2 || m.toasts[0].msg == "first" {
		t.Fatalf("expiry dropped the wrong toast: %+v", m.toasts)
	}
	// The queue is capped and keeps the newest entries.
	for range toastMax + 2 {
		m.toast("n", sevInfo)
	}
	if len(m.toasts) > toastMax {
		t.Fatalf("queue grew to %d, cap is %d", len(m.toasts), toastMax)
	}
	// Severity picks the colour.
	if toastStyle(sevError).Render("x") == toastStyle(sevInfo).Render("x") {
		t.Error("error and info toasts render identically")
	}
	if toastStyle(sevWarn).Render("x") == toastStyle(sevInfo).Render("x") {
		t.Error("warn and info toasts render identically")
	}
}

// --- item 1: helpRows covers every screen that renders a help bar ---

func TestHelpRowsCoverEveryHelpBarScreen(t *testing.T) {
	// Host list.
	m := New([]models.Host{host("alpha")}, nil)
	if rows := m.helpRows(); len(rows) == 0 {
		t.Error("host list helpRows empty")
	} else if v := m.View(); !strings.Contains(v, "?") {
		t.Error("host list bar missing ? more hint")
	}
	// Dashboard, each sub-mode.
	dash := func(mut func(d *Dashboard)) (rows []keyHelp, view string) {
		d := mkC1Dash(composeC("a", ""))
		if mut != nil {
			mut(d)
		}
		return d.helpRows(), d.View()
	}
	for name, mut := range map[string]func(d *Dashboard){
		"main":   nil,
		"perf":   func(d *Dashboard) { d.showPerf = true },
		"disk":   func(d *Dashboard) { d.showDisk = true },
		"images": func(d *Dashboard) { d.showImages = true },
		"detail": func(d *Dashboard) { d.detail = &detailState{name: "a"} },
	} {
		rows, view := dash(mut)
		if len(rows) == 0 {
			t.Errorf("%s helpRows empty", name)
		}
		if !strings.Contains(view, "more") {
			t.Errorf("%s help bar missing ? more", name)
		}
	}
	// Log view, filter input state included.
	cv := NewContainerView("id1", "a", 80, 24)
	if len(cv.helpRows()) == 0 {
		t.Error("log view helpRows empty")
	}
	cv.searching = true
	if len(cv.helpRows()) == 0 {
		t.Error("searching helpRows empty")
	}
	// Errors screen, expanded and not.
	e := NewErrorsView(80, 24)
	if len(e.helpRows()) == 0 {
		t.Error("errors helpRows empty")
	}
	e.expanded = 0
	if len(e.helpRows()) == 0 {
		t.Error("expanded errors helpRows empty")
	}
	// Host form.
	f := NewHostForm(models.Host{}, true)
	if len(hostFormRows(false)) == 0 || len(hostFormRows(true)) == 0 {
		t.Error("host form helpRows empty")
	}
	if f.Help() == "" {
		t.Error("host form help bar empty")
	}
	// The overlay opens with ? and any key closes it.
	m2 := c1Model(t)
	_, _ = m2.Update(key("?"))
	if !m2.helpOpen {
		t.Fatal("? did not open the overlay")
	}
	if v := m2.View(); !strings.Contains(v, "keys — dashboard") {
		t.Errorf("overlay not rendered:\n%s", v[:min(len(v), 400)])
	}
	_, _ = m2.Update(key("esc"))
	if m2.helpOpen {
		t.Error("esc did not close the overlay")
	}
}

// --- item 5: mouse ---

func TestMouseTableAndTabs(t *testing.T) {
	m := c1Model(t)
	d := m.sessions[0].dash
	// A click on the third data row (header at headerLines, rows start +1).
	d.Click(0, d.headerLines()+3)
	if got := d.tbl.Cursor(); got != 2 {
		t.Fatalf("click selected row %d, want 2", got)
	}
	// A click on the State header selects that sort; clicking a non-sorted
	// column (the image cell here) cycles.
	d.sortMode = sortGrouped
	d.Click(40, d.headerLines()) // x=40 lands in the State column at width 100
	if d.sortMode != sortState {
		t.Fatalf("header click set sort %d, want sortState", d.sortMode)
	}
	d.Click(22, d.headerLines())
	if d.sortMode != (sortState+1)%sortCount {
		t.Fatalf("non-sort column click set sort %d, want a cycle", d.sortMode)
	}
	// Tab-bar click switches sessions.
	b := &Session{host: host("beta"), gen: 1}
	b.dash = NewDashboard(b.host, nil, 100, 30, b.gen)
	m.sessions = append(m.sessions, b)
	if got := m.tabIndexAt(0); got != 0 {
		t.Fatalf("tabIndexAt(0)=%d, want 0", got)
	}
	if got := m.tabIndexAt(15); got != 1 {
		t.Fatalf("tabIndexAt(15)=%d, want 1", got)
	}
	_, cmd := m.mouseEvent(tea.MouseMsg{Button: tea.MouseButtonLeft, Action: tea.MouseActionPress, Y: 0, X: 15})
	if m.active != 1 || cmd == nil {
		t.Fatalf("tab click: active=%d cmd=%v", m.active, cmd != nil)
	}
	// Wheel translates to j/k.
	m2 := c1Model(t)
	_, _ = m2.mouseEvent(tea.MouseMsg{Button: tea.MouseButtonWheelDown, Action: tea.MouseActionPress})
	if got := m2.sessions[0].dash.tbl.Cursor(); got != 1 {
		t.Fatalf("wheel-down cursor=%d, want 1", got)
	}
}

// --- items 8+10: settings screen save and wrap toggle persist ---

func TestSettingsScreenSaves(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("VPSTUI_LOG_TAIL", "")
	t.Setenv("VPSTUI_ERR_TAIL", "")
	t.Setenv("VPSTUI_ERR_SINCE", "")
	t.Setenv("VPSTUI_HTTP_ADDR", "")

	m := New([]models.Host{host("alpha")}, nil)
	_, _ = m.Update(key("S"))
	if m.view != viewSettings || m.settingsForm == nil {
		t.Fatalf("S did not open settings: view=%v", m.view)
	}
	// Walk to the theme field (first) and pick light with →.
	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyRight})
	// Submit from the last field through the model, so the live-apply path runs.
	sf := m.settingsForm
	sf.focus = len(sf.kinds()) - 1
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("submit returned no persist command")
	}
	runAll(t, cmd)
	out, err := config.LoadSettings()
	if err != nil {
		t.Fatal(err)
	}
	if out.Theme != "light" {
		t.Fatalf("saved theme %q, want light", out.Theme)
	}
	if m.cfg.Theme != "light" || m.view != viewHosts {
		t.Fatalf("theme not applied live: %+v view=%v", m.cfg, m.view)
	}
	// esc cancels without saving.
	_, _ = m.Update(key("S"))
	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyEscape})
	if m.view != viewHosts || m.settingsForm != nil {
		t.Fatalf("esc: view=%v form=%v", m.view, m.settingsForm)
	}
}

func TestLogWrapTogglePersists(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("VPSTUI_LOG_TAIL", "")
	t.Setenv("VPSTUI_ERR_TAIL", "")
	t.Setenv("VPSTUI_ERR_SINCE", "")
	t.Setenv("VPSTUI_HTTP_ADDR", "")

	m := c1Model(t)
	s := m.sessions[0]
	s.cv = NewContainerView("id1", "nginx-1", 80, 24)
	s.cv.hostID, s.cv.gen = s.host.ID, s.gen
	m.view = viewContainer
	m.cfg.LogWrap = false

	_, cmd := m.Update(key("w"))
	if !s.cv.wrap || !m.cfg.LogWrap {
		t.Fatalf("w did not toggle wrap on: wrap=%v cfg=%v", s.cv.wrap, m.cfg.LogWrap)
	}
	if cmd == nil {
		t.Fatal("wrap toggle returned no persist command")
	}
	runAll(t, cmd)
	out, err := config.LoadSettings()
	if err != nil {
		t.Fatal(err)
	}
	if !out.LogWrap {
		t.Fatalf("logWrap not persisted: %+v", out)
	}
	// A fresh log view honours the setting.
	m.view = viewDashboard
	s.dash.logReq = &containerReq{id: "id1", name: "nginx-1"}
	m.Update(key("enter"))
	if !s.cv.wrap {
		t.Error("new log view did not inherit logWrap")
	}
}
