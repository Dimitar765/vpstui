package app

import (
	"os"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
)

// TestMain pins the color profile so Render always emits ANSI, letting tests
// assert on styling regardless of the CI terminal.
func TestMain(m *testing.M) {
	lipgloss.DefaultRenderer().SetColorProfile(termenv.ANSI256)
	os.Exit(m.Run())
}

func TestStateCell(t *testing.T) {
	cases := []struct {
		state   string
		wantSeq string
		wantTxt string
	}{
		{"running", "\x1b[38;5;42m", "● running"},
		{"exited", "\x1b[38;5;241m", "○ exited"},
		{"restarting", "\x1b[38;5;214m", "● restarting"},
		{"dead", "\x1b[38;5;214m", "● dead"},
	}
	for _, tc := range cases {
		got := stateCell(tc.state)
		if !strings.HasPrefix(got, tc.wantSeq) {
			t.Errorf("stateCell(%q) = %q, want prefix %q", tc.state, got, tc.wantSeq)
		}
		if lipgloss.Width(got) > 12 {
			t.Errorf("stateCell(%q) width %d exceeds State column 12", tc.state, lipgloss.Width(got))
		}
		if !strings.Contains(got, tc.wantTxt) {
			t.Errorf("stateCell(%q) = %q, want to contain %q", tc.state, got, tc.wantTxt)
		}
	}
}

func TestPctCell(t *testing.T) {
	cases := []struct {
		in      string
		wantSeq string // "" means unstyled passthrough
	}{
		{"-", ""},
		{"abc%", ""},
		{"12.34%", ""},
		{"80.00%", "\x1b[38;5;214m"},
		{"100.0%", "\x1b[38;5;196m"},
		{"74.99%", ""},
	}
	for _, tc := range cases {
		got := pctCell(tc.in)
		if tc.wantSeq == "" {
			if got != tc.in {
				t.Errorf("pctCell(%q) = %q, want unchanged", tc.in, got)
			}
			continue
		}
		if !strings.HasPrefix(got, tc.wantSeq) {
			t.Errorf("pctCell(%q) = %q, want prefix %q", tc.in, got, tc.wantSeq)
		}
	}
}

func newTestTable(rows int) *ctable {
	t := &ctable{}
	t.SetColumns(dashCols)
	t.SetSize(120, 4)
	data := make([][]string, rows)
	hdr := make([]bool, rows)
	for i := range data {
		data[i] = []string{"c" + strings.Repeat("x", 30), "img", "running", "Up 1h", "1.00%", "2.00%", "10MiB / 2GiB"}
		if i%3 == 2 {
			hdr[i] = true
			data[i][0] = "▾ compose:web (2)"
		}
	}
	t.SetRows(data, hdr)
	return t
}

func TestCTableCursorClampAndScroll(t *testing.T) {
	tt := newTestTable(10)
	if tt.Cursor() != 0 {
		t.Fatalf("initial cursor = %d, want 0", tt.Cursor())
	}
	tt.SetCursor(9)
	if tt.Cursor() != 9 || tt.offset != 6 {
		t.Fatalf("after SetCursor(9): cursor=%d offset=%d, want 9/6", tt.Cursor(), tt.offset)
	}
	tt.SetCursor(42)
	if tt.Cursor() != 9 {
		t.Fatalf("cursor clamp high = %d, want 9", tt.Cursor())
	}
	tt.SetCursor(-5)
	if tt.Cursor() != 0 || tt.offset != 0 {
		t.Fatalf("cursor clamp low: cursor=%d offset=%d, want 0/0", tt.Cursor(), tt.offset)
	}
}

func TestCTableKeyNav(t *testing.T) {
	tt := newTestTable(10)
	down, _ := tt.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}})
	if down.Cursor() != 0 {
		t.Fatalf("unknown key moved cursor to %d", down.Cursor())
	}
	down, _ = tt.Update(tea.KeyMsg{Type: tea.KeyDown})
	if down.Cursor() != 1 {
		t.Fatalf("down key: cursor=%d want 1", down.Cursor())
	}
	up, _ := down.Update(tea.KeyMsg{Type: tea.KeyUp})
	end, _ := up.Update(tea.KeyMsg{Type: tea.KeyEnd})
	if end.Cursor() != 9 {
		t.Fatalf("end key: cursor=%d want 9", end.Cursor())
	}
	home, _ := end.Update(tea.KeyMsg{Type: tea.KeyHome})
	if home.Cursor() != 0 {
		t.Fatalf("home key: cursor=%d want 0", home.Cursor())
	}
	pg, _ := home.Update(tea.KeyMsg{Type: tea.KeyPgDown})
	if pg.Cursor() != 4 {
		t.Fatalf("pgdown with height 4: cursor=%d want 4", pg.Cursor())
	}
}

func TestCTableViewShape(t *testing.T) {
	tt := newTestTable(10)
	tt.SetCursor(8)
	out := tt.View()
	lines := strings.Split(out, "\n")
	if len(lines) != 1+tt.height { // header + visible rows
		t.Fatalf("View has %d lines, want %d", len(lines), 1+tt.height)
	}
	for _, ln := range lines {
		if w := lipgloss.Width(ln); w > tt.totalWidth() {
			t.Fatalf("line width %d exceeds table width %d: %q", w, tt.totalWidth(), ln)
		}
	}
	if !strings.Contains(out, "…") {
		t.Fatal("expected truncation ellipsis for 30-char name in width-16 column")
	}
}

func TestCTableStyledCellsNotCorrupted(t *testing.T) {
	tt := newTestTable(3)
	out := tt.View()
	if strings.Contains(out, "[38;5") && !strings.Contains(out, "\x1b[38;5") {
		t.Fatal("visible escape-sequence text leaked into render")
	}
	if !strings.Contains(out, "\x1b[38;5;42m") {
		t.Fatal("running state should render with ok color")
	}
	if !strings.Contains(out, "48;5;236") {
		t.Fatal("selected row should render with background")
	}
}

func TestCTableEmpty(t *testing.T) {
	tt := &ctable{}
	tt.SetColumns(dashCols)
	tt.SetSize(100, 5)
	if tt.View() != "" {
		t.Fatal("empty table should render nothing")
	}
}

func TestCTableVimKeys(t *testing.T) {
	tt := newTestTable(10)
	key := func(r string) tea.KeyMsg {
		return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(r)}
	}
	j, _ := tt.Update(key("j"))
	if j.Cursor() != 1 {
		t.Fatalf("j: cursor=%d want 1", j.Cursor())
	}
	k, _ := j.Update(key("k"))
	if k.Cursor() != 0 {
		t.Fatalf("k: cursor=%d want 0", k.Cursor())
	}
	G, _ := k.Update(key("G"))
	if G.Cursor() != 9 {
		t.Fatalf("G: cursor=%d want 9", G.Cursor())
	}
	g, _ := G.Update(key("g"))
	if g.Cursor() != 0 {
		t.Fatalf("g: cursor=%d want 0", g.Cursor())
	}
}

func TestLogsViewTopBottom(t *testing.T) {
	c := NewContainerView("abcdef0123456789", "web", 80, 30)
	for i := 0; i < 100; i++ {
		c.Append("line\n")
	}
	c.running = false // stop auto-follow so we can scroll
	key := func(r string) tea.KeyMsg {
		return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(r)}
	}
	c, _ = c.Update(key("g"))
	if !c.vp.AtTop() {
		t.Fatal("g should scroll logs to top")
	}
	c, _ = c.Update(key("G"))
	if !c.vp.AtBottom() {
		t.Fatal("G should scroll logs to bottom")
	}
}
