package app

import (
	"errors"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"vpstui/internal/docker"
	"vpstui/internal/models"
	"vpstui/internal/perf"
	"vpstui/internal/sshclient"
)

// perfCellOf reads one cell out of the rendered ranking table by container id and
// column title, so an assertion can name the value it means rather than grep the
// whole screen for a "-" that appears for some other reason.
func perfCellOf(d *Dashboard, id, title string) string {
	col := perfColIndex(perfColsFor(max(d.width-2, 20)), title)
	if col < 0 {
		return ""
	}
	for i, r := range d.perfRanks {
		if r.ID == id && i < len(d.perfTbl.rows) {
			return d.perfTbl.rows[i][col]
		}
	}
	return ""
}

func pstats(id, name, cpu, mem string) models.ContainerStats {
	return models.ContainerStats{ID: id, Name: name, CPUPerc: cpu, MemUsage: mem, MemPerc: "1.00%"}
}

// perfDash is a dashboard with two live containers and the ranking screen open.
// The dock is nil, so nothing here can reach a network; samples go in through
// recordPerf exactly as the stats tick feeds them.
func perfDash(width int) *Dashboard {
	d := NewDashboard(host("h"), nil, width, 24, 1)
	d.SetTags(NewTagStore(nil))
	d.containers = []models.Container{
		{ID: "aaa111111111", Names: "idle", State: "running"},
		{ID: "bbb222222222", Names: "hog", State: "running"},
	}
	d.hstats = models.HostStats{NumCPU: 12, MemTotalMB: 1024}
	d.hasHostStats = true
	return d
}

// feed pushes n readings, oldest first, spaced 2 s back from now.
func feedPerf(d *Dashboard, cpu, mem string, n int) {
	now := time.Now()
	for i := 0; i < n; i++ {
		at := now.Add(-time.Duration(n-i) * 2 * time.Second)
		d.recordPerf(at, []models.ContainerStats{
			pstats("aaa111111111", "idle", "1.00%", "1.00MiB / 1GiB"),
			pstats("bbb222222222", "hog", cpu, mem),
		})
	}
	d.rebuildPerf()
}

func TestPerfScreenOpensAndCloses(t *testing.T) {
	d := perfDash(100)
	d.Update(key("p"))
	if !d.showPerf {
		t.Fatal("p did not open perf")
	}
	if d.showDisk {
		t.Fatal("perf must not leave the disk panel open")
	}
	// i switches to disk and closes perf, so one table is ever on screen.
	d.Update(key("i"))
	if d.showPerf || !d.showDisk {
		t.Fatalf("i: perf=%v disk=%v", d.showPerf, d.showDisk)
	}
	// Back out of disk, then re-open perf. Inside the disk panel p and P are the
	// prune confirmations, so perf is reached from the dashboard, not from there.
	d.Update(tea.KeyMsg{Type: tea.KeyEscape})
	d.Update(key("p"))
	if !d.showPerf || d.showDisk {
		t.Fatalf("perf did not re-open: perf=%v disk=%v", d.showPerf, d.showDisk)
	}
	d.Update(tea.KeyMsg{Type: tea.KeyEscape})
	if d.showPerf {
		t.Fatal("esc did not close perf")
	}
	if d.TakeBack() {
		t.Fatal("esc on a sub-mode must not leave the dashboard")
	}
	// q closes it too, and re-opening is idempotent.
	d.Update(key("p"))
	d.Update(key("q"))
	if d.showPerf {
		t.Fatal("q did not close perf")
	}
}

func TestPerfSamplesFromStatsTick(t *testing.T) {
	d := perfDash(100)
	if d.perfSamples() != 0 {
		t.Fatalf("fresh dashboard holds %d samples, want 0", d.perfSamples())
	}
	// The 2 s path records without any extra round trip.
	d.Update(statsLoadedMsg{
		msgTag:     msgTag{hostID: "h", gen: 1},
		containers: d.containers,
		listOK:     true,
		cstats:     []models.ContainerStats{pstats("aaa111111111", "idle", "1.00%", "1.00MiB / 1GiB")},
	})
	if d.perfSamples() != 1 {
		t.Fatalf("after one tick: %d samples, want 1", d.perfSamples())
	}
	// A container only docker stats has reported still gets a row: a background
	// session's container list is stale by design.
	d.recordPerf(time.Now(), []models.ContainerStats{pstats("ccc333333333", "newcomer", "5.00%", "2.00MiB / 1GiB")})
	d.rebuildPerf()
	var found bool
	for _, r := range d.perfRanks {
		if r.ID == "ccc333333333" {
			found = true
		}
	}
	if !found {
		t.Fatalf("unlisted container absent from ranking: %+v", d.perfRanks)
	}
}

func TestPerfRankedOrderAndBadges(t *testing.T) {
	d := perfDash(100)
	d.Update(key("p"))
	// hog eats a core and a tenth of the box's RAM; idle never moves.
	feedPerf(d, "130.00%", "110MiB / 1GiB", 10)
	if len(d.perfRanks) != 2 {
		t.Fatalf("ranks=%d want 2", len(d.perfRanks))
	}
	if d.perfRanks[0].ID != "bbb222222222" {
		t.Fatalf("hog should rank first, got %+v", d.perfRanks)
	}
	if !d.perfRanks[0].Flagged(perf.FlagHog) {
		t.Fatalf("130%% of 12 cores and 110MiB of 1GiB is not a hog: %+v", d.perfRanks[0])
	}
	if len(d.perfRanks[1].Flags) != 0 {
		t.Fatalf("idle should carry no flags: %+v", d.perfRanks[1])
	}
	v := d.View()
	if !strings.Contains(v, "HOG") {
		t.Fatalf("View missing the hog badge:\n%s", v)
	}
	if !strings.Contains(v, "window 20m") || !strings.Contains(v, "12 cpu") {
		t.Fatalf("View does not state its window and denominator:\n%s", v)
	}
	if !strings.Contains(v, "hog") || !strings.Contains(v, "idle") {
		t.Fatalf("View missing container names:\n%s", v)
	}
	// A spike is its own claim: a flat baseline that jumps.
	d2 := perfDash(100)
	d2.Update(key("p"))
	now := time.Now()
	for i := 0; i < 10; i++ {
		d2.recordPerf(now.Add(-time.Duration(20-i)*time.Second), []models.ContainerStats{
			pstats("aaa111111111", "idle", "2.00%", "1.00MiB / 1GiB"),
			pstats("bbb222222222", "hog", "2.00%", "1.00MiB / 1GiB"),
		})
	}
	d2.recordPerf(now, []models.ContainerStats{
		pstats("aaa111111111", "idle", "2.00%", "1.00MiB / 1GiB"),
		pstats("bbb222222222", "hog", "20.00%", "1.00MiB / 1GiB"),
	})
	d2.rebuildPerf()
	var spike bool
	for _, r := range d2.perfRanks {
		if r.ID == "bbb222222222" && r.Flagged(perf.FlagSpike) {
			spike = true
		}
	}
	if !spike {
		t.Fatalf("2%%→20%% is not a spike? %+v", d2.perfRanks)
	}
	if !strings.Contains(d2.View(), "SPIKE") {
		t.Fatalf("View missing the spike badge:\n%s", d2.View())
	}
}

func TestPerfCursorIsAnchoredByContainer(t *testing.T) {
	d := perfDash(100)
	d.Update(key("p"))
	feedPerf(d, "0.50%", "1.00MiB / 1GiB", 10)
	if d.perfRanks[0].ID != "aaa111111111" {
		t.Fatalf("a quieter hog should rank second: %+v", d.perfRanks)
	}
	// Point at idle, then let the other container outrank it.
	d.perfTbl.SetCursor(0)
	d.takePerfCursor()
	feedPerf(d, "130.00%", "110MiB / 1GiB", 10)
	if d.perfRanks[0].ID != "bbb222222222" {
		t.Fatalf("hog did not take rank 1: %+v", d.perfRanks)
	}
	if got := d.perfTbl.Cursor(); got != 1 {
		t.Fatalf("cursor moved to row %d; the anchor is the container, not the rank", got)
	}
	if c, ok := d.perfTarget(); !ok || c.Names != "idle" {
		t.Fatalf("perfTarget after re-rank: %+v ok=%v", c, ok)
	}
}

func TestPerfViewClipsForNarrowWidth(t *testing.T) {
	d := perfDash(56)
	d.Update(key("p"))
	feedPerf(d, "130.00%", "110MiB / 1GiB", 10)
	v := d.View()
	for _, absent := range []string{"AVG", "PEAK", "TREND", "M.PEAK", "THROT", "SHARE"} {
		if strings.Contains(v, absent) {
			t.Fatalf("column %s survived a %d-column terminal:\n%s", absent, d.width, v)
		}
	}
	for _, present := range []string{"NAME", "CPU%", "HOG"} {
		if !strings.Contains(v, present) {
			t.Fatalf("column %s was dropped, but it is the one this screen exists for:\n%s", present, v)
		}
	}
}

func TestPerfPeaksProbeAndDegradation(t *testing.T) {
	d := perfDash(160)
	d.Update(key("p"))
	feedPerf(d, "5.00%", "1.00MiB / 1GiB", 4)
	// No probe yet: the header must say so rather than show blank columns.
	if !strings.Contains(d.View(), "all-time: f to probe") {
		t.Fatalf("missing the un-probed hint:\n%s", d.perfStatus())
	}
	d.Update(peaksLoadedMsg{
		msgTag: msgTag{hostID: "h", gen: 1},
		rows: []models.ContainerPeak{
			{ID: "bbb222222222", MemPeakBytes: 115343360, CPUUsec: 3768589852, Throttled: 0, Periods: 0},
		},
		at: time.Now(),
	})
	if d.peakFetchedAt.IsZero() || d.peakProblem != "" {
		t.Fatalf("probe not recorded: at=%v err=%q", d.peakFetchedAt, d.peakProblem)
	}
	v := d.View()
	if !strings.Contains(v, "probed") || !strings.Contains(v, "110MiB") {
		t.Fatalf("all-time peak not rendered:\n%s", v)
	}
	if got := perfCellOf(d, "aaa111111111", "M.PEAK"); got != "-" {
		t.Fatalf("a container the probe did not answer for shows %q, not -", got)
	}
	// A host that exposes no counter at all says so out loud.
	d.Update(peaksLoadedMsg{
		msgTag: msgTag{hostID: "h", gen: 1},
		rows: []models.ContainerPeak{
			{ID: "aaa111111111", MemPeakBytes: models.Unknown, CPUUsec: models.Unknown, Throttled: models.Unknown, Periods: models.Unknown},
			{ID: "bbb222222222", MemPeakBytes: models.Unknown, CPUUsec: models.Unknown, Throttled: models.Unknown, Periods: models.Unknown},
		},
		at: time.Now(),
	})
	if !strings.Contains(d.perfStatus(), "cgroup peak not available") {
		t.Fatalf("unsupported host not reported as such:\n%s", d.perfStatus())
	}
	// A failed probe keeps the counters the user is reading.
	d.Update(peaksLoadedMsg{
		msgTag: msgTag{hostID: "h", gen: 1},
		err:    errors.New("sh: remote command failed"),
	})
	if d.peakProblem == "" || len(d.peaks) != 2 {
		t.Fatalf("failed probe erased state: problem=%q peaks=%d", d.peakProblem, len(d.peaks))
	}
}

func TestPerfBackgroundSampleRecordsAndRepublishes(t *testing.T) {
	d := perfDash(100)
	// With no dock there is nothing to fetch, and fetchPerf must say so rather
	// than queue a nil-pointer command.
	if d.fetchPerf() != nil {
		t.Fatal("fetchPerf armed a command with no dock")
	}
	d.perfBusy = true
	if d.fetchPerf() != nil {
		t.Fatal("fetchPerf stacked behind an in-flight sample")
	}
	d.perfBusy = false
	d.Update(perfLoadedMsg{
		msgTag: msgTag{hostID: "h", gen: 1},
		cstats: []models.ContainerStats{pstats("aaa111111111", "idle", "7.00%", "3.00MiB / 1GiB")},
	})
	if d.perfBusy {
		t.Fatal("perfBusy still set after the sample returned")
	}
	if d.perfSamples() != 1 {
		t.Fatalf("background sample not recorded: %d", d.perfSamples())
	}
	// A failed background sample is not a reportable error on a screen the user
	// is not looking at.
	d.Update(perfLoadedMsg{msgTag: msgTag{hostID: "h", gen: 1}, err: errors.New("connection reset")})
	if d.errMsg != "" || d.perfSamples() != 1 {
		t.Fatalf("failed sample leaked into the UI: %q", d.errMsg)
	}
}

func TestPerfKeysEnterOpensLogsOfRankedRow(t *testing.T) {
	d := perfDash(100)
	d.Update(key("p"))
	feedPerf(d, "130.00%", "110MiB / 1GiB", 10)
	d.perfTbl.SetCursor(0)
	d.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if d.logReq == nil || d.logReq.id != "bbb222222222" {
		t.Fatalf("enter did not target the ranked row: %+v", d.logReq)
	}
	// f re-probes — with no dock that is a no-op — and esc still closes.
	d.Update(key("f"))
	d.Update(tea.KeyMsg{Type: tea.KeyEscape})
	if d.showPerf {
		t.Fatal("esc after enter/f did not close perf")
	}
}

// perfSession is a connected session with a dock that is never dialed: fetchPerf
// and fetchPeaks only check that a dock exists, and a test never runs the closure.
func perfSession(id string, gen uint64) *Session {
	s := &Session{host: host(id), gen: gen}
	s.conn = &sshclient.Conn{}
	s.dock = docker.New(s.conn)
	s.dash = NewDashboard(s.host, s.dock, 120, 30, gen)
	s.dash.SetTags(NewTagStore(nil))
	return s
}

func TestPerfTickSamplesBackgroundHostOnly(t *testing.T) {
	m := New([]models.Host{host("a"), host("b")}, nil)
	m.width, m.height = 120, 30
	a, b := perfSession("a", 1), perfSession("b", 1)
	m.sessions = []*Session{a, b}
	m.active, m.view = 0, viewDashboard

	// The host on screen is already sampled every 2 s: its perf tick re-arms the
	// chain but adds no round trip beside it.
	_, cmd := m.Update(perfTickMsg{msgTag: msgTag{hostID: "a", gen: 1}})
	if cmd == nil {
		t.Fatal("the perf chain died for the active host")
	}
	if a.dash.perfBusy {
		t.Fatal("the perf tick polled the host the 2 s chain already polls")
	}

	// A background host is sampled, and the chain continues.
	_, cmd = m.Update(perfTickMsg{msgTag: msgTag{hostID: "b", gen: 1}})
	if cmd == nil || !b.dash.perfBusy {
		t.Fatalf("background host not sampled: cmd=%v busy=%v", cmd != nil, b.dash.perfBusy)
	}
	m.Update(perfLoadedMsg{
		msgTag: msgTag{hostID: "b", gen: 1},
		cstats: []models.ContainerStats{pstats("aaa111111111", "x", "1.00%", "1MiB / 1GiB")},
	})
	if b.dash.perfBusy || b.dash.perfSamples() != 1 {
		t.Fatalf("sample not recorded: busy=%v n=%d", b.dash.perfBusy, b.dash.perfSamples())
	}

	// A tick for a generation that no longer exists stops there.
	if _, cmd = m.Update(perfTickMsg{msgTag: msgTag{hostID: "b", gen: 99}}); cmd != nil {
		t.Fatal("a stale generation kept a chain alive")
	}
}

func TestPeaksProbeArmsOncePerGeneration(t *testing.T) {
	m := New([]models.Host{host("a")}, nil)
	m.width, m.height = 120, 30
	s := perfSession("a", 1)
	s.dash.containers = []models.Container{{ID: "aaa111111111", Names: "x", State: "running"}}
	m.sessions = []*Session{s}
	m.active, m.view = 0, viewDashboard

	list := statsLoadedMsg{
		msgTag:     msgTag{hostID: "a", gen: 1},
		containers: s.dash.containers,
		listOK:     true,
	}
	_, cmd := m.Update(list)
	// The first container list that succeeds arms the log sweep and the cgroup
	// probe once each.
	if !s.dash.errArmed || !s.dash.peakArmed {
		t.Fatalf("probe not armed by the first list: err=%v peak=%v", s.dash.errArmed, s.dash.peakArmed)
	}
	if cmd == nil {
		t.Fatal("arming produced no command")
	}
	// Every later tick of the same generation must not probe again; that is the
	// difference between one read-only command and a poll.
	if _, cmd = m.Update(list); cmd != nil {
		t.Fatal("the 2 s tick re-armed the probe")
	}
	// A failed list is not evidence the host is readable.
	fresh := perfSession("a", 2)
	m.sessions = []*Session{fresh}
	if _, cmd = m.Update(statsLoadedMsg{msgTag: msgTag{hostID: "a", gen: 2}, err: errors.New("connection closed")}); cmd != nil {
		t.Fatal("a failed list armed a probe")
	}
	if fresh.dash.peakArmed || fresh.dash.errArmed {
		t.Fatal("a failed list set the arming flags")
	}
}

// TestPerfSnapshotCarriesTheWindow is the seam between the screen and the page:
// what the terminal ranked is exactly what gets published, in the same order,
// with the same flags and the same denominators — the HTTP layer never sees a
// container list it would have to rank itself.
func TestPerfSnapshotCarriesTheWindow(t *testing.T) {
	d := perfDash(100)
	if snap := d.snapshot(host("h"), true); snap.Perf != nil {
		t.Fatalf("a session with no readings publishes a window: %+v", snap.Perf)
	}

	feedPerf(d, "130.00%", "110MiB / 1GiB", 200)
	d.Update(peaksLoadedMsg{
		msgTag: msgTag{hostID: "h", gen: 1},
		rows: []models.ContainerPeak{
			{ID: "bbb222222222", MemPeakBytes: 115343360, CPUUsec: 3768589852, Throttled: 0, Periods: 0},
		},
		at: time.Now(),
	})
	sec := d.snapshot(host("h"), true).Perf
	if sec == nil {
		t.Fatal("window not published")
	}
	if sec.Window != perf.Window || sec.Retention != perf.Horizon {
		t.Errorf("window/retention = %v/%v, want %v/%v", sec.Window, sec.Retention, perf.Window, perf.Horizon)
	}
	if sec.Samples != d.perfSamples() || !sec.ProbedAt.Equal(d.peakFetchedAt) {
		t.Errorf("samples=%d probedAt=%v do not match the screen (%d, %v)",
			sec.Samples, sec.ProbedAt, d.perfSamples(), d.peakFetchedAt)
	}
	if !sec.Supported {
		t.Error("a host that answered with a real counter reported unsupported")
	}
	// 130%% of one core on a 12-core box is 10.8%% of the box, and the memory side
	// (110MiB of 1GiB) is a hair under it. Published as a percentage, because the
	// cell prints a percentage: one computation feeds the screen and the page.
	if sec.Rows[0].Share != 10.8 {
		t.Errorf("share = %v, want 10.8 (percent of the box, not a fraction)", sec.Rows[0].Share)
	}
	if cell := shareCell(d.perfRanks[0]); cell != strconv.FormatFloat(sec.Rows[0].Share, 'f', 1, 64)+"%" {
		t.Errorf("published share does not print the cell the terminal shows: %q vs %v", cell, sec.Rows[0].Share)
	}
	if len(sec.Rows) != 2 || sec.Rows[0].ID != "bbb222222222" {
		t.Fatalf("rows not in the terminal's rank order: %+v", sec.Rows)
	}
	if !reflect.DeepEqual(sec.Rows[0].Flags, d.perfRanks[0].Flags) {
		t.Errorf("flags diverged from the badges on screen: %v vs %v", sec.Rows[0].Flags, d.perfRanks[0].Flags)
	}
	// The label rides with the row from the same store the ranking cell reads,
	// so the page's perf tab names containers exactly like the terminal does.
	d.tags.Set(d.hostID, "hog", "mail", "sky", "bbb222222222")
	if tagged := d.snapshot(host("h"), true).Perf.Rows[0]; tagged.Tag != "mail" || tagged.TagColor != "sky" {
		t.Errorf("tagged row published %q/%q, want mail/sky", tagged.Tag, tagged.TagColor)
	}
	// Clearing the label clears it on the wire too (a nil store must not panic).
	d.tags.Clear(d.hostID, "hog")
	if untagged := d.snapshot(host("h"), true).Perf.Rows[0]; untagged.Tag != "" || untagged.TagColor != "" {
		t.Errorf("untagged row published %q/%q", untagged.Tag, untagged.TagColor)
	}
	if sec.Rows[0].AllTimeMemPeakBytes == nil || *sec.Rows[0].AllTimeMemPeakBytes != 115343360 {
		t.Errorf("all-time peak not published: %+v", sec.Rows[0])
	}
	// The container the probe did not answer for claims nothing.
	if sec.Rows[1].AllTimeMemPeakBytes != nil || sec.Rows[1].CPUSeconds != nil ||
		sec.Rows[1].CPUAvgCores != nil ||
		sec.Rows[1].Throttled != nil || sec.Rows[1].Periods != nil {
		t.Errorf("unprobed container published counters: %+v", sec.Rows[1])
	}
	// A 200-sample window leaves the process downsampled, however long it ran.
	if len(sec.Rows[0].Series) > perf.MaxPoints {
		t.Fatalf("series has %d points, cap is %d", len(sec.Rows[0].Series), perf.MaxPoints)
	}
	// Cumulative CPU is not yet a rate: with no start time there is no honest
	// denominator, so the field stays absent instead of inventing a lifetime.
	if sec.Rows[0].CPUAvgCores != nil {
		t.Errorf("a rate computed from an unknown start time: %v", *sec.Rows[0].CPUAvgCores)
	}
	d.Update(peaksLoadedMsg{
		msgTag: msgTag{hostID: "h", gen: 1},
		rows: []models.ContainerPeak{
			{ID: "bbb222222222", MemPeakBytes: 115343360, CPUUsec: 3768589852, Throttled: 0, Periods: 0,
				StartedAt: time.Now().Add(-time.Hour).Format(time.RFC3339Nano)},
		},
		at: time.Now(),
	})
	// 3768.6 CPU-seconds across an hour of life is 1.05 cores — the number that
	// says whether 64 minutes of burn is a hog or a week of noise.
	sec = d.snapshot(host("h"), true).Perf
	if cores := sec.Rows[0].CPUAvgCores; cores == nil || *cores < 1.0 || *cores > 1.1 {
		t.Errorf("all-time CPU rate = %v, want ~1.05 cores", cores)
	}
	// A rate too small to print is still not a measured zero: it must survive the
	// trip with enough precision for the page to say "<0.01" rather than "0".
	d.Update(peaksLoadedMsg{
		msgTag: msgTag{hostID: "h", gen: 1},
		rows: []models.ContainerPeak{
			{ID: "bbb222222222", MemPeakBytes: 115343360, CPUUsec: 3768589852, Throttled: 0, Periods: 0,
				StartedAt: time.Now().Add(-240 * time.Hour).Format(time.RFC3339Nano)},
		},
		at: time.Now(),
	})
	if cores := d.snapshot(host("h"), true).Perf.Rows[0].CPUAvgCores; cores == nil || *cores <= 0 || *cores > 0.005 {
		t.Errorf("a tenth-of-a-percent rate published as %v; the page needs it above zero and below 0.005", cores)
	}

	// An unsupported host still publishes its window; only the all-time half goes.
	d2 := perfDash(100)
	feedPerf(d2, "5.00%", "1.00MiB / 1GiB", 10)
	d2.Update(peaksLoadedMsg{
		msgTag: msgTag{hostID: "h", gen: 1},
		rows: []models.ContainerPeak{
			{ID: "aaa111111111", MemPeakBytes: models.Unknown, CPUUsec: models.Unknown, Throttled: models.Unknown, Periods: models.Unknown},
		},
		at: time.Now(),
	})
	sec2 := d2.snapshot(host("h"), true).Perf
	if sec2 == nil || sec2.Supported {
		t.Fatalf("unsupported host published supported=%v", func() bool { return sec2 != nil && sec2.Supported }())
	}
	for _, r := range sec2.Rows {
		if r.AllTimeMemPeakBytes != nil || r.CPUSeconds != nil || r.CPUAvgCores != nil ||
			r.Throttled != nil || r.Periods != nil {
			t.Errorf("a counter the host did not expose was published as a measurement: %+v", r)
		}
	}
	if sec2.ProbedAt.IsZero() {
		t.Error("a dated probe lost its timestamp when the host proved unsupported")
	}
}

// TestPerfViewDoesNotRepeatTheHeader is a frame-height guard: the sub-mode adds
// its own status line under the dashboard header, and the table sizes itself to
// what is left. A screen that names its host twice is one that wrote the header
// into the body as well, which also steals a row from the ranking.
func TestPerfViewDoesNotRepeatTheHeader(t *testing.T) {
	d := perfDash(100)
	d.Update(key("p"))
	feedPerf(d, "130.00%", "110MiB / 1GiB", 10)
	v := d.View()
	if n := strings.Count(v, "[root@127.0.0.1:22]"); n != 1 {
		t.Fatalf("header target printed %d times, want 1:\n%s", n, v)
	}
	// "window" is only ever written by the perf status line; the host name beside
	// it carries escape codes, so count the word that does not.
	if n := strings.Count(v, "window 20m"); n != 1 {
		t.Fatalf("perf status printed %d times, want 1:\n%s", n, v)
	}
	if lipgloss.Height(v) > 24 {
		t.Fatalf("perf view is %d lines tall in a 24-line terminal:\n%s", lipgloss.Height(v), v)
	}
}
