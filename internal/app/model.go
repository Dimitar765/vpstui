package app

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
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
	viewSettings
	// Hub mode (Phase C2) appends after the direct-mode screens: route(),
	// inputOwning, helpRows and viewBody all switch on this enum, and the hub
	// screens extend each at the end so existing cases never move.
	viewHubFleet
	viewHubApps
	viewHubDeployments
	viewHubLogs
	viewHubForm
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

// logOpenedMsg carries an opened log stream to the UI goroutine, which alone
// may set cv.reader (Attach). Opening used to happen inside the cmd goroutine
// and raced cv.Stop/cv.Update on those fields.
type logOpenedMsg struct {
	msgTag
	reader io.ReadCloser
}

// retryMsg fires once, retryAfter after a typed connection loss, to redial the
// session whose dashboard showed the banner. Log-stream drops are not retried;
// the user reopens the stream.
type retryMsg struct {
	msgTag
}

// retryAfter paces the single automatic reconnect. A var so tests can shrink it.
var retryAfter = 5 * time.Second

// severity colours a toast line: info green, warn amber, error red.
type severity int

const (
	sevInfo severity = iota
	sevWarn
	sevError
)

// toast is one entry of the model-level notification queue. TTL is toastTTL;
// expiry is an Update-time event (toastExpireMsg) so a toast survives repaints.
type toast struct {
	id  int
	msg string
	sev severity
	at  time.Time
}

const toastTTL = 5 * time.Second
const toastMax = 3

// toastExpireMsg drops the toast with the given id. One tick per toast keeps
// the "clears on its own tick" semantics the single dashboard toast had.
type toastExpireMsg struct{ id int }

func tickToast(id int) tea.Cmd {
	return tea.Tick(toastTTL, func(time.Time) tea.Msg { return toastExpireMsg{id} })
}

// persistDoneMsg reports the outcome of a config/secret write that ran off the
// UI goroutine. Success feedback is synchronous at the call site; only
// failures come back here.
type persistDoneMsg struct {
	what string
	err  error
}

// persistCmd runs a blocking file/keyring write in its own goroutine, so a
// hung keyring daemon cannot freeze the UI thread. fn must only touch values
// captured before the call — never live model state.
func persistCmd(what string, fn func() error) tea.Cmd {
	return func() tea.Msg {
		return persistDoneMsg{what: what, err: fn()}
	}
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

type imagesLoadedMsg struct {
	msgTag
	images []models.Image
	err    error
}

// eventsMsg carries one docker container lifecycle event from the per-host
// events stream to the UI goroutine. It is a refresh accelerator only.
type eventsMsg struct {
	msgTag
	ev docker.Event
}

type exportDoneMsg struct {
	msgTag
	path string
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

	// retried limits the automatic reconnect to one attempt per generation:
	// the first typed connection loss arms a ~5s redial, a second loss leaves
	// the manual "press c" banner. Reset when a connect succeeds.
	retried bool
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

	// cfg is the loaded settings.json; pal the `:` command palette; helpOpen
	// the ? overlay. toasts is the model-level notification queue rendered
	// bottom-right.
	cfg      config.Settings
	pal      palette
	helpOpen bool
	toasts   []toast
	toastSeq int

	settingsForm *settingsForm

	// Hub mode state. hub is the daemon connection (nil until connected);
	// hubLog reuses ContainerView for live deployment logs; hubForm collects
	// URL + token; hubConfirm holds the pending deploy/rollback confirmation.
	hub            *hubState
	hubLog         *ContainerView
	hubLogStop     func()
	hubLogDep      string
	hubLogVerb     string // "deploy" | "rollback" | "" (just viewing)
	hubForm        *hubForm
	hubConfirm     string
	hubConfirmName string

	// startCmd is run by Init, so main can hand --hub startup work to the UI
	// goroutine before the first frame.
	startCmd tea.Cmd

	send     func(tea.Msg)
	quitting bool
	api      *httpapi.Store
}

func New(hosts []models.Host, tags map[string]models.Tag) *Model {
	return NewWithSettings(hosts, tags, config.DefaultSettings())
}

// NewWithSettings builds the model with user preferences already resolved.
func NewWithSettings(hosts []models.Host, tags map[string]models.Tag, cfg config.Settings) *Model {
	m := &Model{
		hosts:  hosts,
		view:   viewHosts,
		active: -1,
		sp:     spinner.New(spinner.WithSpinner(spinner.Dot)),
		api:    httpapi.NewStore(),
		tags:   NewTagStore(tags),
		tag:    newTagPrompt(),
		cfg:    cfg,
		pal:    newPalette(),
	}
	applyTheme(cfg.Theme)
	m.list = NewHostList(hosts)
	return m
}

// HTTPStore exposes the live-session snapshot store to the embedded JSON API.
func (m *Model) HTTPStore() *httpapi.Store { return m.api }

// Attach lets background goroutines deliver messages to the program.
func (m *Model) Attach(send func(tea.Msg)) { m.send = send }

func (m *Model) Init() tea.Cmd {
	if m.startCmd != nil {
		return tea.Batch(m.sp.Tick, m.startCmd)
	}
	return m.sp.Tick
}

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
		if m.hub != nil {
			m.hub.SetSize(msg.Width, contentH)
		}
		if m.hubLog != nil {
			m.hubLog.SetSize(msg.Width, contentH)
		}
		if m.hubForm != nil {
			m.hubForm.SetSize(msg.Width)
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

	case tea.MouseMsg:
		return m.mouseEvent(msg)

	case toastExpireMsg:
		m.dropToast(msg.id)
		return m, nil

	case connectDoneMsg:
		return m.onConnectDone(msg)

	case persistDoneMsg:
		return m.onPersistDone(msg)

	case hubMsg:
		return m.routeHub(msg)

	case statsTickMsg, statsLoadedMsg, actionDoneMsg, logChunkMsg, logEndMsg, logOpenedMsg, logFlushMsg, detailLoadedMsg, diskLoadedMsg, imagesLoadedMsg, eventsMsg, exportDoneMsg, errLoadedMsg, shellExitedMsg, shellProbeDoneMsg, peaksLoadedMsg, perfTickMsg, perfLoadedMsg, retryMsg:
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
	// The palette owns every key while open, before anything else looks.
	if m.pal.open {
		return m.updatePalette(msg)
	}
	// The ? overlay is a mode: while open it shows the current screen's key
	// table and any key closes it. Inputs that own typing suppress the toggle.
	if m.helpOpen {
		m.helpOpen = false
		return m, nil
	}
	if msg.String() == "?" && !m.tag.active && !m.inputOwning() {
		m.helpOpen = true
		return m, nil
	}
	if msg.String() == ":" && !m.tag.active && !m.inputOwning() {
		return m, m.openPalette()
	}
	if m.view == viewHosts {
		// Status expiry is an Update-time event, like the dashboard toast:
		// the line survives repaints and clears on the next keypress.
		m.status = ""
	}
	switch m.view {
	case viewHosts:
		if msg.String() == "q" {
			m.cleanup()
			return m, tea.Quit
		}
		if msg.String() == "a" {
			return m.openErrors()
		}
		// The pinned hub tab is reachable from the host list too: tab moves
		// to it, H opens the connect form.
		if m.hub != nil {
			switch msg.String() {
			case "tab":
				return m, m.cycleTab(1)
			case "shift+tab":
				return m, m.cycleTab(-1)
			}
		}
		if msg.String() == "H" {
			url := ""
			if m.hub != nil {
				url = m.hub.url
			}
			m.hubForm = newHubForm(url)
			m.view = viewHubForm
			return m, nil
		}
		if msg.String() == "S" {
			m.settingsForm = NewSettingsForm(m.cfg)
			m.view = viewSettings
			return m, nil
		}
		var cmd tea.Cmd
		m.list, cmd = m.list.Update(msg)
		return m, cmd

	case viewSettings:
		next, cmd, saved, cancelled := m.settingsForm.Update(msg)
		m.settingsForm = next
		if saved {
			// Theme applies to the running session immediately; the write itself
			// runs off-thread via the form's persist command.
			s := m.settingsForm.saved
			m.cfg.Theme = s.Theme
			m.cfg.Mouse = s.Mouse
			m.cfg.LogWrap = s.LogWrap
			applyTheme(s.Theme)
			for _, ses := range m.sessions {
				if ses.cv != nil {
					ses.cv.wrap = s.LogWrap
					ses.cv.rebuildRows()
				}
			}
			m.settingsForm = nil
			m.view = viewHosts
			return m, tea.Batch(cmd, m.toast("settings saved", sevInfo))
		}
		if cancelled {
			m.view = viewHosts
			m.settingsForm = nil
			return m, nil
		}
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
			var cmd tea.Cmd
			if s != "" {
				ref := h.PasswordRef()
				if h.AuthType == models.AuthKey {
					ref = h.PassphraseRef()
				}
				// The dial carries the secret itself; storage happens beside
				// it, off the UI thread.
				cmd = persistCmd("storing the secret", func() error { return secret.Set(ref, s) })
			}
			return m, tea.Batch(cmd, m.dialCmd(h, m.pendingGen, s))
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
		s := m.activeSession()
		// The dashboard filter input owns the keys while open — same rule as
		// the log view's filter below.
		if s == nil || s.dash == nil || !s.dash.Filtering() {
			if handled, cmd := m.sessionKeys(msg.String()); handled {
				return m, cmd
			}
		}
		switch msg.String() {
		case "a":
			return m.openErrors()
		case "t":
			return m.openTagPrompt()
		case "T":
			return m.untagCursor()
		}
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
			s.cv.wrap = m.cfg.LogWrap
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
			if wrap, changed := s.cv.TakeWrapToggle(); changed {
				m.cfg.LogWrap = wrap
				return m, tea.Batch(cmd, persistCmd("saving settings", func() error {
					return config.SaveSettings(m.settingsSnapshot(wrap))
				}))
			}
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

	case viewHubForm:
		next, cmd, submitted, cancelled := m.hubForm.Update(msg)
		m.hubForm = next
		if submitted {
			u, tok := m.hubForm.URL(), m.hubForm.Token()
			m.hubForm = nil
			var cmds []tea.Cmd
			if tok != "" {
				// The keyring write runs off the UI thread; the connection
				// starts immediately with the in-hand token either way.
				ref := HubTokenRef(u)
				cmds = append(cmds, persistCmd("storing the hub token", func() error {
					return secret.Set(ref, tok)
				}))
			}
			cmds = append(cmds, m.ConnectHub(u, tok))
			return m, tea.Batch(cmds...)
		}
		if cancelled {
			m.hubForm = nil
			m.view = viewHosts
			return m, nil
		}
		return m, cmd

	case viewHubFleet, viewHubApps, viewHubDeployments, viewHubLogs:
		return m.updateHub(msg)
	}
	return m, nil
}

// settingsSnapshot copies the model's current settings with the given wrap
// mode, for persisting after a `w` toggle in the log view.
func (m *Model) settingsSnapshot(wrap bool) config.Settings {
	s := m.cfg
	s.LogWrap = wrap
	return s
}

// inputOwning reports whether a text input currently owns the keys, so `?` and
// `:` must not hijack typing.
func (m *Model) inputOwning() bool {
	switch m.view {
	case viewForm, viewPrompt, viewSettings, viewHubForm:
		return true
	case viewContainer:
		if s := m.activeSession(); s != nil && s.cv != nil {
			return s.cv.Searching()
		}
	case viewDashboard:
		if s := m.activeSession(); s != nil && s.dash != nil {
			return s.dash.Filtering()
		}
	case viewHubLogs:
		return m.hubLog != nil && m.hubLog.Searching()
	}
	return false
}

// toast queues a severity-coloured notification, capped, and returns the
// expiry tick command the caller must return — the model-level replacement
// for the single dashboard line, with its clears-on-tick semantics per toast.
func (m *Model) toast(msg string, sev severity) tea.Cmd {
	if msg == "" {
		return nil
	}
	m.toastSeq++
	t := toast{id: m.toastSeq, msg: msg, sev: sev, at: time.Now()}
	m.toasts = append(m.toasts, t)
	if len(m.toasts) > toastMax {
		m.toasts = m.toasts[len(m.toasts)-toastMax:]
	}
	return tickToast(t.id)
}

func (m *Model) dropToast(id int) {
	out := m.toasts[:0]
	for _, t := range m.toasts {
		if t.id != id {
			out = append(out, t)
		}
	}
	m.toasts = out
}

// toastCmds schedules one expiry tick per queued toast.
func (m *Model) toastCmds() tea.Cmd {
	if len(m.toasts) == 0 {
		return nil
	}
	var cmds []tea.Cmd
	for _, t := range m.toasts {
		cmds = append(cmds, tickToast(t.id))
	}
	return tea.Batch(cmds...)
}

// applyToasts splices the toast lines into the frame's bottom-right corner,
// stacked upward from just above the footer row.
func (m *Model) applyToasts(frame string, w, h int) string {
	if len(m.toasts) == 0 {
		return frame
	}
	lines := strings.Split(frame, "\n")
	base := len(lines) - 1 // the footer (or last body) row stays untouched
	for i := range m.toasts {
		t := m.toasts[len(m.toasts)-1-i] // newest at the bottom of the stack
		y := base - 1 - i
		if y < 0 || y >= len(lines) {
			break
		}
		line := toastStyle(t.sev).Render(fitPlain(t.msg, max(w-2, 8)))
		pad := clampInt(w-lipgloss.Width(line), 0, w)
		lines[y] = padRight(strings.TrimRight(lines[y], " "), pad) + line
	}
	return strings.Join(lines, "\n")
}

// mouseEvent translates mouse activity into the focused screen's native
// actions: wheel = j/k, tab-bar click = switch session, dashboard clicks land
// on the table via Dashboard.Click.
func (m *Model) mouseEvent(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	switch msg.Button {
	case tea.MouseButtonWheelUp:
		return m.updateKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'k'}})
	case tea.MouseButtonWheelDown:
		return m.updateKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
	case tea.MouseButtonLeft:
		if msg.Action != tea.MouseActionPress {
			return m, nil
		}
		switch m.view {
		case viewDashboard, viewContainer, viewErrors:
			if msg.Y == 0 && (len(m.sessions) > 0 || m.hub != nil) {
				if i := m.tabIndexAt(msg.X); i >= 0 {
					if i == len(m.sessions) {
						return m, m.activateHub()
					}
					return m, m.activate(i)
				}
				return m, nil
			}
			y := msg.Y - m.tabRows()
			if m.view == viewDashboard {
				if s := m.activeSession(); s != nil && s.dash != nil {
					s.dash.Click(msg.X, y)
				}
			}
			return m, nil
		case viewHubFleet, viewHubApps, viewHubDeployments:
			// The tab bar row clicks switch surfaces; clicks in the table
			// body move the cursor row.
			if msg.Y == 0 {
				if i := m.tabIndexAt(msg.X); i >= 0 {
					if i == len(m.sessions) {
						return m, m.activateHub()
					}
					return m, m.activate(i)
				}
				return m, nil
			}
			if h := m.hub; h != nil {
				y := msg.Y - m.tabRows() - 2 // title + blank line above the table
				switch m.view {
				case viewHubFleet:
					if h.showContainers {
						h.contTbl.ClickRow(y)
					} else {
						h.fleetTbl.ClickRow(y)
					}
				case viewHubApps:
					h.appsTbl.ClickRow(y)
				case viewHubDeployments:
					h.depsTbl.ClickRow(y)
				}
			}
			return m, nil
		}
		return m, nil
	}
	return m, nil
}

// tabIndexAt maps an x coordinate on the tab bar to a tab index: a session
// index, or len(sessions) for the pinned hub tab; -1 between or past labels.
func (m *Model) tabIndexAt(x int) int {
	pos := 0
	for i, s := range m.sessions {
		label := fmt.Sprintf("%d %s ●", i+1, s.host.Name)
		w := lipgloss.Width(label) + 2 // active-tab padding is part of the cell
		if x >= pos && x < pos+w {
			return i
		}
		pos += w + 3 // " │ " separator
	}
	if m.hub != nil {
		if x >= pos && x < pos+lipgloss.Width("hub")+2 {
			return len(m.sessions)
		}
	}
	return -1
}

// sessionKeys handles multi-session navigation from dashboard/log views.
func (m *Model) sessionKeys(key string) (bool, tea.Cmd) {
	if len(m.sessions) == 0 {
		return false, nil
	}
	switch key {
	case "tab":
		return true, m.cycleTab(1)
	case "shift+tab":
		return true, m.cycleTab(-1)
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
	s.cv.wrap = m.cfg.LogWrap
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
	return m, m.afterTagChange(t.hostID, "tag cleared from "+t.name, false)
}

// updateTag runs the prompt. Every key it consumes is stopped here, so nothing
// else on the screen can react to typing.
func (m *Model) updateTag(msg tea.Msg) (tea.Model, tea.Cmd) {
	outcome, cmd := m.tag.Update(msg)
	switch outcome {
	case promptCommit:
		cmd = m.commitTag()
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

func (m *Model) commitTag() tea.Cmd {
	t := m.tag.target
	label, ok := m.tag.value()
	if !ok {
		// Emptied (or nothing but separators): the prefilled label was deleted on
		// purpose, so this is an untag.
		if m.tags.Clear(t.hostID, t.name) {
			return m.afterTagChange(t.hostID, "tag cleared from "+t.name, false)
		}
		m.tagFeedback("tag needs some text", true)
		return nil
	}
	if m.tags.Set(t.hostID, t.name, label, m.tag.color, t.id) {
		return m.afterTagChange(t.hostID, "tagged "+t.name+" as "+label, false)
	}
	m.syncTagPrompt()
	return nil
}

// afterTagChange persists the label and repaints every surface that shows it.
// The success message goes out synchronously; the write itself runs off the UI
// thread and reports failures via persistDoneMsg next to it.
func (m *Model) afterTagChange(hostID, msg string, isErr bool) tea.Cmd {
	tags := m.tags.All()
	m.repaintTag(hostID)
	// Last, because refreshing the Errors screen clears a note set before it.
	m.tagFeedback(msg, isErr)
	m.syncTagPrompt()
	return persistCmd("saving tags", func() error { return config.SaveTags(tags) })
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
	case logOpenedMsg:
		t = msg.msgTag
	case logFlushMsg:
		t = msg.msgTag
	case detailLoadedMsg:
		t = msg.msgTag
	case errLoadedMsg:
		t = msg.msgTag
	case diskLoadedMsg:
		t = msg.msgTag
	case imagesLoadedMsg:
		t = msg.msgTag
	case eventsMsg:
		t = msg.msgTag
	case exportDoneMsg:
		t = msg.msgTag
	case peaksLoadedMsg:
		t = msg.msgTag
	case perfTickMsg:
		t = msg.msgTag
	case retryMsg:
		t = msg.msgTag
	case perfLoadedMsg:
		t = msg.msgTag
	case shellExitedMsg:
		t = msg.msgTag
	case shellProbeDoneMsg:
		t = msg.msgTag
	}
	// The hub log view shares logFlushMsg; its tag carries the hub host id,
	// and hub messages never look up a session.
	if t.hostID == hubHostID {
		return m.routeHubLogFlush(t)
	}
	s := m.findSession(t.hostID, t.gen)
	if s == nil {
		// A stream that opens after its session is gone would otherwise leak;
		// close it instead of attaching nowhere.
		if lo, ok := msg.(logOpenedMsg); ok {
			lo.reader.Close()
		}
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
			var dcmd tea.Cmd
			s.dash, dcmd = s.dash.Update(msg)
			m.publish(s)
			// One sweep and one cgroup probe per generation, both started by the
			// first container list that succeeded — never by the 2 s tick beside it.
			var cmds []tea.Cmd
			if dcmd != nil {
				cmds = append(cmds, dcmd)
			}
			if msg.listOK {
				if !s.dash.evArmed {
					s.dash.evArmed = true
					cmds = append(cmds, startEvents(s, m.send))
				}
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
			} else if msg.err != nil && errors.Is(msg.err, sshclient.ErrConnLost) && !s.retried {
				// First typed loss on this generation: redial once, ~5s out.
				// A retry failure or a second loss leaves the "press c" banner
				// that Dashboard.Update just raised.
				s.retried = true
				cmds = append(cmds, tea.Tick(retryAfter, func(time.Time) tea.Msg {
					return retryMsg{msgTag: t}
				}))
			}
			if len(cmds) > 0 {
				return m, tea.Batch(cmds...)
			}
		}
		return m, nil
	case retryMsg:
		// The dashboard is gone if reconnect already ran; disconnected is false
		// if the session recovered (or the user pressed c) before we fired.
		if s.dash != nil && s.dash.disconnected {
			return m.reconnect(s)
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
	case logOpenedMsg:
		if s.cv != nil {
			s.cv.Attach(msg.reader)
		} else {
			msg.reader.Close()
		}
		return m, tickLogs(t.hostID, t.gen)
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
	case imagesLoadedMsg:
		if s.dash != nil {
			s.dash, _ = s.dash.Update(msg)
			m.publish(s)
		}
		return m, nil
	case eventsMsg:
		// Only the dashboard on screen reacts: background hosts must not gain
		// SSH traffic their paused tick deliberately avoids.
		if s.dash == nil || m.activeSession() != s || s.dash.fetchBusy {
			if s.dash != nil && m.activeSession() == s {
				s.dash.evPending = true
			}
			return m, nil
		}
		return m, s.dash.fetchNow()
	case exportDoneMsg:
		if s.cv != nil {
			s.cv.exporting = false
			if msg.err != nil {
				s.cv.SetNote("export failed: " + msg.err.Error())
			} else {
				s.cv.SetNote("saved: " + msg.path)
			}
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
	case viewSettings:
		if m.settingsForm != nil {
			m.settingsForm, _, _, _ = m.settingsForm.Update(msg)
		}
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
	frame := m.viewBody()
	frame = m.applyToasts(frame, m.width, m.height)
	if m.pal.open {
		frame = pasteOver(frame, m.palView(), m.width, m.height)
	}
	if m.helpOpen {
		frame = helpOverlay(frame, m.helpTitle(), m.helpRows(), m.width, m.height)
	}
	return frame
}

// helpTitle names the screen the overlay is showing.
func (m *Model) helpTitle() string {
	switch m.view {
	case viewHosts:
		return "hosts"
	case viewForm:
		return "host form"
	case viewPrompt:
		return "unlock"
	case viewDashboard:
		if s := m.activeSession(); s != nil && s.dash != nil {
			return s.dash.helpTitle()
		}
	case viewContainer:
		return "logs"
	case viewErrors:
		return "errors"
	case viewSettings:
		return "settings"
	case viewHubFleet:
		return "hub fleet"
	case viewHubApps:
		return "hub apps"
	case viewHubDeployments:
		return "hub deployments"
	case viewHubLogs:
		return "logs"
	case viewHubForm:
		return "hub connect"
	}
	return "keys"
}

func (m *Model) viewBody() string {
	if m.quitting {
		return ""
	}
	switch m.view {
	case viewSettings:
		if m.settingsForm != nil {
			return pinFooter(m.settingsForm.View(m.width), m.settingsForm.Help(), m.width, m.height)
		}
		return ""
	case viewHosts:
		body := styleTitle.Render("vpstui — VPS & containers over SSH") + "\n\n" + m.list.View()
		if m.status != "" {
			body += "\n" + styleWarn.Render(m.status)
		}
		// Hub mode pins its tab onto the host list as well; pure direct-mode
		// rendering (no hub) stays byte-identical to before.
		if m.hub != nil {
			body = m.tabBar() + "\n" + body
		}
		return pinFooter(body, helpBar(m.helpRows(), 5), m.width, m.height)
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
	case viewHubFleet, viewHubApps, viewHubDeployments, viewHubLogs:
		return m.hubBody()
	case viewHubForm:
		if m.hubForm != nil {
			return pinFooter(m.hubForm.View(m.width), m.hubForm.Help(), m.width, m.height)
		}
		return ""
	}
	return ""
}

// tabRows is the number of layout rows the session tab bar occupies. The
// pinned hub tab keeps the bar up even with no direct session connected.
func (m *Model) tabRows() int {
	if len(m.sessions) == 0 && m.hub == nil {
		return 0
	}
	return 1
}

func (m *Model) tabBar() string {
	if len(m.sessions) == 0 && m.hub == nil {
		return ""
	}
	var parts []string
	for i, s := range m.sessions {
		dot := "○"
		if s.conn != nil {
			dot = "●"
		}
		label := fmt.Sprintf("%d %s %s", i+1, s.host.Name, dot)
		if i == m.active && !isHubView(m.view) {
			parts = append(parts, lipglossActiveTab().Render(label))
		} else {
			parts = append(parts, styleMuted.Render(label))
		}
	}
	if m.hub != nil {
		if isHubView(m.view) {
			parts = append(parts, lipglossActiveTab().Render("hub"))
		} else {
			parts = append(parts, styleMuted.Render("hub"))
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
	m.stopHub()
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
	// The write and secret cleanup run off the UI thread; both refs are for the
	// same removal, so the first failure reports.
	pwRef, ppRef := h.PasswordRef(), h.PassphraseRef()
	hosts := make([]models.Host, len(m.hosts))
	copy(hosts, m.hosts)
	cmd = tea.Batch(cmd, persistCmd("saving hosts", func() error {
		if err := secret.Delete(pwRef); err != nil {
			return err
		}
		if err := secret.Delete(ppRef); err != nil {
			return err
		}
		return config.Save(hosts)
	}))
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
	// The write and the secret storage run off the UI thread; failures arrive
	// as persistDoneMsg and land in the same status row the sync errors used.
	hosts := make([]models.Host, len(m.hosts))
	copy(hosts, m.hosts)
	var cmd tea.Cmd = persistCmd("saving hosts", func() error { return config.Save(hosts) })
	if msg.secretV != "" {
		ref := msg.host.PasswordRef()
		if msg.host.AuthType == models.AuthKey {
			ref = msg.host.PassphraseRef()
		}
		secretV := msg.secretV
		cmd = tea.Batch(cmd, persistCmd("storing the secret", func() error { return secret.Set(ref, secretV) }))
	}
	m.form = nil
	m.list.SetItems(m.hosts)
	return m, cmd
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
			return m, m.toast("connect failed: "+es, sevError)
		}
		m.status = "connect " + msg.host.Name + " failed: " + es
		return m, m.toast("connect "+msg.host.Name+" failed: "+es, sevError)
	}
	s.conn = msg.conn
	s.retried = false // a fresh generation earns its one automatic retry back
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

// onPersistDone surfaces a background write failure where the user is looking.
// A success toast for the same operation may already be on screen — known and
// accepted: the correction lands moments later, and the writes are atomic.
func (m *Model) onPersistDone(msg persistDoneMsg) (tea.Model, tea.Cmd) {
	if msg.err == nil {
		return m, nil
	}
	tcmd := m.toast(msg.what+" failed: "+msg.err.Error(), sevError)
	if m.view == viewDashboard {
		if s := m.activeSession(); s != nil && s.dash != nil {
			s.dash.SetToast(msg.what+" failed: "+msg.err.Error(), true)
			return m, tcmd
		}
	}
	m.status = msg.what + " failed: " + msg.err.Error()
	return m, tcmd
}

// startEvents opens one docker events stream per generation. Each parsed
// container event nudges the active session's dashboard to refetch; the
// 2s tick remains the floor. The stream dies with the connection, and a
// fresh Dashboard (every reconnect mints one) re-arms it via evArmed.
func startEvents(s *Session, send func(tea.Msg)) tea.Cmd {
	dock := s.dock
	t := msgTag{hostID: s.host.ID, gen: s.gen}
	return func() tea.Msg {
		r, err := dock.StreamEvents(context.Background())
		if err != nil {
			return nil // events are an accelerator; the tick still refreshes
		}
		go func() {
			defer r.Close()
			sc := bufio.NewScanner(r)
			sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
			for sc.Scan() {
				if ev, ok := docker.ParseEvent(sc.Text()); ok && send != nil {
					send(eventsMsg{msgTag: t, ev: ev})
				}
			}
		}()
		return nil
	}
}

// exportLogs writes the whole buffered history to
// <DataDir>/logs/<host>-<container>-<timestamp>.log off the UI goroutine,
// via the atomic-write helper.
func exportLogs(c *ContainerView) tea.Cmd {
	t := msgTag{hostID: c.hostID, gen: c.gen}
	host, name := sanitizeFileToken(c.hostID), sanitizeFileToken(c.name)
	lines := append([]string(nil), c.lines...) // copy: Append may extend the original while we write
	return func() tea.Msg {
		dir, err := config.DataDir()
		if err != nil {
			return exportDoneMsg{msgTag: t, err: err}
		}
		path := filepath.Join(dir, "logs",
			host+"-"+name+"-"+time.Now().Format("20060102-150405")+".log")
		if err := config.WriteFileAtomic(path, []byte(strings.Join(lines, "\n")+"\n"), 0600); err != nil {
			return exportDoneMsg{msgTag: t, err: err}
		}
		return exportDoneMsg{msgTag: t, path: path}
	}
}

// sanitizeFileToken keeps filename-safe runes and replaces everything else
// (docker names may contain any printable character).
func sanitizeFileToken(s string) string {
	if s == "" {
		return "container"
	}
	var b strings.Builder
	for _, r := range s {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' ||
			r == '.' || r == '_' || r == '-' {
			b.WriteRune(r)
			continue
		}
		b.WriteByte('_')
	}
	return b.String()
}

func startLogStream(s *Session, id string, send func(tea.Msg)) tea.Cmd {
	dock := s.dock
	t := msgTag{hostID: s.host.ID, gen: s.gen}
	return func() tea.Msg {
		// Opened entirely in this cmd goroutine; the reader is handed to the
		// UI goroutine via logOpenedMsg, so cv.reader is only touched there.
		r, err := dock.StreamLogs(context.Background(), id, logTail())
		if err != nil {
			return logEndMsg{msgTag: t, err: err}
		}
		go streamLogs(r, send, t)
		return logOpenedMsg{msgTag: t, reader: r}
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
