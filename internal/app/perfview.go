package app

import (
	"context"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"vpstui/internal/httpapi"
	"vpstui/internal/models"
	"vpstui/internal/perf"
)

// perfEvery is the background sample interval. The dashboard the user is
// looking at already samples every 2 s; a host they are not looking at is
// sampled at 10 s, which keeps a 20 minute window populated for a tenth of the
// round trips. Nothing on screen depends on it being finer.
const perfEvery = 10 * time.Second

// perfColSpecs is the ranking table's column set in display order, each with the
// table width that earns it. Columns are dropped whole rather than clipped,
// because a peak reduced to "1…" is not a peak: this screen's job is to say which
// container to look at, and the numbers have to survive a narrow terminal.
var perfColSpecs = []struct {
	title string
	width int
	kind  cellKind
	min   int // least table width this column appears in
}{
	{"#", 2, cellPlain, 0},
	{"NAME", 12, cellTag, 0},
	{"CPU%", 6, cellPct, 0},
	{"AVG", 6, cellPct, 84},
	{"PEAK", 6, cellPct, 84},
	{"TREND", 12, cellPlain, 100},
	{"SHARE", 6, cellPlain, 118},
	{"MEM", 9, cellPlain, 118},
	{"M.PEAK", 9, cellPlain, 146},
	{"THROT", 6, cellPlain, 146},
	{"FLAG", 9, cellFlag, 0},
}

// perfColsFor returns the columns sized for a table area of total width.
func perfColsFor(total int) []ctableCol {
	c := make([]ctableCol, 0, len(perfColSpecs))
	for _, s := range perfColSpecs {
		if total >= s.min {
			c = append(c, ctableCol{title: s.title, width: s.width, kind: s.kind})
		}
	}
	// Name and trend are the two columns worth widening, and the trend first:
	// three blocks of sparkline say nothing, while a name clips with an ellipsis
	// and stays recognisable.
	used := cellGap * (len(c) - 1)
	for _, x := range c {
		used += x.width
	}
	slack := total - used
	if slack <= 0 {
		return c
	}
	if ti := perfColIndex(c, "TREND"); ti >= 0 {
		gain := min(slack, 28)
		c[ti].width += gain
		slack -= gain
	}
	if ni := perfColIndex(c, "NAME"); ni >= 0 {
		c[ni].width += slack
	}
	return c
}

func perfColIndex(c []ctableCol, title string) int {
	for i, x := range c {
		if x.title == title {
			return i
		}
	}
	return -1
}

// recordPerf appends this reading to the session's history and forgets the
// containers that neither the reading nor the last known container list mentions,
// so a recreated container cannot inherit a dead one's numbers. A background
// session's list is stale by design, which is why the sampled ids also count as
// known — otherwise the first tick would prune the container it just recorded.
func (d *Dashboard) recordPerf(at time.Time, cstats []models.ContainerStats) {
	if d.perf == nil {
		d.perf = perf.New(perf.Cap, perf.Horizon)
	}
	if d.perfNames == nil {
		d.perfNames = map[string]string{}
	}
	d.perf.Record(at, cstats)
	keep := make(map[string]bool, len(d.containers)+len(cstats))
	for _, c := range d.containers {
		keep[c.ID] = true
	}
	for _, s := range cstats {
		keep[s.ID] = true
		if s.Name != "" {
			d.perfNames[s.ID] = s.Name
		}
	}
	d.perf.Forget(keep)
	for id := range d.perfNames {
		if !keep[id] {
			delete(d.perfNames, id)
		}
	}
}

// fetchPeaks reads the kernel's all-time counters for the containers currently
// listed: one read-only command, armed by root once per generation and re-armed
// by f on the ranking screen — never beside the stats tick.
func (d *Dashboard) fetchPeaks() tea.Cmd {
	if d.dock == nil || d.peakLoading {
		return nil
	}
	ids := make([]string, 0, len(d.containers))
	for _, c := range d.containers {
		ids = append(ids, c.ID)
	}
	if len(ids) == 0 {
		return nil
	}
	d.peakLoading = true
	d.peakProblem = ""
	dock := d.dock
	t := msgTag{hostID: d.hostID, gen: d.gen}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		rows, err := dock.ContainerPeaks(ctx, ids)
		return peaksLoadedMsg{msgTag: t, rows: rows, err: err, at: time.Now()}
	}
}

// fetchPerf samples docker stats only — no container list, no /proc probe. Root
// arms it every 10 s for a session whose dashboard is not on screen, which is
// 0.1 SSH round trips a second against the 1.5/s an open dashboard already makes.
// perfBusy keeps a slow round trip from being stacked under the next tick.
func (d *Dashboard) fetchPerf() tea.Cmd {
	if d.dock == nil || d.perfBusy {
		return nil
	}
	d.perfBusy = true
	dock := d.dock
	t := msgTag{hostID: d.hostID, gen: d.gen}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		cstats, err := dock.ContainerStats(ctx)
		return perfLoadedMsg{msgTag: t, cstats: cstats, err: err}
	}
}

// openPerf and closePerf keep the two sub-modes exclusive: one table on screen at
// a time, and leaving one must not keep a stale ranking alive.
func (d *Dashboard) openPerf() tea.Cmd {
	d.showPerf = true
	d.showDisk = false
	d.rebuildPerf()
	return d.fetchPeaks()
}

func (d *Dashboard) closePerf() { d.showPerf = false }

// perfSubjects lists every container the table lists, stopped ones included: a
// container with no samples still gets a row, so it cannot silently vanish from
// the ranking the moment it stops reporting. Ids that only docker stats has ever
// reported are listed too, which is what lets a background session rank a
// container its stale list does not know about yet. RankAll's id tie-break makes
// the result independent of the order a map is walked in.
func (d *Dashboard) perfSubjects() []perf.Subject {
	out := make([]perf.Subject, 0, len(d.containers)+len(d.perfNames))
	known := make(map[string]bool, len(d.containers))
	for _, c := range d.containers {
		out = append(out, perf.Subject{ID: c.ID, Name: c.ShortName()})
		known[c.ID] = true
	}
	for id, name := range d.perfNames {
		if !known[id] {
			out = append(out, perf.Subject{ID: id, Name: name})
		}
	}
	return out
}

// rebuildPerf recomputes the ranking and hands the table its rows. The order is
// the one perf.RankAll chose, which is also the order the web page renders, so
// neither surface sorts anything. It runs on every sample, not only while the
// screen is open, because the published snapshot ranks from these rows.
func (d *Dashboard) rebuildPerf() {
	if d.perf == nil {
		d.perf = perf.New(perf.Cap, perf.Horizon)
	}
	now := time.Now()
	rows := perf.RankAll(now, d.perf, d.perfSubjects(), perf.Window, d.hstats.NumCPU, d.hstats.MemTotalMB)
	d.perfRanks = rows

	cols := perfColsFor(max(d.width-2, 20))
	d.perfTbl.SetColumns(cols)
	trend := perfColIndex(cols, "TREND")
	name, cpu, avg, peak := perfColIndex(cols, "NAME"), perfColIndex(cols, "CPU%"), perfColIndex(cols, "AVG"), perfColIndex(cols, "PEAK")
	share, mem, mpeak, throt, flag := perfColIndex(cols, "SHARE"), perfColIndex(cols, "MEM"), perfColIndex(cols, "M.PEAK"), perfColIndex(cols, "THROT"), perfColIndex(cols, "FLAG")

	out := make([][]string, 0, len(rows))
	tint := make([]string, 0, len(rows))
	for i, r := range rows {
		cells := make([]string, len(cols))
		for j := range cells {
			cells[j] = "-"
		}
		// A column the terminal is too narrow for has no index, so every cell goes
		// through set rather than assuming its column survived.
		set := func(j int, v string) {
			if j >= 0 {
				cells[j] = v
			}
		}
		cells[0] = strconv.Itoa(i + 1)
		set(name, d.tags.Display(d.host.ID, r.Name))
		set(flag, perfFlags(r))
		if r.Sum.Samples > 0 {
			set(cpu, cpuCell(r.Sum.Last))
			set(avg, cpuCell(r.Sum.Mean))
			set(peak, cpuCell(r.Sum.Peak))
			set(share, shareCell(r))
			set(mem, perf.Bytes(r.Sum.MemBytes))
			if trend >= 0 {
				// The series aliases the ring, so reduce it to plain numbers before
				// anything else can record into it.
				points := perf.Points(d.perf.Series(now, r.ID, perf.Window), cols[trend].width)
				set(trend, perf.Spark(points, cols[trend].width))
			}
		}
		if p, ok := d.peaks[r.ID]; ok {
			set(mpeak, perf.Bytes(p.MemPeakBytes))
			set(throt, throttleCell(p))
		}
		out = append(out, cells)
		tint = append(tint, d.perfTint(r.Name))
	}
	d.perfTbl.SetRows(out, nil)
	d.perfTbl.SetTint(tint)
	d.perfTbl.SetCursor(d.perfIndex())
}

func (d *Dashboard) perfTint(name string) string {
	tag, ok := d.tags.Get(d.host.ID, name)
	if !ok {
		return ""
	}
	return tag.Color
}

func cpuCell(v float64) string { return strconv.FormatFloat(v, 'f', 1, 64) }

// sharePct is the ranking's own number: whichever of the two denominators the
// container dominates, as a percentage of the box. One function computes it, so
// the cell the user reads and the field the page draws cannot round differently.
func sharePct(r perf.Rank) float64 {
	share := r.CPUShare
	if r.MemShare > share {
		share = r.MemShare
	}
	if share <= 0 {
		return 0
	}
	return math.Round(share*1000) / 10
}

// shareCell is the column form of sharePct. It is the value RankAll ordered by,
// so the column and the order can never disagree.
func shareCell(r perf.Rank) string {
	v := sharePct(r)
	if v <= 0 {
		return "-"
	}
	return strconv.FormatFloat(v, 'f', 1, 64) + "%"
}

// perfFlags is the badge column. The words are the constants the published JSON
// carries, so a badge means the same thing on both surfaces.
func perfFlags(r perf.Rank) string {
	var b []string
	if r.Flagged(perf.FlagHog) {
		b = append(b, strings.ToUpper(perf.FlagHog))
	}
	if r.Flagged(perf.FlagSpike) {
		b = append(b, strings.ToUpper(perf.FlagSpike))
	}
	return strings.Join(b, " ")
}

// throttleCell reports the cgroup's own throttling counter. A container that has
// never been throttled says 0; a host that exposes no counter says "-", and the
// two must not look alike.
func throttleCell(p models.ContainerPeak) string {
	if p.Throttled == models.Unknown {
		return "-"
	}
	if p.Throttled == 0 {
		return "0"
	}
	return strconv.FormatInt(p.Throttled, 10) + "/" + strconv.FormatInt(max(p.Periods, 0), 10)
}

// perfStatus states the window and the denominators every number beside it is
// measured against, because a peak without a stated window is not a claim.
func (d *Dashboard) perfStatus() string {
	var b strings.Builder
	b.WriteString(styleTitle.Render("perf") + " " + styleKey.Render(d.host.Name))
	if d.hasHostStats {
		fmt.Fprintf(&b, " · %d cpu · %s", d.hstats.NumCPU, perf.Bytes(int64(d.hstats.MemTotalMB)<<20))
	}
	fmt.Fprintf(&b, " · window %s · %d samples", windowLabel(perf.Window), d.perfSamples())
	switch {
	case d.peakLoading:
		b.WriteString(styleMuted.Render(" · probing…"))
	case d.peakFetchedAt.IsZero():
		b.WriteString(styleMuted.Render(" · all-time: f to probe"))
	default:
		fmt.Fprintf(&b, " · probed %s", d.peakFetchedAt.Format("15:04:05"))
		if !d.perfSupported() {
			b.WriteString(styleWarn.Render(" · " + d.host.Name + ": cgroup peak not available"))
		}
	}
	if d.peakProblem != "" {
		b.WriteString("\n" + styleErr.Render(d.peakProblem))
	}
	return b.String()
}

func windowLabel(w time.Duration) string {
	if w < time.Minute {
		return strconv.FormatFloat(w.Seconds(), 'f', 0, 64) + "s"
	}
	return strconv.FormatFloat(w.Minutes(), 'f', 0, 64) + "m"
}

// perfSamples counts the readings this session holds, which is what tells the
// user whether "no peak yet" means "idle" or "only just started".
func (d *Dashboard) perfSamples() int {
	if d.perf == nil {
		return 0
	}
	return d.perf.Total()
}

// perfSupported reports whether any container on this host exposed a cgroup
// counter. Nothing at all is not the same as zero, so the header says which.
func (d *Dashboard) perfSupported() bool {
	for _, p := range d.peaks {
		if p.MemPeakBytes != models.Unknown || p.CPUUsec != models.Unknown {
			return true
		}
	}
	return false
}

// perfKeys handles the ranking screen's own keys, returning false for the ones
// the table widget navigates by itself.
func (d *Dashboard) perfKeys(key string) (bool, tea.Cmd) {
	switch key {
	case "esc", "q":
		d.closePerf()
		return true, nil
	case "f":
		return true, d.fetchPeaks()
	case "enter":
		if c, ok := d.perfTarget(); ok {
			d.logReq = &containerReq{id: c.ID, name: c.ShortName()}
		}
		return true, nil
	case "i":
		d.showPerf = false
		d.showDisk = true
		d.diskLoading = true
		d.diskErr = ""
		return true, d.fetchDisk()
	}
	return false, nil
}

// perfTarget is the container under the cursor on the ranking screen, resolved
// the same way the table resolves its own selection — so logs and labels open on
// what the user is pointing at, whichever screen they are on.
func (d *Dashboard) perfTarget() (models.Container, bool) {
	i := d.perfTbl.Cursor()
	if i < 0 || i >= len(d.perfRanks) {
		return models.Container{}, false
	}
	for _, c := range d.containers {
		if c.ID == d.perfRanks[i].ID {
			return c, true
		}
	}
	return models.Container{}, false
}

// perfIndex resolves the anchored id to a row. A container that has left the list
// releases the anchor onto whatever the cursor already held, so it can never
// point past the end.
func (d *Dashboard) perfIndex() int {
	if d.perfCursorID != "" {
		for i, r := range d.perfRanks {
			if r.ID == d.perfCursorID {
				return i
			}
		}
	}
	return clampInt(d.perfTbl.Cursor(), 0, max(len(d.perfRanks)-1, 0))
}

// takePerfCursor records where the cursor ended up after a navigation key, so the
// anchor follows the user rather than the data.
func (d *Dashboard) takePerfCursor() {
	if i := d.perfTbl.Cursor(); i >= 0 && i < len(d.perfRanks) {
		d.perfCursorID = d.perfRanks[i].ID
	}
}

// perfSection is the published form of this session's window. The rows arrive in
// the terminal's own rank order and already carry their flags, so the page draws
// the list without sorting or re-thresholding anything. A container with no
// readings is left out rather than published as a row of zeros.
func (d *Dashboard) perfSection(now time.Time) *httpapi.PerfSection {
	if d.perf == nil {
		return nil
	}
	rows := make([]httpapi.PerfSnapshot, 0, len(d.perfRanks))
	for _, r := range d.perfRanks {
		if r.Sum.Samples == 0 {
			continue
		}
		p := httpapi.PerfSnapshot{
			ID:           r.ID,
			Name:         r.Name,
			CPU:          perf.Round(r.Sum.Last),
			CPUAvg:       perf.Round(r.Sum.Mean),
			CPUPeak:      perf.Round(r.Sum.Peak),
			Share:        sharePct(r),
			Spike:        perf.Round(r.Spike),
			Flags:        append([]string{}, r.Flags...),
			Samples:      r.Sum.Samples,
			MemBytes:     r.Sum.MemBytes,
			MemPeakBytes: r.Sum.MemPeak,
			Series:       perf.Points(d.perf.Series(now, r.ID, perf.Window), perf.MaxPoints),
		}
		if peak, ok := d.peaks[r.ID]; ok {
			if v := peak.MemPeakBytes; v != models.Unknown {
				p.AllTimeMemPeakBytes = &v
			}
			if peak.CPUUsec != models.Unknown {
				sec := perf.Round(float64(peak.CPUUsec) / 1e6)
				p.CPUSeconds = &sec
				// Cumulative CPU is only a cost once it is a rate: 64 minutes is
				// idle noise over a week and a burn over ten minutes. Without the
				// start time there is no honest denominator, so no number.
				if started, err := time.Parse(time.RFC3339Nano, peak.StartedAt); err == nil {
					if life := now.Sub(started).Seconds(); life > 0 {
						// Four decimals, not two: a rate that rounds to 0.00 here is
						// still a rate the page can print as "<0.01" rather than as a
						// measured zero.
						cores := math.Round(float64(peak.CPUUsec)/1e6/life*1e4) / 1e4
						p.CPUAvgCores = &cores
					}
				}
			}
			if v := peak.Throttled; v != models.Unknown {
				p.Throttled = &v
			}
			if v := peak.Periods; v != models.Unknown {
				p.Periods = &v
			}
		}
		rows = append(rows, p)
	}
	if len(rows) == 0 {
		return nil
	}
	return &httpapi.PerfSection{
		Rows:      rows,
		Window:    perf.Window,
		Retention: perf.Horizon,
		Samples:   d.perfSamples(),
		ProbedAt:  d.peakFetchedAt,
		Supported: d.perfSupported(),
	}
}
