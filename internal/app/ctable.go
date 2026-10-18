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
	cellSev
	// cellTag is the container name cell: the text already carries the user's
	// label and the docker name, and the row's tag colour goes on after fitting.
	cellTag
	// cellFlag is the perf badge cell, which colours each word of the pair on
	// its own — a column kind cannot do that for one shared cell.
	cellFlag
	// cellHubStatus styles a hub server/app/deployment status string after
	// fitting, exactly like cellState does for docker states.
	cellHubStatus
	// cellAlert styles the fleet view's firing-alert badge.
	cellAlert
)

type ctableCol struct {
	title string
	width int
	kind  cellKind
	// style is the prebuilt Width/MaxWidth/Inline renderer for this column's
	// cells and header title. Built once by SetColumns; a View-time
	// lipgloss.NewStyle per cell per frame is measurable at dashboard rates.
	style lipgloss.Style
}

// ctable is a minimal fixed-column table with per-column value styling.
// The bubbles table truncates cells with runewidth.Truncate, which counts
// ANSI escape bytes as visible width, so colors must be applied by this
// local renderer after plain-text fitting instead.
type ctable struct {
	cols   []ctableCol
	rows   [][]string
	header []bool   // group header rows (compose)
	tint   []string // per-row tag colour name, "" when untagged; parallel to rows
	cursor int
	offset int
	width  int
	height int

	// Render caches, rebuilt by SetColumns: the styled header line and the
	// summed column width the cursor-row pad needs. View used to rebuild the
	// header per frame and re-measure the width on every cursor row.
	headerLine string
	totalW     int
}

func (t *ctable) SetColumns(cols []ctableCol) {
	t.cols = cols
	t.headerLine, t.totalW = "", 0
	if len(cols) == 0 {
		return
	}
	parts := make([]string, len(cols))
	for i := range t.cols {
		c := &t.cols[i]
		c.style = lipgloss.NewStyle().Width(c.width).MaxWidth(c.width).Inline(true)
		// Padded exactly as renderRow pads a cell, or the labels would sit
		// left-packed above columns that start much further right.
		parts[i] = c.style.Render(fitPlain(c.title, c.width))
		t.totalW += c.width
		if i > 0 {
			t.totalW += cellGap
		}
	}
	t.headerLine = styleTableHeader.Render(strings.Join(parts, cellGapStr))
}

// SetTint gives the rows their tag colours. It is set alongside SetRows by the
// views that use cellTag, and may be nil.
func (t *ctable) SetTint(tint []string) { t.tint = tint }

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

func (t *ctable) totalWidth() int { return t.totalW }

// ColumnAt maps a header-line x coordinate to a column index, -1 when the
// click falls on a gap or past the table. Widths match View's rendering.
func (t *ctable) ColumnAt(x int) int {
	start := 0
	for i, c := range t.cols {
		if x >= start && x < start+c.width {
			return i
		}
		start += c.width + cellGap
	}
	return -1
}

// ClickRow moves the cursor to the visible row at view-relative y (0 is the
// first row under the header line); out-of-frame clicks are ignored.
func (t *ctable) ClickRow(y int) {
	if y < 0 || len(t.rows) == 0 {
		return
	}
	t.SetCursor(t.offset + y)
}

// cellGap mirrors the padding the previous bubbles table put between cells.
const cellGap = 2

var cellGapStr = strings.Repeat(" ", cellGap)

func (t ctable) View() string {
	if len(t.rows) == 0 || len(t.cols) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString(t.headerLine + "\n")

	end := min(t.offset+t.height, len(t.rows))
	for i := t.offset; i < end; i++ {
		line := t.renderRow(i)
		if i == t.cursor {
			pad := max(t.totalW-lipgloss.Width(line), 0)
			line = styleTableSelected.Render(line + strings.Repeat(" ", pad))
		}
		b.WriteString(line + "\n")
	}
	return strings.TrimSuffix(b.String(), "\n")
}

func (t ctable) renderRow(i int) string {
	parts := make([]string, len(t.cols))
	for ci := range t.cols {
		c := &t.cols[ci]
		v := t.rows[i][ci]
		switch c.kind {
		case cellState:
			v = stateCell(v)
		case cellPct:
			v = pctCell(v)
		case cellSev:
			v = sevCell(v)
		case cellTag:
			v = t.rowTint(i).Render(fitPlain(v, c.width))
		case cellFlag:
			v = flagCell(fitPlain(v, c.width))
		case cellHubStatus:
			v = hubStatusCell(fitPlain(v, c.width))
		case cellAlert:
			v = styleErr.Render(fitPlain(v, c.width))
		default:
			v = fitPlain(v, c.width)
		}
		parts[ci] = c.style.Render(v)
	}
	line := strings.Join(parts, cellGapStr)
	if i < len(t.header) && t.header[i] {
		line = styleComposeHeader.Render(line)
	}
	return line
}

// rowTint is the colour a tagged row asks for; an untagged or out-of-range row
// renders in the terminal's default foreground.
func (t ctable) rowTint(i int) lipgloss.Style {
	if i < 0 || i >= len(t.tint) || t.tint[i] == "" {
		return lipgloss.NewStyle()
	}
	return tagStyle(t.tint[i])
}

func fitPlain(s string, w int) string {
	return runewidth.Truncate(s, w, "…")
}
