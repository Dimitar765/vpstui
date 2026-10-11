package app

import (
	"fmt"
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
)

// newLogView builds a small view: six content rows, so the tests can scroll.
func newLogView() *ContainerView {
	return NewContainerView("abcdef0123456789", "web", 60, 12)
}

func feed(c *ContainerView, lines ...string) {
	for _, l := range lines {
		c.Append(l + "\n")
	}
}

func TestFlushBatchesSetContent(t *testing.T) {
	c := newLogView()
	feed(c, "one", "two", "three")
	if len(c.rows) != 3 {
		t.Fatalf("Append must style rows immediately, got %d", len(c.rows))
	}
	if strings.Contains(c.vp.View(), "three") {
		t.Fatal("Append must not render: the viewport is only touched by flush")
	}
	c.flush()
	if !strings.Contains(c.vp.View(), "three") {
		t.Fatalf("flush must render the whole batch: %q", c.vp.View())
	}
	if c.dirty {
		t.Error("flush must clear dirty, or the tick chain never stops")
	}
}

func TestStreamDoneFlushesLastBatch(t *testing.T) {
	c := newLogView()
	feed(c, "first", "second")
	c.StreamDone(nil)
	if !strings.Contains(c.vp.View(), "second") {
		t.Errorf("the final batch would be lost: %q", c.vp.View())
	}
	if c.running {
		t.Error("StreamDone must end the stream")
	}
}

// bolded reports a bold SGR, which lipgloss renders standalone (\x1b[1m) when
// the base is unstyled and merged with the colour (\x1b[1;38;…) otherwise.
func bolded(s string) bool {
	return strings.Contains(s, "\x1b[1m") || strings.Contains(s, "\x1b[1;")
}

func TestFilterHidesAndHighlightKeeps(t *testing.T) {
	c := newLogView()
	feed(c, "alpha", "ERROR one", "beta", "error two", "gamma")
	c.flush()

	c.applyQuery("err")
	if len(c.rows) != 2 {
		t.Fatalf("hide mode kept %d rows, want 2: %q", len(c.rows), c.rows)
	}
	if len(c.hits) != 0 {
		t.Errorf("hits are meaningless while hiding, got %v", c.hits)
	}

	c.toggleMode()
	if len(c.rows) != 5 {
		t.Fatalf("highlight mode must keep every line, got %d rows", len(c.rows))
	}
	if len(c.hits) != 2 {
		t.Fatalf("hits = %v, want two entries", c.hits)
	}
	for _, h := range c.hits {
		if !bolded(c.rows[h]) {
			t.Errorf("row %d is a hit but is not bolded: %q", h, c.rows[h])
		}
	}
	// A non-matching row must stay plain, or highlighting is just tinting.
	if bolded(c.rows[0]) {
		t.Errorf("non-match bolded: %q", c.rows[0])
	}
}

func TestJumpWalksMatchesAndWraps(t *testing.T) {
	c := newLogView()
	feed(c,
		"a1", "ERR first", "a2", "ERR second",
		"a3", "a4", "a5", "a6", "a7", "a8", "a9", "a10",
	)
	c.applyQuery("err")
	c.toggleMode()            // highlight mode: rows 1 and 3 are the hits
	_, _ = c.Update(key("g")) // top, and follow off

	if got := c.vp.YOffset; got != 0 {
		t.Fatalf("g should land at the top, YOffset=%d", got)
	}
	c.jump(1)
	if c.vp.YOffset != 1 {
		t.Fatalf("first jump = %d, want 1", c.vp.YOffset)
	}
	c.jump(1)
	if c.vp.YOffset != 3 {
		t.Fatalf("second jump = %d, want 3", c.vp.YOffset)
	}
	c.jump(1) // wraps to the first hit
	if c.vp.YOffset != 1 {
		t.Fatalf("wrap forward = %d, want 1", c.vp.YOffset)
	}
	c.jump(-1) // wraps to the last hit
	if c.vp.YOffset != 3 {
		t.Fatalf("wrap back = %d, want 3", c.vp.YOffset)
	}
}

func TestFollowPausesWhenScrolledUp(t *testing.T) {
	c := newLogView()
	feed(c, "l1", "l2", "l3", "l4", "l5", "l6", "l7", "l8", "l9", "l10")
	c.flush()
	if !c.follow {
		t.Fatal("a fresh view follows the stream")
	}

	_, _ = c.Update(key("g"))
	if c.follow {
		t.Fatal("scrolling to the top must pause follow")
	}

	feed(c, "new1", "new2")
	c.flush()
	if c.vp.YOffset != 0 {
		t.Errorf("flush yanked the reader to YOffset=%d, want 0", c.vp.YOffset)
	}
	if c.pending != 2 {
		t.Errorf("pending = %d, want 2 unseen rows", c.pending)
	}
	if !strings.Contains(c.header(), "2 new") {
		t.Errorf("header must advertise the backlog: %q", c.header())
	}

	_, _ = c.Update(key("G"))
	if !c.follow || c.pending != 0 {
		t.Errorf("G must resume follow and clear the badge: follow=%v pending=%d", c.follow, c.pending)
	}
}

func TestViewportScrollKeyPausesFollow(t *testing.T) {
	c := newLogView()
	feed(c, "l1", "l2", "l3", "l4", "l5", "l6", "l7", "l8", "l9", "l10")
	c.flush()
	// "k" is the viewport's own up binding, not one of ours.
	_, _ = c.Update(key("k"))
	if c.follow {
		t.Error("scrolling up with a viewport key must pause follow")
	}
	if !strings.Contains(c.header(), "● streaming") {
		t.Errorf("streaming status lost: %q", c.header())
	}
}

func TestEscapeLadder(t *testing.T) {
	c := newLogView()
	feed(c, "alpha", "ERROR one")
	c.applyQuery("err")

	c.searching = true
	if !c.HandleEscape() || c.searching {
		t.Fatal("first esc leaves the filter input and is consumed")
	}
	if !c.HandleEscape() || c.query != "" {
		t.Fatal("second esc clears the filter and is consumed")
	}
	if c.HandleEscape() {
		t.Error("third esc must fall through and leave the screen")
	}
	if len(c.rows) != 2 {
		t.Errorf("clearing the filter must restore every row, got %d", len(c.rows))
	}
}

func TestTypingDigitsWhileSearchingDoesNotSwitchSession(t *testing.T) {
	m := New(nil, nil)
	a := &Session{host: host("a"), gen: 1}
	a.dash = NewDashboard(a.host, nil, 80, 24, a.gen)
	b := &Session{host: host("b"), gen: 1}
	b.dash = NewDashboard(b.host, nil, 80, 24, b.gen)
	m.sessions = []*Session{a, b}
	m.active = 0
	m.view = viewContainer

	c := newLogView()
	c.hostID, c.gen = "a", 1
	c.searching = true
	c.in.Focus()
	a.cv = c

	_, _ = m.Update(key("1"))
	if m.active != 0 {
		t.Fatalf("sessionKeys stole the digit: active=%d", m.active)
	}
	if c.query != "1" {
		t.Errorf("the digit should have reached the filter, query=%q", c.query)
	}

	// With the filter committed, session hotkeys work again.
	c.searching = false
	_, _ = m.Update(key("2"))
	if m.active != 1 {
		t.Errorf("digits must still switch sessions outside the input, active=%d", m.active)
	}
}

func TestRingTrimDropsWholeLines(t *testing.T) {
	origCap, origKeep := logLinesCap, logLinesKeep
	logLinesCap, logLinesKeep = 20, 10
	t.Cleanup(func() { logLinesCap, logLinesKeep = origCap, origKeep })

	c := newLogView()
	for i := 0; i < 40; i++ {
		c.Append("line " + pad(i) + "\n")
	}
	c.flush()

	if len(c.lines) > logLinesCap {
		t.Fatalf("kept %d lines, cap is %d", len(c.lines), logLinesCap)
	}
	if !c.dropped {
		t.Error("dropped flag must be set once history is trimmed")
	}
	if c.rows[0] != logMarkerRow {
		t.Errorf("first row must be the marker, got %q", c.rows[0])
	}
	if strings.Count(strings.Join(c.rows, "\n"), "older lines dropped") != 1 {
		t.Error("the marker must appear exactly once")
	}
	for _, l := range c.lines {
		if len(l) != len("line 00") {
			t.Fatalf("a line was sliced mid-line: %q", l)
		}
	}
	// rows mirrors lines one-to-one (plus the marker) when unfiltered.
	if len(c.rows) != len(c.lines)+1 {
		t.Errorf("rows=%d lines=%d, want rows == lines+marker", len(c.rows), len(c.lines))
	}
	want := 0
	for _, l := range c.lines {
		want += len(l) + 1
	}
	if c.size != want {
		t.Errorf("size = %d, want %d", c.size, want)
	}
}

func TestByteCapTrimsToTheFloor(t *testing.T) {
	origBytes, origKeep, origLines, origLinesKeep := logBytesCap, logBytesKeep, logLinesCap, logLinesKeep
	logBytesCap, logBytesKeep = 100, 50
	logLinesCap, logLinesKeep = 1_000_000, 1_000_000
	t.Cleanup(func() {
		logBytesCap, logBytesKeep, logLinesCap, logLinesKeep = origBytes, origKeep, origLines, origLinesKeep
	})

	c := newLogView()
	const line = "x"
	for i := 0; i < 3; i++ {
		c.Append(strings.Repeat(line, 30) + "\n")
	}
	if c.dropped {
		t.Fatal("93 bytes is still under the 100 byte cap")
	}
	c.Append(strings.Repeat(line, 30) + "\n") // 124 bytes crosses the cap
	if !c.dropped {
		t.Fatal("crossing the byte cap must trim")
	}
	if c.size > logBytesKeep {
		t.Errorf("size = %d, want trimmed to the floor of %d", c.size, logBytesKeep)
	}
	for _, l := range c.lines {
		if len(l) != 30 {
			t.Fatalf("a line was sliced mid-line: %q", l)
		}
	}

	// Steady state: growth is bounded by the cap, not by how fast we append.
	for i := 0; i < 200; i++ {
		c.Append(strings.Repeat(line, 30) + "\n")
		if c.size > logBytesCap+31 {
			t.Fatalf("append %d: size grew to %d, past the cap of %d", i, c.size, logBytesCap)
		}
	}
}

// pad keeps fixture line lengths identical so trimming is easy to assert on.
func pad(i int) string { return fmt.Sprintf("%02d", i) }

// The flush tick has no explicit teardown: it reschedules itself only while the
// stream is open or rows are unrendered, so a closed view must end the chain.
func TestFlushTickChainStopsWhenStreamEnds(t *testing.T) {
	m := New(nil, nil)
	s := &Session{host: host("a"), gen: 7}
	s.dash = NewDashboard(s.host, nil, 80, 24, s.gen)
	s.cv = NewContainerView("abcdef0123456789", "web", 80, 24)
	s.cv.hostID, s.cv.gen = "a", 7
	m.sessions = []*Session{s}
	m.view = viewContainer
	tag := msgTag{hostID: "a", gen: 7}

	m.Update(logChunkMsg{msgTag: tag, chunk: "ERROR: one\n"})
	if _, cmd := m.Update(logFlushMsg{msgTag: tag}); cmd == nil {
		t.Fatal("a live stream must keep the flush tick alive")
	}
	if !strings.Contains(s.cv.vp.View(), "ERROR: one") {
		t.Fatal("the flush did not render the chunk")
	}

	m.Update(logChunkMsg{msgTag: tag, chunk: "ERROR: two\n"})
	s.cv.Stop()
	if _, cmd := m.Update(logFlushMsg{msgTag: tag}); cmd != nil {
		t.Error("the tick chain must end once the stream is closed and flushed")
	}
	if !strings.Contains(s.cv.vp.View(), "ERROR: two") {
		t.Error("Stop must flush the last batch")
	}
}

func TestLogHelpBarFitsOneRow(t *testing.T) {
	tests := []struct {
		name    string
		prepare func(*ContainerView)
	}{
		{"no filter", func(*ContainerView) {}},
		{"filter committed", func(c *ContainerView) { c.applyQuery("err") }},
		{"highlight mode", func(c *ContainerView) { c.applyQuery("err"); c.toggleMode() }},
		{"typing", func(c *ContainerView) { c.searching = true }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := NewContainerView("abcdef0123456789", "web", 80, 30)
			tt.prepare(c)
			if h := lipgloss.Height(c.help()); h != 1 {
				t.Errorf("help bar wrapped to %d rows, which breaks the pinned footer", h)
			}
		})
	}
}
