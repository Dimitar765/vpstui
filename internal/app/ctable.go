package app

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/mattn/go-runewidth"
)

type cellKind int

const (
	cellPlain cellKind = iota
	cellState
	cellPct
)

type ctableCol struct {
	title string
	width int
	kind  cellKind
}

// ctable is a minimal fixed-column table with per-column value styling.
// The bubbles table truncates cells with runewidth.Truncate, which counts
// ANSI escape bytes as visible width, so colors must be applied by this
// local renderer after plain-text fitting instead.
type ctable struct {
	cols   []ctableCol
	rows   [][]string
	header []bool // group header rows (compose)
	cursor int
	offset int
	width  int
	height int
}

func (t *ctable) SetColumns(cols []ctableCol) { t.cols = cols }

func (t *ctable) SetSize(w, h int) {
	t.width = max(w, 10)
	t.height = max(h, 1)
	t.clamp()
}

func (t *ctable) SetRows(rows [][]string, header []bool) {
	t.rows = rows
	t.header = header
	t.clamp()
}

func (t *ctable) Cursor() int { return t.cursor }

func (t *ctable) SetCursor(n int) {
	t.cursor = clampInt(n, 0, max(len(t.rows)-1, 0))
	t.scroll()
}

func (t *ctable) clamp() {
	t.cursor = clampInt(t.cursor, 0, max(len(t.rows)-1, 0))
	t.scroll()
}

func (t *ctable) scroll() {
	if t.cursor < t.offset {
		t.offset = t.cursor
	}
	if t.cursor >= t.offset+t.height {
		t.offset = t.cursor - t.height + 1
	}
	t.offset = clampInt(t.offset, 0, max(len(t.rows)-1, 0))
}

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func (t ctable) Update(msg tea.Msg) (ctable, tea.Cmd) {
	km, ok := msg.(tea.KeyMsg)
	if !ok || len(t.rows) == 0 {
		return t, nil
	}
	switch km.String() {
	case "up", "k":
		t.SetCursor(t.cursor - 1)
	case "down", "j":
		t.SetCursor(t.cursor + 1)
	case "pgup":
		t.SetCursor(t.cursor - t.height)
	case "pgdown":
		t.SetCursor(t.cursor + t.height)
	case "home", "g":
		t.SetCursor(0)
	case "end", "G":
		t.SetCursor(len(t.rows) - 1)
	default:
		return t, nil
	}
	return t, nil
}

func (t ctable) totalWidth() int {
	var w int
	for i, c := range t.cols {
		w += c.width
		if i > 0 {
			w += cellGap
		}
	}
	return w
}

// cellGap mirrors the padding the previous bubbles table put between cells.
const cellGap = 2

func (t ctable) View() string {
	if len(t.rows) == 0 || len(t.cols) == 0 {
		return ""
	}
	parts := make([]string, len(t.cols))
	for i, c := range t.cols {
		parts[i] = fitPlain(c.title, c.width)
	}
	var b strings.Builder
	b.WriteString(styleTableHeader.Render(strings.Join(parts, strings.Repeat(" ", cellGap))) + "\n")

	end := min(t.offset+t.height, len(t.rows))
	for i := t.offset; i < end; i++ {
		line := t.renderRow(i)
		if i == t.cursor {
			pad := max(t.totalWidth()-lipgloss.Width(line), 0)
			line = styleTableSelected.Render(line + strings.Repeat(" ", pad))
		}
		b.WriteString(line + "\n")
	}
	return strings.TrimSuffix(b.String(), "\n")
}

func (t ctable) renderRow(i int) string {
	parts := make([]string, len(t.cols))
	for ci, c := range t.cols {
		v := t.rows[i][ci]
		switch c.kind {
		case cellState:
			v = stateCell(v)
		case cellPct:
			v = pctCell(v)
		default:
			v = fitPlain(v, c.width)
		}
		parts[ci] = lipgloss.NewStyle().Width(c.width).MaxWidth(c.width).Inline(true).Render(v)
	}
	line := strings.Join(parts, strings.Repeat(" ", cellGap))
	if i < len(t.header) && t.header[i] {
		line = styleComposeHeader.Render(line)
	}
	return line
}

func fitPlain(s string, w int) string {
	return runewidth.Truncate(s, w, "…")
}
