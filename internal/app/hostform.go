package app

import (
	"strconv"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"vpstui/internal/models"
)

const (
	modeFull = iota
	modeSecret
)

var authTypes = []string{models.AuthPassword, models.AuthKey, models.AuthAgent}

type fieldKind int

const (
	fName fieldKind = iota
	fAddr
	fPort
	fUser
	fAuth
	fKey
	fSecret
)

type HostForm struct {
	mode  int
	host  models.Host
	isNew bool

	name, addr, port, user, keypath, secret textinput.Model
	authIdx                                 int
	focus                                   int
	err                                     string
	width                                   int
}

func newTI(placeholder string) textinput.Model {
	ti := textinput.New()
	ti.Placeholder = placeholder
	ti.CharLimit = 128
	return ti
}

func NewHostForm(h models.Host, isNew bool) *HostForm {
	f := &HostForm{mode: modeFull, host: h, isNew: isNew}
	f.name = newTI("my-vps")
	f.addr = newTI("203.0.113.7")
	f.port = newTI("22")
	f.user = newTI("root")
	f.keypath = newTI("~/.ssh/id_ed25519")
	f.secret = newTI("stored in keyring")
	f.secret.EchoMode = textinput.EchoPassword
	f.secret.EchoCharacter = '*'

	f.name.SetValue(h.Name)
	f.addr.SetValue(h.Address)
	if h.Port > 0 {
		f.port.SetValue(strconv.Itoa(h.Port))
	}
	f.user.SetValue(h.User)
	f.keypath.SetValue(h.KeyPath)
	f.authIdx = authIndex(h.AuthType)
	f.syncFocus()
	return f
}

// NewSecretPrompt shows only the secret field (password or key passphrase).
func NewSecretPrompt(h models.Host, label string) *HostForm {
	f := &HostForm{mode: modeSecret, host: h}
	f.secret = newTI("")
	f.secret.EchoMode = textinput.EchoPassword
	f.secret.EchoCharacter = '*'
	f.secret.Prompt = label + ": "
	f.secret.Focus()
	return f
}

func authIndex(a string) int {
	for i, t := range authTypes {
		if t == a {
			return i
		}
	}
	return 0
}

func (f *HostForm) kinds() []fieldKind {
	if f.mode == modeSecret {
		return []fieldKind{fSecret}
	}
	out := []fieldKind{fName, fAddr, fPort, fUser, fAuth}
	switch authTypes[f.authIdx] {
	case models.AuthKey:
		out = append(out, fKey, fSecret)
	case models.AuthPassword:
		out = append(out, fSecret)
	}
	return out
}

func (f *HostForm) label(k fieldKind) string {
	switch k {
	case fName:
		return "Name"
	case fAddr:
		return "Address"
	case fPort:
		return "Port"
	case fUser:
		return "User"
	case fAuth:
		return "Auth"
	case fKey:
		return "Key path"
	case fSecret:
		if authTypes[f.authIdx] == models.AuthKey {
			return "Key passphrase"
		}
		return "Password"
	}
	return ""
}

func (f *HostForm) input(k fieldKind) *textinput.Model {
	switch k {
	case fName:
		return &f.name
	case fAddr:
		return &f.addr
	case fPort:
		return &f.port
	case fUser:
		return &f.user
	case fKey:
		return &f.keypath
	case fSecret:
		return &f.secret
	}
	return nil
}

func (f *HostForm) syncFocus() {
	ks := f.kinds()
	if f.focus >= len(ks) {
		f.focus = len(ks) - 1
	}
	if f.focus < 0 {
		f.focus = 0
	}
	focused := ks[f.focus]
	for _, k := range []fieldKind{fName, fAddr, fPort, fUser, fKey, fSecret} {
		ti := f.input(k)
		if k == focused {
			ti.Focus()
		} else {
			ti.Blur()
		}
	}
}

func (f *HostForm) SetSize(w int) { f.width = w }

func (f *HostForm) SecretValue() string { return f.secret.Value() }

// Update returns the form, a cmd, and submitted/cancelled flags.
func (f *HostForm) Update(msg tea.Msg) (*HostForm, tea.Cmd, bool, bool) {
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
			if cur == fAuth {
				if km.String() == "left" {
					f.authIdx = (f.authIdx - 1 + len(authTypes)) % len(authTypes)
				} else {
					f.authIdx = (f.authIdx + 1) % len(authTypes)
				}
				f.secret.SetValue("")
				f.err = ""
				f.syncFocus()
				return f, nil, false, false
			}
		case "enter":
			if f.focus == len(f.kinds())-1 {
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

func (f *HostForm) trySubmit() (*HostForm, tea.Cmd, bool, bool) {
	if f.mode == modeSecret {
		return f, nil, true, false
	}
	port := 22
	if p := strings.TrimSpace(f.port.Value()); p != "" {
		n, err := strconv.Atoi(p)
		if err != nil || n <= 0 || n > 65535 {
			f.err = "invalid port"
			return f, nil, false, false
		}
		port = n
	}
	addr := strings.TrimSpace(f.addr.Value())
	if addr == "" {
		f.err = "address is required"
		return f, nil, false, false
	}
	auth := authTypes[f.authIdx]
	keypath := strings.TrimSpace(f.keypath.Value())
	if auth == models.AuthKey && keypath == "" {
		f.err = "private key path is required"
		return f, nil, false, false
	}
	user := strings.TrimSpace(f.user.Value())
	if user == "" {
		user = "root"
	}
	name := strings.TrimSpace(f.name.Value())
	if name == "" {
		name = addr
	}

	h := f.host
	if f.isNew {
		h.ID = models.NewID()
	}
	h.Name = name
	h.Address = addr
	h.Port = port
	h.User = user
	h.AuthType = auth
	h.KeyPath = keypath

	f.err = ""
	s := f.secret.Value()
	f.secret.SetValue("")
	return f, func() tea.Msg { return formSubmittedMsg{host: h, secretV: s, isNew: f.isNew} }, true, false
}

func (f *HostForm) View(_ int) string {
	var b strings.Builder
	if f.mode == modeSecret {
		b.WriteString(styleTitle.Render("Unlock connection") + "\n\n")
		ti := f.secret
		ti.Prompt = styleKey.Render(ti.Prompt)
		b.WriteString(ti.View() + "\n\n")
		b.WriteString(helpLine("enter", "submit", "esc", "cancel") + "\n")
		return b.String()
	}
	b.WriteString(styleTitle.Render("Host details") + " " + styleMuted.Render("(esc to cancel)") + "\n\n")
	ks := f.kinds()
	labelStyle := lipgloss.NewStyle().Width(14).Align(lipgloss.Right)
	for i, k := range ks {
		line := labelStyle.Render(f.label(k)) + " "
		if k == fAuth {
			v := authTypes[f.authIdx]
			if i == f.focus {
				line += styleWarn.Render("< "+v+" >") + styleMuted.Render("  (←/→)")
			} else {
				line += v
			}
		} else {
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
	b.WriteString(helpLine("up/down", "fields", "enter", "next / save", "esc", "cancel") + "\n")
	return b.String()
}
