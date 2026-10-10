package app

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"

	"vpstui/internal/models"
)

func TestAllocCols(t *testing.T) {
	cases := []struct {
		total     int
		wantName  int
		wantImage int
	}{
		{20, 12, 10},  // below floors
		{72, 12, 10},  // narrow: floors apply
		{96, 16, 12},  // slack 28 → 3:2
		{140, 39, 27}, // wide
		{200, 75, 51}, // very wide
	}
	for _, tc := range cases {
		n, i := allocCols(tc.total)
		if n != tc.wantName || i != tc.wantImage {
			t.Errorf("allocCols(%d) = (%d, %d), want (%d, %d)", tc.total, n, i, tc.wantName, tc.wantImage)
		}
		if tc.total >= 90 && n+i < tc.total-staticColsW(tc.total) {
			t.Errorf("allocCols(%d): unused slack, sum=%d", tc.total, n+i)
		}
	}
}

func TestColsFor(t *testing.T) {
	narrow := colsFor(60)
	if narrow[3].width != 12 || narrow[6].width != 12 {
		t.Errorf("narrow cols: status=%d usage=%d, want 12/12", narrow[3].width, narrow[6].width)
	}
	mid := colsFor(96)
	if mid[3].width != 18 || mid[6].width != 12 {
		t.Errorf("mid cols: status=%d usage=%d, want 18/12", mid[3].width, mid[6].width)
	}
	wide := colsFor(140)
	if wide[3].width != 18 || wide[6].width != 18 {
		t.Errorf("wide cols: status=%d usage=%d, want 18/18", wide[3].width, wide[6].width)
	}
	if wide[2].width != 12 || wide[4].width != 7 {
		t.Error("state/cpu columns must stay fixed")
	}
	base := colsFor(100)
	if got := base[0].width + base[1].width; got != 100-staticColsW(100) {
		t.Errorf("colsFor(100) name+image=%d want %d", got, 100-staticColsW(100))
	}
}

func TestDashboardPinsFooter(t *testing.T) {
	d := NewDashboard(host("h"), nil, 100, 30, 1)
	d.containers = []models.Container{{ID: "c1", Names: "c1", State: "running", Status: "Up 1 hour"}}
	d.rebuild()
	v := d.View()
	if h := lipgloss.Height(v); h != 30 {
		t.Errorf("dashboard height = %d, want 30 (pinned frame)", h)
	}
	last := strings.Split(v, "\n")[h(v)-1]
	if !strings.Contains(last, "esc") || !strings.Contains(last, "back") {
		t.Errorf("footer not on last row: %q", last)
	}
	// disk panel pins its footer too
	d.showDisk = true
	v = d.View()
	if h := lipgloss.Height(v); h != 30 {
		t.Errorf("disk panel height = %d, want 30", h)
	}
	if last := lastLine(v); !strings.Contains(last, "prune") {
		t.Errorf("disk footer not pinned: %q", last)
	}
}

func h(s string) int { return lipgloss.Height(s) }

func lastLine(s string) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	return lines[len(lines)-1]
}
