package app

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"vpstui/internal/config"
	"vpstui/internal/docker"
	"vpstui/internal/httpapi"
	"vpstui/internal/models"
	"vpstui/internal/secret"
	"vpstui/internal/shell"
	"vpstui/internal/sshclient"
)

type View int

const (
	viewHosts View = iota
	viewForm
	viewPrompt
	viewConnecting
	viewDashboard
	viewContainer
	viewErrors
)

// internal messages
type msgTag struct {
	hostID string
	gen    uint64
}

type connectHostReq struct{ host models.Host }
type newHostReq struct{}
type editHostReq struct{ host models.Host }
type deleteHostReq struct{ host models.Host }
type connectDoneMsg struct {
	msgTag
	conn *sshclient.Conn
	err  error
	host models.Host
}
type formSubmittedMsg struct {
	host    models.Host
	secretV string
	isNew   bool
}
type statsTickMsg struct{ msgTag }
type statsLoadedMsg struct {
	msgTag
	containers []models.Container
	listOK     bool // containers fetched successfully (possibly empty)
	cstats     []models.ContainerStats
	hstats     models.HostStats
	err        error
}
type actionDoneMsg struct {
	msgTag
	verb string
	name string
	err  error
}
type logChunkMsg struct {
	msgTag
	chunk string
}
type logEndMsg struct {
	msgTag
	err error
}

// logFlushMsg batches the rows Append recorded, so the viewport pays one
// SetContent per tick instead of one per log line.
type logFlushMsg struct {
	msgTag
}
type detailLoadedMsg struct {
	msgTag
	detail models.ContainerDetail
	err    error
}

// errLoadedMsg carries one host's parsed error sweep. Parsing happens in the
// command goroutine, so a multi-megabyte payload never reaches Update.
type errLoadedMsg struct {
	msgTag
	lines     []ErrLine
	truncated bool
	at        time.Time
	err       error
}

type diskLoadedMsg struct {
	msgTag
	objs []models.DiskObject
	err  error
}

// peaksLoadedMsg carries the kernel's all-time cgroup counters from one
// read-only probe. at is when the probe returned, so the screen can date it.
type peaksLoadedMsg struct {
	msgTag
	rows []models.ContainerPeak
	at   time.Time
	err  error
}

// perfTickMsg paces the stats-only sample for a session whose dashboard is not
// on screen. The 2 s tick runs only for the active host, so without this every
// background host's ranking would freeze at whatever it looked like once.
type perfTickMsg struct{ msgTag }

type perfLoadedMsg struct {
	msgTag
	cstats []models.ContainerStats
	err    error
}

type shellExitedMsg struct {
	msgTag
	name   string
	err    error
	detail string
}

type shellProbeDoneMsg struct {
	msgTag
	req containerReq
	err error
}

// Session is one live (or dialing) connection to a host. Only the active
// session's dashboard ticks at 2 s; background sessions pause that chain but
// keep buffering log chunks and take the slower perf sample (§ perfTickMsg).
type Session struct {
	host models.Host
	gen  uint64
	conn *sshclient.Conn
	dock *docker.Executor
	dash *Dashboard
	cv   *ContainerView

	// perfArmed guarantees one 10 s stats-only chain per session generation,
	// the same once-per-generation guarantee errArmed gives the log sweep.
	perfArmed bool
}

type Model struct {
	hosts []models.Host
	view  View

	width  int
	height int
	status string

	list *HostList
	form *HostForm

	sp spinner.Model

	sessions []*Session
	active   int

	pendingHost models.Host
	pendingGen  uint64
	genSeq      uint64

	// errView is the cross-host aggregate, built on demand. errReturn is the
	// screen "a" was pressed from, so esc lands where the user came from.
	errView   *ErrorsView
	errReturn View

	// tags holds the user's own labels; tag is the prompt that edits them. Both
	// live at root because a label spans every surface, not one screen.
	tags *TagStore
	tag  tagPrompt

	send     func(tea.Msg)
	quitting bool
	api      *httpapi.Store
}

func New(hosts []models.Host, tags map[string]models.Tag) *Model {
	m := &Model{
		hosts:  hosts,
		view:   viewHosts,
		active: -1,
		sp:     spinner.New(spinner.WithSpinner(spinner.Dot)),
		api:    httpapi.NewStore(),
		tags:   NewTagStore(tags),
		tag:    newTagPrompt(),
	}
	m.list = NewHostList(hosts)
	return m
}

// HTTPStore exposes the live-session snapshot store to the embedded JSON API.
func (m *Model) HTTPStore() *httpapi.Store { return m.api }

// Attach lets background goroutines deliver messages to the program.
func (m *Model) Attach(send func(tea.Msg)) { m.send = send }

func (m *Model) Init() tea.Cmd { return m.sp.Tick }

func (m *Model) nextGen() uint64 {
	m.genSeq++
	return m.genSeq
}

func (m *Model) findSession(hostID string, gen uint64) *Session {
	for _, s := range m.sessions {
		if s.host.ID == hostID && s.gen == gen {
			return s
		}
	}
	return nil
}

func (m *Model) findSessionByHost(hostID string) *Session {
	for _, s := range m.sessions {
		if s.host.ID == hostID {
			return s
		}
	}
	return nil
}

func (m *Model) indexOf(s *Session) int {
	for i, x := range m.sessions {
		if x == s {
			return i
		}
	}
	return -1
}

func (m *Model) activeSession() *Session {
	if m.active < 0 || m.active >= len(m.sessions) {
		return nil
	}
	return m.sessions[m.active]
}

func (m *Model) removeSession(s *Session) {
	if idx := m.indexOf(s); idx >= 0 {
		m.sessions = append(m.sessions[:idx], m.sessions[idx+1:]...)
		if m.active > idx {
			m.active--
		}
		if m.active >= len(m.sessions) {
			m.active = len(m.sessions) - 1
		}
	}
	if m.api != nil && s.host.ID != "" {
		m.api.Unpublish(s.host.ID)
	}
	m.refreshConnected()
}

func (m *Model) refreshConnected() {
	ids := make([]string, 0, len(m.sessions))
	for _, s := range m.sessions {
		if s.conn != nil {
			ids = append(ids, s.host.ID)
		}
	}
	m.list.SetConnected(ids)
}

func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.list.SetSize(msg.Width, msg.Height)
		if m.form != nil {
			m.form.SetSize(msg.Width)
		}
		contentH := msg.Height - m.tabRows()
		if m.errView != nil {
			m.errView.SetSize(msg.Width, contentH)
		}
		for _, s := range m.sessions {
			if s.dash != nil {
				s.dash.SetSize(msg.Width, contentH)
			}
			if s.cv != nil {
				s.cv.SetSize(msg.Width, contentH)
			}
		}
		return m, nil

	case tea.KeyMsg:
		if msg.Type == tea.KeyCtrlC {
			m.cleanup()
			return m, tea.Quit
		}
		return m.updateKey(msg)

	case connectDoneMsg:
		return m.onConnectDone(msg)

	case statsTickMsg, statsLoadedMsg, actionDoneMsg, logChunkMsg, logEndMsg, logFlushMsg, detailLoadedMsg, diskLoadedMsg, errLoadedMsg, shellExitedMsg, shellProbeDoneMsg, peaksLoadedMsg, perfTickMsg, perfLoadedMsg:
		return m.route(msg)

	case connectHostReq:
		return m.openOrConnect(msg.host)

	case newHostReq:
		m.form = NewHostForm(models.Host{}, true)
		m.view = viewForm
		return m, nil

	case editHostReq:
		m.form = NewHostForm(msg.host, false)
		m.view = viewForm
		return m, nil

	case deleteHostReq:
		return m.deleteHost(msg.host)

	case formSubmittedMsg:
		return m.saveHost(msg)

	case yankMsg:
		// Reported as counts only — the yanked text came off a remote container
		// and must not be put back on screen as though it were our own message.
		if m.errView != nil && m.view == viewErrors {
			m.errView.SetNote(msg.Describe())
		} else {
			m.status = msg.Describe()
		}
		return m, nil

	case spinner.TickMsg:
		var cmd tea.Cmd
		m.sp, cmd = m.sp.Update(msg)
		return m, cmd
	}

	return m.routeInternal(msg)
}

func (m *Model) updateKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch m.view {
	case viewHosts:
		if msg.String() == "q" {
			m.cleanup()
			return m, tea.Quit
		}
		if msg.String() == "a" {
			return m.openErrors()
		}
		var cmd tea.Cmd
		m.list, cmd = m.list.Update(msg)
		return m, cmd

	case viewForm:
		next, cmd, submitted, cancelled := m.form.Update(msg)
		m.form = next
		if submitted {
			m.view = viewHosts
			m.status = ""
			return m, cmd
		}
		if cancelled {
			m.view = viewHosts
			m.form = nil
			m.status = ""
			return m, nil
		}
		return m, cmd

	case viewPrompt:
		next, cmd, submitted, cancelled := m.form.Update(msg)
		m.form = next
		if submitted {
			h := m.pendingHost
			s := next.SecretValue()
			m.form = nil
			if s != "" {
				ref := h.PasswordRef()
				if h.AuthType == models.AuthKey {
					ref = h.PassphraseRef()
				}
				_ = secret.Set(ref, s)
			}
			return m, m.dialCmd(h, m.pendingGen, s)
		}
		if cancelled {
			m.form = nil
			m.view = viewHosts
			if s := m.findSession(m.pendingHost.ID, m.pendingGen); s != nil && s.conn == nil {
				m.removeSession(s)
			}
			return m, nil
		}
		return m, cmd

	case viewConnecting:
		// esc keeps the dial running in the background
		if msg.String() == "esc" {
			m.view = viewHosts
		}
		return m, nil

	case viewDashboard:
		// The label prompt has the keys while it is open: tab chooses a colour
		// and digits are ordinary typing, so sessionKeys must not run first.
		if m.tag.active {
			return m.updateTag(msg)
		}
		if handled, cmd := m.sessionKeys(msg.String()); handled {
			return m, cmd
		}
		switch msg.String() {
		case "a":
			return m.openErrors()
		case "t":
			return m.openTagPrompt()
		case "T":
			return m.untagCursor()
		}
		s := m.activeSession()
		if s == nil || s.dash == nil {
			return m, nil
		}
		dash, cmd := s.dash.Update(msg)
		s.dash = dash
		if s.dash.TakeReconnect() {
			return m.reconnect(s)
		}
		if s.dash.TakeBack() {
			m.view = viewHosts
			return m, nil
		}
		if req, ok := s.dash.OpenLogs(); ok {
			s.cv = NewContainerView(req.id, req.name, m.width, m.height-m.tabRows())
			s.cv.hostID = s.dash.hostID
			s.cv.gen = s.dash.gen
			s.cv.SetTag(m.tags.Display(s.host.ID, req.name), m.tags.Tint(s.host.ID, req.name))
			m.view = viewContainer
			return m, startLogStream(s, req.id, m.send)
		}
		if req, ok := s.dash.OpenShell(); ok {
			return m, s.dash.probeShell(req)
		}
		return m, cmd

	case viewContainer:
		s := m.activeSession()
		key := msg.String()
		// While the filter input has the keys, sessionKeys must not steal
		// digits or X — a query like "500" would otherwise switch sessions.
		if s == nil || s.cv == nil || !s.cv.Searching() {
			if handled, cmd := m.sessionKeys(key); handled {
				return m, cmd
			}
		}
		if key == "esc" && s != nil && s.cv != nil && s.cv.HandleEscape() {
			return m, nil // left the filter input, then cleared the filter
		}
		if key == "esc" || key == "q" {
			if s != nil && s.cv != nil {
				s.cv.Stop()
				s.cv = nil
			}
			m.view = viewDashboard
			if s != nil && s.dash != nil {
				return m, tea.Batch(s.dash.fetchNow(), tickStats(s.host.ID, s.gen))
			}
			return m, nil
		}
		var cmd tea.Cmd
		if s != nil && s.cv != nil {
			s.cv, cmd = s.cv.Update(msg)
		}
		return m, cmd

	case viewErrors:
		if m.errView == nil {
			m.view = viewHosts
			return m, nil
		}
		key := msg.String()
		if m.tag.active {
			return m.updateTag(msg)
		}
		if key == "esc" && m.errView.HandleEscape() {
			return m, nil // closed the expand pane, stay on the screen
		}
		if key == "esc" || key == "q" {
			m.view = m.errReturn
			return m, nil
		}
		if key == "r" {
			cmd := m.sweepAll()
			// Re-merge immediately: the sessions now report a sweep in
			// flight, and a key press that shows nothing looks like a no-op.
			m.errView.Refresh(m.sessions, len(m.hosts))
			if cmd == nil {
				m.errView.SetNote("no connected host to sweep")
			}
			return m, cmd
		}
		if key == "l" {
			return m.openLiveLog()
		}
		if key == "y" {
			return m, yankCmd(m.errView.yankLine())
		}
		if key == "Y" {
			return m, yankCmd(m.errView.yankGroup())
		}
		if key == "t" {
			return m.openTagPrompt()
		}
		if key == "T" {
			return m.untagCursor()
		}
		var cmd tea.Cmd
		m.errView, cmd = m.errView.Update(msg)
		return m, cmd
	}
	return m, nil
}

// sessionKeys handles multi-session navigation from dashboard/log views.
func (m *Model) sessionKeys(key string) (bool, tea.Cmd) {
	if len(m.sessions) == 0 {
		return false, nil
	}
	switch key {
	case "tab":
		return true, m.activate((m.active + 1) % len(m.sessions))
	case "shift+tab":
		return true, m.activate((m.active - 1 + len(m.sessions)) % len(m.sessions))
	case "X":
		return true, m.closeActive()
	}
	if len(key) == 1 && key[0] >= '1' && key[0] <= '9' {
		i := int(key[0] - '1')
		if i < len(m.sessions) {
			return true, m.activate(i)
		}
	}
	return false, nil
}

func (m *Model) activate(i int) tea.Cmd {
	m.active = i
	s := m.sessions[i]
	if s.dash == nil {
		m.pendingHost, m.pendingGen = s.host, s.gen
		m.view = viewConnecting
		return nil
	}
	m.view = viewDashboard
	return tea.Batch(s.dash.fetchNow(), tickStats(s.host.ID, s.gen))
}

func (m *Model) closeActive() tea.Cmd {
	s := m.activeSession()
	if s == nil {
		return nil
	}
	if s.cv != nil {
		s.cv.Stop()
	}
	if s.conn != nil {
		s.conn.Close()
	}
	m.removeSession(s)
	if len(m.sessions) == 0 {
		m.active = -1
		m.view = viewHosts
		return nil
	}
	return m.activate(m.active)
}

func (m *Model) reconnect(s *Session) (tea.Model, tea.Cmd) {
	if s.conn != nil {
		s.conn.Close()
	}
	s.conn, s.dock, s.dash = nil, nil, nil
	if s.cv != nil {
		s.cv.Stop()
		s.cv = nil
	}
	s.gen = m.nextGen()
	m.pendingHost, m.pendingGen = s.host, s.gen
	m.view = viewConnecting
	m.refreshConnected()
	return m.connectSlot(s)
}

func (m *Model) openOrConnect(h models.Host) (tea.Model, tea.Cmd) {
	if s := m.findSessionByHost(h.ID); s != nil {
		m.activate(m.indexOf(s))
		return m, nil
	}
	return m.startConnect(h)
}

// openErrors shows the cross-host aggregate. It merges what the sessions have
// already swept; it never starts a sweep of its own, so opening the screen is
// free. "r" is the explicit request to go and read more hosts.
func (m *Model) openErrors() (tea.Model, tea.Cmd) {
	contentH := m.height - m.tabRows()
	if m.errView == nil {
		m.errView = NewErrorsView(m.width, contentH)
	} else {
		m.errView.SetSize(m.width, contentH)
	}
	m.errView.SetTags(m.tags)
	m.errView.Refresh(m.sessions, len(m.hosts))
	m.errReturn = m.view
	m.view = viewErrors
	return m, nil
}

// sweepAll re-reads every connected host. This is the only path that touches a
// host the user is not looking at, and it exists only as an explicit key press.
func (m *Model) sweepAll() tea.Cmd {
	var cmds []tea.Cmd
	for _, s := range m.sessions {
		if s.dash == nil || s.conn == nil {
			continue
		}
		s.dash.errArmed = true // the sweep is running; do not arm it again
		if cmd := s.dash.SweepErrors(); cmd != nil {
			cmds = append(cmds, cmd)
		}
	}
	if len(cmds) == 0 {
		return nil
	}
	return tea.Batch(cmds...)
}

// openLiveLog jumps from an aggregate row to that container's live stream,
// switching to the session that owns the host first.
func (m *Model) openLiveLog() (tea.Model, tea.Cmd) {
	t, ok := m.errView.LiveTarget()
	if !ok {
		return m, nil
	}
	s := m.findSessionByHost(t.hostID)
	if s == nil || s.dock == nil {
		name := t.hostID
		for _, h := range m.hosts {
			if h.ID == t.hostID {
				name = h.Name
			}
		}
		m.errView.SetNote("connect " + name + " first to follow its logs")
		return m, nil
	}
	m.active = m.indexOf(s)
	s.cv = NewContainerView(t.id, t.name, m.width, m.height-m.tabRows())
	s.cv.hostID, s.cv.gen = s.host.ID, s.gen
	s.cv.SetTag(m.tags.Display(t.hostID, t.name), m.tags.Tint(t.hostID, t.name))
	m.view = viewContainer
	return m, startLogStream(s, t.id, m.send)
}

// tagCursor opens the label prompt for whatever the cursor points at. The Errors
// screen and the dashboard resolve their own row, so one key means one thing on
// both.
func (m *Model) tagCursor() (tagTarget, bool) {
	if m.view == viewErrors {
		if m.errView == nil {
			return tagTarget{}, false
		}
		return m.errView.TagTarget()
	}
	s := m.activeSession()
	if s == nil || s.dash == nil {
		return tagTarget{}, false
	}
	return s.dash.TagTarget()
}

func (m *Model) openTagPrompt() (tea.Model, tea.Cmd) {
	t, ok := m.tagCursor()
	if !ok {
		m.tagFeedback("no container under the cursor", true)
		return m, nil
	}
	cur, _ := m.tags.Get(t.hostID, t.name)
	cmd := m.tag.open(t, cur.Tag, cur.Color)
	m.syncTagPrompt()
	return m, cmd
}

// untagCursor drops a label without opening the prompt — pressing T on an
// unlabelled container is a no-op rather than a surprise file write.
func (m *Model) untagCursor() (tea.Model, tea.Cmd) {
	t, ok := m.tagCursor()
	if !ok {
		m.tagFeedback("no container under the cursor", true)
		return m, nil
	}
	if !m.tags.Clear(t.hostID, t.name) {
		m.tagFeedback(t.name+" has no tag", false)
		return m, nil
	}
	m.afterTagChange(t.hostID, "tag cleared from "+t.name, false)
	return m, nil
}

// updateTag runs the prompt. Every key it consumes is stopped here, so nothing
// else on the screen can react to typing.
func (m *Model) updateTag(msg tea.Msg) (tea.Model, tea.Cmd) {
	outcome, cmd := m.tag.Update(msg)
	switch outcome {
	case promptCommit:
		m.commitTag()
	default:
		// The prompt row is a pre-rendered string, so every keystroke that keeps
		// it open has to refresh it — otherwise the field looks dead while typing.
		m.syncTagPrompt()
	}
	if cmd == nil {
		return m, nil
	}
	return m, cmd
}

func (m *Model) commitTag() {
	t := m.tag.target
	label, ok := m.tag.value()
	if !ok {
		// Emptied (or nothing but separators): the prefilled label was deleted on
		// purpose, so this is an untag.
		if m.tags.Clear(t.hostID, t.name) {
			m.afterTagChange(t.hostID, "tag cleared from "+t.name, false)
			return
		}
		m.tagFeedback("tag needs some text", true)
		return
	}
	if m.tags.Set(t.hostID, t.name, label, m.tag.color, t.id) {
		m.afterTagChange(t.hostID, "tagged "+t.name+" as "+label, false)
		return
	}
	m.syncTagPrompt()
}

// afterTagChange persists the label and repaints every surface that shows it.
// The save is the only part that can fail, and its error goes to the same place
// as the success message.
func (m *Model) afterTagChange(hostID, msg string, isErr bool) {
	if err := config.SaveTags(m.tags.All()); err != nil {
		msg, isErr = "could not save tags: "+err.Error(), true
	}
	m.repaintTag(hostID)
	// Last, because refreshing the Errors screen clears a note set before it.
	m.tagFeedback(msg, isErr)
	m.syncTagPrompt()
}

// repaintTag redraws every surface that reads labels, for the host whose label
// just changed.
func (m *Model) repaintTag(hostID string) {
	for _, s := range m.sessions {
		if s.host.ID != hostID {
			continue
		}
		if s.dash != nil {
			s.dash.rebuild()
		}
		if s.cv != nil {
			s.cv.SetTag(m.tags.Display(hostID, s.cv.name), m.tags.Tint(hostID, s.cv.name))
		}
		m.publish(s)
	}
	if m.view == viewErrors && m.errView != nil {
		m.errView.Refresh(m.sessions, len(m.hosts))
	}
}

// tagFeedback puts a one-shot message where the user is looking.
func (m *Model) tagFeedback(msg string, isErr bool) {
	switch {
	case m.view == viewErrors && m.errView != nil:
		m.errView.SetNote(msg)
	case m.view == viewDashboard:
		if s := m.activeSession(); s != nil && s.dash != nil {
			s.dash.SetToast(msg, isErr)
		}
	default:
		m.status = msg
	}
}

// syncTagPrompt mirrors the live prompt text into the surface that owns the row,
// and clears it once the prompt closes.
func (m *Model) syncTagPrompt() {
	line := m.tag.line(m.width)
	if s := m.activeSession(); s != nil && s.dash != nil {
		s.dash.SetPrompt(line)
	}
	if m.errView != nil {
		m.errView.SetPrompt(line)
	}
}

func (m *Model) route(msg tea.Msg) (tea.Model, tea.Cmd) {
	var t msgTag
	switch msg := msg.(type) {
	case statsTickMsg:
		t = msg.msgTag
	case statsLoadedMsg:
		t = msg.msgTag
	case actionDoneMsg:
		t = msg.msgTag
	case logChunkMsg:
		t = msg.msgTag
	case logEndMsg:
		t = msg.msgTag
	case logFlushMsg:
		t = msg.msgTag
	case detailLoadedMsg:
		t = msg.msgTag
	case errLoadedMsg:
		t = msg.msgTag
	case diskLoadedMsg:
		t = msg.msgTag
	case peaksLoadedMsg:
		t = msg.msgTag
	case perfTickMsg:
		t = msg.msgTag
	case perfLoadedMsg:
		t = msg.msgTag
	case shellExitedMsg:
		t = msg.msgTag
	case shellProbeDoneMsg:
		t = msg.msgTag
	}
	s := m.findSession(t.hostID, t.gen)
	if s == nil {
		return m, nil // stale
	}
	switch msg := msg.(type) {
	case statsTickMsg:
		if m.view == viewDashboard && m.activeSession() == s && s.dash != nil {
			s.dash, _ = s.dash.Update(msg)
			return m, tea.Batch(s.dash.fetchNow(), tickStats(t.hostID, t.gen))
		}
		return m, nil // chain stops while not viewing
	case statsLoadedMsg:
		if s.dash != nil {
			s.dash, _ = s.dash.Update(msg)
			m.publish(s)
			// One sweep and one cgroup probe per generation, both started by the
			// first container list that succeeded — never by the 2 s tick beside it.
			var cmds []tea.Cmd
			if msg.listOK {
				if !s.dash.errArmed {
					s.dash.errArmed = true
					if c := s.dash.SweepErrors(); c != nil {
						cmds = append(cmds, c)
					}
				}
				if !s.dash.peakArmed {
					s.dash.peakArmed = true
					if c := s.dash.fetchPeaks(); c != nil {
						cmds = append(cmds, c)
					}
				}
			}
			if len(cmds) > 0 {
				return m, tea.Batch(cmds...)
			}
		}
		return m, nil
	case errLoadedMsg:
		if s.dash != nil {
			s.dash, _ = s.dash.Update(msg)
			m.publish(s)
			if m.view == viewErrors && m.errView != nil {
				m.errView.Refresh(m.sessions, len(m.hosts))
			}
		}
		return m, nil
	case perfTickMsg:
		if s.dash == nil {
			return m, nil
		}
		// The host on screen is already sampled every 2 s by its own tick; the
		// chain still runs so that switching away resumes it instead of leaving a
		// frozen ranking behind.
		if m.view == viewDashboard && m.activeSession() == s {
			return m, tickPerf(t.hostID, t.gen)
		}
		return m, tea.Batch(s.dash.fetchPerf(), tickPerf(t.hostID, t.gen))
	case perfLoadedMsg, peaksLoadedMsg:
		if s.dash != nil {
			s.dash, _ = s.dash.Update(msg)
			m.publish(s)
		}
		return m, nil
	case actionDoneMsg:
		if s.dash != nil {
			var cmd tea.Cmd
			s.dash, cmd = s.dash.Update(msg)
			if m.view == viewDashboard && m.activeSession() == s {
				if cmd == nil {
					cmd = s.dash.fetchNow()
				}
				return m, cmd
			}
		}
		return m, nil
	case logChunkMsg:
		if s.cv != nil {
			s.cv.Append(msg.chunk)
		}
		return m, nil
	case logEndMsg:
		if s.cv != nil {
			s.cv.StreamDone(msg.err)
		}
		return m, nil
	case logFlushMsg:
		if s.cv == nil {
			return m, nil
		}
		s.cv.flush()
		if s.cv.running || s.cv.dirty {
			return m, tickLogs(t.hostID, t.gen)
		}
		return m, nil
	case detailLoadedMsg, diskLoadedMsg:
		if s.dash != nil {
			s.dash, _ = s.dash.Update(msg)
		}
		return m, nil
	case shellExitedMsg:
		if s.dash != nil {
			var cmd tea.Cmd
			s.dash, cmd = s.dash.Update(msg)
			return m, cmd
		}
		return m, nil
	case shellProbeDoneMsg:
		if s.dash == nil {
			return m, nil
		}
		if msg.err != nil {
			s.dash, _ = s.dash.Update(shellExitedMsg{msgTag: t, name: msg.req.name, err: msg.err})
			return m, nil
		}
		return m, m.suspendForShell(s, msg.req)
	}
	return m, nil
}

// suspendForShell re-invokes the binary as a PTY-bridging child; bubbletea
// releases the terminal for the duration and resumes us when it exits.
func (m *Model) suspendForShell(s *Session, req containerReq) tea.Cmd {
	t := msgTag{hostID: s.host.ID, gen: s.gen}
	exe, err := os.Executable()
	if err != nil {
		return func() tea.Msg { return shellExitedMsg{msgTag: t, name: req.name, err: err} }
	}
	var stderr bytes.Buffer
	cmd := exec.Command(exe, "_shell", "--host", s.host.ID, "--cid", req.id)
	cmd.Stderr = &stderr
	return tea.ExecProcess(cmd, func(rerr error) tea.Msg {
		return shellExitedMsg{
			msgTag: t,
			name:   req.name,
			err:    rerr,
			detail: shell.FailureMessage(rerr, stderr.String()),
		}
	})
}

func (m *Model) routeInternal(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd
	switch m.view {
	case viewHosts:
		m.list, cmd = m.list.Update(msg)
	case viewForm, viewPrompt:
		m.form, _, _, _ = m.form.Update(msg)
	case viewDashboard:
		if s := m.activeSession(); s != nil && s.dash != nil {
			s.dash, cmd = s.dash.Update(msg)
		}
	case viewContainer:
		if s := m.activeSession(); s != nil && s.cv != nil {
			s.cv, cmd = s.cv.Update(msg)
		}
	}
	return m, cmd
}

func (m *Model) View() string {
	if m.quitting {
		return ""
	}
	switch m.view {
	case viewHosts:
		body := styleTitle.Render("vpstui — VPS & containers over SSH") + "\n\n" + m.list.View()
		if m.status != "" {
			body += "\n" + styleWarn.Render(m.status)
			m.status = ""
		}
		return pinFooter(body, helpLine("↑/↓ j/k", "move", "n", "new", "e", "edit", "d", "delete", "enter", "connect/resume", "q", "quit"), m.width, m.height)
	case viewForm, viewPrompt:
		return pinFooter(m.form.View(m.width), m.form.Help(), m.width, m.height)
	case viewConnecting:
		inner := m.sp.View() + " connecting to " + styleKey.Render(m.pendingHost.Target()) + "…\n" +
			styleMuted.Render("esc: keep dialing in background")
		card := panel("", inner, min(m.width-6, 60))
		out := m.tabBar()
		if out != "" {
			out += "\n"
		}
		return out + lipgloss.NewStyle().
			Width(m.width).Height(max(m.height-m.tabRows(), 1)).
			Align(lipgloss.Center, lipgloss.Center).
			Render(card)
	case viewDashboard:
		if s := m.activeSession(); s != nil && s.dash != nil {
			return m.tabBar() + "\n" + s.dash.View()
		}
		return m.tabBar()
	case viewContainer:
		if s := m.activeSession(); s != nil && s.cv != nil {
			return m.tabBar() + "\n" + s.cv.View()
		}
		return m.tabBar()
	case viewErrors:
		if m.errView == nil {
			return m.tabBar()
		}
		body := m.errView.View()
		if bar := m.tabBar(); bar != "" {
			body = bar + "\n" + body
		}
		return body
	}
	return ""
}

// tabRows is the number of layout rows the session tab bar occupies.
func (m *Model) tabRows() int {
	if len(m.sessions) == 0 {
		return 0
	}
	return 1
}

func (m *Model) tabBar() string {
	if len(m.sessions) == 0 {
		return ""
	}
	var parts []string
	for i, s := range m.sessions {
		dot := "○"
		if s.conn != nil {
			dot = "●"
		}
		label := fmt.Sprintf("%d %s %s", i+1, s.host.Name, dot)
		if i == m.active {
			parts = append(parts, lipglossActiveTab().Render(label))
		} else {
			parts = append(parts, styleMuted.Render(label))
		}
	}
	return strings.Join(parts, styleMuted.Render(" │ "))
}

func (m *Model) cleanup() {
	for _, s := range m.sessions {
		if s.cv != nil {
			s.cv.Stop()
		}
		if s.conn != nil {
			s.conn.Close()
		}
	}
	m.sessions = nil
	m.active = -1
	m.quitting = true
	if m.api != nil {
		m.api.Close()
	}
}

func (m *Model) deleteHost(h models.Host) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd
	if s := m.findSessionByHost(h.ID); s != nil {
		if s.cv != nil {
			s.cv.Stop()
		}
		if s.conn != nil {
			s.conn.Close()
		}
		wasActive := m.indexOf(s) == m.active
		m.removeSession(s)
		if len(m.sessions) == 0 {
			m.active = -1
		} else if wasActive {
			cmd = m.activate(m.active)
		}
	}
	out := m.hosts[:0]
	for _, x := range m.hosts {
		if x.ID != h.ID {
			out = append(out, x)
		}
	}
	m.hosts = out
	_ = secret.Delete(h.PasswordRef())
	_ = secret.Delete(h.PassphraseRef())
	if err := config.Save(m.hosts); err != nil {
		m.status = "could not save hosts: " + err.Error()
	}
	m.list.SetItems(m.hosts)
	m.refreshConnected()
	return m, cmd
}

func (m *Model) saveHost(msg formSubmittedMsg) (tea.Model, tea.Cmd) {
	replaced := false
	for i, x := range m.hosts {
		if x.ID == msg.host.ID {
			m.hosts[i] = msg.host
			replaced = true
		}
	}
	if !replaced {
		m.hosts = append(m.hosts, msg.host)
	}
	if err := config.Save(m.hosts); err != nil {
		m.status = "could not save hosts: " + err.Error()
	}
	if msg.secretV != "" {
		ref := msg.host.PasswordRef()
		if msg.host.AuthType == models.AuthKey {
			ref = msg.host.PassphraseRef()
		}
		if err := secret.Set(ref, msg.secretV); err != nil {
			m.status = "host saved, but secret storage failed: " + err.Error()
		}
	}
	m.form = nil
	m.list.SetItems(m.hosts)
	return m, nil
}

func (m *Model) startConnect(h models.Host) (tea.Model, tea.Cmd) {
	s := &Session{host: h, gen: m.nextGen()}
	m.sessions = append(m.sessions, s)
	return m.connectSlot(s)
}

func (m *Model) connectSlot(s *Session) (tea.Model, tea.Cmd) {
	m.active = m.indexOf(s)
	m.pendingHost, m.pendingGen = s.host, s.gen
	switch s.host.AuthType {
	case models.AuthAgent:
		m.view = viewConnecting
		return m, m.dialCmd(s.host, s.gen, "")
	case models.AuthPassword:
		pw, err := secret.Get(s.host.PasswordRef())
		if err != nil || pw == "" {
			return m.openPrompt(s.host, "Password for "+s.host.User+"@"+s.host.Address)
		}
		m.view = viewConnecting
		return m, m.dialCmd(s.host, s.gen, pw)
	case models.AuthKey:
		pass, _ := secret.Get(s.host.PassphraseRef())
		m.view = viewConnecting
		return m, m.dialCmd(s.host, s.gen, pass)
	}
	m.status = "unknown auth type for host"
	m.removeSession(s)
	return m, nil
}

func (m *Model) openPrompt(h models.Host, label string) (tea.Model, tea.Cmd) {
	m.form = NewSecretPrompt(h, label)
	m.view = viewPrompt
	return m, nil
}

func (m *Model) dialCmd(h models.Host, gen uint64, s string) tea.Cmd {
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	return func() tea.Msg {
		defer cancel()
		t := msgTag{hostID: h.ID, gen: gen}
		conn, err := sshclient.Dial(ctx, h, s)
		if err != nil {
			return connectDoneMsg{msgTag: t, err: err, host: h}
		}
		return connectDoneMsg{msgTag: t, conn: conn, host: h}
	}
}

func (m *Model) onConnectDone(msg connectDoneMsg) (tea.Model, tea.Cmd) {
	s := m.findSession(msg.hostID, msg.gen)
	if s == nil {
		if msg.conn != nil {
			msg.conn.Close()
		}
		return m, nil
	}
	es := ""
	if msg.err != nil {
		es = msg.err.Error()
	}
	if msg.err != nil {
		foreground := m.view == viewConnecting && m.pendingGen == msg.gen
		m.removeSession(s)
		if foreground {
			m.view = viewHosts
			if strings.Contains(es, "passphrase-protected") || strings.Contains(es, "no password provided") {
				ps := &Session{host: msg.host, gen: m.nextGen()}
				m.sessions = append(m.sessions, ps)
				m.active = m.indexOf(ps)
				m.pendingHost, m.pendingGen = ps.host, ps.gen
				label := "Password for "
				if strings.Contains(es, "passphrase") {
					label = "Key passphrase for "
				}
				return m.openPrompt(msg.host, label+msg.host.User+"@"+msg.host.Address)
			}
			m.status = "connect failed: " + es
			return m, nil
		}
		m.status = "connect " + msg.host.Name + " failed: " + es
		return m, nil
	}
	s.conn = msg.conn
	s.dock = docker.New(msg.conn)
	s.dash = NewDashboard(msg.host, s.dock, m.width, m.height-m.tabRows(), msg.gen)
	s.dash.SetTags(m.tags)
	m.refreshConnected()
	m.publish(s)
	if m.view == viewConnecting && m.pendingGen == msg.gen {
		m.view = viewDashboard
		m.active = m.indexOf(s)
	}
	var cmds []tea.Cmd
	if m.view == viewDashboard && m.active == m.indexOf(s) {
		cmds = append(cmds, s.dash.fetchNow(), tickStats(msg.host.ID, msg.gen))
	}
	// One perf chain per generation, for every connected host including the one
	// on screen: the tick itself decides whether a fetch is needed.
	if !s.perfArmed {
		s.perfArmed = true
		cmds = append(cmds, tickPerf(msg.host.ID, msg.gen))
	}
	if len(cmds) > 0 {
		return m, tea.Batch(cmds...)
	}
	return m, nil
}

func startLogStream(s *Session, id string, send func(tea.Msg)) tea.Cmd {
	dock := s.dock
	t := msgTag{hostID: s.host.ID, gen: s.gen}
	return func() tea.Msg {
		r, err := s.cv.Open(dock, id)
		if err != nil {
			return logEndMsg{msgTag: t, err: err}
		}
		go streamLogs(r, send, t)
		// Kick the flush chain; it reschedules itself while the stream lives.
		return tickLogs(t.hostID, t.gen)
	}
}

func tickStats(hostID string, gen uint64) tea.Cmd {
	return tea.Tick(2*time.Second, func(time.Time) tea.Msg {
		return statsTickMsg{msgTag: msgTag{hostID: hostID, gen: gen}}
	})
}

// tickPerf paces the stats-only sample for a session whose dashboard is not on
// screen. Unlike the 2 s chain it never stops when the host loses focus, because
// a frozen ranking and an empty sparkline are exactly what this exists to avoid.
func tickPerf(hostID string, gen uint64) tea.Cmd {
	return tea.Tick(perfEvery, func(time.Time) tea.Msg {
		return perfTickMsg{msgTag: msgTag{hostID: hostID, gen: gen}}
	})
}

// tickLogs paces the log view's SetContent. Rendering per line would make the
// viewport re-split and re-scan its whole buffer each time, which is quadratic
// once the history is multiple megabytes.
func tickLogs(hostID string, gen uint64) tea.Cmd {
	return tea.Tick(logFlushEvery, func(time.Time) tea.Msg {
		return logFlushMsg{msgTag: msgTag{hostID: hostID, gen: gen}}
	})
}

func fmtDuration(sec float64) string {
	d := int64(sec)
	switch {
	case d < 60:
		return fmt.Sprintf("%ds", d)
	case d < 3600:
		return fmt.Sprintf("%dm", d/60)
	case d < 86400:
		return fmt.Sprintf("%dh %dm", d/3600, (d%3600)/60)
	default:
		return fmt.Sprintf("%dd %dh", d/86400, (d%86400)/3600)
	}
}
