package app

import (
	"context"
	"fmt"
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

type Dashboard struct {
	host models.Host
	dock *docker.Executor

	tbl          table.Model
	containers   []models.Container
	stats        map[string]models.ContainerStats // by container name
	hstats       models.HostStats
	hasHostStats bool

	errMsg       string
	toast        string
	toastIsErr   bool
	confirm      string // verb pending confirmation: start/stop/restart
	disconnected bool

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

func NewDashboard(host models.Host, dock *docker.Executor, w, h int) *Dashboard {
	d := &Dashboard{host: host, dock: dock, stats: map[string]models.ContainerStats{}, width: w, height: h}
	t := table.New(table.WithColumns(tableColumns), table.WithWidth(max(w-2, 20)))
	d.tbl = t
	return d
}

func (d *Dashboard) SetSize(w, h int) {
	d.width, d.height = w, h
	d.tbl.SetWidth(max(w-2, 20))
	d.tbl.SetHeight(max(h-10, 3))
}

func (d *Dashboard) GoBack() bool { return d.back }

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
	if idx < 0 || idx >= len(d.containers) {
		return models.Container{}, false
	}
	return d.containers[idx], true
}

func (d *Dashboard) fetchNow() tea.Cmd {
	dock := d.dock
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
		defer cancel()
		containers, err := dock.ListContainers(ctx)
		if err != nil {
			return statsLoadedMsg{err: err}
		}
		cstats, _ := dock.ContainerStats(ctx)
		hstats, herr := dock.HostStats(ctx)
		return statsLoadedMsg{containers: containers, cstats: cstats, hstats: hstats, err: herr}
	}
}

func (d *Dashboard) Update(msg tea.Msg) (*Dashboard, tea.Cmd) {
	switch msg := msg.(type) {
	case statsTickMsg:
		return d, nil // root re-schedules the fetch chain

	case statsLoadedMsg:
		if msg.err != nil && msg.containers == nil {
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
		d.errMsg = ""
		d.rebuild()
		return d, nil

	case actionDoneMsg:
		if msg.err != nil {
			d.toast = msg.verb + " " + msg.name + " failed: " + msg.err.Error()
			d.toastIsErr = true
		} else {
			d.toast = msg.verb + " " + msg.name + " ok"
			d.toastIsErr = false
		}
		d.errMsg = ""
		return d, d.fetchNow()

	case tea.KeyMsg:
		if d.confirm != "" {
			switch msg.String() {
			case "y", "Y":
				verb := d.confirm
				d.confirm = ""
				c, ok := d.selected()
				if !ok {
					return d, nil
				}
				dock := d.dock
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
					}
					return actionDoneMsg{verb: verb, name: c.ShortName(), err: err}
				}
			case "n", "N", "esc":
				d.confirm = ""
				return d, nil
			default:
				d.confirm = ""
			}
		}
		switch msg.String() {
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
	rows := make([]table.Row, 0, len(d.containers))
	for _, c := range d.containers {
		cpu, mem, memUsage := "-", "-", "-"
		if s, ok := d.stats[c.ShortName()]; ok {
			cpu, mem, memUsage = s.CPUPerc, s.MemPerc, s.MemUsage
		}
		rows = append(rows, table.Row{c.ShortName(), c.Image, c.State, c.Status, cpu, mem, memUsage})
	}
	d.tbl.SetRows(rows)
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
		b.WriteString(styleErr.Render("connection lost — press esc to go back") + "\n")
	}
	if d.confirm != "" {
		if c, ok := d.selected(); ok {
			b.WriteString(styleWarn.Render(fmt.Sprintf("%s %s? [y/N]", d.confirm, c.ShortName())) + "\n")
		}
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
	if len(d.containers) == 0 && d.errMsg == "" && !d.disconnected {
		b.WriteString(styleMuted.Render("no containers found on this host") + "\n")
	}
	b.WriteString(d.tbl.View() + "\n")
	b.WriteString(helpLine("enter", "logs", "s", "start", "x", "stop", "r", "restart", "f", "refresh", "esc", "back") + "\n")
	return b.String()
}
