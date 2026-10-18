package app

import (
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// hubForm collects the daemon URL and API token for hub mode. It copies the
// HostForm field-walking pattern: one ordered kinds slice drives focus, keys,
// labels and rendering, so a new field is a one-line change.
type hubForm struct {
	url, token textinput.Model
	focus      int
	err        string
	width      int
}

func newHubForm(url string) *hubForm {
	f := &hubForm{}
	f.url = newTI("http://127.0.0.1:8713")
	f.token = newTI("paste an API key (vps_…) or login token")
	f.token.EchoMode = textinput.EchoPassword
	f.token.EchoCharacter = '*'
	f.url.SetValue(url)
	f.focus = 0
	if strings.TrimSpace(url) != "" {
		f.focus = 1 // URL came from --hub; the token is what is missing
	}
	f.syncFocus()
	return f
}

func (f *hubForm) inputs() []*textinput.Model {
	return []*textinput.Model{&f.url, &f.token}
}

func (f *hubForm) labels() []string { return []string{"URL", "Token"} }

func (f *hubForm) syncFocus() {
	ins := f.inputs()
	if f.focus >= len(ins) {
		f.focus = len(ins) - 1
	}
	if f.focus < 0 {
		f.focus = 0
	}
	for i, ti := range ins {
		if i == f.focus {
			ti.Focus()
		} else {
			ti.Blur()
		}
	}
}

func (f *hubForm) SetSize(w int) { f.width = w }

func (f *hubForm) URL() string   { return strings.TrimSpace(f.url.Value()) }
func (f *hubForm) Token() string { return strings.TrimSpace(f.token.Value()) }

// Update returns the form, a cmd, and submitted/cancelled flags.
func (f *hubForm) Update(msg tea.Msg) (*hubForm, tea.Cmd, bool, bool) {
	if km, ok := msg.(tea.KeyMsg); ok {
		switch km.String() {
		case "esc":
			return f, nil, false, true
		case "up", "shift+tab":
			f.focus = (f.focus - 1 + 2) % 2
			f.syncFocus()
			return f, nil, false, false
		case "down", "tab":
			f.focus = (f.focus + 1) % 2
			f.syncFocus()
			return f, nil, false, false
		case "enter":
			if f.focus == 1 {
				return f.trySubmit()
			}
			f.focus++
			f.syncFocus()
			return f, nil, false, false
		}
	}
	var cmd tea.Cmd
	ins := f.inputs()
	focus := ins[f.focus]
	*focus, cmd = focus.Update(msg)
	return f, cmd, false, false
}

func (f *hubForm) trySubmit() (*hubForm, tea.Cmd, bool, bool) {
	if f.URL() == "" {
		f.err = "url is required"
		return f, nil, false, false
	}
	f.err = ""
	// The token stays in the input: the caller reads URL/Token and then drops
	// the form, so no clearing round-trip is needed.
	return f, nil, true, false
}

func (f *hubForm) View(_ int) string {
	var b strings.Builder
	b.WriteString(styleTitle.Render("Connect to hub") + " " +
		styleMuted.Render("(esc to cancel)") + "\n\n")
	labelStyle := lipgloss.NewStyle().Width(14).Align(lipgloss.Right)
	for i, ti := range f.inputs() {
		line := labelStyle.Render(f.labels()[i]) + " "
		ti := *ti
		ti.Prompt = ""
		ti.Width = 40
		line += ti.View()
		if i == f.focus {
			line = lipgloss.NewStyle().Bold(true).Render(line)
		}
		b.WriteString(line + "\n")
	}
	b.WriteString("\n" + styleMuted.Render("the token is stored in the keyring, keyed by URL") + "\n")
	if f.err != "" {
		b.WriteString(styleErr.Render(f.err) + "\n")
	}
	return b.String()
}

func (f *hubForm) Help() string {
	return helpBar([]keyHelp{
		{"up/down", "fields"}, {"enter", "next / connect"}, {"esc", "cancel"},
	}, 5)
}
