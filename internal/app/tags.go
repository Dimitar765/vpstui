package app

import (
	"strings"
	"time"
	"unicode"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"vpstui/internal/config"
	"vpstui/internal/models"
)

// maxTagRunes keeps a label short enough that the Name column can still show
// the docker name behind it in an 80-column terminal.
const maxTagRunes = 24

// tagColor is one palette slot. The name is what reaches tags.json and the API,
// so the web page can colour the same label without reading ANSI numbers.
type tagColor struct {
	name string
	ansi string
}

// tagColors is the whole palette, in cycle order. The codes deliberately avoid
// the two meanings the table already owns: 42 means "running" and 196 means an
// error line, so tags use 71 and 203 instead.
var tagColors = []tagColor{
	{"sky", "110"},
	{"green", "71"},
	{"amber", "214"},
	{"rose", "203"},
	{"violet", "147"},
	{"slate", "241"},
}

func tagColorIndex(name string) int {
	for i, c := range tagColors {
		if c.name == name {
			return i
		}
	}
	return -1
}

// nextTagColor cycles forward through the palette; an unknown name restarts at
// the first slot rather than sticking.
func nextTagColor(name string) string {
	i := tagColorIndex(name)
	if i < 0 {
		return tagColors[0].name
	}
	return tagColors[(i+1)%len(tagColors)].name
}

func tagStyle(name string) lipgloss.Style {
	if i := tagColorIndex(name); i >= 0 {
		return lipgloss.NewStyle().Foreground(lipgloss.Color(tagColors[i].ansi))
	}
	// A hand-edited tags.json may name anything. An unknown colour must not
	// hide the label: plain text still reads.
	return lipgloss.NewStyle()
}

// normalizeTag cleans a typed label. Slashes are rejected because a tag is
// addressed as "host/name" everywhere it travels, and a tag containing one would
// make that string ambiguous.
func normalizeTag(s string) (string, bool) {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, s)
	fields := strings.Fields(strings.ReplaceAll(s, "/", " "))
	if len(fields) == 0 {
		return "", false
	}
	out := strings.Join(fields, " ")
	if r := []rune(out); len(r) > maxTagRunes {
		out = string(r[:maxTagRunes])
	}
	return out, true
}

// TagStore is the user's labels, keyed by host ID and container name. One
// goroutine owns it — Bubble Tea's Update — so the views read it by pointer
// without a lock, exactly like the error corpus.
type TagStore struct {
	by map[string]models.Tag
}

func NewTagStore(m map[string]models.Tag) *TagStore {
	if m == nil {
		m = map[string]models.Tag{}
	}
	return &TagStore{by: m}
}

func (t *TagStore) Get(hostID, name string) (models.Tag, bool) {
	if t == nil {
		return models.Tag{}, false
	}
	tag, ok := t.by[config.TagKey(hostID, name)]
	if !ok || tag.Tag == "" {
		return models.Tag{}, false
	}
	return tag, true
}

// Set stores a label and reports whether anything changed, so an unchanged
// re-commit does not touch the disk.
func (t *TagStore) Set(hostID, name, label, color, id string) bool {
	if t == nil {
		return false
	}
	key := config.TagKey(hostID, name)
	if cur, ok := t.by[key]; ok && cur.Tag == label && cur.Color == color {
		return false
	}
	if t.by == nil {
		t.by = map[string]models.Tag{}
	}
	t.by[key] = models.Tag{HostID: hostID, Name: name, Tag: label, Color: color, ID: id, SetAt: time.Now()}
	return true
}

// Clear removes a label and reports whether one was there.
func (t *TagStore) Clear(hostID, name string) bool {
	if t == nil {
		return false
	}
	if _, ok := t.by[config.TagKey(hostID, name)]; !ok {
		return false
	}
	delete(t.by, config.TagKey(hostID, name))
	return true
}

// All is the map to persist.
func (t *TagStore) All() map[string]models.Tag {
	if t == nil {
		return nil
	}
	return t.by
}

// Display is the one renderer helper: the label first, then the docker name, as
// plain text. Callers fit it and only then apply tagStyle, because fitPlain is
// not ANSI-aware.
func (t *TagStore) Display(hostID, name string) string {
	tag, ok := t.Get(hostID, name)
	if !ok {
		return name
	}
	return tag.Tag + " " + name
}

// Tint is the palette name a labelled row should be drawn in, "" when the
// container carries no label. It is the colour half of the same lookup Display
// does, so cell text and cell colour can never disagree.
func (t *TagStore) Tint(hostID, name string) string {
	tag, ok := t.Get(hostID, name)
	if !ok {
		return ""
	}
	return tag.Color
}

// tagOr draws a display string in its label colour, falling back to whatever
// style the surface uses for that text when the container is untagged. An
// unlabelled container must look exactly as it did before tags existed.
func tagOr(fallback lipgloss.Style, tint, display string) string {
	if tint == "" {
		return fallback.Render(display)
	}
	return tagStyle(tint).Render(display)
}

// tagTarget is the container a prompt or a yank is about.
type tagTarget struct {
	hostID string
	name   string
	id     string
}

type promptOutcome int

const (
	promptTyping promptOutcome = iota
	promptCommit
	promptCancel
)

// tagPrompt is the label editor. It follows the log filter's recipe: a flag
// that hands keys to the input, enter to commit, esc to cancel, and a gate in
// root so session switching never eats the typing.
type tagPrompt struct {
	in     textinput.Model
	active bool
	target tagTarget
	color  string
}

func newTagPrompt() tagPrompt {
	p := tagPrompt{in: textinput.New()}
	p.in.CharLimit = maxTagRunes + 8
	p.in.Placeholder = "mail"
	return p
}

func (p *tagPrompt) open(target tagTarget, current, color string) tea.Cmd {
	p.active = true
	p.target = target
	p.color = color
	p.in.SetValue(current)
	p.in.CursorEnd()
	return p.in.Focus()
}

func (p *tagPrompt) close() {
	p.active = false
	p.in.Blur()
}

// value is the cleaned label; ok is false when the input holds nothing usable.
func (p *tagPrompt) value() (string, bool) {
	return normalizeTag(p.in.Value())
}

func (p *tagPrompt) Update(msg tea.Msg) (promptOutcome, tea.Cmd) {
	if km, ok := msg.(tea.KeyMsg); ok && p.active {
		switch km.String() {
		case "enter":
			// An emptied field is an untag: the prompt opens prefilled, so
			// reaching empty means the user deleted the label deliberately.
			p.close()
			return promptCommit, nil
		case "esc":
			p.close()
			return promptCancel, nil
		case "tab", "shift+tab":
			p.color = nextTagColor(p.color)
			return promptTyping, nil
		}
	}
	var cmd tea.Cmd
	p.in, cmd = p.in.Update(msg)
	return promptTyping, cmd
}

// line is the prompt row: a live preview, so the colour choice is visible before
// committing. It clips the tail rather than folding it — a second row inside a
// pinned frame would push the footer off the screen.
func (p *tagPrompt) line(width int) string {
	if !p.active {
		return ""
	}
	width = max(width, 40)
	label, _ := p.value()
	if label == "" {
		label = "…"
	}
	label = fitPlain(label, maxTagRunes)
	const hint = "· tab colour · enter save · esc cancel"
	used := len("tag ") + lipgloss.Width(label) + len(hint) + 3
	name := ""
	if room := width - used; room >= 4 {
		name = fitPlain(p.target.name, room)
	}
	parts := []string{styleKey.Render("tag "), tagStyle(p.color).Render(label)}
	if name != "" {
		parts = append(parts, styleMuted.Render(name))
	}
	return strings.Join(parts, " ") + " " + styleMuted.Render(hint)
}
