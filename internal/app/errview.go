package app

import (
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// errTarget is the container a live stream should be opened for, resolved by
// root against the sessions it owns.
type errTarget struct {
	hostID string
	id     string
	name   string
}

// ErrorsView is the cross-host aggregate. It owns no connection: root merges
// the per-session corpora into it on every Refresh, so two hosts can be shown
// at once without a single extra lock.
type ErrorsView struct {
	tbl ctable

	grouped  bool
	showWarn bool

	all       []ErrLine // every kept line, before the severity filter
	rowsGroup []ErrGroup
	flat      []ErrLine
	lineCount int

	swept     int
	connected int
	hostCount int
	truncated bool
	loading   bool
	problems  []string

	expanded int // index into rowsGroup, -1 when collapsed
	note     string
	prompt   string // pre-rendered tag prompt, owned by root
	width    int
	height   int

	tags *TagStore // labels applied at render time; nil-safe

	// hostIDs maps the host name a group's "host/container" identity strings
	// carry back to the host id a tag is keyed by. Tags hang on the id, because
	// two hosts can share a name after a rename.
	hostIDs map[string]string
}

func NewErrorsView(w, h int) *ErrorsView {
	return &ErrorsView{grouped: true, showWarn: true, expanded: -1, width: w, height: h}
}

func (e *ErrorsView) SetSize(w, h int) {
	e.width, e.height = w, h
}

// SetTags shares root's label store with this view. Tags join at render time
// only: the corpus is built inside the sweep goroutine and must stay independent
// of mutable UI state, so relabelling repaints without re-sweeping.
func (e *ErrorsView) SetTags(t *TagStore) { e.tags = t }

// SetNote leaves a one-off message on the status block — used when a key asks
// for something the sessions cannot currently satisfy.
func (e *ErrorsView) SetNote(s string) { e.note = s }

// SetPrompt shows or clears root's pre-rendered tag prompt. It occupies a status
// row, so the table's height budget shrinks with it and the footer stays pinned.
func (e *ErrorsView) SetPrompt(s string) { e.prompt = s }

// Refresh re-merges the corpora held by the given sessions. Root calls it when
// the screen opens and after every sweep lands, which is why it takes sessions
// rather than lines.
func (e *ErrorsView) Refresh(sessions []*Session, hostCount int) {
	e.note = ""
	e.swept, e.connected, e.loading = 0, 0, false
	e.problems = e.problems[:0]
	e.truncated = false
	e.hostIDs = make(map[string]string, len(sessions))

	var corpus []ErrLine
	for _, s := range sessions {
		if s.conn != nil {
			e.connected++
		}
		e.hostIDs[s.host.Name] = s.host.ID
		if s.dash == nil {
			continue
		}
		if s.dash.errLoading {
			e.loading = true
		}
		if s.dash.errFetchedAt.IsZero() {
			continue
		}
		e.swept++
		if s.dash.errTruncated {
			e.truncated = true
		}
		if s.dash.errProblem != "" {
			e.problems = append(e.problems, s.host.Name+": "+s.dash.errProblem)
		}
		corpus = append(corpus, s.dash.errCorpus...)
	}
	e.all = corpus
	e.hostCount = hostCount
	e.rebuild()
}

// rebuild applies the severity filter and re-derives both views. The corpus is
// capped per host, so this is cheap enough to run on every mode key.
func (e *ErrorsView) rebuild() {
	lines := e.all
	if !e.showWarn {
		kept := make([]ErrLine, 0, len(lines))
		for _, l := range lines {
			if l.Sev == lvlError {
				kept = append(kept, l)
			}
		}
		lines = kept
	}
	e.lineCount = len(lines)

	flat := make([]ErrLine, len(lines))
	copy(flat, lines)
	sortNewestFirst(flat)
	if len(flat) > errFlatCap {
		flat = flat[:errFlatCap]
	}
	e.flat = flat

	groups := groupErrors(lines)
	if len(groups) > maxErrorGroups {
		groups = groups[:maxErrorGroups]
		e.truncated = true
	}
	e.rowsGroup = groups

	rows := make([][]string, 0, max(len(groups), len(flat)))
	tint := make([]string, 0, max(len(groups), len(flat)))
	if e.grouped {
		// Cell order follows errColsFor: the signature is the adaptive column and
		// it comes last. Emitting it third handed the group's "2h·2c" tally the
		// wide column and squeezed the signature into eight cells.
		for _, g := range groups {
			rows = append(rows, []string{
				strconv.Itoa(g.Count),
				sevName(g.Sev),
				strconv.Itoa(len(g.Hosts)) + "h·" + strconv.Itoa(len(g.Containers)) + "c",
				errTime(g.Last),
				g.Sig,
			})
			tint = append(tint, "")
		}
	} else {
		for _, l := range e.flat {
			rows = append(rows, []string{errStampCell(l), l.HostName, e.tags.Display(l.HostID, l.Name), sevName(l.Sev), l.Text})
			tint = append(tint, e.tags.Tint(l.HostID, l.Name))
		}
	}
	e.tbl.SetColumns(errColsFor(e.grouped, max(e.width-2, 20)))
	e.tbl.SetRows(rows, nil)
	e.tbl.SetTint(tint)
	e.expanded = -1
}

// errColsFor sizes the one adaptive column (signature, or message in flat
// mode) against everything else, mirroring colsFor.
func errColsFor(grouped bool, total int) []ctableCol {
	if grouped {
		return []ctableCol{
			{title: "COUNT", width: 5, kind: cellPlain},
			{title: "SEV", width: 5, kind: cellSev},
			{title: "SEEN", width: 8, kind: cellPlain},
			{title: "LAST", width: 8, kind: cellPlain},
			{title: "SIGNATURE", width: max(total-(5+5+8+8+4*cellGap), 16), kind: cellPlain},
		}
	}
	return []ctableCol{
		{title: "TIME", width: 8, kind: cellPlain},
		{title: "HOST", width: 12, kind: cellPlain},
		// cellTag: the cell carries "label name", and the row's colour goes on
		// after fitting. Grouped mode has no container column, so no tint there.
		{title: "CONTAINER", width: 14, kind: cellTag},
		{title: "SEV", width: 5, kind: cellSev},
		{title: "MESSAGE", width: max(total-(8+12+14+5+4*cellGap), 14), kind: cellPlain},
	}
}

// errStampCell prefers the container's own wall clock, so the flat feed and the
// log view show the same string for the same line.
func errStampCell(l ErrLine) string {
	if l.Stamp != "" {
		return l.Stamp
	}
	if l.At.IsZero() {
		return "—"
	}
	return l.At.Format("15:04:05")
}

func errTime(t time.Time) string {
	if t.IsZero() {
		return "—"
	}
	now := time.Now()
	if t.Year() == now.Year() && t.YearDay() == now.YearDay() {
		return t.Format("15:04:05")
	}
	return t.Format("01-02 15:04")
}

// LiveTarget is the container to open a live stream for: the newest example
// under the cursor.
func (e *ErrorsView) LiveTarget() (errTarget, bool) {
	l, ok := e.cursorLine()
	if !ok {
		return errTarget{}, false
	}
	return errTarget{hostID: l.HostID, id: l.ID, name: l.Name}, true
}

// cursorLine is the one line the cursor points at: a flat row, or the newest
// sample of the highlighted group. Streaming, labelling and yanking all resolve
// their target this way, so the three keys can never disagree about the row.
func (e *ErrorsView) cursorLine() (ErrLine, bool) {
	if !e.grouped {
		i := e.tbl.Cursor()
		if i < 0 || i >= len(e.flat) {
			return ErrLine{}, false
		}
		return e.flat[i], true
	}
	g, ok := e.cursorGroup()
	if !ok || len(g.Examples) == 0 {
		return ErrLine{}, false
	}
	return g.Examples[len(g.Examples)-1], true
}

// TagTarget is the container to label: the same row a stream would open on.
func (e *ErrorsView) TagTarget() (tagTarget, bool) {
	l, ok := e.cursorLine()
	if !ok {
		return tagTarget{}, false
	}
	return tagTarget{hostID: l.HostID, name: l.Name, id: l.ID}, true
}

// yankLine is the text for y: the line under the cursor, as it would be grepped
// out of docker logs. It deliberately uses the docker name rather than a tag —
// pasted text is meant to be searchable on the host, where the label does not
// exist.
func (e *ErrorsView) yankLine() string {
	l, ok := e.cursorLine()
	if !ok {
		return ""
	}
	return errLineText(l)
}

// yankGroup is the text for Y: a header a human reads, then every raw sample
// behind the signature. The header carries the labels, the body stays greppable.
// In flat mode there is no highlighted group, so the group is whichever one the
// line under the cursor belongs to.
func (e *ErrorsView) yankGroup() string {
	var g ErrGroup
	var ok bool
	if e.grouped {
		g, ok = e.cursorGroup()
	} else if l, found := e.cursorLine(); found {
		g, ok = e.groupFor(l.Sig)
	}
	if !ok {
		return ""
	}
	head := g.Sig + "  (" + sevName(g.Sev) + ", " + strconv.Itoa(g.Count) + "×, " +
		strings.Join(e.displayContainers(g.Containers), " + ") + ", " +
		errTime(g.First) + " → " + errTime(g.Last) + ")"
	var b strings.Builder
	b.WriteString(head)
	for _, l := range g.Examples {
		b.WriteString("\n" + errLineText(l))
	}
	return b.String()
}

// groupFor finds the signature a flat row was grouped under. The group list is
// capped, so a very old line may have no group left to yank.
func (e *ErrorsView) groupFor(sig string) (ErrGroup, bool) {
	for _, g := range e.rowsGroup {
		if g.Sig == sig {
			return g, true
		}
	}
	return ErrGroup{}, false
}

// errLineText is the one shape a yanked line takes, in both modes:
// "host/container stamp severity message".
func errLineText(l ErrLine) string {
	return l.HostName + "/" + l.Name + " " + errStampCell(l) + " " + sevName(l.Sev) + " " + l.Text
}

func (e *ErrorsView) cursorGroup() (ErrGroup, bool) {
	i := e.tbl.Cursor()
	if i < 0 || i >= len(e.rowsGroup) {
		return ErrGroup{}, false
	}
	return e.rowsGroup[i], true
}

// HandleEscape collapses the expand pane before Esc leaves the screen — the
// same ladder the log view uses, so a detail view is never exit-trapped.
func (e *ErrorsView) HandleEscape() bool {
	if e.expanded >= 0 {
		e.expanded = -1
		return true
	}
	return false
}

func (e *ErrorsView) Update(msg tea.Msg) (*ErrorsView, tea.Cmd) {
	if km, ok := msg.(tea.KeyMsg); ok {
		switch km.String() {
		case "enter":
			// A flat row is a single line, not a signature, so there is no
			// pane to open: expanding here would render rowsGroup[cursor]
			// beside a table of unrelated rows.
			if !e.grouped {
				return e, nil
			}
			cur := e.tbl.Cursor()
			if _, ok := e.cursorGroup(); ok {
				if e.expanded == cur {
					e.expanded = -1
				} else {
					e.expanded = cur
				}
			}
			return e, nil
		case "m":
			e.grouped = !e.grouped
			e.rebuild()
			return e, nil
		case "w":
			e.showWarn = !e.showWarn
			e.rebuild()
			return e, nil
		}
	}
	var cmd tea.Cmd
	e.tbl, cmd = e.tbl.Update(msg)
	return e, cmd
}

func (e *ErrorsView) View() string {
	status := e.status()
	footer := e.help()
	// The frame is exact: the table gets whatever the status and footer leave,
	// so pinFooter never has to overflow the terminal.
	avail := max(e.height-lipgloss.Height(status)-lipgloss.Height(footer)-1, 1)

	var body string
	switch {
	case e.expanded >= 0 && e.expanded < len(e.rowsGroup):
		g := e.rowsGroup[e.expanded]
		body = status + "\n" + panel(styleTitle.Render(fitPlain(g.Sig, paneText(e.width))),
			e.detail(g, avail), e.width-2)
	case len(e.rowsGroup) == 0 && len(e.flat) == 0:
		body = status + "\n" + styleMuted.Render("nothing swept yet — press r to sweep the connected hosts")
	default:
		e.tbl.SetSize(max(e.width-2, 20), avail)
		body = status + "\n" + e.tbl.View()
	}
	return pinFooter(body, footer, e.width, e.height)
}

// paneText is how many cells one line of the expand pane may occupy. panel is
// drawn e.width-2 wide and lipgloss's Width counts its padding, so the text
// area — measured, not inferred — is four cells narrower again. Anything
// longer wraps to a second row, and a wrapped row inside a pinned frame pushes
// the footer off the screen.
func paneText(width int) int { return max(width-8, 16) }

// detail lists where a signature was seen and the newest raw lines behind it,
// clipped to the rows the frame has left.
func (e *ErrorsView) detail(g ErrGroup, budget int) string {
	var b strings.Builder
	b.WriteString(statusLine(styleMuted, strconv.Itoa(g.Count)+"× · "+strconv.Itoa(len(g.Hosts))+
		" host(s) · "+strconv.Itoa(len(g.Containers))+" container(s) · first "+errTime(g.First)+
		" · last "+errTime(g.Last), paneText(e.width)) + "\n")
	b.WriteString(statusLine(styleMuted, "containers: "+strings.Join(e.displayContainers(g.Containers), ", "), paneText(e.width)) + "\n")
	// The pane spends three rows on its border and title and two on the headers
	// above the samples, so the sample list gets what the frame has left minus
	// those — not simply the newest twenty.
	n := max(min(budget-5, len(g.Examples)), 0)
	for i := len(g.Examples) - 1; i >= len(g.Examples)-n; i-- {
		l := g.Examples[i]
		b.WriteString(levelStyle(l.Sev).Render(fitPlain(l.HostName+"/"+e.tags.Display(l.HostID, l.Name)+" "+l.Text, paneText(e.width))) + "\n")
	}
	return b.String()
}

// displayContainers relabels a group's identity strings for the pane. The
// identity itself stays "hostName/dockerName" — listing a tag there would
// silently re-group history the moment a label changes — so this mapping is
// purely cosmetic and runs on the copy that goes to screen.
func (e *ErrorsView) displayContainers(names []string) []string {
	out := make([]string, 0, len(names))
	for _, n := range names {
		a := strings.IndexByte(n, '/')
		if a < 0 {
			out = append(out, n)
			continue
		}
		host, cont := n[:a], n[a+1:]
		out = append(out, host+"/"+e.tags.Display(e.hostIDs[host], cont))
	}
	return out
}

func (e *ErrorsView) status() string {
	cov := strconv.Itoa(e.swept) + " of " + strconv.Itoa(e.hostCount) + " hosts swept"
	if e.swept < e.connected {
		cov += " · " + strconv.Itoa(e.connected-e.swept) + " connected, pending"
	}
	head := styleTitle.Render("errors") + " " + styleMuted.Render("["+cov+"]")
	if e.loading {
		head += " " + styleOK.Render("sweeping…")
	}

	var b strings.Builder
	b.WriteString(head + "\n")
	mode := "grouped"
	if !e.grouped {
		mode = "flat feed"
	}
	b.WriteString(statusLine(styleMuted, strconv.Itoa(e.lineCount)+" lines · "+
		strconv.Itoa(len(e.rowsGroup))+" signatures · "+mode, e.width) + "\n")
	if !e.showWarn {
		b.WriteString(statusLine(styleWarn, "errors only — w brings warnings back", e.width) + "\n")
	}
	if e.truncated {
		b.WriteString(statusLine(styleWarn, "older lines were dropped — widen VPSTUI_ERR_SINCE or VPSTUI_ERR_TAIL", e.width) + "\n")
	}
	if e.prompt != "" {
		// The prompt is already styled and already clipped to the terminal
		// width by whoever rendered it.
		b.WriteString(e.prompt + "\n")
	}
	if e.note != "" {
		b.WriteString(statusLine(styleBanner, e.note, e.width) + "\n")
	}
	for _, p := range e.problems {
		b.WriteString(statusLine(styleErr, "sweep failed — "+p, e.width) + "\n")
	}
	return b.String()
}

// statusLine clips an unbounded message before styling it. runewidth counts
// escape bytes, so fitPlain has to see the plain text — and a status row wider
// than the terminal wraps, pushing the pinned footer off screen.
func statusLine(s lipgloss.Style, text string, width int) string {
	return s.Render(fitPlain(text, max(width, 10)))
}

func (e *ErrorsView) help() string {
	// 77 cells, the same budget the previous eight-pair bar used: pinFooter counts
	// this as one row, so a bar that wraps lands past the last line of the frame.
	// The status block spells out what m and w currently mean.
	return helpBar(e.helpRows(), 5)
}

// helpRows is the errors screen's full key table, resolved by expand state.
func (e *ErrorsView) helpRows() []keyHelp {
	if e.expanded >= 0 {
		return []keyHelp{
			{"esc", "close"}, {"l", "live"}, {"y/Y", "yank"},
			{"m/w", "mode"}, {"r", "sweep"}, {"q", "back"},
		}
	}
	return []keyHelp{
		{"j/k g/G", "nav"}, {"enter", "open"}, {"l", "live"}, {"y/Y", "yank"},
		{"t", "tag"}, {"m/w", "mode"}, {"r", "sweep"}, {"esc", "back"},
	}
}
