package app

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"

	"vpstui/internal/config"
	"vpstui/internal/httpapi"
	"vpstui/internal/models"
)

// tagFixture is one host's labels: only the postfix container is named, so a test
// can always compare a labelled row against an unlabelled one.
func tagFixture() *TagStore {
	return NewTagStore(map[string]models.Tag{
		config.TagKey("h1", "odysseus-postfix-1"): {
			HostID: "h1", Name: "odysseus-postfix-1", Tag: "mail", Color: "sky", ID: "11f4c9",
		},
	})
}

// errFixtureOn retags the shared error fixture so each line's HostID is the id of
// the session that swept it — which is what parseErrSweep does in production, and
// what makes a label resolve through the same key the table uses.
func errFixtureOn(hostID string) []ErrLine {
	out := []ErrLine(nil)
	for _, l := range errFixture() {
		if l.HostName == "beta" {
			continue
		}
		l.HostID, l.HostName = hostID, hostID
		out = append(out, l)
	}
	return out
}

func TestTagKeyAndDisplay(t *testing.T) {
	store := tagFixture()

	if got, want := config.TagKey("h1", "web"), "h1/web"; got != want {
		t.Errorf("tagKey = %q, want %q", got, want)
	}
	if got := store.Display("h1", "odysseus-postfix-1"); got != "mail odysseus-postfix-1" {
		t.Errorf("Display = %q, want the label first so truncation eats the name tail", got)
	}
	// Unlabelled, and labelled on another host: the plain docker name either way.
	for _, tc := range [][2]string{{"h1", "redis"}, {"h2", "odysseus-postfix-1"}} {
		if got := store.Display(tc[0], tc[1]); got != tc[1] {
			t.Errorf("Display(%q, %q) = %q, want the docker name", tc[0], tc[1], got)
		}
	}
	if got := store.Tint("h1", "odysseus-postfix-1"); got != "sky" {
		t.Errorf("Tint = %q, want sky", got)
	}
	if got := store.Tint("h1", "redis"); got != "" {
		t.Errorf("Tint of an unlabelled container = %q, want no colour", got)
	}

	// A nil store is a real state, and an unknown colour must stay readable:
	// tags.json is hand-editable.
	var nilStore *TagStore
	if got := nilStore.Display("h1", "web"); got != "web" {
		t.Errorf("nil store Display = %q, want the name", got)
	}
	if got := tagStyle("chartreuse").Render("mail"); got != "mail" {
		t.Errorf("unknown colour rendered %q, want plain text", got)
	}
	if got := tagStyle("sky").Render("mail"); !strings.HasPrefix(got, "\x1b[38;5;110m") {
		t.Errorf("sky = %q, want ANSI 110", got)
	}
}

func TestTagSetClearChanged(t *testing.T) {
	store := tagFixture()

	if store.Set("h1", "odysseus-postfix-1", "mail", "sky", "11f4c9") {
		t.Error("re-committing the same label reported a change — that would rewrite tags.json on every enter")
	}
	if !store.Set("h1", "odysseus-postfix-1", "mail", "rose", "11f4c9") {
		t.Error("a colour change is a change")
	}
	if !store.Set("h1", "redis", "cache", "green", "aa11") {
		t.Error("a new label is a change")
	}
	if tag, ok := store.Get("h1", "redis"); !ok || tag.Tag != "cache" || tag.Color != "green" {
		t.Errorf("Get after Set = %+v, %v", tag, ok)
	}
	if !store.Clear("h1", "redis") {
		t.Error("Clear of a labelled container reported nothing to do")
	}
	if store.Clear("h1", "redis") {
		t.Error("Clear ran twice and still reported a change")
	}
	if _, ok := store.Get("h1", "redis"); ok {
		t.Error("the label survived Clear")
	}
	// An entry whose text is empty is not a label: a hand-edited file must not
	// paint a blank chip or claim the container is named.
	store.Set("h1", "ghost", "", "sky", "x")
	if _, ok := store.Get("h1", "ghost"); ok {
		t.Error("an empty tag reads as a label")
	}
	if got := store.Display("h1", "ghost"); got != "ghost" {
		t.Errorf("Display of an empty tag = %q, want the plain name", got)
	}
	if !reflect.DeepEqual(store.All(), store.by) {
		t.Error("All is not the backing map, so a save cannot see what was just set")
	}
}

func TestTagValidate(t *testing.T) {
	cases := []struct {
		in   string
		want string
		ok   bool
	}{
		{"mail", "mail", true},
		{"  mail  ", "mail", true},
		{"mail relay", "mail relay", true},
		{"a/b", "a b", true}, // a slash would make "host/name" ambiguous
		{"web\x00", "web", true},
		{"", "", false},
		{"   ", "", false},
		{"/", "", false},
		{strings.Repeat("x", maxTagRunes+10), strings.Repeat("x", maxTagRunes), true},
	}
	for _, tc := range cases {
		got, ok := normalizeTag(tc.in)
		if got != tc.want || ok != tc.ok {
			t.Errorf("normalizeTag(%q) = (%q, %v), want (%q, %v)", tc.in, got, ok, tc.want, tc.ok)
		}
	}
	// The clamp counts runes, so a CJK alias keeps its length.
	if got, _ := normalizeTag(strings.Repeat("字", maxTagRunes+5)); len([]rune(got)) != maxTagRunes {
		t.Errorf("clamped to %d runes, want %d", len([]rune(got)), maxTagRunes)
	}
}

func TestNextTagColorCycles(t *testing.T) {
	seen := map[string]bool{}
	name := ""
	for range len(tagColors) {
		name = nextTagColor(name)
		seen[name] = true
	}
	if len(seen) != len(tagColors) {
		t.Errorf("tab reached %d colours of %d", len(seen), len(tagColors))
	}
	if got := nextTagColor("nonsense"); got != tagColors[0].name {
		t.Errorf("an unknown colour restarts at %q, want %q", got, tagColors[0].name)
	}
}

// tagDash is a dashboard on host h1 with two containers, one of which the store
// names.
func tagDash(t *testing.T, width int, store *TagStore) *Dashboard {
	t.Helper()
	d := NewDashboard(host("h1"), nil, width, 30, 1)
	d.containers = []models.Container{
		{ID: "11f4c9", Names: "odysseus-postfix-1", Image: "postfix", State: "running", Status: "Up 3 days"},
		{ID: "aa11bb", Names: "redis", Image: "redis:7", State: "running", Status: "Up 2 weeks"},
	}
	if store != nil {
		d.SetTags(store)
	}
	d.rebuild()
	return d
}

func TestDashboardNameCellTagged(t *testing.T) {
	store := tagFixture()
	for _, width := range []int{60, 80, 100, 140} {
		plain := tagDash(t, width, nil)
		tagged := tagDash(t, width, store)

		// No new column: the label rides inside the adaptive Name cell, so the
		// width machinery — and every test of it — is untouched by tagging.
		if !reflect.DeepEqual(plain.tbl.cols, tagged.tbl.cols) {
			t.Errorf("width %d: cols changed by tagging: %+v vs %+v", width, plain.tbl.cols, tagged.tbl.cols)
		}
		if got := tagged.tbl.rows[0][0]; got != "mail odysseus-postfix-1" {
			t.Errorf("width %d: name cell = %q, want the label then the docker name", width, got)
		}
		if got := tagged.tbl.rows[1][0]; got != "redis" {
			t.Errorf("width %d: unlabelled cell = %q, want the plain name", width, got)
		}
		if got := tagged.tbl.tint[0]; got != "sky" {
			t.Errorf("width %d: row tint = %q, want sky", width, got)
		}
		if got := tagged.tbl.tint[1]; got != "" {
			t.Errorf("width %d: unlabelled tint = %q, want none", width, got)
		}
		// The old cell text must still fit the old column: the label is inside the
		// string fitPlain clips, not beside it.
		nameCol := tagged.tbl.cols[0].width
		if got := lipgloss.Width(fitPlain(tagged.tbl.rows[0][0], nameCol)); got > nameCol {
			t.Errorf("width %d: fitted name cell is %d cells in a %d column", width, got, nameCol)
		}

		v := tagged.View()
		plainV := plain.View()
		// The frame is the footer's floor, not the terminal's: a long help line
		// already overflows a 60-column window with or without labels. What
		// tagging must not do is make it worse.
		if got, want := lipgloss.Width(v), lipgloss.Width(plainV); got != want {
			t.Errorf("width %d: tagged frame is %d cells, untagged is %d", width, got, want)
		}
		if width >= 100 {
			if got := lipgloss.Width(v); got > width {
				t.Errorf("width %d: frame is %d wide", width, got)
			}
		}
		if got := h(v); got != 30 {
			t.Errorf("width %d: frame height = %d, want the pinned 30", width, got)
		}
		// Colour goes on after fitting, so an escape sequence can neither inflate
		// the cell nor make the width check above lie.
		if !strings.Contains(v, "\x1b[38;5;110m") {
			t.Errorf("width %d: no sky in the frame\n%s", width, v)
		}
		if strings.Contains(plain.View(), "\x1b[38;5;110m") {
			t.Errorf("width %d: an untagged table borrowed the label colour", width)
		}
		// Truncation must eat the name tail, never the label.
		row := tableRowUnder(t, tagged, "Name")
		if !strings.Contains(row, "mail") {
			t.Errorf("width %d: the narrow frame clipped the label away: %q", width, row)
		}
	}
}

// tableRowUnder returns the rendered row directly beneath the table header that
// carries the given title.
func tableRowUnder(t *testing.T, d *Dashboard, title string) string {
	t.Helper()
	lines := strings.Split(d.View(), "\n")
	for i, ln := range lines {
		if strings.Contains(ln, title) && i+1 < len(lines) {
			return lines[i+1]
		}
	}
	t.Fatalf("no %q header in the frame\n%s", title, d.View())
	return ""
}

func TestTaggedDetailPaneNamesTheLabel(t *testing.T) {
	d := tagDash(t, 100, tagFixture())
	d.detail = &detailState{name: "odysseus-postfix-1", text: "Image postfix · State running"}
	v := d.View()
	if !strings.Contains(v, "mail") {
		t.Errorf("detail pane does not show the label\n%s", v)
	}
	// The full docker name stays discoverable there, because the table cell clips.
	if !strings.Contains(v, "odysseus-postfix-1") {
		t.Errorf("detail pane lost the docker name\n%s", v)
	}
}

func TestLogTitleShowsTag(t *testing.T) {
	store := tagFixture()
	c := NewContainerView("11f4c9ab12cd", "odysseus-postfix-1", 100, 30)
	c.SetTag(store.Display("h1", "odysseus-postfix-1"), store.Tint("h1", "odysseus-postfix-1"))
	v := c.View()
	if !strings.Contains(v, "mail odysseus-postfix-1") {
		t.Errorf("log title = %q, want the label beside the docker name", v)
	}
	if !strings.Contains(v, "\x1b[38;5;110m") {
		t.Errorf("log title is not in the label colour\n%s", v)
	}
	// An empty label must not blank the title: root may open a stream for a
	// container nobody has named.
	untouched := NewContainerView("11f4c9ab12cd", "redis", 100, 30)
	untouched.SetTag("", "")
	if got := untouched.View(); !strings.Contains(got, "redis") {
		t.Errorf("SetTag with no label erased the name\n%s", got)
	}
}

// The prompt owns the keys while it is open: sessionKeys uses tab and 1-9, so an
// ungated prompt would switch sessions instead of choosing a colour.
func TestTagPromptKeys(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	m := New(nil, nil)
	s := &Session{host: host("h1"), gen: 1}
	// The store reaches a dashboard when its session is built, so the fixture
	// wires it by hand: without this the table would render names only and the
	// commit check below would prove nothing.
	s.dash = tagDash(t, 100, nil)
	s.dash.SetTags(m.tags)
	m.sessions = []*Session{s}
	m.active = 0
	m.view = viewDashboard
	m.width, m.height = 100, 30

	if _, cmd := m.Update(key("t")); cmd == nil {
		t.Fatal("t on a container row did not focus the prompt")
	}
	if !m.tag.active {
		t.Fatal("prompt did not open")
	}
	if s.dash.prompt == "" {
		t.Error("the prompt row was not mirrored into the dashboard")
	}
	if s.dash.tbl.rows[0][0] != "odysseus-postfix-1" {
		t.Errorf("prompt target is not the selected row: %q", s.dash.tbl.rows[0][0])
	}

	before := m.tag.color
	m.Update(key("tab"))
	if m.tag.color == before {
		t.Errorf("tab did not change the colour (still %q)", m.tag.color)
	}
	// Digits type rather than select a session slot.
	m.Update(key("1"))
	if m.active != 0 {
		t.Errorf("typing switched the session to %d", m.active)
	}
	if m.tag.in.Value() != "1" {
		t.Errorf("input holds %q, want the typed digit", m.tag.in.Value())
	}
	// The mirrored row has to follow every keystroke. It is a pre-rendered
	// string, so a prompt that only paints when it opens shows a dead field
	// while the user types — which is what happened on the first real run.
	m.Update(key("x"))
	if !strings.Contains(s.dash.prompt, "1x") {
		t.Errorf("prompt row did not follow typing: %q", s.dash.prompt)
	}
	m.tag.in.SetValue("")
	m.Update(key("esc"))
	m.Update(key("t"))
	if s.dash.prompt == "" {
		t.Fatal("reopening the prompt cleared its row")
	}
	// The prompt row clips instead of folding: a second row inside a pinned frame
	// would push the footer off the screen.
	if got := h(m.tag.line(60)); got != 1 {
		t.Errorf("prompt line is %d rows at 60 columns", got)
	}

	m.Update(key("esc"))
	if m.tag.active {
		t.Error("esc left the prompt open")
	}
	if s.dash.prompt != "" {
		t.Error("esc left the prompt row on screen")
	}
	if len(m.tags.All()) != 0 {
		t.Errorf("esc saved %+v", m.tags.All())
	}
	if last := lastLine(s.dash.View()); !strings.Contains(last, "esc") || !strings.Contains(last, "back") {
		t.Errorf("footer is not the last row after closing the prompt: %q", last)
	}

	// enter commits: the label lands in the store, on disk, and in the table.
	m.Update(key("t"))
	m.tag.in.SetValue("mail")
	m.Update(key("tab"))
	_, cmd := m.Update(key("enter"))
	if m.tag.active {
		t.Error("enter left the prompt open")
	}
	// The write runs off the UI thread; executing its command is what the
	// runtime does with every returned command.
	if cmd != nil {
		cmd()
	}
	if got := s.dash.tbl.rows[0][0]; got != "mail odysseus-postfix-1" {
		t.Errorf("committed table cell = %q", got)
	}
	tag, ok := m.tags.Get("h1", "odysseus-postfix-1")
	if !ok {
		t.Fatal("commit did not reach the store")
	}
	if tag.ID != "11f4c9" {
		t.Errorf("stored id = %q, want the container id the label was written for", tag.ID)
	}
	saved, err := config.LoadTags()
	if err != nil || len(saved) != 1 {
		t.Errorf("commit did not persist: %+v, %v", saved, err)
	}
	if !strings.Contains(s.dash.toast, "tagged") {
		t.Errorf("toast = %q, want confirmation of the label", s.dash.toast)
	}

	// An emptied field is an untag: the prompt opens prefilled, so reaching empty
	// means the label was deleted on purpose.
	m.Update(key("t"))
	m.tag.in.SetValue("")
	m.Update(key("enter"))
	if _, ok := m.tags.Get("h1", "odysseus-postfix-1"); ok {
		t.Error("committing an empty field kept the label")
	}

	// T clears without a prompt, and says so when there was nothing to clear.
	m.Update(key("t"))
	m.tag.in.SetValue("mail")
	m.Update(key("enter"))
	m.Update(key("T"))
	if _, ok := m.tags.Get("h1", "odysseus-postfix-1"); ok {
		t.Error("T did not clear the label")
	}
	m.Update(key("T"))
	if !strings.Contains(s.dash.toast, "has no tag") {
		t.Errorf("T on an unlabelled row = %q, want a no-op report", s.dash.toast)
	}
}

func TestTagPromptOnErrorsRowPrefillsAndDoesNotSweep(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	m := New(nil, nil)
	s := errSweptSession("alpha", errFixtureOn("alpha")...)
	m.tags.Set("alpha", "web", "shop", "violet", "c1")
	m.sessions = []*Session{s}
	m.active = 0
	m.hosts = []models.Host{s.host}
	m.width, m.height = 100, 30

	if _, cmd := m.Update(key("a")); cmd != nil {
		t.Fatalf("opening errors returned a command: %v", cmd)
	}
	if m.view != viewErrors || m.errView == nil {
		t.Fatalf("a did not open the errors screen (view=%d)", m.view)
	}
	m.Update(key("m")) // flat feed: every row is one line with one container
	if _, cmd := m.Update(key("t")); cmd == nil {
		t.Fatal("t on an errors row did not focus the prompt")
	}
	if want := (tagTarget{hostID: "alpha", name: "web", id: "c1"}); m.tag.target != want {
		t.Errorf("prompt target = %+v, want %+v", m.tag.target, want)
	}
	if m.tag.in.Value() != "shop" || m.tag.color != "violet" {
		t.Errorf("prompt did not pre-fill: value=%q colour=%q", m.tag.in.Value(), m.tag.color)
	}
	if m.errView.prompt == "" {
		t.Error("the prompt row was not mirrored into the errors screen")
	}
	// r would sweep every connected host and y would write to the clipboard;
	// neither may run while the user is typing.
	m.Update(key("r"))
	m.Update(key("y"))
	if s.dash.errLoading {
		t.Error("typing in the prompt started a sweep")
	}
	m.tag.in.SetValue("relay")
	m.Update(key("enter"))
	// Flat cell order follows errColsFor: TIME, HOST, CONTAINER, SEV, MESSAGE.
	// The label committed here differs from the pre-filled one, so a cell that
	// still said "shop" would mean the commit never repainted the screen.
	if got := m.errView.tbl.rows[0][2]; !strings.Contains(got, "relay") {
		t.Errorf("flat CONTAINER cell after commit = %q, want the new label", got)
	}
	if m.errView.prompt != "" {
		t.Error("the prompt row survived the commit")
	}
}

// Tags join the corpus at render time. The sweep builds lines inside a command
// goroutine that must not read mutable UI state, so relabelling has to repaint
// without re-running anything: no Refresh, and no regrouped history.
func TestTagsJoinIsRenderTime(t *testing.T) {
	sess := []*Session{errSweptSession("alpha", errFixtureOn("alpha")...)}
	store := NewTagStore(nil)
	e := NewErrorsView(100, 30)
	e.SetTags(store)
	e.Refresh(sess, 1)

	before := append([]string(nil), e.rowsGroup[0].Containers...)
	e.tbl.SetCursor(0)
	if got := e.displayContainers(e.rowsGroup[0].Containers); strings.Contains(strings.Join(got, " "), "shop") {
		t.Fatalf("untagged group already says %v", got)
	}

	store.Set("alpha", "web", "shop", "sky", "c1")
	e.rebuild() // no Refresh, no sweep, no docker command

	// The grouped table has no container column, so the label is checked where
	// the screen shows one.
	e.tbl.SetCursor(0)
	e.Update(key("m")) // flat feed, which rebuilds from the same corpus
	if got := e.tbl.rows[0][2]; !strings.Contains(got, "shop") {
		t.Errorf("rebuild did not pick up the label: %q", got)
	}
	// Identity is the docker name, so a label never re-groups the history.
	if !reflect.DeepEqual(before, append([]string(nil), e.rowsGroup[0].Containers...)) {
		t.Errorf("grouping changed: %v -> %v", before, e.rowsGroup[0].Containers)
	}
	// The expand pane labels; the yank body does not.
	e.Update(key("m")) // back to grouped
	e.tbl.SetCursor(0)
	e.Update(key("enter"))
	v := e.View()
	if !strings.Contains(v, "containers: alpha/shop web") {
		t.Errorf("detail pane containers line is missing the label:\n%s", v)
	}
	if !strings.Contains(v, "alpha/shop web ERROR db down") {
		t.Errorf("detail pane sample lost the docker name:\n%s", v)
	}
	if got := e.yankLine(); strings.Contains(got, "shop") {
		t.Errorf("yanked line carries a label that does not exist on the host: %q", got)
	}
	if got := h(v); got != 30 {
		t.Errorf("tagged expand pane is %d rows, want the pinned 30\n%s", got, v)
	}
}

func TestYankCmdTexts(t *testing.T) {
	f := errFixture()
	sess := []*Session{errSweptSession("alpha", f[:2]...), errSweptSession("beta", f[2:]...)}

	e := NewErrorsView(100, 30)
	e.SetTags(NewTagStore(nil))
	e.Refresh(sess, 2)

	// Grouped mode: y is the newest sample under the cursor, Y the header plus
	// every raw line the group kept.
	if got, want := e.yankLine(), "alpha/web 01:01:00 error ERROR db down"; got != want {
		t.Errorf("yankLine = %q, want %q", got, want)
	}
	head := e.yankGroup()
	header, body, _ := strings.Cut(head, "\n")
	if !strings.HasPrefix(header, "db down  (error, 2×, alpha/web, ") {
		t.Errorf("group header = %q, want sig, severity, count and containers", header)
	}
	if !strings.HasSuffix(header, ")") || !strings.Contains(header, "→") {
		t.Errorf("group header lost the time window: %q", header)
	}
	if want := "alpha/web 01:00:00 warn WARN db down\nalpha/web 01:01:00 error ERROR db down"; body != want {
		t.Errorf("group body = %q, want both raw lines oldest first", body)
	}

	// Flat mode: y is the row under the cursor, Y the group that line belongs to.
	e.Update(key("m"))
	e.tbl.SetCursor(1) // the feed is newest first: 01:01:00, then 01:00:00, then beta
	if got, want := e.yankLine(), "alpha/web 01:01:00 error ERROR db down"; got != want {
		t.Errorf("flat yankLine = %q, want %q", got, want)
	}
	if !strings.HasPrefix(e.yankGroup(), "db down  (error, 2×,") {
		t.Errorf("flat yankGroup = %q, want the signature this line was grouped under", e.yankGroup())
	}
	e.tbl.SetCursor(0)
	if got, want := e.yankLine(), "beta/api 01:02:00 error ERROR no space left"; got != want {
		t.Errorf("flat yankLine on the newest row = %q, want %q", got, want)
	}

	// Nothing under the cursor, nothing to yank — and no helper process invoked.
	empty := NewErrorsView(100, 30)
	empty.Refresh([]*Session{{host: host("alpha"), gen: 1}}, 1)
	if empty.yankLine() != "" || empty.yankGroup() != "" {
		t.Error("an unswept screen offered text to yank")
	}
	if yankCmd("") != nil {
		t.Error("yankCmd on nothing returned a command — that would call a clipboard helper with empty input")
	}
	if yankCmd("one line") == nil {
		t.Fatal("yankCmd on real text returned nil")
	}
	// The command is deliberately not invoked: it shells out to wl-copy or xclip.

	// Root routes the keypress through a command, so Update never blocks on the
	// helper process.
	m := New(nil, nil)
	m.errView = e
	m.view = viewErrors
	m.width, m.height = 100, 30
	for _, k := range []string{"y", "Y"} {
		if _, cmd := m.Update(key(k)); cmd == nil {
			t.Errorf("%s did not return a command", k)
		}
	}
	m.errView = empty
	for _, k := range []string{"y", "Y"} {
		if _, cmd := m.Update(key(k)); cmd != nil {
			t.Errorf("%s on an empty screen returned a command", k)
		}
	}
}

var errClipboard = errors.New("exit status 1")

func TestYankStatusIsCountsOnly(t *testing.T) {
	// The shape of the message is the guarantee: it carries sizes and an error, so
	// a hostile log line cannot be echoed back into the status row.
	typ := reflect.TypeFor[yankMsg]()
	var fields []string
	for i := range typ.NumField() {
		fields = append(fields, typ.Field(i).Name)
	}
	if want := []string{"lines", "bytes", "err"}; !reflect.DeepEqual(fields, want) {
		t.Errorf("yankMsg fields = %v, want %v — no field may hold the yanked text", fields, want)
	}

	cases := []struct {
		msg  yankMsg
		want string
	}{
		{yankMsg{lines: 1, bytes: 212}, "yanked 1 line (212 bytes)"},
		{yankMsg{lines: 20, bytes: 4199}, "yanked 20 lines (4.1 KiB)"},
		{yankMsg{lines: 3, bytes: 100}, "yanked 3 lines (100 bytes)"},
		{yankMsg{err: errClipboard}, "clipboard unavailable — install wl-copy, xclip or pbcopy"},
	}
	for _, tc := range cases {
		if got := tc.msg.Describe(); got != tc.want {
			t.Errorf("Describe(%+v) = %q, want %q", tc.msg, got, tc.want)
		}
	}

	// Where it lands: the errors screen notes it, every other surface uses the
	// status row — and neither receives the text, only the counts.
	m := New(nil, nil)
	m.errView = NewErrorsView(100, 30)
	m.errView.Refresh([]*Session{errSweptSession("alpha", errFixtureOn("alpha")...)}, 1)
	m.view = viewErrors
	m.Update(yankMsg{lines: 2, bytes: 64})
	if !strings.Contains(m.errView.note, "yanked 2 lines") {
		t.Errorf("errors note = %q", m.errView.note)
	}
	m.view = viewDashboard
	m.Update(yankMsg{lines: 2, bytes: 64})
	if got := "yanked 2 lines (64 bytes)"; m.status != got {
		t.Errorf("status = %q, want %q", m.status, got)
	}
}

func TestTagsPublishDeterministic(t *testing.T) {
	d := tagDash(t, 100, tagFixture())
	d.errCorpus = errFixture()
	d.errFetchedAt = time.Unix(1700000000, 0).UTC()
	// web is one of the fixture's container names, so the label resolves through
	// the same host id + name key the table used.
	d.tags.Set("h1", "web", "shop", "green", "c1")

	first := d.snapshot(host("h1"), true)
	second := d.snapshot(host("h1"), true)

	// FetchedAt is a wall clock and is excluded; everything the page compares has
	// to repeat byte for byte, or its skip-repaint never fires.
	for _, part := range []struct {
		name string
		a, b any
	}{
		{"containers", first.Containers, second.Containers},
		{"errors", first.Errors, second.Errors},
	} {
		A, err := json.Marshal(part.a)
		if err != nil {
			t.Fatalf("marshal %s: %v", part.name, err)
		}
		B, err := json.Marshal(part.b)
		if err != nil {
			t.Fatalf("marshal %s: %v", part.name, err)
		}
		if string(A) != string(B) {
			t.Errorf("%s changed between two publishes of unchanged state:\n%s\n%s", part.name, A, B)
		}
	}

	c0, c1 := first.Containers[0], first.Containers[1]
	if c0.Tag != "mail" || c0.TagColor != "sky" {
		t.Errorf("container snapshot = %+v, want the label and its colour", c0)
	}
	if c1.Tag != "" || c1.TagColor != "" {
		t.Errorf("unlabelled container published %+v/%+v", c1.Tag, c1.TagColor)
	}
	var checked bool
	for _, e := range first.Errors {
		if e.Name != "web" {
			continue
		}
		checked = true
		if e.Tag != "shop" {
			t.Errorf("error snapshot tag = %q, want shop", e.Tag)
		}
		// The stamp is the cell the TUI yanks, so the page can copy it verbatim.
		if e.Stamp != "01:00:00" && e.Stamp != "01:01:00" {
			t.Errorf("error snapshot stamp = %q, want the resolved time", e.Stamp)
		}
	}
	if !checked {
		t.Fatal("the fixture corpus holds no web line to check")
	}
}

// TestWebErrorsCopyText pins the contract behind the page's copy buttons: the
// strings sampleLine and groupText build in JavaScript must equal what the TUI's y
// and Y write for the same published data. The mirror below reads only fields the
// endpoint actually publishes, so a renamed key fails here instead of silently
// changing what lands on the clipboard.
func TestWebErrorsCopyText(t *testing.T) {
	// Local instants, because the page formats timestamps in the browser's zone
	// exactly as the TUI does in its own — the same box, the same wall clock.
	at := time.Now()
	lines := []ErrLine{
		{HostID: "alpha", HostName: "alpha", ID: "c1", Name: "odysseus-postfix-1",
			Sev: lvlError, Sig: "queue stalled", At: at.Add(-time.Minute), Text: "ERROR queue stalled"},
		{HostID: "alpha", HostName: "alpha", ID: "c1", Name: "odysseus-postfix-1",
			Sev: lvlWarn, Sig: "queue stalled", At: at, Text: "WARN queue stalled again"},
	}
	store := NewTagStore(nil)
	store.Set("alpha", "odysseus-postfix-1", "mail", "sky", "c1")

	sess := errSweptSession("alpha", lines...)
	sess.dash.SetTags(store)

	api := httpapi.NewStore()
	srv := httptest.NewServer(httpapi.NewServer(api, "").Handler)
	defer srv.Close()

	snap := sess.dash.snapshot(sess.host, true)
	api.Publish(snap.ID, snap)

	body := get(t, srv.URL+"/api/errors")
	type sample struct {
		Host      string `json:"host"`
		Container string `json:"container"`
		Tag       string `json:"tag"`
		Sev       string `json:"sev"`
		Stamp     string `json:"stamp"`
		Text      string `json:"text"`
	}
	var got struct {
		Groups []struct {
			Sig        string    `json:"sig"`
			Sev        string    `json:"sev"`
			Count      int       `json:"count"`
			Containers []string  `json:"containers"`
			FirstSeen  time.Time `json:"firstSeen"`
			LastSeen   time.Time `json:"lastSeen"`
			Sample     *sample   `json:"sample"`
			Examples   []sample  `json:"examples"`
		} `json:"groups"`
	}
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("decode: %v\n%s", err, body)
	}
	if len(got.Groups) != 1 || got.Groups[0].Sample == nil {
		t.Fatalf("groups = %s", body)
	}
	g := got.Groups[0]
	if g.Sample.Tag != "mail" {
		t.Errorf("sample tag = %q, want the label the TUI published", g.Sample.Tag)
	}
	if len(g.Examples) != 2 {
		t.Fatalf("examples = %d, want both lines of the group: %s", len(g.Examples), body)
	}

	// Mirrors of the page's two builders, field for field.
	sampleLine := func(s sample) string {
		return s.Host + "/" + s.Container + " " + orDash(s.Stamp) + " " + s.Sev + " " + s.Text
	}
	head := g.Sig + "  (" + g.Sev + ", " + strconv.Itoa(g.Count) + "×, " +
		strings.Join(g.Containers, " + ") + ", " + jsErrTime(g.FirstSeen) + " → " + jsErrTime(g.LastSeen) + ")"
	line := sampleLine(*g.Sample)
	var groupBody strings.Builder
	for _, s := range g.Examples {
		groupBody.WriteString(sampleLine(s))
		groupBody.WriteString("\n")
	}

	e := NewErrorsView(100, 30)
	e.SetTags(store)
	e.Refresh([]*Session{sess}, 1)
	e.tbl.SetCursor(0)
	if got := e.yankLine(); got != line {
		t.Errorf("TUI y   = %q\nweb copy = %q", got, line)
	}
	if want := head + "\n" + strings.TrimSuffix(groupBody.String(), "\n"); e.yankGroup() != want {
		t.Errorf("TUI Y    = %q\nweb copy = %q", e.yankGroup(), want)
	}
	// The newest published sample is the last example, so the row and the copy
	// body cannot point at different lines.
	if g.Examples[len(g.Examples)-1] != *g.Sample {
		t.Errorf("sample is not the newest example: %+v vs %+v", *g.Sample, g.Examples[len(g.Examples)-1])
	}
	// The header labels, because it is for a human; the body keeps docker names,
	// because it is for grep on the host.
	if !strings.Contains(head, "alpha/mail odysseus-postfix-1") {
		t.Errorf("group header containers = %q, want the label beside the name", g.Containers)
	}

	page := string(get(t, srv.URL))
	for _, forbidden := range []string{".innerHTML", "document.write", "fetch('", `fetch("`} {
		if strings.Contains(page, forbidden) {
			t.Errorf("the page contains %q", forbidden)
		}
	}
	for _, need := range []string{"navigator.clipboard.writeText", "tagChip(c.tag, c.tagColor)",
		"ctrShown(n)", "groupText(g)", "sampleLine(s)", "g.examples", "tag-sky"} {
		if !strings.Contains(page, need) {
			t.Errorf("the page is missing %q", need)
		}
	}
}

func orDash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}

// jsErrTime mirrors the page's errTime. Where the TUI prints a time in the zone
// its lines carry, the browser prints it in the local zone, so the test reads the
// instant the way JavaScript would.
func jsErrTime(t time.Time) string {
	if t.IsZero() {
		return "—"
	}
	l := t.Local()
	now := time.Now()
	if l.Year() == now.Year() && l.Month() == now.Month() && l.Day() == now.Day() {
		return l.Format("15:04:05")
	}
	return l.Format("01-02 15:04")
}

func get(t *testing.T, url string) []byte {
	t.Helper()
	res, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer res.Body.Close()
	body, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if res.StatusCode != http.StatusOK {
		t.Fatalf("GET %s -> %d: %s", url, res.StatusCode, body)
	}
	return body
}

// A real compose fleet logs the same message on two containers in the same
// second, so a group's newest-20 window can cut mid-instant and the body it
// pastes depends on the tie-break. The screen and the page must hand back the
// identical text, which is why both trim with one shared rule
// (internal/errsample) instead of two comparators that can drift apart. This
// fixture is built to make that cut land inside the window.
func TestWebErrorsCopyTextTiedHosts(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	base := time.Now().Add(-time.Hour).Truncate(time.Second)

	mk := func(name string) *Session {
		var lines []ErrLine
		for i := 0; i < 25; i++ {
			at := base.Add(time.Duration(i) * time.Second)
			lines = append(lines, ErrLine{
				HostID: name, HostName: name, ID: "c1", Name: "web",
				Sev: lvlWarn, Sig: "db down", At: at, Stamp: at.Format("15:04:05"),
				Text: "WARN db down",
			})
		}
		return errSweptSession(name, lines...)
	}
	alpha, beta := mk("alpha"), mk("beta")

	e := NewErrorsView(100, 30)
	e.Refresh([]*Session{alpha, beta}, 2)
	e.tbl.SetCursor(0)
	tuiText := e.yankGroup()

	api := httpapi.NewStore()
	for _, s := range []*Session{alpha, beta} {
		api.Publish(s.host.ID, s.dash.snapshot(s.host, true))
	}
	srv := httptest.NewServer(httpapi.NewServer(api, "").Handler)
	defer srv.Close()

	var got struct {
		Groups []struct {
			Sig        string    `json:"sig"`
			Sev        string    `json:"sev"`
			Count      int       `json:"count"`
			Containers []string  `json:"containers"`
			FirstSeen  time.Time `json:"firstSeen"`
			LastSeen   time.Time `json:"lastSeen"`
			Examples   []struct {
				Host      string `json:"host"`
				Container string `json:"container"`
				Sev       string `json:"sev"`
				Stamp     string `json:"stamp"`
				Text      string `json:"text"`
			} `json:"examples"`
		} `json:"groups"`
	}
	if err := json.Unmarshal(get(t, srv.URL+"/api/errors"), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got.Groups) != 1 {
		t.Fatalf("groups = %d, want the two hosts merged: %+v", len(got.Groups), got.Groups)
	}
	g := got.Groups[0]
	if len(g.Examples) != errExamplesPerGroup {
		t.Fatalf("examples = %d, want the shared cap %d", len(g.Examples), errExamplesPerGroup)
	}
	// Without both hosts in the window the tie-break was never exercised.
	seen := map[string]int{}
	for _, s := range g.Examples {
		seen[s.Host]++
	}
	if seen["alpha"] == 0 || seen["beta"] == 0 {
		t.Fatalf("the trimmed window is single-host: %v", seen)
	}

	var b strings.Builder
	b.WriteString(g.Sig + "  (" + g.Sev + ", " + strconv.Itoa(g.Count) + "×, " +
		strings.Join(g.Containers, " + ") + ", " + jsErrTime(g.FirstSeen) + " → " +
		jsErrTime(g.LastSeen) + ")")
	for _, s := range g.Examples {
		b.WriteString("\n" + s.Host + "/" + s.Container + " " + orDash(s.Stamp) + " " +
			s.Sev + " " + s.Text)
	}
	if web := b.String(); web != tuiText {
		t.Errorf("web copy != TUI Y\nweb:\n%s\ntui:\n%s", web, tuiText)
	}
}
