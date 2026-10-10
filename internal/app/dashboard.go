package app

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/table"
	tea "github.com/charmbracelet/bubbletea"

	"vpstui/internal/docker"
	"vpstui/internal/models"
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

	tbl          table.Model
	containers   []models.Container
	stats        map[string]models.ContainerStats // by container name
	hstats       models.HostStats
	hasHostStats bool

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

	width, height int
	back          bool
	logReq        *containerReq
}

var tableColumns = []table.Column{
	{Title: "Name", Width: 16},
	{Title: "Image", Width: 18},
	{Title: "State", Width: 9},
	{Title: "Status", Width: 18},
	{Title: "CPU %", Width: 7},
	{Title: "MEM %", Width: 7},
	{Title: "MEM USAGE", Width: 18},
}

func NewDashboard(host models.Host, dock *docker.Executor, w, h int, gen uint64) *Dashboard {
	d := &Dashboard{host: host, dock: dock, hostID: host.ID, gen: gen, stats: map[string]models.ContainerStats{}, collapsed: map[string]bool{}, width: w, height: h}
	t := table.New(table.WithColumns(tableColumns), table.WithWidth(max(w-2, 20)))
	t.Focus()
	d.tbl = t
	return d
}

func (d *Dashboard) tag(hostID string, gen uint64) bool {
	return d.hostID == hostID && d.gen == gen
}

func (d *Dashboard) SetSize(w, h int) {
	d.width, d.height = w, h
	d.tbl.SetWidth(max(w-2, 20))
	d.tbl.SetHeight(max(h-10, 3))
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

	case diskLoadedMsg:
		d.diskLoading = false
		if msg.err != nil {
			d.diskErr = msg.err.Error()
		} else {
			d.disk = msg.objs
			d.diskErr = ""
		}
		return d, nil

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
			d.showDisk = true
			d.diskLoading = true
			d.diskErr = ""
			return d, d.fetchDisk()
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

	rows := make([]table.Row, 0, len(d.containers))
	d.rowCIdx = d.rowCIdx[:0]
	d.rowProj = d.rowProj[:0]
	blank := func() table.Row { return table.Row{"", "", "", "", "", "", ""} }

	for _, p := range order {
		idxs := byProj[p]
		if p == "" {
			for _, i := range idxs {
				rows = append(rows, d.containerRow(d.containers[i]))
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
		d.rowCIdx = append(d.rowCIdx, -1)
		d.rowProj = append(d.rowProj, p)
		if folded {
			continue
		}
		for _, i := range idxs {
			rows = append(rows, d.containerRow(d.containers[i]))
			d.rowCIdx = append(d.rowCIdx, i)
			d.rowProj = append(d.rowProj, p)
		}
	}

	d.tbl.SetRows(rows)
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

func (d *Dashboard) containerRow(c models.Container) table.Row {
	cpu, mem, memUsage := "-", "-", "-"
	if s, ok := d.stats[c.ShortName()]; ok {
		cpu, mem, memUsage = s.CPUPerc, s.MemPerc, s.MemUsage
	}
	return table.Row{c.ShortName(), c.Image, c.State, c.Status, cpu, mem, memUsage}
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
		b.WriteString(styleTitle.Render("detail — "+d.detail.name) + "\n")
		if d.detail.loading {
			b.WriteString(styleMuted.Render("inspecting…") + "\n")
		} else if d.detail.isErr {
			b.WriteString(styleErr.Render(d.detail.text) + "\n")
		} else {
			b.WriteString(d.detail.text)
		}
		b.WriteString(helpLine("esc", "close", "d", "close", "f", "refresh") + "\n")
		return b.String()
	}
	if d.showDisk {
		b.WriteString(styleTitle.Render("disk usage") + "\n")
		if d.diskLoading {
			b.WriteString(styleMuted.Render("querying\u2026") + "\n")
		} else if d.diskErr != "" {
			b.WriteString(styleErr.Render(d.diskErr) + "\n")
		} else if len(d.disk) == 0 {
			b.WriteString(styleMuted.Render("no data") + "\n")
		} else {
			b.WriteString(styleMuted.Render(fmt.Sprintf("%-12s %8s %8s %12s %12s", "TYPE", "TOTAL", "ACTIVE", "SIZE", "RECLAIMABLE")) + "\n")
			for _, o := range d.disk {
				b.WriteString(fmt.Sprintf("%-12s %8s %8s %12s %12s\n", o.Type, o.Total, o.Active, o.Size, o.Reclaimable))
			}
		}
		b.WriteString(helpLine("p", "prune images", "P", "prune system", "f", "refresh", "esc", "close") + "\n")
		return b.String()
	}
	if len(d.containers) == 0 && d.errMsg == "" && !d.disconnected {
		b.WriteString(styleMuted.Render("no containers found on this host") + "\n")
	}
	b.WriteString(d.tbl.View() + "\n")
	if d.disconnected {
		b.WriteString(helpLine("c", "reconnect", "esc", "back") + "\n")
	} else {
		b.WriteString(helpLine("enter", "logs", "d", "detail", "space", "fold", "s", "start", "x", "stop", "r", "restart", "D", "rm", "i", "disk", "f", "refresh", "esc", "back") + "\n")
	}
	return b.String()
}
