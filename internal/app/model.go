package app

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"

	"vpstui/internal/config"
	"vpstui/internal/docker"
	"vpstui/internal/models"
	"vpstui/internal/secret"
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

type Model struct {
	hosts []models.Host
	view  View

	width  int
	height int
	status string

	list *HostList
	form *HostForm

	sp spinner.Model

	pendingHost models.Host
	pendingGen  uint64
	genSeq      uint64

	conn *sshclient.Conn
	dock *docker.Executor
	dash *Dashboard
	cv   *ContainerView

	send     func(tea.Msg)
	quitting bool
}

func New(hosts []models.Host) *Model {
	m := &Model{
		hosts: hosts,
		view:  viewHosts,
		sp:    spinner.New(spinner.WithSpinner(spinner.Dot)),
	}
	m.list = NewHostList(hosts)
	return m
}

// Attach lets background goroutines deliver messages to the program.
func (m *Model) Attach(send func(tea.Msg)) { m.send = send }

func (m *Model) Init() tea.Cmd { return m.sp.Tick }

func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.list.SetSize(msg.Width, msg.Height)
		if m.form != nil {
			m.form.SetSize(msg.Width)
		}
		if m.dash != nil {
			m.dash.SetSize(msg.Width, msg.Height)
		}
		if m.cv != nil {
			m.cv.SetSize(msg.Width, msg.Height)
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

	case statsTickMsg, statsLoadedMsg, actionDoneMsg, logChunkMsg, logEndMsg:
		return m.route(msg)

	case connectHostReq:
		return m.startConnect(msg.host)

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
			return m.connectWithSecret(h, s)
		}
		if cancelled {
			m.form = nil
			m.view = viewHosts
			return m, nil
		}
		return m, cmd

	case viewDashboard:
		dash, cmd := m.dash.Update(msg)
		m.dash = dash
		if m.dash.TakeReconnect() {
			h := m.dash.host
			m.conn.Close()
			m.conn = nil
			m.dock = nil
			m.dash = nil
			m.cv = nil
			return m.startConnect(h)
		}
		if m.dash.GoBack() {
			m.conn.Close()
			m.conn = nil
			m.dock = nil
			m.dash = nil
			m.view = viewHosts
			m.status = ""
			return m, nil
		}
		if req, ok := m.dash.OpenLogs(); ok {
			m.cv = NewContainerView(req.id, req.name, m.width, m.height)
			m.cv.hostID = m.dash.hostID
			m.cv.gen = m.dash.gen
			m.view = viewContainer
			return m, m.startLogStream(m.cv, req.id)
		}
		return m, cmd

	case viewContainer:
		key := msg.String()
		if key == "esc" || key == "q" {
			m.cv.Stop()
			m.cv = nil
			m.view = viewDashboard
			return m, tea.Batch(m.dash.fetchNow(), tickStats(m.dash.hostID, m.dash.gen))
		}
		var cmd tea.Cmd
		m.cv, cmd = m.cv.Update(msg)
		return m, cmd
	}
	return m, nil
}

func (m *Model) route(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case statsTickMsg:
		if m.view == viewDashboard && m.dash.tag(msg.hostID, msg.gen) {
			m.dash, _ = m.dash.Update(msg)
			return m, tea.Batch(m.dash.fetchNow(), tickStats(msg.hostID, msg.gen))
		}
		return m, nil
	case statsLoadedMsg:
		if m.view == viewDashboard && m.dash.tag(msg.hostID, msg.gen) {
			dash, cmd := m.dash.Update(msg)
			m.dash = dash
			return m, cmd
		}
		return m, nil
	case actionDoneMsg:
		if m.view == viewDashboard && m.dash.tag(msg.hostID, msg.gen) {
			dash, cmd := m.dash.Update(msg)
			m.dash = dash
			return m, cmd
		}
		return m, nil
	case logChunkMsg:
		if m.view == viewContainer && m.cv.tag(msg.hostID, msg.gen) {
			m.cv.Append(msg.chunk)
		}
		return m, nil
	case logEndMsg:
		if m.view == viewContainer && m.cv.tag(msg.hostID, msg.gen) {
			m.cv.StreamDone(msg.err)
		}
		return m, nil
	}
	return m, nil
}

func (m *Model) routeInternal(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd
	switch m.view {
	case viewHosts:
		m.list, cmd = m.list.Update(msg)
	case viewForm, viewPrompt:
		m.form, _, _, _ = m.form.Update(msg)
	case viewDashboard:
		m.dash, cmd = m.dash.Update(msg)
	case viewContainer:
		m.cv, cmd = m.cv.Update(msg)
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
		}
		out += "\n" + helpLine("n", "new", "e", "edit", "d", "delete", "enter", "connect", "q", "quit")
		return out
	case viewForm:
		return m.form.View(m.width)
	case viewPrompt:
		return m.form.View(m.width)
	case viewConnecting:
		return "\n" + m.sp.View() + " connecting to " + styleKey.Render(m.pendingHost.Target()) + "…"
	case viewDashboard:
		return m.dash.View()
	case viewContainer:
		return m.cv.View()
	}
	return ""
}

func (m *Model) cleanup() {
	if m.cv != nil {
		m.cv.Stop()
	}
	if m.conn != nil {
		m.conn.Close()
	}
	m.quitting = true
}

func (m *Model) deleteHost(h models.Host) (tea.Model, tea.Cmd) {
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
	return m, nil
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

func (m *Model) nextGen() uint64 {
	m.genSeq++
	return m.genSeq
}

func (m *Model) startConnect(h models.Host) (tea.Model, tea.Cmd) {
	m.pendingHost = h
	m.pendingGen = m.nextGen()
	switch h.AuthType {
	case models.AuthAgent:
		return m.connectWithSecret(h, "")
	case models.AuthPassword:
		s, err := secret.Get(h.PasswordRef())
		if err != nil || s == "" {
			return m.openPrompt(h, "Password for "+h.User+"@"+h.Address)
		}
		return m.connectWithSecret(h, s)
	case models.AuthKey:
		s, _ := secret.Get(h.PassphraseRef())
		return m.connectWithSecret(h, s)
	}
	m.status = "unknown auth type for host"
	return m, nil
}

func (m *Model) openPrompt(h models.Host, label string) (tea.Model, tea.Cmd) {
	m.form = NewSecretPrompt(h, label)
	m.view = viewPrompt
	return m, nil
}

func (m *Model) connectWithSecret(h models.Host, s string) (tea.Model, tea.Cmd) {
	m.pendingHost = h
	m.view = viewConnecting
	gen := m.pendingGen
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	return m, func() tea.Msg {
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
	if m.view != viewConnecting {
		if msg.conn != nil {
			msg.conn.Close()
		}
		return m, nil
	}
	if msg.err != nil {
		if msg.gen != m.pendingGen {
			return m, nil
		}
		m.conn = nil
		m.view = viewHosts
		es := msg.err.Error()
		if strings.Contains(es, "passphrase-protected") {
			return m.openPrompt(msg.host, "Key passphrase for "+msg.host.User+"@"+msg.host.Address)
		}
		if strings.Contains(es, "no password provided") {
			return m.openPrompt(msg.host, "Password for "+msg.host.User+"@"+msg.host.Address)
		}
		m.status = "connect failed: " + es
		return m, nil
	}
	if msg.gen != m.pendingGen {
		msg.conn.Close()
		return m, nil
	}
	m.conn = msg.conn
	m.dock = docker.New(msg.conn)
	m.dash = NewDashboard(msg.host, m.dock, m.width, m.height, msg.gen)
	m.view = viewDashboard
	return m, tea.Batch(m.dash.fetchNow(), tickStats(msg.host.ID, msg.gen))
}

func (m *Model) startLogStream(cv *ContainerView, id string) tea.Cmd {
	dock := m.dock
	send := m.send
	t := msgTag{hostID: cv.hostID, gen: cv.gen}
	return func() tea.Msg {
		r, err := cv.Open(dock, id)
		if err != nil {
			return logEndMsg{msgTag: t, err: err}
		}
		go func() {
			streamLogs(r, send, t)
		}()
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
