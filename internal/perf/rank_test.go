package perf

import (
	"strings"
	"testing"
	"time"

	"vpstui/internal/models"
)

// TestRankFlags is the whole flag matrix in one table, because "hog" is two
// rules and "spike" is three conditions, and a reader has to be able to see
// which row fails which.
func TestRankFlags(t *testing.T) {
	const (
		nproc     = 12
		hostMemMB = 1024
		// 1 MiB of a 1 GiB box: 0.1 %, nowhere near hogging.
		small = "1MiB / 2GiB"
		// 200 MiB of a 1 GiB box: 19.5 %, a memory hog.
		big = "200MiB / 1GiB"
	)
	now := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	start := now.Add(-10 * time.Second)

	cases := []struct {
		name      string
		cpu       []float64
		mem       string
		wantHog   bool
		wantSpike bool
	}{
		// 240 % of one core is 2 cores, or 16.7 % of a 12-core box: a CPU hog on
		// share alone, and steady, so not a spike.
		{
			name:    "hog by cpu",
			cpu:     flat(11, 240),
			mem:     small,
			wantHog: true,
		},
		{
			name:    "hog by mem",
			cpu:     flat(11, 5),
			mem:     big,
			wantHog: true,
		},
		{
			name:    "hog by both",
			cpu:     flat(11, 240),
			mem:     big,
			wantHog: true,
		},
		{
			// 3 % then 20 %: 6.7x its own mean, above the floor, with enough
			// samples behind the baseline.
			name:      "spike",
			cpu:       append(flat(10, 3), 20),
			mem:       small,
			wantSpike: true,
		},
		{
			name: "quiet",
			cpu:  flat(11, 0.1),
			mem:  small,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			b := New(Cap, Horizon)
			at := start
			for i, v := range c.cpu {
				b.Record(at.Add(time.Duration(i)*time.Second), []models.ContainerStats{
					stats("subj", formatPct(v), c.mem),
				})
			}
			rows := RankAll(now, b, []Subject{{"subj", "subj"}}, Window, nproc, hostMemMB)
			if len(rows) != 1 {
				t.Fatalf("rows = %d; want 1", len(rows))
			}
			got := rows[0]
			if got.Flagged(FlagHog) != c.wantHog {
				t.Errorf("hog = %v; want %v (cpuShare %v, memShare %v)",
					got.Flagged(FlagHog), c.wantHog, got.CPUShare, got.MemShare)
			}
			if got.Flagged(FlagSpike) != c.wantSpike {
				t.Errorf("spike = %v; want %v (last %v, mean %v, ratio %v, samples %d)",
					got.Flagged(FlagSpike), c.wantSpike, got.Sum.Last, got.Sum.Mean, got.Spike, got.Sum.Samples)
			}
		})
	}
}

// TestRankAllOrdersByShare pins the ordering rule: the row holding the most of
// the machine comes first whether it got there by CPU or by memory.
func TestRankAllOrdersByShare(t *testing.T) {
	b := New(Cap, Horizon)
	now := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	start := now.Add(-10 * time.Second)
	feed(b, "cpu", start, flat(11, 240), "1MiB / 2GiB") // share 0.167
	feed(b, "mem", start, flat(11, 1), "400MiB / 1GiB") // share 0.39
	feed(b, "mid", start, flat(11, 60), "1MiB / 2GiB")  // share 0.05
	feed(b, "none", start, flat(11, 1), "1MiB / 2GiB")  // share 0.001

	got := ids(RankAll(now, b, []Subject{
		{"none", "none"}, {"mid", "mid"}, {"cpu", "cpu"}, {"mem", "mem"},
	}, Window, 12, 1024))
	want := "mem,cpu,mid,none"
	if strings.Join(got, ",") != want {
		t.Errorf("order = %v; want %q", got, want)
	}
}

// A container with no samples is still ranked, last: the screen has to show the
// row the table shows, and dropping it would hide a container that stopped
// reporting.
func TestRankAllIncludesSilentSubjects(t *testing.T) {
	b := New(Cap, Horizon)
	now := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	feed(b, "loud", now.Add(-10*time.Second), flat(11, 5), "1MiB / 2GiB")

	rows := RankAll(now, b, []Subject{{"silent", "silent"}, {"loud", "loud"}}, Window, 12, 1024)
	if len(rows) != 2 {
		t.Fatalf("rows = %d; want 2", len(rows))
	}
	if rows[0].ID != "loud" || rows[1].ID != "silent" {
		t.Errorf("order = %v, %v", rows[0].ID, rows[1].ID)
	}
	if rows[1].Sum.Samples != 0 {
		t.Errorf("silent subject has %d samples", rows[1].Sum.Samples)
	}
	if rows[1].Flagged(FlagHog) || rows[1].Flagged(FlagSpike) {
		t.Error("a container with no data was flagged")
	}
}
