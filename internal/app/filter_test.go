package app

import (
	"strings"
	"testing"
)

// The filter matches case-insensitively through the pre-folded mirror, and the
// mirror stays index-aligned with lines across appends and trims.
func TestFilterFoldedMirrorMatchesAndStaysAligned(t *testing.T) {
	c := NewContainerView("id", "web", 80, 24)
	c.Append("2026-10-11T01:02:03.5Z ERROR db down\n")
	c.Append("2026-10-11T01:02:04.5Z plain line\n")
	c.Append("2026-10-11T01:02:05.5Z error two\n")

	c.applyQuery("error")
	c.toggleMode() // highlight mode: keep every row, record hit indices
	if c.hide {
		t.Fatal("toggleMode did not leave highlight mode")
	}
	if len(c.hits) != 2 {
		t.Fatalf("hits = %d, want 2 (ERROR and error lines)", len(c.hits))
	}
	if len(c.lower) != len(c.lines) {
		t.Fatalf("mirror length %d, lines %d", len(c.lower), len(c.lines))
	}
	for i := range c.lines {
		if c.lower[i] != strings.ToLower(c.lines[i]) {
			t.Fatalf("mirror[%d] = %q, want %q", i, c.lower[i], strings.ToLower(c.lines[i]))
		}
	}

	// A trim drops the mirror's head in lockstep, so filtering still hits the
	// surviving lines and nothing panics on a stale index. The ERROR line is
	// appended last so it lands inside the kept window.
	oldCap, oldKeep := logLinesCap, logLinesKeep
	oldBCap, oldBKeep := logBytesCap, logBytesKeep
	t.Cleanup(func() { logLinesCap, logLinesKeep, logBytesCap, logBytesKeep = oldCap, oldKeep, oldBCap, oldBKeep })
	logLinesCap, logLinesKeep = 5, 3
	logBytesCap, logBytesKeep = 1<<30, 1<<30

	c.Append("2026-10-11T01:02:06.5Z plain three\n")
	c.Append("2026-10-11T01:02:07.5Z plain four\n")
	c.Append("2026-10-11T01:02:08.5Z plain five\n")
	c.Append("2026-10-11T01:02:09.5Z ERROR later\n")
	if !c.dropped {
		t.Fatal("trim did not run")
	}
	if len(c.lower) != len(c.lines) {
		t.Fatalf("after trim: mirror %d, lines %d", len(c.lower), len(c.lines))
	}
	for i := range c.lines {
		if c.lower[i] != strings.ToLower(c.lines[i]) {
			t.Fatalf("after trim mirror[%d] = %q, want %q", i, c.lower[i], strings.ToLower(c.lines[i]))
		}
	}
	c.applyQuery("error")
	if len(c.hits) != 1 {
		t.Fatalf("hits after trim = %d, want 1 (the surviving error line)", len(c.hits))
	}

	// Clearing the query releases the mirror; appending with no query stays
	// cheap and a later query re-folds from scratch.
	c.applyQuery("")
	if c.lower != nil {
		t.Fatal("mirror kept after the query was cleared")
	}
	c.Append("2026-10-11T01:02:10.5Z plain six\n")
	if c.lower != nil {
		t.Fatal("mirror grown while no query was active")
	}
	c.applyQuery("SIX")
	if len(c.hits) != 1 {
		t.Fatalf("re-folded hits = %d, want 1", len(c.hits))
	}
}
