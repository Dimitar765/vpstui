package app

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"vpstui/internal/docker"
	"vpstui/internal/models"
	"vpstui/internal/perf"
)

type containerReq struct {
	id   string
	name string
}

type detailState struct {
	name    string
	loading bool
	text    string
	isErr   bool
}

type Dashboard struct {
	host models.Host
	dock *docker.Executor

	hostID string
	gen    uint64

	tbl          ctable
	containers   []models.Container
	stats        map[string]models.ContainerStats // by container name
	tags         *TagStore                        // shared with root; nil-safe
	hstats       models.HostStats
	hasHostStats bool

	// perf is this session's CPU and memory history. It records every stats tick,
	// screen open or not: a peak that only exists while the user is looking is not
	// a peak.
	perf *perf.Buffer

	// The ranking screen. perfRanks is exactly what the table shows, and the
	// cursor is anchored by container id rather than row index, because a live
	// ranking re-orders rows every 2 s and an index cursor would teleport.
	showPerf     bool
	perfTbl      ctable
	perfRanks    []perf.Rank
	perfCursorID string

	// perfBusy marks the 10 s background sample as in flight, so a host that
	// answers slowly cannot stack round trips behind its own tick.
	perfBusy bool

	// perfNames remembers the name each sampled id reported. A background
	// session's container list is frozen until it is opened, so without this a
	// container started after the connection would be sampled but never ranked.
	perfNames map[string]string

	// peaks holds the kernel's all-time counters, keyed by short container id.
	// peakArmed makes the probe once-per-generation exactly like errArmed: it is
	// one extra read-only command, so it runs when the container list first
	// succeeds and when the user asks with f — never beside the stats tick.
	peaks         map[string]models.ContainerPeak
	peakArmed     bool
	peakLoading   bool
	peakProblem   string
	peakFetchedAt time.Time

	collapsed map[string]bool // compose project → folded
	rowCIdx   []int           // table row → containers index, -1 for group headers
	rowProj   []string        // table row → compose project, "" for standalone

	detail *detailState

	showDisk    bool
	diskLoading bool
	diskErr     string
	disk        []models.DiskObject

	errMsg       string
	toast        string
	toastIsErr   bool
	confirm      string // verb pending confirmation: start/stop/restart
	disconnected bool
	reconnect    bool

	// Error sweep state. errArmed makes the sweep once-per-generation: root
	// arms it after the first successful container list and after reconnect,
	// and "f" re-arms it. The 2 s stats tick never does.
	errCorpus    []ErrLine
	errFetchedAt time.Time
	errTruncated bool
	errLoading   bool
	errProblem   string
	errArmed     bool

	// prompt is the pre-rendered tag prompt line, owned by root and shown in
	// the same slot a toast uses so it cannot add a row to the frame.
	prompt string

	width, height int
	back          bool
	logReq        *containerReq
	shellReq      *containerReq
	shellBusy     bool
}

var dashCols = []ctableCol{
	{title: "Name", width: 16, kind: cellTag},
	{title: "Image", width: 18, kind: cellPlain},
	{title: "State", width: 12, kind: cellState},
	{title: "Status", width: 18, kind: cellPlain},
	{title: "CPU %", width: 7, kind: cellPct},
	{title: "MEM %", width: 7, kind: cellPct},
	{title: "MEM USAGE", width: 18, kind: cellPlain},
}

const cellGapTotal = 12 // 2-space gap × 6 between seven columns

// staticColsW returns the width consumed by all non-adaptive columns
// (State, CPU, MEM, gaps) plus Status and MEM USAGE at the given total.
func staticColsW(total int) int {
	usage, status := 18, 18
	if total < 110 {
		usage = 12
	}
	if total < 90 {
		status = 12
	}
	return 12 + 7 + 7 + status + usage + cellGapTotal
}

// allocCols splits the remaining width between Name and Image (3:2),
// honoring floors of 12 and 10.
func allocCols(total int) (nameW, imageW int) {
	nameW, imageW = 12, 10
	slack := total - staticColsW(total)
	if slack <= nameW+imageW {
		return nameW, imageW
	}
	n := slack * 3 / 5
	i := slack - n
	if n < nameW {
		n = nameW
		i = max(slack-n, imageW)
	} else if i < imageW {
		i = imageW
		n = max(slack-i, nameW)
	}
	return n, i
}

// colsFor returns the column set sized for a table area of total width.
func colsFor(total int) []ctableCol {
	c := make([]ctableCol, len(dashCols))
	copy(c, dashCols)
	c[6].width, c[3].width = 18, 18
	if total < 110 {
		c[6].width = 12
	}
	if total < 90 {
		c[3].width = 12
	}
	c[0].width, c[1].width = allocCols(total)
	return c
}

func NewDashboard(host models.Host, dock *docker.Executor, w, h int, gen uint64) *Dashboard {
	d := &Dashboard{
		host: host, dock: dock, hostID: host.ID, gen: gen,
		stats: map[string]models.ContainerStats{}, collapsed: map[string]bool{},
		perf:      perf.New(perf.Cap, perf.Horizon),
		peaks:     map[string]models.ContainerPeak{},
		perfNames: map[string]string{},
		width:     w, height: h,
	}
	d.tbl.SetColumns(colsFor(max(w-2, 20)))
	d.tbl.SetSize(max(w-2, 20), max(h-10, 3))
	d.perfTbl.SetColumns(perfColsFor(max(w-2, 20)))
	d.perfTbl.SetSize(max(w-2, 20), max(h-10, 3))
	return d
}

// SetTags hands the dashboard the shared label store. Root owns the store and
// every view reads it by pointer, so a tag shows up everywhere at once without
// any copying or locking.
func (d *Dashboard) SetTags(t *TagStore) { d.tags = t }

// SetPrompt shows or clears the pre-rendered tag prompt line.
func (d *Dashboard) SetPrompt(line string) { d.prompt = line }

// SetToast lets root report a result of its own — a label that was saved, or
// a file that could not be written — through the same one-shot row the docker
// actions use.
func (d *Dashboard) SetToast(msg string, isErr bool) { d.toast, d.toastIsErr = msg, isErr }

// TagTarget is the container under the cursor, for the label prompt. A compose
// header row is not a container, so it reports false there. On the ranking screen
// it resolves the ranking's own cursor, so one key means the row the user is on.
func (d *Dashboard) TagTarget() (tagTarget, bool) {
	if d.showPerf {
		c, ok := d.perfTarget()
		if !ok {
			return tagTarget{}, false
		}
		return tagTarget{hostID: d.host.ID, name: c.ShortName(), id: c.ID}, true
	}
	c, ok := d.selected()
	if !ok {
		return tagTarget{}, false
	}
	return tagTarget{hostID: d.host.ID, name: c.ShortName(), id: c.ID}, true
}

func (d *Dashboard) SetSize(w, h int) {
	d.width, d.height = w, h
	d.tbl.SetColumns(colsFor(max(w-2, 20)))
	d.tbl.SetSize(max(w-2, 20), max(h-10, 3))
	d.perfTbl.SetColumns(perfColsFor(max(w-2, 20)))
	d.perfTbl.SetSize(max(w-2, 20), max(h-11, 3))
}

func (d *Dashboard) TakeBack() bool {
	b := d.back
	d.back = false
	return b
}

func (d *Dashboard) TakeReconnect() bool {
	r := d.reconnect
	d.reconnect = false
	return r
}

func (d *Dashboard) OpenLogs() (containerReq, bool) {
	if d.logReq == nil {
		return containerReq{}, false
	}
	r := *d.logReq
	d.logReq = nil
	return r, true
}

func (d *Dashboard) OpenShell() (containerReq, bool) {
	if d.shellReq == nil {
		return containerReq{}, false
	}
	r := *d.shellReq
	d.shellReq = nil
	return r, true
}

func (d *Dashboard) selected() (models.Container, bool) {
	idx := d.tbl.Cursor()
	if idx < 0 || idx >= len(d.rowCIdx) {
		return models.Container{}, false
	}
	ci := d.rowCIdx[idx]
	if ci < 0 {
		return models.Container{}, false
	}
	return d.containers[ci], true
}

// selectedProject returns the compose project of the row under the cursor,
// "" for standalone rows, false when out of range.
func (d *Dashboard) selectedProject() (string, bool) {
	idx := d.tbl.Cursor()
	if idx < 0 || idx >= len(d.rowProj) {
		return "", false
	}
	return d.rowProj[idx], true
}

func (d *Dashboard) fetchNow() tea.Cmd {
	dock := d.dock
	t := msgTag{hostID: d.hostID, gen: d.gen}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
		defer cancel()
		containers, err := dock.ListContainers(ctx)
		if err != nil {
			return statsLoadedMsg{msgTag: t, err: err}
		}
		cstats, _ := dock.ContainerStats(ctx)
		hstats, herr := dock.HostStats(ctx)
		return statsLoadedMsg{msgTag: t, containers: containers, listOK: true, cstats: cstats, hstats: hstats, err: herr}
	}
}

// SweepErrors greps every container's recent logs on this host for problems.
// Root arms it once per session generation, never from the stats tick: fetchNow
// runs every 2 s, and hanging a log sweep on that would poll harder than the
// design allows. Parsing happens in the goroutine because a multi-megabyte
// payload must never touch the UI thread.
func (d *Dashboard) SweepErrors() tea.Cmd {
	if d.dock == nil || d.errLoading {
		return nil
	}
	d.errLoading = true
	d.errProblem = ""
	dock := d.dock
	t := msgTag{hostID: d.hostID, gen: d.gen}
	hostID, hostName := d.hostID, d.host.Name
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		at := time.Now()
		payload, err := dock.SweepErrors(ctx, errWindow(), errTail())
		lines, trunc := parseErrSweep(payload, hostID, hostName, at)
		return errLoadedMsg{msgTag: t, lines: lines, truncated: trunc, at: at, err: err}
	}
}

func (d *Dashboard) fetchDisk() tea.Cmd {
	dock := d.dock
	t := msgTag{hostID: d.hostID, gen: d.gen}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
		defer cancel()
		objs, err := dock.SystemDiskUsage(ctx)
		return diskLoadedMsg{msgTag: t, objs: objs, err: err}
	}
}

func (d *Dashboard) probeShell(req containerReq) tea.Cmd {
	dock := d.dock
	t := msgTag{hostID: d.hostID, gen: d.gen}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
		defer cancel()
		err := dock.ProbeShell(ctx, req.id)
		return shellProbeDoneMsg{msgTag: t, req: req, err: err}
	}
}

func (d *Dashboard) Update(msg tea.Msg) (*Dashboard, tea.Cmd) {
	switch msg := msg.(type) {
	case statsTickMsg:
		return d, nil // root re-schedules the fetch chain

	case statsLoadedMsg:
		if !msg.listOK {
			d.errMsg = msg.err.Error()
			if looksLikeConnLoss(msg.err.Error()) {
				d.disconnected = true
			}
			return d, nil
		}
		d.containers = msg.containers
		d.stats = make(map[string]models.ContainerStats, len(msg.cstats))
		for _, s := range msg.cstats {
			d.stats[s.Name] = s
		}
		d.hstats = msg.hstats
		d.hasHostStats = true
		if msg.err != nil {
			d.errMsg = "host stats unavailable: " + msg.err.Error()
		} else {
			d.errMsg = ""
		}
		d.disconnected = false
		d.rebuild()
		// The history records every tick, screen open or not: this is the 2 s path
		// the plan samples on, and it costs no extra SSH.
		d.recordPerf(time.Now(), msg.cstats)
		d.rebuildPerf()
		return d, nil

	case actionDoneMsg:
		label := msg.verb
		if msg.name != "" {
			label += " " + msg.name
		}
		if msg.err != nil {
			d.toast = label + " failed: " + msg.err.Error()
			d.toastIsErr = true
		} else {
			d.toast = label + " ok"
			d.toastIsErr = false
		}
		d.errMsg = ""
		if d.showDisk {
			return d, tea.Batch(d.fetchNow(), d.fetchDisk())
		}
		return d, d.fetchNow()

	case detailLoadedMsg:
		if d.detail == nil {
			return d, nil
		}
		if msg.err != nil {
			d.detail.text = msg.err.Error()
			d.detail.isErr = true
		} else {
			d.detail.text = detailText(msg.detail)
			d.detail.isErr = false
		}
		d.detail.loading = false
		return d, nil

	case errLoadedMsg:
		d.errLoading = false
		d.errFetchedAt = msg.at
		d.errTruncated = msg.truncated
		d.errProblem = ""
		if msg.err != nil {
			// Keep whatever lines arrived: one unreadable container should not
			// hide the rest of the host.
			d.errProblem = msg.err.Error()
		}
		d.errCorpus = capCorpus(msg.lines, errCorpusCap)
		return d, nil

	case diskLoadedMsg:
		d.diskLoading = false
		if msg.err != nil {
			d.diskErr = msg.err.Error()
		} else {
			d.disk = msg.objs
			d.diskErr = ""
		}
		return d, nil

	case perfTickMsg:
		return d, nil // root runs the fetch; the dashboard only records

	case perfLoadedMsg:
		d.perfBusy = false
		if msg.err != nil {
			// A failed background sample is not a reportable error: the screen a
			// user is reading keeps its own status line, and this host is not it.
			return d, nil
		}
		d.recordPerf(time.Now(), msg.cstats)
		d.rebuildPerf()
		return d, nil

	case peaksLoadedMsg:
		d.peakLoading = false
		if msg.err != nil {
			// Keep whatever rows a previous probe found: one failed command must not
			// erase the counters the user is reading.
			d.peakProblem = msg.err.Error()
			d.rebuildPerf()
			return d, nil
		}
		d.peaks = make(map[string]models.ContainerPeak, len(msg.rows))
		for _, p := range msg.rows {
			d.peaks[p.ID] = p
		}
		d.peakFetchedAt = msg.at
		d.rebuildPerf()
		return d, nil

	case shellExitedMsg:
		d.shellBusy = false
		if msg.err != nil {
			detail := msg.detail
			if detail == "" {
				detail = msg.err.Error()
			}
			d.toast = "shell " + msg.name + " failed: " + detail
			d.toastIsErr = true
		} else {
			d.toast = "shell " + msg.name + " ended"
			d.toastIsErr = false
		}
		return d, d.fetchNow()

	case tea.KeyMsg:
		if d.detail != nil {
			switch msg.String() {
			case "esc", "q", "d", "x", "enter":
				d.detail = nil
			}
			return d, nil
		}
		if d.confirm != "" {
			switch msg.String() {
			case "y", "Y":
				verb := d.confirm
				d.confirm = ""
				dock := d.dock
				t := msgTag{hostID: d.hostID, gen: d.gen}
				if strings.HasPrefix(verb, "prune") {
					return d, func() tea.Msg {
						ctx, cancel := context.WithTimeout(context.Background(), 125*time.Second)
						defer cancel()
						var err error
						switch verb {
						case "prune images":
							err = dock.PruneImages(ctx)
						case "prune system":
							err = dock.PruneSystem(ctx)
						}
						return actionDoneMsg{msgTag: t, verb: verb, err: err}
					}
				}
				c, ok := d.selected()
				if !ok {
					return d, nil
				}
				return d, func() tea.Msg {
					ctx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
					defer cancel()
					var err error
					switch verb {
					case "start":
						err = dock.Start(ctx, c.ID)
					case "stop":
						err = dock.Stop(ctx, c.ID)
					case "restart":
						err = dock.Restart(ctx, c.ID)
					case "rm":
						err = dock.Rm(ctx, c.ID)
					}
					return actionDoneMsg{msgTag: t, verb: verb, name: c.ShortName(), err: err}
				}
			case "n", "N", "esc":
				d.confirm = ""
				return d, nil
			default:
				d.confirm = ""
			}
		}
		// Whichever sub-mode is open owns the keys after a confirmation has been
		// settled, and only one of them is ever open.
		if d.showPerf {
			if handled, cmd := d.perfKeys(msg.String()); handled {
				return d, cmd
			}
			var cmd tea.Cmd
			d.perfTbl, cmd = d.perfTbl.Update(msg)
			// Navigation moves the anchor, so the next re-rank keeps the cursor on
			// the container the user chose rather than on the row number.
			d.takePerfCursor()
			return d, cmd
		}
		if d.showDisk {
			switch msg.String() {
			case "esc":
				d.showDisk = false
			case "f":
				d.diskLoading = true
				d.diskErr = ""
				return d, d.fetchDisk()
			case "p":
				d.confirm = "prune images"
			case "P":
				d.confirm = "prune system"
			}
			return d, nil
		}
		if k := msg.String(); k == " " || k == "space" {
			d.toggleFold()
			return d, nil
		}
		switch msg.String() {
		case "c":
			if d.disconnected {
				d.reconnect = true
			}
			return d, nil
		case "esc", "q":
			d.back = true
			return d, nil
		case "f":
			// Refresh re-arms the sweep and the cgroup probe too, so "f" is the
			// user's explicit re-read of this host's logs and kernel counters.
			d.errArmed = false
			d.peakArmed = false
			return d, d.fetchNow()
		case "enter":
			if c, ok := d.selected(); ok {
				d.logReq = &containerReq{id: c.ID, name: c.ShortName()}
			}
			return d, nil
		case "s":
			if _, ok := d.selected(); ok {
				d.confirm = "start"
			}
			return d, nil
		case "x":
			if _, ok := d.selected(); ok {
				d.confirm = "stop"
			}
			return d, nil
		case "r":
			if _, ok := d.selected(); ok {
				d.confirm = "restart"
			}
			return d, nil
		case "i":
			d.showPerf = false
			d.showDisk = true
			d.diskLoading = true
			d.diskErr = ""
			return d, d.fetchDisk()
		case "p":
			// The ranking screen. Its all-time columns come from the same probe the
			// first container list armed, so opening it usually shows numbers at once.
			return d, d.openPerf()
		case "D":
			if c, ok := d.selected(); ok {
				if c.State == "exited" || c.State == "dead" || c.State == "created" {
					d.confirm = "rm"
				} else {
					d.toast = "only stopped containers can be removed"
					d.toastIsErr = true
				}
			}
			return d, nil
		case "e":
			if d.shellBusy {
				return d, nil // a probe or shell is already in flight
			}
			if c, ok := d.selected(); ok {
				if c.State == "running" {
					d.shellBusy = true
					d.shellReq = &containerReq{id: c.ID, name: c.ShortName()}
				} else {
					d.toast = "shell needs a running container"
					d.toastIsErr = true
				}
			}
			return d, nil
		case "d":
			if c, ok := d.selected(); ok {
				d.detail = &detailState{name: c.ShortName(), loading: true}
				dock := d.dock
				t := msgTag{hostID: d.hostID, gen: d.gen}
				id := c.ID
				return d, func() tea.Msg {
					ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
					defer cancel()
					det, err := dock.Inspect(ctx, id)
					return detailLoadedMsg{msgTag: t, detail: det, err: err}
				}
			}
			return d, nil
		}
	}

	var cmd tea.Cmd
	d.tbl, cmd = d.tbl.Update(msg)
	return d, cmd
}

func looksLikeConnLoss(s string) bool {
	low := strings.ToLower(s)
	return strings.Contains(low, "connection lost") ||
		strings.Contains(low, "use of closed") ||
		strings.Contains(low, "eof") ||
		strings.Contains(low, "broken pipe") ||
		strings.Contains(low, "no such host") ||
		strings.Contains(low, "tcp connect")
}

func (d *Dashboard) rebuild() {
	cur := d.tbl.Cursor()

	byProj := map[string][]int{}
	var order []string
	for i, c := range d.containers {
		p := c.ComposeProject()
		if _, ok := byProj[p]; !ok {
			order = append(order, p)
		}
		byProj[p] = append(byProj[p], i)
	}
	sort.SliceStable(order, func(i, k int) bool {
		if (order[i] == "") != (order[k] == "") {
			return order[i] == ""
		}
		return order[i] < order[k]
	})

	rows := make([][]string, 0, len(d.containers))
	hdrRows := make([]bool, 0, len(d.containers))
	tint := make([]string, 0, len(d.containers))
	d.rowCIdx = d.rowCIdx[:0]
	d.rowProj = d.rowProj[:0]
	blank := func() []string { return []string{"", "", "", "", "", "", ""} }

	for _, p := range order {
		idxs := byProj[p]
		if p == "" {
			for _, i := range idxs {
				rows = append(rows, d.containerRow(d.containers[i]))
				hdrRows = append(hdrRows, false)
				tint = append(tint, d.tagColorOf(d.containers[i]))
				d.rowCIdx = append(d.rowCIdx, i)
				d.rowProj = append(d.rowProj, "")
			}
			continue
		}
		folded := d.collapsed[p]
		marker := "▾"
		if folded {
			marker = "▸"
		}
		hdr := blank()
		hdr[0] = fmt.Sprintf("%s compose:%s (%d)", marker, p, len(idxs))
		rows = append(rows, hdr)
		hdrRows = append(hdrRows, true)
		tint = append(tint, "")
		d.rowCIdx = append(d.rowCIdx, -1)
		d.rowProj = append(d.rowProj, p)
		if folded {
			continue
		}
		for _, i := range idxs {
			rows = append(rows, d.containerRow(d.containers[i]))
			hdrRows = append(hdrRows, false)
			tint = append(tint, d.tagColorOf(d.containers[i]))
			d.rowCIdx = append(d.rowCIdx, i)
			d.rowProj = append(d.rowProj, p)
		}
	}

	d.tbl.SetRows(rows, hdrRows)
	d.tbl.SetTint(tint)
	if cur >= len(rows) {
		cur = len(rows) - 1
	}
	if cur < 0 {
		cur = 0
	}
	if len(rows) > 0 {
		d.tbl.SetCursor(cur)
	}
}

func (d *Dashboard) containerRow(c models.Container) []string {
	cpu, mem, memUsage := "-", "-", "-"
	if s, ok := d.stats[c.ShortName()]; ok {
		cpu, mem, memUsage = s.CPUPerc, s.MemPerc, s.MemUsage
	}
	// The label goes first so a narrow terminal clips the docker name's tail,
	// never the word the user chose.
	return []string{d.tags.Display(d.host.ID, c.ShortName()), c.Image, c.State, c.Status, cpu, mem, memUsage}
}

// tagColorOf is the row's tint for ctable: the palette name, or "" for plain.
func (d *Dashboard) tagColorOf(c models.Container) string {
	tag, ok := d.tags.Get(d.host.ID, c.ShortName())
	if !ok {
		return ""
	}
	return tag.Color
}

func (d *Dashboard) toggleFold() {
	p, ok := d.selectedProject()
	if !ok || p == "" {
		return
	}
	d.collapsed[p] = !d.collapsed[p]
	d.rebuild()
}

func detailText(det models.ContainerDetail) string {
	var b strings.Builder
	fmt.Fprintf(&b, "status: %s · restarts: %d · created: %s\n", det.Status, det.RestartCount, det.Created)
	fmt.Fprintf(&b, "image:  %s\n", det.Image)
	if len(det.Networks) == 0 {
		b.WriteString("networks: none\n")
	} else {
		b.WriteString("networks:\n")
		for _, n := range det.Networks {
			fmt.Fprintf(&b, "  %s  %s\n", n.Name, n.IPAddress)
		}
	}
	if len(det.Ports) == 0 {
		b.WriteString("ports: none\n")
	} else {
		b.WriteString("ports:\n")
		for _, p := range det.Ports {
			b.WriteString("  " + p + "\n")
		}
	}
	if len(det.Mounts) == 0 {
		b.WriteString("mounts: none\n")
	} else {
		b.WriteString("mounts:\n")
		for _, m := range det.Mounts {
			b.WriteString("  " + m + "\n")
		}
	}
	return b.String()
}

func (d *Dashboard) View() string {
	var b strings.Builder
	b.WriteString(styleTitle.Render("vpstui") + " " +
		styleKey.Render(d.host.Name) + " " +
		styleMuted.Render("["+d.host.Target()+"]") + "\n")

	if d.hasHostStats {
		hs := d.hstats
		b.WriteString(styleMuted.Render(fmt.Sprintf("load %.2f %.2f %.2f · mem %d/%d MB · %d cpu · up %s",
			hs.Load1, hs.Load5, hs.Load15, hs.MemUsedMB, hs.MemTotalMB, hs.NumCPU,
			fmtDuration(hs.UptimeSec))) + "\n")
	}
	if d.disconnected {
		b.WriteString(styleErr.Render("connection lost — press c to reconnect, esc to go back") + "\n")
	}
	if d.confirm != "" {
		label := d.confirm
		if !strings.HasPrefix(label, "prune") {
			if c, ok := d.selected(); ok {
				label = fmt.Sprintf("%s %s", d.confirm, c.ShortName())
			}
		}
		b.WriteString(styleWarn.Render(label+"? [y/N]") + "\n")
	} else if d.prompt != "" {
		// The tag prompt takes the toast's slot: one row, whichever it shows.
		b.WriteString(d.prompt + "\n")
	} else if d.toast != "" {
		if d.toastIsErr {
			b.WriteString(styleErr.Render(d.toast) + "\n")
		} else {
			b.WriteString(styleOK.Render(d.toast) + "\n")
		}
		d.toast = ""
	}
	if d.errMsg != "" {
		b.WriteString(styleErr.Render(d.errMsg) + "\n")
	}
	b.WriteString("\n")
	if d.detail != nil {
		var inner strings.Builder
		if d.detail.loading {
			inner.WriteString(styleMuted.Render("inspecting…") + "\n")
		} else if d.detail.isErr {
			inner.WriteString(styleErr.Render(d.detail.text) + "\n")
		} else {
			inner.WriteString(d.detail.text)
		}
		// The table cell can clip a long name, so the detail pane states both the
		// label and the docker name in full.
		if tag, ok := d.tags.Get(d.host.ID, d.detail.name); ok {
			inner.WriteString(tagStyle(tag.Color).Render("tag        "+tag.Tag) +
				styleMuted.Render("  (docker name: "+tag.Name+")") + "\n")
		}
		b.WriteString(panel(styleTitle.Render("detail — "+d.detail.name), inner.String(), d.width) + "\n")
		return pinFooter(b.String(), helpLine("esc", "close", "d", "close", "f", "refresh"), d.width, d.height)
	}
	if d.showPerf {
		footer := helpLine("j/k g/G", "nav", "enter", "logs", "t", "tag", "T", "untag", "i", "disk", "f", "re-probe", "esc", "close")
		status := d.perfStatus() + "\n"
		head := b.String() + status
		// The frame is exact: the table gets whatever the header and the footer
		// leave, so pinFooter never has to overflow the terminal.
		d.perfTbl.SetSize(max(d.width-2, 20),
			max(d.height-lipgloss.Height(head)-lipgloss.Height(footer)-1, 1))
		b.WriteString(status)
		if len(d.perfRanks) == 0 {
			b.WriteString(styleMuted.Render("no containers to rank") + "\n")
		}
		b.WriteString(d.perfTbl.View() + "\n")
		return pinFooter(b.String(), footer, d.width, d.height)
	}
	if d.showDisk {
		var inner strings.Builder
		if d.diskLoading {
			inner.WriteString(styleMuted.Render("querying\u2026") + "\n")
		} else if d.diskErr != "" {
			inner.WriteString(styleErr.Render(d.diskErr) + "\n")
		} else if len(d.disk) == 0 {
			inner.WriteString(styleMuted.Render("no data") + "\n")
		} else {
			inner.WriteString(styleMuted.Render(fmt.Sprintf("%-12s %8s %8s %12s %12s", "TYPE", "TOTAL", "ACTIVE", "SIZE", "RECLAIMABLE")) + "\n")
			for _, o := range d.disk {
				inner.WriteString(fmt.Sprintf("%-12s %8s %8s %12s %12s\n", o.Type, o.Total, o.Active, o.Size, o.Reclaimable))
			}
		}
		b.WriteString(panel(styleTitle.Render("disk usage"), inner.String(), d.width) + "\n")
		return pinFooter(b.String(), helpLine("p", "prune images", "P", "prune system", "f", "refresh", "esc", "close"), d.width, d.height)
	}
	if len(d.containers) == 0 && d.errMsg == "" && !d.disconnected {
		b.WriteString(styleMuted.Render("no containers found on this host") + "\n")
	}
	b.WriteString(d.tbl.View() + "\n")
	var footer string
	if d.disconnected {
		footer = helpLine("c", "reconnect", "esc", "back")
	} else {
		footer = helpLine("↑/↓ j/k", "nav", "g/G", "top/bot", "enter", "logs", "d", "detail", "space", "fold", "s", "start", "x", "stop", "r", "restart", "D", "rm", "e", "shell", "i", "disk", "p", "perf", "t", "tag", "T", "untag", "a", "errors", "f", "refresh", "esc", "back")
	}
	return pinFooter(b.String(), footer, d.width, d.height)
}
