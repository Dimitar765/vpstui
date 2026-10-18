package app

import (
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/sahilm/fuzzy"
)

// palette is the `:` command palette. While it is open it owns every key —
// the same focus rule as the log filter input — so session hotkeys and screen
// keys never fire mid-query.
type palette struct {
	open     bool
	in       textinput.Model
	sel      int
	matches  []palAction
	sourceID []int // matches[i] indexes palCandidates(); rebuilt on every keystroke
}

// palAction is one runnable palette entry. label is what the user fuzzy-matches
// and sees; run executes on enter and returns any follow-up command.
type palAction struct {
	label string
	run   func(m *Model) tea.Cmd
}

const palMaxVisible = 8

func newPalette() palette {
	in := textinput.New()
	in.Placeholder = "command"
	in.CharLimit = 64
	in.Prompt = ":"
	return palette{in: in}
}

// palCandidates builds the action list for the current state: one connect per
// host, one logs entry per container across every session, the sort set for
// the active dashboard, and the navigation/quit verbs.
func (m *Model) palCandidates() []palAction {
	var acts []palAction
	for _, h := range m.hosts {
		h := h
		acts = append(acts, palAction{"connect " + h.Name, func(m *Model) tea.Cmd {
			_, cmd := m.openOrConnect(h)
			return cmd
		}})
	}
	seen := map[string]bool{}
	for _, s := range m.sessions {
		if s.dash == nil {
			continue
		}
		for _, c := range s.dash.containers {
			name := c.ShortName()
			if seen[name] {
				continue
			}
			seen[name] = true
			acts = append(acts, palAction{"logs " + name, func(m *Model) tea.Cmd {
				return m.openLogsByName(name)
			}})
		}
	}
	for _, name := range sortNames {
		name := name
		acts = append(acts, palAction{"sort " + name, func(m *Model) tea.Cmd {
			if s := m.activeSession(); s != nil && s.dash != nil {
				for i, n := range sortNames {
					if n == name {
						s.dash.setSort(i)
						break
					}
				}
			}
			return nil
		}})
	}
	static := []palAction{
		{"tag", func(m *Model) tea.Cmd { _, cmd := m.openTagPrompt(); return cmd }},
		{"errors", func(m *Model) tea.Cmd { _, cmd := m.openErrors(); return cmd }},
		{"perf", func(m *Model) tea.Cmd {
			if s := m.activeSession(); s != nil && s.dash != nil {
				return s.dash.openPerf()
			}
			return nil
		}},
		{"images", func(m *Model) tea.Cmd {
			if s := m.activeSession(); s != nil && s.dash != nil {
				d := s.dash
				d.showPerf = false
				d.showDisk = false
				d.showImages = true
				d.imgErr = ""
				d.imgTbl.SetColumns(imgCols)
				return d.fetchImages()
			}
			return nil
		}},
		{"disk", func(m *Model) tea.Cmd {
			if s := m.activeSession(); s != nil && s.dash != nil {
				d := s.dash
				d.showPerf = false
				d.showDisk = true
				d.diskLoading = true
				d.diskErr = ""
				return d.fetchDisk()
			}
			return nil
		}},
		{"hostlist", func(m *Model) tea.Cmd { m.view = viewHosts; return nil }},
		{"quit", func(m *Model) tea.Cmd { m.cleanup(); return tea.Quit }},
	}
	return append(acts, static...)
}

// openLogsByName jumps to the named container's live stream, switching to the
// first session that owns it — the palette's `logs <name>` verb.
func (m *Model) openLogsByName(name string) tea.Cmd {
	for _, s := range m.sessions {
		if s.dash == nil || s.dock == nil {
			continue
		}
		for _, c := range s.dash.containers {
			if c.ShortName() != name {
				continue
			}
			m.active = m.indexOf(s)
			s.cv = NewContainerView(c.ID, name, m.width, m.height-m.tabRows())
			s.cv.hostID, s.cv.gen = s.host.ID, s.gen
			s.cv.SetTag(m.tags.Display(s.host.ID, name), m.tags.Tint(s.host.ID, name))
			if m.cfg.LogWrap {
				s.cv.wrap = true
			}
			m.view = viewContainer
			return startLogStream(s, c.ID, m.send)
		}
	}
	return m.toast("no container named "+name, sevError)
}

// palRefresh re-runs the fuzzy match for the current query. An empty query
// lists everything in candidate order.
func (m *Model) palRefresh() {
	m.pal.sourceID = m.pal.sourceID[:0]
	m.pal.matches = m.pal.matches[:0]
	acts := m.palCandidates()
	q := strings.TrimSpace(m.pal.in.Value())
	if q == "" {
		for i, a := range acts {
			m.pal.sourceID = append(m.pal.sourceID, i)
			m.pal.matches = append(m.pal.matches, a)
		}
	} else {
		labels := make([]string, len(acts))
		for i, a := range acts {
			labels[i] = a.label
		}
		for _, mt := range fuzzy.Find(q, labels) {
			m.pal.sourceID = append(m.pal.sourceID, mt.Index)
			m.pal.matches = append(m.pal.matches, acts[mt.Index])
		}
	}
	if m.pal.sel >= len(m.pal.matches) {
		m.pal.sel = max(len(m.pal.matches)-1, 0)
	}
}

func (m *Model) openPalette() tea.Cmd {
	m.pal.open = true
	m.pal.sel = 0
	m.pal.in.SetValue("")
	m.palRefresh()
	return m.pal.in.Focus()
}

func (m *Model) closePalette() {
	m.pal.open = false
	m.pal.in.Blur()
}

// updatePalette runs the palette while it is open. It is called from updateKey
// before anything else, so no other screen ever sees a palette keystroke.
func (m *Model) updatePalette(msg tea.Msg) (tea.Model, tea.Cmd) {
	km, ok := msg.(tea.KeyMsg)
	if !ok {
		return m, nil
	}
	switch km.String() {
	case "esc", "ctrl+c":
		m.closePalette()
		return m, nil
	case "down", "ctrl+n":
		if m.pal.sel < len(m.pal.matches)-1 {
			m.pal.sel++
		}
		return m, nil
	case "up", "ctrl+p":
		if m.pal.sel > 0 {
			m.pal.sel--
		}
		return m, nil
	case "j":
		if m.pal.sel < len(m.pal.matches)-1 {
			m.pal.sel++
		}
		return m, nil
	case "k":
		if m.pal.sel > 0 {
			m.pal.sel--
		}
		return m, nil
	case "enter":
		if m.pal.sel >= len(m.pal.matches) {
			return m, nil
		}
		act := m.pal.matches[m.pal.sel]
		m.closePalette()
		cmd := act.run(m)
		if cmd == nil {
			return m, nil
		}
		return m, cmd
	}
	var cmd tea.Cmd
	m.pal.in, cmd = m.pal.in.Update(msg)
	m.palRefresh()
	return m, cmd
}

// palView renders the floating palette card near the top of the frame.
func (m *Model) palView() string {
	var b strings.Builder
	b.WriteString(m.pal.in.View() + "\n")
	if len(m.pal.matches) == 0 {
		b.WriteString(styleMuted.Render("no matching command") + "\n")
	}
	end := min(len(m.pal.matches), palMaxVisible)
	// Keep the selection visible when the list is longer than the window.
	start := clampInt(m.pal.sel-palMaxVisible+1, 0, max(end-palMaxVisible, 0))
	for i := start; i < end; i++ {
		line := m.pal.matches[i].label
		if i == m.pal.sel {
			b.WriteString(styleTableSelected.Render(padRight(line, 40)) + "\n")
		} else {
			b.WriteString(styleMuted.Render(line) + "\n")
		}
	}
	return panel("", b.String(), 50)
}

// toastStyle picks the severity colour a toast line renders in.
func toastStyle(s severity) lipgloss.Style {
	switch s {
	case sevWarn:
		return styleWarn
	case sevError:
		return styleErr
	}
	return styleOK
}
