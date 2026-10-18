package app

import (
	"bufio"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// History bounds. Trimming drops from the cap down to the floor rather than
// one line at a time, because a trim invalidates every styled row and forces a
// rebuild: halving keeps that off the hot path. These are vars so tests can
// drive the trim path without generating eight megabytes of logs.
var (
	logBytesCap  = 8 << 20
	logBytesKeep = 4 << 20
	logLinesCap  = 100_000
	logLinesKeep = 50_000
)

const (
	// logLevelScanBytes bounds the window a line is searched for a severity
	// word, so a long message body cannot recolour its own line.
	logLevelScanBytes = 120
	logQueryShowRunes = 24
	logFlushEvery     = 150 * time.Millisecond
	defaultLogTail    = 2000
	maxLogTail        = 100_000
)

// logSev is a coarse severity class inferred from the text; docker gives us no
// structured level.
type logSev int

const (
	lvlNone logSev = iota
	lvlDebug
	lvlInfo
	lvlWarn
	lvlError
)

var logSevs = map[string]logSev{
	"err":       lvlError,
	"error":     lvlError,
	"fatal":     lvlError,
	"crit":      lvlError,
	"critical":  lvlError,
	"panic":     lvlError,
	"emergency": lvlError,
	"warn":      lvlWarn,
	"warning":   lvlWarn,
	"info":      lvlInfo,
	"notice":    lvlInfo,
	"debug":     lvlDebug,
	"trace":     lvlDebug,
	"verbose":   lvlDebug,
}

var logMarkerRow = styleMuted.Render("… (older lines dropped) …")

func levelStyle(s logSev) lipgloss.Style {
	switch s {
	case lvlError:
		return styleErr
	case lvlWarn:
		return styleWarn
	case lvlDebug:
		return styleMuted
	}
	return lipgloss.NewStyle()
}

type ContainerView struct {
	id, name string
	display  string // what the title shows: "tag name" once labelled
	tint     string // palette name for that label, "" when untagged
	hostID   string
	gen      uint64
	vp       viewport.Model

	lines []string // raw log lines, oldest first
	size  int      // bytes held by lines, counting one newline per line
	rows  []string // styled, filtered rows — what the viewport shows
	hits  []int    // indices into rows matching query (highlight mode only)
	// lower mirrors lines lowercased, kept in lockstep (same indices) and only
	// while a query is active. rebuildRows matches against it so every
	// keystroke does not re-fold the whole buffer.
	lower []string

	query     string
	hide      bool // true: drop non-matching rows; false: keep all and bold matches
	searching bool
	in        textinput.Model
	wrap      bool // soft-wrap long lines; false truncates them horizontally
	// wrapToggled latches the user's `w` presses so root can persist the new
	// mode into settings.json; TakeWrapToggle reads and clears it.
	wrapToggled bool

	pending int
	follow  bool // stick to the bottom as lines arrive

	// Log export state: exporting guards a second E while a write is in
	// flight; note carries the one-row result shown in the header.
	note      string
	exporting bool
	dropped   bool // history has been trimmed, so rows[0] is the marker
	dirty     bool // rows changed since the last SetContent
	err       string
	running   bool
	width     int
	height    int

	reader io.ReadCloser
}

func NewContainerView(id, name string, w, h int) *ContainerView {
	c := &ContainerView{
		id: id, name: name, display: name, running: true, follow: true, hide: true,
		width: w, height: h,
	}
	c.vp = viewport.New(max(w-6, 20), max(h-6, 5))
	c.in = textinput.New()
	c.in.Placeholder = "filter"
	c.in.CharLimit = 64
	c.in.Width = logQueryShowRunes
	return c
}

// SetTag relabels the title. Root calls it when the stream opens and again after
// a label changes, so the log view never shows a stale name.
func (c *ContainerView) SetTag(display, tint string) {
	if display != "" {
		c.display, c.tint = display, tint
	}
}

// Attach registers an already-opened stream. It must be called on the UI
// goroutine (via logOpenedMsg) — the same rule Stop and Update follow.
func (c *ContainerView) Attach(r io.ReadCloser) {
	c.reader = r
	c.running = true
}

func (c *ContainerView) Stop() {
	if c.reader != nil {
		// stream.Close cancels StreamExec's internal context and closes the
		// session, which ends the remote `docker logs -f`.
		c.reader.Close()
	}
	c.running = false
	c.flush()
}

func (c *ContainerView) tag(hostID string, gen uint64) bool {
	return c.hostID == hostID && c.gen == gen
}

// StreamDone ends the stream and flushes, so the last batch is not lost when
// docker exits or the connection drops.
func (c *ContainerView) StreamDone(err error) {
	c.running = false
	if err != nil {
		c.err = err.Error()
	}
	c.flush()
}

func streamLogs(r io.ReadCloser, send func(tea.Msg), t msgTag) {
	defer r.Close()
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		if send == nil {
			return
		}
		send(logChunkMsg{msgTag: t, chunk: sc.Text() + "\n"})
	}
	err := sc.Err()
	if send != nil {
		if err != nil && !strings.Contains(err.Error(), "file already closed") {
			send(logEndMsg{msgTag: t, err: err})
		} else {
			send(logEndMsg{msgTag: t})
		}
	}
}

// Append records lines and styles only what is new. It deliberately does not
// touch the viewport: see flush.
func (c *ContainerView) Append(chunk string) {
	text := strings.TrimSuffix(chunk, "\n")
	start := len(c.lines)
	for _, line := range strings.Split(text, "\n") {
		c.size += len(line) + 1
		c.lines = append(c.lines, line)
		if c.lower != nil {
			c.lower = append(c.lower, strings.ToLower(line))
		}
	}
	if c.size > logBytesCap || len(c.lines) > logLinesCap {
		c.trimHistory()
		return
	}
	qlow := ""
	if c.query != "" {
		c.ensureLower()
		qlow = strings.ToLower(c.query)
	}
	for i := start; i < len(c.lines); i++ {
		low := ""
		if qlow != "" {
			low = c.lower[i]
		}
		c.pushRow(c.lines[i], low, qlow)
	}
}

// pushRow styles and appends one freshly arrived line. low is the lowered line
// (valid whenever qlow is set) and qlow the lowered query, precomputed once per
// Append instead of per line.
func (c *ContainerView) pushRow(line, low, qlow string) {
	matched := qlow == "" || strings.Contains(low, qlow)
	if !matched {
		if c.hide {
			return
		}
		c.rows = append(c.rows, styleLine(line, c.query, false))
		c.dirty = true
		return
	}
	c.rows = append(c.rows, styleLine(line, c.query, qlow != "" && !c.hide))
	if qlow != "" && !c.hide {
		c.hits = append(c.hits, len(c.rows)-1)
	}
	if !c.follow {
		c.pending++
	}
	c.dirty = true
}

// ensureLower makes the lowered mirror exactly as long as lines, backfilling
// anything missing. The mirror is only maintained while a query is active;
// without one the per-line ToLower would be paid for nothing.
func (c *ContainerView) ensureLower() {
	if c.query == "" || len(c.lower) == len(c.lines) {
		return
	}
	grow := make([]string, len(c.lines)-len(c.lower))
	for i, line := range c.lines[len(c.lower):] {
		grow[i] = strings.ToLower(line)
	}
	c.lower = append(c.lower, grow...)
}

// trimHistory drops whole lines until the floors are met, then rebuilds every
// row. Dropping whole lines is also why no line can be sliced in half anymore.
func (c *ContainerView) trimHistory() {
	c.dropped = true
	drop := 0
	for (c.size > logBytesKeep || len(c.lines)-drop > logLinesKeep) && drop < len(c.lines) {
		c.size -= len(c.lines[drop]) + 1
		drop++
	}
	if drop > 0 {
		c.lines = append(c.lines[:0], c.lines[drop:]...)
		if c.lower != nil {
			c.lower = append(c.lower[:0], c.lower[drop:]...)
		}
	}
	c.rebuildRows()
}

// rebuildRows is the only place rows are built from scratch, so the dropped
// marker, hits and rows can never disagree.
func (c *ContainerView) rebuildRows() {
	rows := c.rows[:0]
	hits := c.hits[:0]
	if c.dropped {
		rows = append(rows, logMarkerRow)
	}
	var qlow string
	if c.query != "" {
		c.ensureLower()
		qlow = strings.ToLower(c.query)
	}
	for i, line := range c.lines {
		matched := qlow == "" || strings.Contains(c.lower[i], qlow)
		if !matched && c.hide {
			continue
		}
		mark := matched && qlow != "" && !c.hide
		if mark {
			hits = append(hits, len(rows))
		}
		row := styleLine(line, c.query, mark)
		if c.wrap && c.vp.Width > 0 {
			// reflow's wordwrap inside lipgloss is ANSI-aware, so styled rows
			// fold without losing their colours. A wrapped row still counts as
			// one viewport row; the buffer caps keep the cost bounded.
			row = lipgloss.NewStyle().Width(c.vp.Width).Render(row)
		}
		rows = append(rows, row)
	}
	c.rows, c.hits = rows, hits
	c.dirty = true
	c.flush()
}

// flush is the only caller of SetContent. The viewport re-splits and re-scans
// its entire content on every SetContent, so lines are batched behind the
// logFlushMsg tick instead of rendered one at a time.
func (c *ContainerView) flush() {
	if !c.dirty {
		return
	}
	c.dirty = false
	c.vp.SetContent(strings.Join(c.rows, "\n"))
	if c.follow {
		c.vp.GotoBottom()
		c.pending = 0
	}
}

// applyQuery re-filters the whole buffer, which is what makes typing live.
func (c *ContainerView) applyQuery(q string) {
	if q == c.query {
		return
	}
	c.query = q
	if q == "" {
		// No active query, no lowered mirror: stop paying for the second copy.
		c.lower = nil
	}
	c.rebuildRows()
}

func (c *ContainerView) toggleMode() {
	if c.query == "" {
		return
	}
	c.hide = !c.hide
	c.rebuildRows()
}

// jump moves to the next/previous match. In hide mode every row matches, so it
// simply walks rows; in highlight mode it seeks the hit list and wraps.
func (c *ContainerView) jump(dir int) {
	if c.query == "" || len(c.rows) == 0 {
		return
	}
	if c.hide {
		c.seek(clampInt(c.vp.YOffset+dir, 0, len(c.rows)-1))
		return
	}
	if len(c.hits) == 0 {
		return
	}
	cur := c.vp.YOffset
	if dir > 0 {
		for _, h := range c.hits {
			if h > cur {
				c.seek(h)
				return
			}
		}
		c.seek(c.hits[0])
		return
	}
	for i := len(c.hits) - 1; i >= 0; i-- {
		if c.hits[i] < cur {
			c.seek(c.hits[i])
			return
		}
	}
	c.seek(c.hits[len(c.hits)-1])
}

func (c *ContainerView) seek(row int) {
	c.vp.SetYOffset(max(row, 0))
	c.follow = c.vp.AtBottom()
	if c.follow {
		c.pending = 0
	}
}

// HandleEscape lets Esc step out of the filter before it leaves the screen:
// typing → committed filter → no filter → back to the dashboard.
func (c *ContainerView) HandleEscape() bool {
	switch {
	case c.searching:
		c.searching = false
		c.in.Blur()
		return true
	case c.query != "":
		c.applyQuery("")
		return true
	}
	return false
}

// Searching reports whether keys belong to the filter input. Root must not run
// sessionKeys while it does, or digits and X would switch sessions mid-query.
func (c *ContainerView) Searching() bool { return c.searching }

// TakeWrapToggle reports and clears the wrap-mode change latch.
func (c *ContainerView) TakeWrapToggle() (wrap, changed bool) {
	changed = c.wrapToggled
	c.wrapToggled = false
	return c.wrap, changed
}

func (c *ContainerView) SetSize(w, h int) {
	c.width, c.height = w, h
	c.vp.Width = max(w-6, 20)
	c.vp.Height = max(h-6, 5)
}

// SetNote shows a transient, clipped note in the header row (log export
// results; the view has no toast of its own).
func (c *ContainerView) SetNote(s string) { c.note = s }

func (c *ContainerView) Update(msg tea.Msg) (*ContainerView, tea.Cmd) {
	km, isKey := msg.(tea.KeyMsg)
	if isKey && c.searching {
		if km.String() == "enter" {
			c.searching = false
			c.in.Blur()
			return c, nil
		}
		var cmd tea.Cmd
		c.in, cmd = c.in.Update(msg)
		c.applyQuery(c.in.Value())
		return c, cmd
	}
	if isKey {
		switch km.String() {
		case "g":
			c.vp.GotoTop()
			c.follow = false
			return c, nil
		case "G":
			c.vp.GotoBottom()
			c.follow = true
			c.pending = 0
			return c, nil
		case "/":
			c.searching = true
			c.in.SetValue(c.query)
			c.in.CursorEnd()
			return c, c.in.Focus()
		case "m":
			c.toggleMode()
			return c, nil
		case "w":
			c.wrap = !c.wrap
			c.wrapToggled = true
			c.rebuildRows()
			return c, nil
		case "n":
			c.jump(1)
			return c, nil
		case "N":
			c.jump(-1)
			return c, nil
		case "E":
			if c.exporting {
				return c, nil
			}
			if len(c.lines) == 0 {
				c.note = "nothing to export"
				return c, nil
			}
			c.exporting = true
			c.note = "exporting…"
			return c, exportLogs(c)
		}
	}
	var cmd tea.Cmd
	c.vp, cmd = c.vp.Update(msg)
	if isKey {
		c.follow = c.vp.AtBottom()
		if c.follow {
			c.pending = 0
		}
	}
	return c, cmd
}

func (c *ContainerView) View() string {
	title := styleTitle.Render("logs — ") + tagOr(styleTitle, c.tint, c.display) +
		" " + styleMuted.Render("["+c.id[:min(len(c.id), 12)]+"]")
	body := panel(title, c.header()+"\n"+c.vp.View(), c.width-2)
	return pinFooter(body, c.help(), c.width, c.height)
}

// header stays on one row: the panel width is fixed, and a wrapped status line
// would push the pinned footer past the frame.
func (c *ContainerView) header() string {
	if c.searching {
		return styleKey.Render("⌕ ") + c.in.View()
	}
	var status string
	switch {
	case c.err != "":
		status = styleErr.Render("stream ended: " + c.err)
	case !c.running:
		status = styleMuted.Render("stream ended")
	default:
		status = styleOK.Render("● streaming…")
	}
	if !c.follow && c.pending > 0 {
		status += "  " + styleWarn.Render("↓ "+strconv.Itoa(c.pending)+" new")
	}
	if c.query != "" {
		status += "  " + styleMuted.Render("⌕ "+quotedTrunc(c.query, logQueryShowRunes)+
			" "+strconv.Itoa(len(c.rows))+"/"+strconv.Itoa(len(c.lines)))
	}
	if c.note != "" {
		status += "  " + styleMuted.Render(quotedTrunc(c.note, 48))
	}
	return status
}

func (c *ContainerView) help() string {
	return helpBar(c.helpRows(), 5)
}

// helpRows is the log view's full key table, resolved by input state.
func (c *ContainerView) helpRows() []keyHelp {
	if c.searching {
		return []keyHelp{{"type", "filter"}, {"enter", "apply"}, {"esc", "cancel"}}
	}
	if c.query != "" {
		want := "highlight"
		if !c.hide {
			want = "hide"
		}
		return []keyHelp{
			{"j/k", "scroll"}, {"g/G", "top/bottom"}, {"/", "edit"},
			{"m", want}, {"n/N", "jump"}, {"w", "wrap"}, {"esc", "clear"},
		}
	}
	return []keyHelp{
		{"j/k", "scroll"}, {"g/G", "top/bottom"}, {"/", "filter"},
		{"m", "mode"}, {"n/N", "jump"}, {"w", "wrap"}, {"E", "export"}, {"esc", "back"},
	}
}

// styleLine renders one raw log line: docker's timestamp dimmed to HH:MM:SS,
// then the message coloured by level. A line that already carries its own ANSI
// escapes is left alone — the container's palette wins, and SGR never nests.
func styleLine(line, query string, mark bool) string {
	stamp, rest := splitLogStamp(line)
	var b strings.Builder
	if stamp != "" {
		b.WriteString(styleMuted.Render(stamp))
		if rest != "" {
			b.WriteString(" ")
		}
	}
	if rest == "" || strings.Contains(rest, "\x1b[") {
		b.WriteString(rest)
		return b.String()
	}
	st := levelStyle(logLevel(rest))
	if mark && query != "" {
		b.WriteString(highlightLine(rest, query, st))
		return b.String()
	}
	b.WriteString(st.Render(rest))
	return b.String()
}

// splitLogStamp separates docker's leading RFC3339 timestamp into the
// wall-clock part and the message. Lines without one (older daemons, some
// stderr) pass through untouched.
func splitLogStamp(line string) (stamp, rest string) {
	i := strings.IndexByte(line, ' ')
	if i < 20 || !strings.Contains(line[:i], "T") {
		return "", line
	}
	if _, err := time.Parse(time.RFC3339Nano, line[:i]); err != nil {
		return "", line
	}
	return line[11:19], line[i+1:]
}

// logLevel returns the severity named by the first word in the leading window.
// Whole words only, so "information" does not read as info.
func logLevel(s string) logSev {
	if len(s) > logLevelScanBytes {
		s = s[:logLevelScanBytes]
	}
	for i := 0; i < len(s); {
		if !isASCIILetter(s[i]) {
			i++
			continue
		}
		j := i
		for j < len(s) && isASCIILetter(s[j]) {
			j++
		}
		if sev, ok := logSevs[strings.ToLower(s[i:j])]; ok {
			return sev
		}
		i = j
	}
	return lvlNone
}

func isASCIILetter(b byte) bool {
	return b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z'
}

// highlightLine bolds each occurrence of query. Segments are rendered apart so
// each closes its own SGR and nothing bleeds into the next run.
func highlightLine(s, query string, base lipgloss.Style) string {
	bold := base.Copy().Bold(true)
	var b strings.Builder
	run, i, q := 0, 0, len(query)
	for i+q <= len(s) {
		if !strings.EqualFold(s[i:i+q], query) {
			i++
			continue
		}
		if i > run {
			b.WriteString(base.Render(s[run:i]))
		}
		b.WriteString(bold.Render(s[i : i+q]))
		i += q
		run = i
	}
	if run < len(s) {
		b.WriteString(base.Render(s[run:]))
	}
	return b.String()
}

func quotedTrunc(s string, n int) string {
	if r := []rune(s); len(r) > n {
		s = string(r[:n]) + "…"
	}
	return `"` + s + `"`
}

// parseLogTail clamps VPSTUI_LOG_TAIL. Anything unparseable falls back to the
// default, so a typo cannot ask docker for the whole history.
func parseLogTail(s string) int {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil || n < 1 {
		return defaultLogTail
	}
	return min(n, maxLogTail)
}

func logTail() int {
	return parseLogTail(os.Getenv("VPSTUI_LOG_TAIL"))
}
