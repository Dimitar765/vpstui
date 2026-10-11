package app

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
)

func TestPinFooter(t *testing.T) {
	got := pinFooter("a\nb", "f", 40, 5)
	if want := "a\nb\n\n\nf"; got != want {
		t.Errorf("pinFooter short body:\n got %q\nwant %q", got, want)
	}
	if h := lipgloss.Height(got); h != 5 {
		t.Errorf("height = %d, want 5", h)
	}
	// body taller than frame: footer follows immediately, no negative gap
	got = pinFooter("a\nb\nc\nd", "f", 40, 3)
	if !strings.HasSuffix(got, "\nf") || strings.Contains(got, "\n\nf") {
		t.Errorf("overflow case wrong: %q", got)
	}
	// empty footer returns body untouched
	if got := pinFooter("x\n", "", 40, 10); got != "x" {
		t.Errorf("empty footer = %q, want %q", got, "x")
	}
}

func TestPinFooterWrapsWideFooter(t *testing.T) {
	footer := helpLine("aaaa", "bb", "cccc", "dd", "eeee", "ff", "gggg", "hh")
	got := pinFooter("body", footer, 20, 8)
	if lipgloss.Height(got) != 8 {
		t.Errorf("height = %d, want 8", lipgloss.Height(got))
	}
	for _, line := range strings.Split(got, "\n") {
		if lipgloss.Width(line) > 20 {
			t.Errorf("wrapped line too wide (%d): %q", lipgloss.Width(line), line)
		}
	}
}

func TestPanelFraming(t *testing.T) {
	p := panel("title", "body", 40)
	if !strings.Contains(p, "╭") || !strings.Contains(p, "╯") {
		t.Errorf("panel missing rounded corners: %q", p)
	}
	if h := lipgloss.Height(p); h != 4 {
		t.Errorf("panel height = %d, want 4", h)
	}
	if w := lipgloss.Width(p); w > 40 {
		t.Errorf("panel width = %d, want ≤ 40", w)
	}
}

func TestLogsViewFramed(t *testing.T) {
	modes := []struct {
		name   string
		prep   func(*ContainerView)
		status string
	}{
		{"streaming", func(*ContainerView) {}, "streaming"},
		{"filter", func(c *ContainerView) { c.applyQuery("err") }, "streaming"},
		{"highlight", func(c *ContainerView) { c.applyQuery("err"); c.toggleMode() }, "streaming"},
		{"scrolled up", func(c *ContainerView) { _, _ = c.Update(key("g")) }, "streaming"},
		{"typing", func(c *ContainerView) { c.searching = true }, "⌕"},
	}
	for _, m := range modes {
		t.Run(m.name, func(t *testing.T) {
			c := NewContainerView("abcdef0123456789", "web", 80, 30)
			feed(c, "alpha", "ERROR one", "beta")
			c.flush()
			m.prep(c)
			v := c.View()
			if !strings.Contains(v, "╭") {
				t.Error("logs viewport should be framed")
			}
			if !strings.Contains(v, m.status) {
				t.Errorf("missing status %q: %q", m.status, v)
			}
			if h := lipgloss.Height(v); h != 30 {
				t.Errorf("logs height = %d, want 30 (pinned frame)", h)
			}
			// A help bar that wraps at 80 columns is what would break the frame.
			if w := lipgloss.Width(v); w > 80 {
				t.Errorf("logs width = %d, want ≤ 80", w)
			}
		})
	}
}
