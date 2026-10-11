package app

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"vpstui/internal/docker"
	"vpstui/internal/models"
	"vpstui/internal/sshclient"
)

// errSweptSession is a connected session that already returned a sweep. Neither
// the Conn nor the Executor is ever dialed: SweepErrors only checks that a dock
// exists, and a test never runs the command closure it returns.
func errSweptSession(id string, lines ...ErrLine) *Session {
	s := &Session{host: host(id), gen: 1}
	s.conn = &sshclient.Conn{}
	s.dock = docker.New(s.conn)
	s.dash = NewDashboard(s.host, s.dock, 80, 30, s.gen)
	s.dash.errCorpus = lines
	s.dash.errFetchedAt = time.Now()
	return s
}

func TestErrorsViewFrames(t *testing.T) {
	f := errFixture()
	// The fixture is ordered by host: alpha's two lines, then beta's one.
	two := func() []*Session {
		return []*Session{
			errSweptSession("alpha", f[:2]...),
			errSweptSession("beta", f[2:]...),
		}
	}
	// pending adds a third connected host that has not been swept yet.
	pending := func() []*Session {
		g := &Session{host: host("gamma"), gen: 1}
		g.conn = &sshclient.Conn{}
		g.dash = NewDashboard(g.host, nil, 80, 30, 1)
		return append(two(), g)
	}

	cases := []struct {
		name string
		sess func() []*Session
		n    int
		mut  func(*Dashboard)
		keys []string
		note string
		want []string
	}{
		{
			name: "grouped", sess: two, n: 2,
			want: []string{"errors", "2 of 2 hosts swept", "SIGNATURE", "db down", "· grouped"},
		},
		{
			name: "flat", sess: two, n: 2, keys: []string{"m"},
			want: []string{"MESSAGE", "WARN db down", "flat feed"},
		},
		{
			name: "expanded", sess: two, n: 2, keys: []string{"enter"},
			want: []string{"2× · 1 host(s) · 1 container(s)", "alpha/web ERROR db down"},
		},
		{
			name: "errors only", sess: two, n: 2, keys: []string{"w"},
			want: []string{"errors only — w brings warnings back", "2 lines · 2 signatures"},
		},
		{
			name: "sweeping", sess: two, n: 2,
			mut:  func(d *Dashboard) { d.errLoading = true },
			want: []string{"sweeping"},
		},
		{
			name: "sweep failed", sess: two, n: 2,
			mut:  func(d *Dashboard) { d.errProblem = "sh: awk: command not found" },
			want: []string{"sweep failed — alpha: sh: awk: command not found"},
		},
		{
			name: "truncated", sess: two, n: 2,
			mut:  func(d *Dashboard) { d.errTruncated = true },
			want: []string{"older lines were dropped"},
		},
		{
			name: "note", sess: two, n: 2,
			note: "connect gamma first to follow its logs",
			want: []string{"connect gamma first to follow its logs"},
		},
		{
			name: "pending", sess: pending, n: 3,
			want: []string{"2 of 3 hosts swept · 1 connected, pending"},
		},
		{
			name: "nothing swept", n: 3,
			sess: func() []*Session { return []*Session{{host: host("alpha"), gen: 1}} },
			want: []string{"0 of 3 hosts swept", "nothing swept yet"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sess := tc.sess()
			if tc.mut != nil {
				tc.mut(sess[0].dash)
			}
			e := NewErrorsView(80, 30)
			e.Refresh(sess, tc.n)
			for _, k := range tc.keys {
				e, _ = e.Update(key(k))
			}
			if tc.note != "" {
				e.SetNote(tc.note)
			}
			v := e.View()
			if got := h(v); got != 30 {
				t.Errorf("height = %d, want 30\n%s", got, v)
			}
			if got := lipgloss.Width(v); got > 80 {
				t.Errorf("width = %d, want ≤ 80", got)
			}
			// A help bar that wraps is what silently breaks a pinned footer.
			if got := h(e.help()); got != 1 {
				t.Errorf("help bar is %d rows, want 1", got)
			}
			if last := lastLine(v); !strings.Contains(last, "esc") || !strings.Contains(last, "back") {
				t.Errorf("footer not pinned as the last row: %q", last)
			}
			for _, w := range tc.want {
				if !strings.Contains(v, w) {
					t.Errorf("frame is missing %q\n%s", w, v)
				}
			}
		})
	}
}

// TestErrorsColumnBudget pins the arithmetic the frame contract depends on: the
// table must never be wider than the area the screen gave it. Below these
// widths the fixed columns keep their minimum readable size, exactly like
// colsFor does for the dashboard's seven.
func TestErrorsColumnBudget(t *testing.T) {
	f := errFixture()
	for _, grouped := range []bool{true, false} {
		for _, w := range []int{72, 80, 100, 140, 200} {
			e := NewErrorsView(w, 30)
			e.grouped = grouped
			e.Refresh([]*Session{errSweptSession("alpha", f...)}, 1)
			if got, want := e.tbl.totalWidth(), w-2; got > want {
				t.Errorf("grouped=%v width=%d: table is %d wide, want ≤ %d", grouped, w, got, want)
			}
			adapt := e.tbl.cols[len(e.tbl.cols)-1].width
			if want := map[bool]int{true: 16, false: 14}[grouped]; adapt < want {
				t.Errorf("grouped=%v width=%d: adaptive column is %d, want ≥ %d", grouped, w, adapt, want)
			}
		}
	}
}

func TestErrorsModeToggles(t *testing.T) {
	f := errFixture()
	e := NewErrorsView(80, 30)
	e.Refresh([]*Session{errSweptSession("alpha", f...)}, 1)

	if !e.grouped || len(e.rowsGroup) != 2 {
		t.Fatalf("opens grouped: grouped=%v groups=%d", e.grouped, len(e.rowsGroup))
	}
	e, _ = e.Update(key("m"))
	if e.grouped || len(e.flat) != 3 {
		t.Fatalf("m did not switch to the flat feed: grouped=%v flat=%d", e.grouped, len(e.flat))
	}
	// The newest line leads the flat feed.
	if e.flat[0].Text != "ERROR no space left" {
		t.Fatalf("flat[0] = %q, want the newest line", e.flat[0].Text)
	}
	e, _ = e.Update(key("m"))
	if !e.grouped {
		t.Fatal("m did not switch back")
	}
	e, _ = e.Update(key("w"))
	if e.showWarn || e.lineCount != 2 {
		t.Fatalf("w: showWarn=%v lines=%d, want false/2", e.showWarn, e.lineCount)
	}
	e, _ = e.Update(key("w"))
	if !e.showWarn || e.lineCount != 3 {
		t.Fatalf("w back: showWarn=%v lines=%d, want true/3", e.showWarn, e.lineCount)
	}
}

func TestErrorsEscapeLadderCollapsesBeforeLeaving(t *testing.T) {
	f := errFixture()
	e := NewErrorsView(80, 30)
	e.Refresh([]*Session{errSweptSession("alpha", f...)}, 1)

	e, _ = e.Update(key("j")) // cursor 1
	e, _ = e.Update(key("enter"))
	if e.expanded != 1 {
		t.Fatalf("expanded = %d, want 1", e.expanded)
	}
	// Switching mode rebuilds the rows, so a stale pane cannot survive it.
	e, _ = e.Update(key("m"))
	if e.expanded != -1 {
		t.Fatalf("rebuild left expanded = %d", e.expanded)
	}
	// A flat row is one line, not a signature: enter stays inert.
	e, _ = e.Update(key("enter"))
	if e.expanded != -1 {
		t.Fatalf("enter opened a pane in flat mode: expanded=%d", e.expanded)
	}
	e, _ = e.Update(key("m"))

	e, _ = e.Update(key("enter"))
	if e.expanded != e.tbl.Cursor() || e.expanded < 0 {
		t.Fatalf("expanded = %d, want the cursor row %d", e.expanded, e.tbl.Cursor())
	}
	if !e.HandleEscape() || e.expanded != -1 {
		t.Fatalf("esc did not collapse: expanded=%d", e.expanded)
	}
	if e.HandleEscape() {
		t.Fatal("esc must fall through to root once the pane is closed")
	}
}

func TestSweepArmsOncePerGeneration(t *testing.T) {
	m := New([]models.Host{host("alpha")}, nil)
	m.width, m.height = 80, 30
	s := errSweptSession("alpha")
	s.dash.errCorpus, s.dash.errFetchedAt = nil, time.Time{}
	m.sessions = []*Session{s}
	m.active, m.view = 0, viewDashboard

	// The 2 s tick is the one thing that must never sweep, however many
	// arrive: it is a far stricter poller than the design allows.
	for i := 0; i < 3; i++ {
		_, _ = m.Update(statsTickMsg{msgTag: msgTag{hostID: "alpha", gen: 1}})
		if s.dash.errArmed || s.dash.errLoading {
			t.Fatalf("tick %d armed a sweep", i)
		}
	}

	// A failed container list is not evidence the host is readable. fetchNow
	// always pairs listOK=false with the daemon error, so the fixture does too.
	_, cmd := m.Update(statsLoadedMsg{msgTag: msgTag{hostID: "alpha", gen: 1}, err: errDial})
	if cmd != nil || s.dash.errArmed {
		t.Fatalf("listOK=false armed a sweep: cmd=%v armed=%v", cmd != nil, s.dash.errArmed)
	}

	_, cmd = m.Update(statsLoadedMsg{msgTag: msgTag{hostID: "alpha", gen: 1}, listOK: true})
	if cmd == nil {
		t.Fatal("first successful list produced no sweep cmd")
	}
	if !s.dash.errArmed || !s.dash.errLoading {
		t.Fatalf("armed=%v loading=%v, want both true", s.dash.errArmed, s.dash.errLoading)
	}

	// Every later tick of the same generation is refused, including after the
	// sweep has finished — the guard is per generation, not per in-flight job.
	_, cmd = m.Update(statsLoadedMsg{msgTag: msgTag{hostID: "alpha", gen: 1}, listOK: true})
	if cmd != nil {
		t.Fatal("second list of the same generation swept again")
	}
	_, _ = m.Update(errLoadedMsg{msgTag: msgTag{hostID: "alpha", gen: 1}, at: time.Now()})
	if s.dash.errLoading {
		t.Fatal("errLoadedMsg left the sweep in flight")
	}
	_, cmd = m.Update(statsLoadedMsg{msgTag: msgTag{hostID: "alpha", gen: 1}, listOK: true})
	if cmd != nil {
		t.Fatal("a completed sweep re-armed within the same generation")
	}

	// "f" is the user's explicit re-read, so it clears the guard.
	if _, cmd := s.dash.Update(key("f")); cmd == nil {
		t.Fatal("f produced no fetch cmd")
	}
	s.dash.errLoading = false
	if s.dash.errArmed {
		t.Fatal("f did not disarm")
	}
	_, cmd = m.Update(statsLoadedMsg{msgTag: msgTag{hostID: "alpha", gen: 1}, listOK: true})
	if cmd == nil {
		t.Fatal("f did not let the next list sweep again")
	}

	// A new generation is a new Dashboard, which is what reconnect does
	// (model.go:521 drops the old one), so the guard is gone with it.
	s.gen = 2
	s.dash = NewDashboard(s.host, s.dock, 80, 30, s.gen)
	_, cmd = m.Update(statsLoadedMsg{msgTag: msgTag{hostID: "alpha", gen: 2}, listOK: true})
	if cmd == nil {
		t.Fatal("reconnect did not re-arm the sweep")
	}
}

func TestErrLoadedAppliesAndStaleGenIsIgnored(t *testing.T) {
	m := New([]models.Host{host("alpha")}, nil)
	m.width, m.height = 80, 30
	s := errSweptSession("alpha")
	fresh := time.Now()
	s.dash.errCorpus = nil
	s.dash.errFetchedAt = fresh.Add(-time.Hour)
	m.sessions = []*Session{s}
	m.active, m.view = 0, viewDashboard

	// The generation is 1; anything older must not touch this host's corpus.
	_, cmd := m.Update(errLoadedMsg{
		msgTag: msgTag{hostID: "alpha", gen: 99},
		lines:  errFixture(), at: fresh,
	})
	if cmd != nil {
		t.Fatal("a msg for an unknown generation produced a command")
	}
	if s.dash.errCorpus != nil || !s.dash.errFetchedAt.Equal(fresh.Add(-time.Hour)) {
		t.Fatalf("stale msg applied: corpus=%d at=%v", len(s.dash.errCorpus), s.dash.errFetchedAt)
	}

	_, cmd = m.Update(errLoadedMsg{
		msgTag: msgTag{hostID: "nope", gen: 1},
		lines:  errFixture(), at: fresh,
	})
	if cmd != nil || s.dash.errCorpus != nil {
		t.Fatalf("unknown host applied: cmd=%v corpus=%d", cmd != nil, len(s.dash.errCorpus))
	}

	// The current generation lands, and the screen re-merges only while open.
	m.errView = NewErrorsView(80, 29)
	m.errView.Refresh(m.sessions, len(m.hosts))
	m.view = viewErrors
	lines := errFixture()
	_, _ = m.Update(errLoadedMsg{msgTag: msgTag{hostID: "alpha", gen: 1}, lines: lines, truncated: true, at: fresh})
	if len(s.dash.errCorpus) != len(lines) || !s.dash.errTruncated {
		t.Fatalf("corpus=%d truncated=%v", len(s.dash.errCorpus), s.dash.errTruncated)
	}
	if m.errView.swept != 1 || !strings.Contains(m.errView.View(), "1 of 1 hosts swept") {
		t.Fatalf("screen did not re-merge: swept=%d", m.errView.swept)
	}

	// A failure keeps whatever lines arrived, so one unreadable container
	// cannot blank the host.
	_, _ = m.Update(errLoadedMsg{
		msgTag: msgTag{hostID: "alpha", gen: 1}, lines: lines[:1],
		at: fresh, err: errDial,
	})
	if len(s.dash.errCorpus) != 1 || s.dash.errProblem == "" {
		t.Fatalf("corpus=%d problem=%q", len(s.dash.errCorpus), s.dash.errProblem)
	}
	if !strings.Contains(m.errView.View(), "sweep failed") {
		t.Fatal("the screen must show that the sweep partially failed")
	}
}

func TestErrCorpusCapAppliesOnArrival(t *testing.T) {
	m := New([]models.Host{host("alpha")}, nil)
	m.width, m.height = 80, 30
	s := errSweptSession("alpha")
	s.dash.errCorpus = nil
	m.sessions, m.active, m.view = []*Session{s}, 0, viewDashboard

	at := time.Now()
	lines := make([]ErrLine, errCorpusCap+50)
	for i := range lines {
		lines[i] = ErrLine{HostID: "alpha", HostName: "alpha", ID: "c1", Name: "web",
			Sev: lvlError, Sig: "boom", At: at.Add(time.Duration(i) * time.Second), Text: "ERROR boom"}
	}
	_, _ = m.Update(errLoadedMsg{msgTag: msgTag{hostID: "alpha", gen: 1}, lines: lines, at: at})
	if len(s.dash.errCorpus) != errCorpusCap {
		t.Fatalf("corpus = %d, want the %d cap", len(s.dash.errCorpus), errCorpusCap)
	}
	// The newest survived.
	if got := s.dash.errCorpus[len(s.dash.errCorpus)-1].At; !got.Equal(lines[len(lines)-1].At) {
		t.Errorf("kept at = %v, want %v", got, lines[len(lines)-1].At)
	}
}

func TestOpenErrorsFromEitherScreenAndReturns(t *testing.T) {
	m := New([]models.Host{host("alpha"), host("beta")}, nil)
	m.width, m.height = 80, 30

	// From the host list: nothing is swept, so opening costs a merge only.
	m.view = viewHosts
	_, cmd := m.Update(key("a"))
	if m.view != viewErrors || m.errView == nil {
		t.Fatalf("a from hosts: view=%d errView=%v", m.view, m.errView)
	}
	if cmd != nil {
		t.Fatal("opening the screen started a sweep")
	}
	if m.errView.hostCount != 2 || m.errView.swept != 0 {
		t.Fatalf("coverage = %d/%d", m.errView.swept, m.errView.hostCount)
	}
	// Session switching is meaningless across hosts, so the keys stay inert.
	for _, k := range []string{"1", "2", "X"} {
		_, c := m.Update(key(k))
		if m.view != viewErrors || m.active != -1 || c != nil {
			t.Fatalf("key %q leaked: view=%d active=%d cmd=%v", k, m.view, m.active, c != nil)
		}
	}
	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyTab})
	if m.view != viewErrors {
		t.Fatalf("tab left the errors screen: view=%d", m.view)
	}

	_, _ = m.Update(key("esc"))
	if m.view != viewHosts {
		t.Fatalf("esc returned to %d, want viewHosts", m.view)
	}

	// From the dashboard: esc must land back on the dashboard, not the list.
	s := errSweptSession("alpha")
	m.sessions, m.active, m.view = []*Session{s}, 0, viewDashboard
	_, cmd = m.Update(key("a"))
	if m.view != viewErrors || m.errReturn != viewDashboard {
		t.Fatalf("a from dashboard: view=%d errReturn=%d", m.view, m.errReturn)
	}
	_, _ = m.Update(key("q"))
	if m.view != viewDashboard {
		t.Fatalf("q returned to %d, want viewDashboard", m.view)
	}
	// Leaving must not leave the tick chain disarmed behind it.
	if _, cmd := m.Update(key("a")); cmd != nil {
		t.Fatal("reopen started a sweep")
	}
	_, _ = m.Update(key("esc"))
}

func TestErrorsSweepAllConnectedHosts(t *testing.T) {
	m := New([]models.Host{host("alpha"), host("beta"), host("gamma")}, nil)
	m.width, m.height = 80, 30
	a := errSweptSession("alpha")
	b := errSweptSession("beta")
	g := &Session{host: host("gamma"), gen: 1}
	g.dash = NewDashboard(g.host, nil, 80, 30, 1) // dialing: no dock
	m.sessions = []*Session{a, b, g}
	m.active, m.view = 0, viewDashboard
	_, _ = m.Update(key("a"))

	_, cmd := m.Update(key("r"))
	if cmd == nil {
		t.Fatal("r produced no sweep batch")
	}
	for i, s := range []*Session{a, b} {
		if !s.dash.errArmed || !s.dash.errLoading {
			t.Errorf("session %d was not swept: armed=%v loading=%v", i, s.dash.errArmed, s.dash.errLoading)
		}
	}
	if g.dash.errLoading || g.dash.errArmed {
		t.Fatal("an unconnected host was swept")
	}
	if !strings.Contains(m.errView.View(), "sweeping") {
		t.Fatal("the screen must report the sweep in flight")
	}

	// Both hosts report the same signature once their corpora merge.
	f := errFixture()
	_, _ = m.Update(errLoadedMsg{msgTag: msgTag{hostID: "alpha", gen: 1}, lines: f[:2], at: time.Now()})
	_, _ = m.Update(errLoadedMsg{msgTag: msgTag{hostID: "beta", gen: 1}, lines: f[2:], at: time.Now()})
	if m.errView.swept != 2 || m.errView.lineCount != 3 {
		t.Fatalf("swept=%d lines=%d, want 2/3", m.errView.swept, m.errView.lineCount)
	}
	// gamma stays counted but un-swept.
	if !strings.Contains(m.errView.View(), "2 of 3 hosts swept") {
		t.Errorf("coverage line wrong:\n%s", m.errView.View())
	}
}

func TestErrorsLiveTargetSwitchesSession(t *testing.T) {
	boom := func(hostID, cont string, at time.Time) ErrLine {
		return ErrLine{HostID: hostID, HostName: hostID, ID: cont, Name: cont,
			Sev: lvlError, Sig: "boom", At: at, Text: "ERROR boom"}
	}
	m := New([]models.Host{host("alpha"), host("beta")}, nil)
	m.width, m.height = 80, 30
	a := errSweptSession("alpha", boom("alpha", "c1", time.Now().Add(-time.Minute)))
	b := errSweptSession("beta", boom("beta", "c2", time.Now()))
	m.sessions = []*Session{a, b}
	m.active, m.view = 0, viewDashboard
	_, _ = m.Update(key("a"))

	_, cmd := m.Update(key("l"))
	if cmd == nil {
		t.Fatal("l produced no stream cmd")
	}
	if m.view != viewContainer {
		t.Fatalf("view = %d, want viewContainer", m.view)
	}
	if m.active != 1 {
		t.Fatalf("active = %d, want 1 (beta owns the newest example)", m.active)
	}
	if b.cv == nil || b.cv.id != "c2" || b.cv.name != "c2" || b.cv.hostID != "beta" || b.cv.gen != 1 {
		t.Fatalf("container view = %+v", b.cv)
	}
	if a.cv != nil {
		t.Fatal("the wrong session got a view")
	}
}

func TestErrorsLiveTargetNeedsAConnection(t *testing.T) {
	f := errFixture()
	m := New([]models.Host{host("alpha"), host("beta")}, nil)
	m.width, m.height = 80, 30
	// The corpus names a host that was never connected.
	s := errSweptSession("alpha", f[2])
	s.dash.errCorpus[0].HostID = "gamma"
	m.sessions, m.active, m.view = []*Session{s}, 0, viewDashboard
	_, _ = m.Update(key("a"))

	_, cmd := m.Update(key("l"))
	if cmd != nil {
		t.Fatal("l started a stream for a host with no connection")
	}
	if m.view != viewErrors {
		t.Fatalf("view = %d, want the errors screen to stay up", m.view)
	}
	if !strings.Contains(m.errView.View(), "connect gamma first to follow its logs") {
		t.Errorf("no explanation shown:\n%s", m.errView.View())
	}
}

func TestErrorsLiveTargetInFlatMode(t *testing.T) {
	// HostID must name a session, because that is how l resolves the host.
	at := time.Now()
	line := func(hostID, cont string, mins int) ErrLine {
		return ErrLine{HostID: hostID, HostName: hostID, ID: cont, Name: cont,
			Sev: lvlError, Sig: "boom " + cont, At: at.Add(time.Duration(mins) * time.Minute),
			Text: "ERROR boom " + cont}
	}
	m := New([]models.Host{host("alpha"), host("beta")}, nil)
	m.width, m.height = 80, 30
	m.sessions = []*Session{
		errSweptSession("alpha", line("alpha", "c1", -2), line("alpha", "c1", -1)),
		errSweptSession("beta", line("beta", "c2", 0)),
	}
	m.active, m.view = 0, viewDashboard
	_, _ = m.Update(key("a"))
	_, _ = m.Update(key("m"))

	// flat[0] is beta's newest line, so l must leave the dashboard on beta.
	_, cmd := m.Update(key("l"))
	if cmd == nil || m.view != viewContainer || m.active != 1 {
		t.Fatalf("cmd=%v view=%d active=%d", cmd != nil, m.view, m.active)
	}
	if m.sessions[1].cv.id != "c2" {
		t.Fatalf("streamed %q, want c2", m.sessions[1].cv.id)
	}
}

// A frame opened over the tab bar must be one row shorter, and a key press
// with nothing to do must still say so.
func TestErrorsSweepWithNothingToSweep(t *testing.T) {
	m := New([]models.Host{host("alpha")}, nil)
	m.width, m.height = 80, 30
	s := &Session{host: host("alpha"), gen: 1}
	s.dash = NewDashboard(s.host, nil, 80, 30, 1) // dialing: no dock to sweep with
	m.sessions, m.active, m.view = []*Session{s}, 0, viewHosts

	_, _ = m.Update(key("a"))
	_, cmd := m.Update(key("r"))
	if cmd != nil {
		t.Fatal("r swept a host with no connection")
	}
	if s.dash.errLoading || s.dash.errArmed {
		t.Fatalf("unconnected dashboard was armed: loading=%v armed=%v", s.dash.errLoading, s.dash.errArmed)
	}
	v := m.errView.View()
	if got := h(v); got != 29 {
		t.Errorf("frame = %d rows, want 29 under the session tab bar\n%s", got, v)
	}
	if !strings.Contains(v, "no connected host to sweep") {
		t.Errorf("r gave no explanation:\n%s", v)
	}
	// The tab bar plus the frame is the whole screen.
	full := m.View()
	if got := h(full); got != 30 {
		t.Errorf("screen = %d rows, want 30", got)
	}
}

// TestErrorsExpandedPaneFitsADeepCorpus is the regression the real-daemon run
// caught and the three-line fixture never could: a group whose samples are long
// and whose container list spans every host overflowed the frame, because the
// pane clipped its lines to a width the panel cannot actually hold. Each sample
// then wrapped to a second row and the pinned footer fell off the bottom.
func TestErrorsExpandedPaneFitsADeepCorpus(t *testing.T) {
	long := strings.Repeat("connection reset by peer while flushing /var/lib/app/segments ", 4)
	var lines []ErrLine
	for _, h := range []string{"alpha", "beta", "gamma"} {
		for _, c := range []string{"api", "web"} {
			for i := 0; i < 8; i++ {
				lines = append(lines, ErrLine{
					HostID: h, HostName: h, ID: h + c, Name: c,
					Stamp: "01:02:03", Sev: lvlError,
					Sig: "connection reset by peer", Text: long,
				})
			}
		}
	}
	sess := []*Session{errSweptSession("alpha", lines...), errSweptSession("beta", lines...)}

	for _, w := range []int{60, 80, 100, 140} {
		for _, rows := range []int{12, 20, 30, 40} {
			e := NewErrorsView(w, rows)
			e.Refresh(sess, 2)
			// The grouped row must land under its own header: the signature is the
			// adaptive column, so a cell-order slip squeezes it to eight cells and
			// pushes the tally into the wide column.
			if got := e.View(); !strings.Contains(got, "connection reset by peer") || !strings.Contains(got, "3h·6c") {
				t.Fatalf("w=%d rows=%d: grouped cells are misplaced\n%s", w, rows, got)
			}
			e, _ = e.Update(key("enter"))
			if e.expanded < 0 {
				t.Fatalf("w=%d rows=%d: enter did not expand", w, rows)
			}
			v := e.View()
			if got := h(v); got != rows {
				t.Errorf("w=%d rows=%d: frame is %d rows\n%s", w, rows, got, v)
			}
			if got := lipgloss.Width(v); got > w {
				t.Errorf("w=%d rows=%d: frame is %d cols", w, rows, got)
			}
			last := lastLine(v)
			if !strings.Contains(last, "esc") || !strings.Contains(last, "back") {
				t.Errorf("w=%d rows=%d: footer is not the last row: %q", w, rows, last)
			}
			// The pane must still say something useful at the smallest size.
			if !strings.Contains(v, "connection reset by peer") {
				t.Errorf("w=%d rows=%d: the signature vanished\n%s", w, rows, v)
			}
		}
	}
}
