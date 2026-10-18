package app

import (
	"net"
	"strconv"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"vpstui/internal/config"
)

// settingsForm edits settings.json in place, following the HostForm
// field-walking pattern: kinds() lists the visible fields, syncFocus focuses
// exactly one, and Update returns submitted/cancelled flags for root.
type settingsForm struct {
	themeIdx int
	mouseIdx int // 0 = on, 1 = off

	tail, since, errtail, addr textinput.Model
	wrapIdx                    int // 0 = truncate, 1 = wrap

	focus int
	err   string
	width int
	// saved carries the settings the last submit built, so root can apply them
	// to the running session before the async write lands.
	saved config.Settings
}

var themes = []string{"dark", "light", "high-contrast"}

type setFieldKind int

const (
	stTheme setFieldKind = iota
	stLogTail
	stErrSince
	stErrTail
	stHTTPAddr
	stMouse
	stLogWrap
)

func NewSettingsForm(s config.Settings) *settingsForm {
	f := &settingsForm{}
	f.tail = newTI("2000")
	f.since = newTI("e.g. 24h")
	f.errtail = newTI("200")
	f.addr = newTI("127.0.0.1:8712")
	f.tail.SetValue(strconv.Itoa(s.LogTail))
	f.since.SetValue(s.ErrSince)
	f.errtail.SetValue(strconv.Itoa(s.ErrTail))
	f.addr.SetValue(s.HTTPAddr)
	f.themeIdx = 0
	for i, t := range themes {
		if t == s.Theme {
			f.themeIdx = i
		}
	}
	f.mouseIdx = 0
	if !s.MouseEnabled() {
		f.mouseIdx = 1
	}
	f.wrapIdx = 0
	if s.LogWrap {
		f.wrapIdx = 1
	}
	f.syncFocus()
	return f
}

func (f *settingsForm) kinds() []setFieldKind {
	return []setFieldKind{stTheme, stLogTail, stErrSince, stErrTail, stHTTPAddr, stMouse, stLogWrap}
}

func (f *settingsForm) label(k setFieldKind) string {
	switch k {
	case stTheme:
		return "Theme"
	case stLogTail:
		return "Log tail"
	case stErrSince:
		return "Err since"
	case stErrTail:
		return "Err tail"
	case stHTTPAddr:
		return "API addr"
	case stMouse:
		return "Mouse"
	case stLogWrap:
		return "Log wrap"
	}
	return ""
}

func (f *settingsForm) input(k setFieldKind) *textinput.Model {
	switch k {
	case stLogTail:
		return &f.tail
	case stErrSince:
		return &f.since
	case stErrTail:
		return &f.errtail
	case stHTTPAddr:
		return &f.addr
	}
	return nil
}

func (f *settingsForm) syncFocus() {
	ks := f.kinds()
	if f.focus >= len(ks) {
		f.focus = len(ks) - 1
	}
	if f.focus < 0 {
		f.focus = 0
	}
	for _, k := range ks {
		if ti := f.input(k); ti != nil {
			ti.Blur()
		}
	}
	if ti := f.input(ks[f.focus]); ti != nil {
		ti.Focus()
	}
}

func (f *settingsForm) SetSize(w int) { f.width = w }

// Update returns the form, a cmd, and submitted/cancelled flags.
func (f *settingsForm) Update(msg tea.Msg) (*settingsForm, tea.Cmd, bool, bool) {
	ks := f.kinds()
	f.syncFocus()
	cur := ks[f.focus]

	if km, ok := msg.(tea.KeyMsg); ok {
		switch km.String() {
		case "esc":
			return f, nil, false, true
		case "up", "shift+tab":
			f.focus = (f.focus - 1 + len(ks)) % len(ks)
			f.syncFocus()
			return f, nil, false, false
		case "down", "tab":
			f.focus = (f.focus + 1) % len(ks)
			f.syncFocus()
			return f, nil, false, false
		case "left", "right":
			step := 1
			if km.String() == "left" {
				step = -1
			}
			switch cur {
			case stTheme:
				f.themeIdx = (f.themeIdx + step + len(themes)) % len(themes)
				return f, nil, false, false
			case stMouse:
				f.mouseIdx = (f.mouseIdx + step + 2) % 2
				return f, nil, false, false
			case stLogWrap:
				f.wrapIdx = (f.wrapIdx + step + 2) % 2
				return f, nil, false, false
			}
		case "enter":
			if f.focus == len(ks)-1 {
				return f.trySubmit()
			}
			f.focus++
			f.syncFocus()
			return f, nil, false, false
		}
	}

	var cmd tea.Cmd
	if ti := f.input(cur); ti != nil {
		*ti, cmd = ti.Update(msg)
	}
	return f, cmd, false, false
}

func (f *settingsForm) trySubmit() (*settingsForm, tea.Cmd, bool, bool) {
	tail, ok := parsePosInt(f.tail.Value(), defaultLogTail)
	if !ok {
		f.err = "log tail must be a positive number"
		return f, nil, false, false
	}
	et, ok := parsePosInt(f.errtail.Value(), defaultErrTail)
	if !ok {
		f.err = "err tail must be a positive number"
		return f, nil, false, false
	}
	addr := strings.TrimSpace(f.addr.Value())
	if addr != "" {
		host, _, err := net.SplitHostPort(addr)
		ip := net.ParseIP(host)
		if err != nil || ip == nil || !ip.IsLoopback() {
			f.err = "API addr must be a loopback address"
			return f, nil, false, false
		}
	}
	mouse := f.mouseIdx == 0
	s := config.Settings{
		Theme:    themes[f.themeIdx],
		LogTail:  tail,
		ErrSince: strings.TrimSpace(f.since.Value()),
		ErrTail:  et,
		HTTPAddr: addr,
		Mouse:    &mouse,
		LogWrap:  f.wrapIdx == 1,
	}
	f.saved = s
	f.err = ""
	return f, persistCmd("saving settings", func() error { return config.SaveSettings(s) }), true, false
}

func (f *settingsForm) View(_ int) string {
	var b strings.Builder
	b.WriteString(styleTitle.Render("Settings") + " " + styleMuted.Render("(esc to cancel)") + "\n\n")
	ks := f.kinds()
	labelStyle := lipgloss.NewStyle().Width(12).Align(lipgloss.Right)
	for i, k := range ks {
		line := labelStyle.Render(f.label(k)) + " "
		switch k {
		case stTheme:
			v := themes[f.themeIdx]
			if i == f.focus {
				line += styleWarn.Render("< "+v+" >") + styleMuted.Render("  (←/→)")
			} else {
				line += v
			}
		case stMouse:
			v := "on"
			if f.mouseIdx == 1 {
				v = "off"
			}
			if i == f.focus {
				line += styleWarn.Render("< "+v+" >") + styleMuted.Render("  (←/→)")
			} else {
				line += v
			}
		case stLogWrap:
			v := "truncate"
			if f.wrapIdx == 1 {
				v = "wrap"
			}
			if i == f.focus {
				line += styleWarn.Render("< "+v+" >") + styleMuted.Render("  (←/→)")
			} else {
				line += v
			}
		default:
			ti := *f.input(k)
			ti.Prompt = ""
			ti.Width = 32
			line += ti.View()
		}
		if i == f.focus {
			line = lipgloss.NewStyle().Bold(true).Render(line)
		}
		b.WriteString(line + "\n")
	}
	b.WriteString("\n")
	if f.err != "" {
		b.WriteString(styleErr.Render(f.err) + "\n")
	}
	return b.String()
}

func (f *settingsForm) Help() string {
	return helpBar(hostFormRowsForSettings(), 5)
}

func hostFormRowsForSettings() []keyHelp {
	return []keyHelp{
		{"up/down", "fields"}, {"←/→", "choice"}, {"enter", "next / save"}, {"esc", "cancel"},
	}
}

func parsePosInt(s string, def int) (int, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return def, true
	}
	n, err := strconv.Atoi(s)
	if err != nil || n <= 0 {
		return 0, false
	}
	return n, true
}
