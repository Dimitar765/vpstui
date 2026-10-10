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
