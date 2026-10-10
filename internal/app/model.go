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

	"vpstui/internal/config"
	"vpstui/internal/docker"
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
type detailLoadedMsg struct {
	msgTag
	detail models.ContainerDetail
	err    error
}

type diskLoadedMsg struct {
	msgTag
	objs []models.DiskObject
	err  error
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
// session's dashboard ticks; background sessions pause but keep buffering
// log chunks.
type Session struct {
	host models.Host
	gen  uint64
	conn *sshclient.Conn
	dock *docker.Executor
	dash *Dashboard
	cv   *ContainerView
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

	send     func(tea.Msg)
	quitting bool
}

func New(hosts []models.Host) *Model {
	m := &Model{
		hosts:  hosts,
		view:   viewHosts,
		active: -1,
		sp:     spinner.New(spinner.WithSpinner(spinner.Dot)),
	}
	m.list = NewHostList(hosts)
	return m
}

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
		for _, s := range m.sessions {
			if s.dash != nil {
				s.dash.SetSize(msg.Width, msg.Height)
			}
			if s.cv != nil {
				s.cv.SetSize(msg.Width, msg.Height)
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

	case statsTickMsg, statsLoadedMsg, actionDoneMsg, logChunkMsg, logEndMsg, detailLoadedMsg, diskLoadedMsg, shellExitedMsg, shellProbeDoneMsg:
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
		if handled, cmd := m.sessionKeys(msg.String()); handled {
			return m, cmd
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
			s.cv = NewContainerView(req.id, req.name, m.width, m.height)
			s.cv.hostID = s.dash.hostID
			s.cv.gen = s.dash.gen
			m.view = viewContainer
			return m, startLogStream(s, req.id, m.send)
		}
		if req, ok := s.dash.OpenShell(); ok {
			return m, s.dash.probeShell(req)
		}
		return m, cmd

	case viewContainer:
		if handled, cmd := m.sessionKeys(msg.String()); handled {
			return m, cmd
		}
		key := msg.String()
		s := m.activeSession()
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
	case detailLoadedMsg:
		t = msg.msgTag
	case diskLoadedMsg:
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
		out := styleTitle.Render("vpstui — VPS & containers over SSH") + "\n\n" + m.list.View()
		if m.status != "" {
			out += "\n" + styleWarn.Render(m.status)
			m.status = ""
		}
		out += "\n" + helpLine("n", "new", "e", "edit", "d", "delete", "enter", "connect/resume", "q", "quit")
		return out
	case viewForm, viewPrompt:
		return m.form.View(m.width)
	case viewConnecting:
		return m.tabBar() + "\n" + m.sp.View() + " connecting to " + styleKey.Render(m.pendingHost.Target()) + "…" +
			styleMuted.Render("  (esc: keep dialing in background)")
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
	}
	return ""
}

func (m *Model) tabBar() string {
	if len(m.sessions) == 0 {
		return ""
	}
	var parts []string
	for i, s := range m.sessions {
		mark := "○"
		if s.conn != nil {
			mark = "●"
		}
		label := fmt.Sprintf("%d %s %s", i+1, s.host.Name, mark)
		if i == m.active {
			parts = append(parts, lipglossActiveTab().Render(label))
		} else {
			parts = append(parts, styleMuted.Render(label))
		}
	}
	return strings.Join(parts, "  ")
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
	s.dash = NewDashboard(msg.host, s.dock, m.width, m.height, msg.gen)
	m.refreshConnected()
	if m.view == viewConnecting && m.pendingGen == msg.gen {
		m.view = viewDashboard
		m.active = m.indexOf(s)
	}
	if m.view == viewDashboard && m.active == m.indexOf(s) {
		return m, tea.Batch(s.dash.fetchNow(), tickStats(msg.host.ID, msg.gen))
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
		return nil
	}
}

func tickStats(hostID string, gen uint64) tea.Cmd {
	return tea.Tick(2*time.Second, func(time.Time) tea.Msg {
		return statsTickMsg{msgTag: msgTag{hostID: hostID, gen: gen}}
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
