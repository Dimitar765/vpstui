package app

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"vpstui/internal/models"
)

func host(id string) models.Host {
	return models.Host{ID: id, Name: id, Address: "127.0.0.1", Port: 22, User: "root", AuthType: models.AuthKey}
}

func TestRemoveSessionAdjustsActive(t *testing.T) {
	tests := []struct {
		name       string
		active     int
		remove     int
		wantActive int // index into remaining slice; -1 means none
	}{
		{"remove before active", 2, 0, 1},
		{"remove active last", 2, 2, 1},
		{"remove active first", 0, 0, 0},
		{"remove first with two active later", 1, 0, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := New(nil)
			a := &Session{host: host("a"), gen: 1}
			b := &Session{host: host("b"), gen: 1}
			c := &Session{host: host("c"), gen: 1}
			m.sessions = []*Session{a, b, c}
			m.active = tt.active
			victims := []*Session{a, b, c}[tt.remove]
			wantObj := []*Session{a, b, c}[tt.active]
			m.removeSession(victims)
			if tt.wantActive < 0 {
				if m.active != -1 || len(m.sessions) != 0 {
					t.Fatalf("active=%d len=%d, want -1/0", m.active, len(m.sessions))
				}
				return
			}
			if m.active != tt.wantActive {
				t.Fatalf("active=%d, want %d", m.active, tt.wantActive)
			}
			if victims == wantObj {
				if m.sessions[m.active] == victims {
					t.Fatal("removed session still referenced")
				}
			} else if m.sessions[m.active] != wantObj {
				t.Fatalf("active points at %v, want %v", m.sessions[m.active].host.ID, wantObj.host.ID)
			}
		})
	}
}

func TestSessionSwitchKeysRearmChain(t *testing.T) {
	m := New(nil)
	a := &Session{host: host("a"), gen: 1}
	a.dash = NewDashboard(a.host, nil, 80, 24, a.gen)
	b := &Session{host: host("b"), gen: 1}
	b.dash = NewDashboard(b.host, nil, 80, 24, b.gen)
	m.sessions = []*Session{a, b}
	m.active = 0
	m.view = viewDashboard

	for _, key := range []tea.KeyType{tea.KeyTab, tea.KeyShiftTab} {
		_, cmd := m.Update(tea.KeyMsg{Type: key})
		if cmd == nil {
			t.Fatalf("key %v: activate returned nil cmd, tick chain not re-armed", key)
		}
	}
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'2'}})
	if cmd == nil {
		t.Fatal("hotkey switch returned nil cmd")
	}
	if m.active != 1 || m.view != viewDashboard {
		t.Fatalf("after '2': active=%d view=%d, want 1/dashboard", m.active, m.view)
	}
}

func TestPromptCancelDropsOrphanSlot(t *testing.T) {
	m := New(nil)
	h := host("pw")
	s := &Session{host: h, gen: 7}
	m.sessions = []*Session{s}
	m.active = 0
	m.pendingHost, m.pendingGen = h, 7
	m.form = NewSecretPrompt(h, "Password for root@pw")
	m.view = viewPrompt

	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyEscape})
	if len(m.sessions) != 0 {
		t.Fatalf("orphan slot kept: %d sessions", len(m.sessions))
	}
	if m.view != viewHosts || m.form != nil {
		t.Fatalf("view=%d form=%v after cancel", m.view, m.form)
	}
}

func TestStaleTaggedMsgDropped(t *testing.T) {
	m := New(nil)
	s := &Session{host: host("a"), gen: 2}
	s.dash = NewDashboard(s.host, nil, 80, 24, s.gen)
	m.sessions = []*Session{s}
	m.active = 0
	m.view = viewDashboard

	// same host, older generation
	_, cmd := m.Update(statsTickMsg{msgTag: msgTag{hostID: "a", gen: 1}})
	if cmd != nil {
		t.Fatal("stale tick produced a command")
	}
	// same gen, unknown host
	_, cmd = m.Update(statsLoadedMsg{msgTag: msgTag{hostID: "zz", gen: 2}})
	if cmd != nil {
		t.Fatal("unknown-host stats produced a command")
	}
}

func TestBackgroundConnectDoesNotArmTicks(t *testing.T) {
	m := New(nil)
	live := &Session{host: host("live"), gen: 1}
	live.dash = NewDashboard(live.host, nil, 80, 24, live.gen)
	m.sessions = []*Session{live}
	m.active = 0
	m.view = viewDashboard

	h := host("bg")
	dialing := &Session{host: h, gen: 9}
	m.sessions = append(m.sessions, dialing)
	m.pendingHost, m.pendingGen = h, 9 // pending but NOT foreground: view stays dashboard of other host

	// simulate dial success: conn nil in msg would be an error path, so craft msg;
	// onConnectDone with err removes the slot and must not arm a tick chain.
	_, cmd := m.Update(connectDoneMsg{
		msgTag: msgTag{hostID: h.ID, gen: 9},
		err:    errDial,
		host:   h,
	})
	if cmd != nil {
		t.Fatal("background connect failure armed a command")
	}
	if len(m.sessions) != 1 {
		t.Fatalf("failed bg slot not removed: %d", len(m.sessions))
	}
	if m.active != 0 || m.sessions[0] != live {
		t.Fatalf("active index wrong after removal: active=%d", m.active)
	}
}

var errDial = &dialErr{}

type dialErr struct{}

func (d *dialErr) Error() string { return "dial tcp 127.0.0.1:1: connect: refused" }

func TestDashboardGroupingAndFold(t *testing.T) {
	mk := func(name, proj string) models.Container {
		c := models.Container{ID: name, Names: name, Image: "img", State: "running", Status: "Up"}
		if proj != "" {
			c.Labels = "com.docker.compose.project=" + proj
		}
		return c
	}
	d := NewDashboard(host("h"), nil, 80, 24, 1)
	d.containers = []models.Container{
		mk("solo1", ""),
		mk("web-a", "web"),
		mk("db-a", "db"),
		mk("web-b", "web"),
	}
	d.rebuild()

	// rows: solo1, hdr(db), db-a, hdr(web), web-a, web-b
	if len(d.rowCIdx) != 6 {
		t.Fatalf("rows=%d, want 6 (%v)", len(d.rowCIdx), d.rowCIdx)
	}
	if d.rowCIdx[0] != 0 || d.rowCIdx[1] != -1 || d.rowCIdx[3] != -1 {
		t.Fatalf("row map wrong: %v", d.rowCIdx)
	}
	// projects sorted: db before web
	if d.rowProj[1] != "db" || d.rowProj[3] != "web" {
		t.Fatalf("group order: %v", d.rowProj)
	}

	// cursor on header of web (row 3), fold it
	d.tbl.SetCursor(3)
	d.toggleFold()
	if !d.collapsed["web"] {
		t.Fatal("web should be collapsed")
	}
	if len(d.rowCIdx) != 4 {
		t.Fatalf("rows after fold=%d, want 4", len(d.rowCIdx))
	}
	// selected() on a header row must fail
	d.tbl.SetCursor(1)
	if _, ok := d.selected(); ok {
		t.Fatal("header row must not be selectable")
	}
	// unfold again: cursor back onto the folded web header (row 3)
	d.tbl.SetCursor(3)
	d.toggleFold()
	if len(d.rowCIdx) != 6 {
		t.Fatalf("rows after unfold=%d, want 6", len(d.rowCIdx))
	}
	// standalone row: fold is a no-op
	d.tbl.SetCursor(0)
	d.toggleFold()
	if len(d.rowCIdx) != 6 {
		t.Fatal("standalone fold should change nothing")
	}
}

func TestSpaceKeyFoldsGroup(t *testing.T) {
	d := NewDashboard(host("h"), nil, 80, 24, 1)
	d.containers = []models.Container{
		{ID: "x", Names: "x", Labels: "com.docker.compose.project=web", State: "running"},
	}
	d.rebuild()
	d.tbl.SetCursor(0) // compose:web header
	_, _ = d.Update(tea.KeyMsg{Type: tea.KeySpace})
	if !d.collapsed["web"] {
		t.Fatalf("space did not fold: %v", d.collapsed)
	}
	if len(d.rowCIdx) != 1 {
		t.Fatalf("rows after fold=%d, want 1 (header only)", len(d.rowCIdx))
	}
	_, _ = d.Update(tea.KeyMsg{Type: tea.KeySpace})
	if d.collapsed["web"] || len(d.rowCIdx) != 2 {
		t.Fatalf("space did not unfold: %v rows=%d", d.collapsed, len(d.rowCIdx))
	}
}

func key(k string) tea.KeyMsg {
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)}
}

func TestDiskPanelOpenFillAndClose(t *testing.T) {
	d := NewDashboard(host("h"), nil, 80, 24, 1)
	_, cmd := d.Update(key("i"))
	if !d.showDisk || !d.diskLoading || cmd == nil {
		t.Fatalf("i: showDisk=%v loading=%v cmd=%v", d.showDisk, d.diskLoading, cmd != nil)
	}
	d.Update(diskLoadedMsg{msgTag: msgTag{hostID: "h", gen: 1}, objs: []models.DiskObject{
		{Type: "Images", Total: "7", Active: "4", Size: "2.5GB", Reclaimable: "500MB (20%)"},
	}})
	if d.diskLoading || len(d.disk) != 1 || d.diskErr != "" {
		t.Fatalf("after load: loading=%v disk=%+v err=%q", d.diskLoading, d.disk, d.diskErr)
	}
	v := d.View()
	if !strings.Contains(v, "RECLAIMABLE") || !strings.Contains(v, "2.5GB") {
		t.Fatalf("View missing panel data:\n%s", v)
	}
	// esc closes the panel, not the dashboard
	d.Update(tea.KeyMsg{Type: tea.KeyEscape})
	if d.showDisk || d.TakeBack() {
		t.Fatalf("esc: showDisk=%v back=%v", d.showDisk, d.back)
	}
}

func TestPruneConfirmNeedsNoSelection(t *testing.T) {
	d := NewDashboard(host("h"), nil, 80, 24, 1) // zero containers
	d.Update(key("i"))
	for _, k := range []struct {
		key  string
		verb string
	}{{"p", "prune images"}, {"P", "prune system"}} {
		d.Update(key(k.key))
		if d.confirm != k.verb {
			t.Fatalf("%s: confirm=%q want %q", k.key, d.confirm, k.verb)
		}
		_, cmd := d.Update(key("y"))
		if cmd == nil {
			t.Fatalf("%s: y produced nil cmd (prune blocked by empty selection?)", k.key)
		}
		if d.confirm != "" {
			t.Fatalf("confirm not cleared")
		}
		// View must render the prompt without a selected container
		d.Update(key(k.key))
		if v := d.View(); !strings.Contains(v, k.verb+"? [y/N]") {
			t.Fatalf("confirm line missing in view:\n%s", v)
		}
		d.Update(key("n"))
		if d.confirm != "" {
			t.Fatal("n did not cancel confirm")
		}
	}
}

func TestRmKeyGating(t *testing.T) {
	mk := func(state string) *Dashboard {
		d := NewDashboard(host("h"), nil, 80, 24, 1)
		d.containers = []models.Container{{ID: "c1", Names: "c1", State: state, Status: state}}
		d.rebuild()
		d.tbl.SetCursor(0)
		return d
	}
	run := mk("running")
	run.Update(key("D"))
	if run.confirm != "" {
		t.Fatal("D on running container must not arm confirm")
	}
	if !strings.Contains(run.toast, "only stopped") || !run.toastIsErr {
		t.Fatalf("toast=%q isErr=%v", run.toast, run.toastIsErr)
	}
	stopped := mk("exited")
	stopped.Update(key("D"))
	if stopped.confirm != "rm" {
		t.Fatalf("confirm=%q, want rm", stopped.confirm)
	}
	_, cmd := stopped.Update(key("y"))
	if cmd == nil {
		t.Fatal("rm y produced nil cmd")
	}
}

func TestStaleDiskMsgDropped(t *testing.T) {
	m := New(nil)
	s := &Session{host: host("a"), gen: 2}
	s.dash = NewDashboard(s.host, nil, 80, 24, s.gen)
	m.sessions = []*Session{s}
	m.active = 0
	m.view = viewDashboard

	_, cmd := m.Update(diskLoadedMsg{msgTag: msgTag{hostID: "a", gen: 1}, objs: []models.DiskObject{{Type: "Images"}}})
	if cmd != nil {
		t.Fatal("stale disk msg produced a command")
	}
	if s.dash.disk != nil {
		t.Fatalf("stale disk msg applied: %+v", s.dash.disk)
	}
}

func TestActionDoneBatchRefetchesDisk(t *testing.T) {
	d := NewDashboard(host("h"), nil, 80, 24, 1)
	d.showDisk = true
	_, cmd := d.Update(actionDoneMsg{msgTag: msgTag{hostID: "h", gen: 1}, verb: "prune images", err: nil})
	if cmd == nil {
		t.Fatal("prune success produced no refresh cmd")
	}
	if !strings.Contains(d.toast, "prune images ok") {
		t.Fatalf("toast=%q", d.toast)
	}
}
