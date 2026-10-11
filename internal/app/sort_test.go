package app

import (
	"strings"
	"testing"

	"vpstui/internal/models"
)

// sortDash builds a dashboard whose container list is out of name order, with
// mixed states and stats whose CPU order differs from the name order.
func sortDash(t *testing.T) *Dashboard {
	t.Helper()
	d := NewDashboard(host("h"), nil, 80, 24, 1)
	d.containers = []models.Container{
		{ID: "c1", Names: "web", State: "running", Labels: "com.docker.compose.project=webproj"},
		{ID: "c2", Names: "api", State: "exited"},
		{ID: "c3", Names: "db", State: "exited"},
	}
	d.stats = map[string]models.ContainerStats{
		"web": {Name: "web", CPUPerc: "1.00%", MemPerc: "2.00%"},
		"db":  {Name: "db", CPUPerc: "9.00%", MemPerc: "5.00%"},
		// "api" deliberately has no stats row: it must sort last.
	}
	d.rebuild()
	return d
}

func rowIndices(d *Dashboard) []int {
	out := make([]int, 0, len(d.rowCIdx))
	for _, v := range d.rowCIdx {
		if v >= 0 {
			out = append(out, v)
		}
	}
	return out
}

func TestDashboardSortCycles(t *testing.T) {
	d := sortDash(t)

	// Default is the compose grouping: one header row for webproj.
	if d.sortMode != sortGrouped {
		t.Fatalf("default sortMode = %d, want %d", d.sortMode, sortGrouped)
	}
	if got := rowIndices(d); len(got) != 3 {
		t.Fatalf("grouped rows = %v, want 3 container rows", d.rowCIdx)
	}
	hasHeader, hasProj := false, false
	for i, v := range d.rowCIdx {
		if v == -1 && strings.Contains(d.rowProj[i], "webproj") {
			hasHeader, hasProj = true, true
		}
	}
	if !hasHeader || !hasProj {
		t.Fatalf("grouped mode missing compose header: rowCIdx=%v rowProj=%q", d.rowCIdx, d.rowProj)
	}

	// > cycles grouped → name: flat order, alphabetical, no header.
	_, cmd := d.Update(key(">"))
	if cmd != nil {
		t.Fatal("> must not schedule work")
	}
	if d.toast != "sort: name" {
		t.Fatalf("toast = %q, want sort: name", d.toast)
	}
	if got := rowIndices(d); len(got) != 3 || d.rowCIdx[0] != 1 || d.rowCIdx[1] != 2 || d.rowCIdx[2] != 0 {
		t.Fatalf("name order rows = %v, want api,db,web", d.rowCIdx)
	}
	for _, p := range d.rowProj {
		if p != "" {
			t.Fatalf("flat mode kept a project %q", p)
		}
	}

	// > again → state: the running container first.
	d.Update(key(">"))
	if d.toast != "sort: state" {
		t.Fatalf("toast = %q, want sort: state", d.toast)
	}
	if got := rowIndices(d); len(got) != 3 || d.rowCIdx[0] != 0 {
		t.Fatalf("state order rows = %v, want running (web) first", d.rowCIdx)
	}

	// > again → cpu%: highest first, no-stats container last.
	d.Update(key(">"))
	if d.toast != "sort: cpu%" {
		t.Fatalf("toast = %q, want sort: cpu%%", d.toast)
	}
	if got := rowIndices(d); len(got) != 3 || d.rowCIdx[0] != 2 || d.rowCIdx[1] != 0 || d.rowCIdx[2] != 1 {
		t.Fatalf("cpu order rows = %v, want db,web,api", d.rowCIdx)
	}

	// > again → mem%: db 5% > web 2% > api none.
	d.Update(key(">"))
	if d.toast != "sort: mem%" {
		t.Fatalf("toast = %q, want sort: mem%%", d.toast)
	}
	if got := rowIndices(d); len(got) != 3 || d.rowCIdx[0] != 2 || d.rowCIdx[1] != 0 || d.rowCIdx[2] != 1 {
		t.Fatalf("mem order rows = %v, want db,web,api", d.rowCIdx)
	}

	// < reverses one step per press and the grouped mode restores headers.
	for range sortCount - 1 {
		d.Update(key("<"))
	}
	if d.sortMode != sortGrouped {
		t.Fatalf("after cycling back sortMode = %d, want %d", d.sortMode, sortGrouped)
	}
	restored := false
	for i, v := range d.rowCIdx {
		if v == -1 && strings.Contains(d.rowProj[i], "webproj") {
			restored = true
		}
	}
	if !restored {
		t.Fatalf("grouped mode did not restore the compose header: rowCIdx=%v rowProj=%q", d.rowCIdx, d.rowProj)
	}
}
