package app

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
)

func TestPinFooter(t *testing.T) {
	got := pinFooter("a\nb", "f", 5)
	if want := "a\nb\n\n\nf"; got != want {
		t.Errorf("pinFooter short body:\n got %q\nwant %q", got, want)
	}
	if h := lipgloss.Height(got); h != 5 {
		t.Errorf("height = %d, want 5", h)
	}
	// body taller than frame: footer follows immediately, no negative gap
	got = pinFooter("a\nb\nc\nd", "f", 3)
	if !strings.HasSuffix(got, "\nf") || strings.Contains(got, "\n\nf") {
		t.Errorf("overflow case wrong: %q", got)
	}
	// empty footer returns body untouched
	if got := pinFooter("x\n", "", 10); got != "x" {
		t.Errorf("empty footer = %q, want %q", got, "x")
	}
}

func TestPanelFraming(t *testing.T) {
	p := panel("title", "body", 40)
	if !strings.Contains(p, "╭") || !strings.Contains(p, "╯") {
		t.Errorf("panel missing rounded corners: %q", p)
	}
	if h := lipgloss.Height(p); h != 3 {
		t.Errorf("panel height = %d, want 3", h)
	}
	if w := lipgloss.Width(p); w > 40 {
		t.Errorf("panel width = %d, want ≤ 40", w)
	}
}

func TestLogsViewFramed(t *testing.T) {
	c := NewContainerView("abcdef0123456789", "web", 80, 30)
	v := c.View()
	if !strings.Contains(v, "╭") {
		t.Error("logs viewport should be framed")
	}
	if !strings.Contains(v, "streaming") {
		t.Error("missing stream status")
	}
	if h := lipgloss.Height(v); h != 30 {
		t.Errorf("logs height = %d, want 30 (pinned frame)", h)
	}
}
