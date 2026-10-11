// Package perf turns docker's instantaneous CPU and memory cells into the
// numbers a peak or a hog actually needs: a windowed history per container, and
// one canonical parse of the pre-formatted strings docker hands out.
//
// It exists as its own package for the same reason as internal/errsample: the
// terminal and the published JSON must rank containers identically, so the
// sample strings, the thresholds and the ordering live here once rather than in
// two comparators that can drift apart.
package perf

import (
	"math"
	"strconv"
	"strings"
	"time"

	"vpstui/internal/models"
)

const (
	// Cap is how many samples one container keeps, and Horizon how far back they
	// reach. The active dashboard samples every 2 s (so Cap binds) and a
	// backgrounded host every 10 s (so Horizon does), which keeps both paths
	// describing the same span of time.
	Cap     = 900
	Horizon = 30 * time.Minute
	// Window is the span peaks, means and spikes are computed over, and the one
	// every header names.
	Window = 20 * time.Minute
	// MaxPoints bounds one container's published series, so a publish stays
	// small however long vpstui has been running.
	MaxPoints = 80
)

// Sample is one reading of one container.
type Sample struct {
	At       time.Time
	CPU      float64 // percent of a single core, as docker reports it
	MemPct   float64
	MemBytes int64
}

// Buffer keeps timestamped samples per container id. It is not safe for
// concurrent use: like every other Dashboard field it is touched only from the
// Update goroutine, which is what lets the published snapshot need no lock.
type Buffer struct {
	limit  int
	horiz  time.Duration
	series map[string][]Sample
}

func New(limit int, horiz time.Duration) *Buffer {
	if limit <= 0 {
		limit = Cap
	}
	if horiz <= 0 {
		horiz = Horizon
	}
	return &Buffer{limit: limit, horiz: horiz, series: map[string][]Sample{}}
}

// Record appends one reading for every container whose cells parse.
//
// A container whose CPU cell is "-" or "--" is skipped rather than recorded as
// zero: docker emits that when it has no stats for the container, and a run of
// zeros would drag its mean down and read as an idle container that was really
// just unmeasured.
func (b *Buffer) Record(at time.Time, stats []models.ContainerStats) {
	for _, s := range stats {
		cpu, ok := Percent(s.CPUPerc)
		if !ok || s.ID == "" {
			continue
		}
		mem, _ := MemUsed(s.MemUsage)
		pct, _ := Percent(s.MemPerc)
		b.series[s.ID] = append(b.series[s.ID], Sample{At: at, CPU: cpu, MemPct: pct, MemBytes: mem})
		b.trim(s.ID, at)
	}
}

// trim enforces both limits: the count, and the age of the oldest reading.
func (b *Buffer) trim(id string, at time.Time) {
	s := b.series[id]
	if len(s) <= b.limit && !at.After(s[0].At.Add(b.horiz)) {
		return
	}
	i := 0
	for i < len(s) && (len(s)-i > b.limit || at.After(s[i].At.Add(b.horiz))) {
		i++
	}
	// Shifting in place keeps a full ring from allocating every 2 s. The cost is
	// that a slice handed out by Series is only valid until the next Record,
	// which is why callers copy it into their own form straight away.
	b.series[id] = s[:len(s)-i]
	copy(s, s[i:])
}

// Series returns the readings for id inside the window ending at now, oldest
// first, or nil for a container that never reported — which callers must render
// as "no data", not as zero. The result aliases the ring, so use it before the
// next Record.
func (b *Buffer) Series(now time.Time, id string, window time.Duration) []Sample {
	all := b.series[id]
	from := now.Add(-window)
	// Samples are appended in time order, so the window is a suffix; scanning
	// from the front would walk the whole ring on every repaint.
	for i, s := range all {
		if !s.At.Before(from) {
			return all[i:]
		}
	}
	return nil
}

// Total sums every kept sample, which is what the perf header reports as the
// host's sample count.
func (b *Buffer) Total() int {
	n := 0
	for _, s := range b.series {
		n += len(s)
	}
	return n
}

// Forget drops containers that are no longer in the list, so a recreated
// container cannot inherit a dead one's history.
func (b *Buffer) Forget(keep map[string]bool) {
	for id := range b.series {
		if !keep[id] {
			delete(b.series, id)
		}
	}
}

// Percent converts docker's pre-formatted CPU/MEM percent cells ("12.34%", "-",
// "--") into a number. Docker emits "-" before stats are available, so
// unparseable input is normal, not an error.
func Percent(s string) (float64, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, false
	}
	v, err := strconv.ParseFloat(strings.TrimSuffix(s, "%"), 64)
	// ParseFloat also accepts NaN/Inf, which would poison a bar width or a
	// ranking.
	if err != nil || v < 0 || math.IsNaN(v) || math.IsInf(v, 0) {
		return 0, false
	}
	return v, true
}

// MemUsed takes the used half of docker's "82.57MiB / 23.31GiB" cell and returns
// it in bytes. The limit half is deliberately ignored: the host's own total is
// the denominator the screen uses, and docker's limit cell reports a quota, not
// the machine.
func MemUsed(s string) (int64, bool) {
	left, _, _ := strings.Cut(s, "/")
	left = strings.TrimSpace(left)
	if left == "" {
		return 0, false
	}
	n, unit, ok := splitSize(left)
	if !ok {
		return 0, false
	}
	return int64(n * unit), true
}

// Ordered longest-name first, so "MiB" is never read as "B" with an orphan
// prefix and the bare "B" stays the fallback.
var sizeUnits = []struct {
	name  string
	multi float64
}{
	{"KiB", 1 << 10}, {"MiB", 1 << 20}, {"GiB", 1 << 30}, {"TiB", 1 << 40},
	{"kB", 1e3}, {"MB", 1e6}, {"GB", 1e9}, {"TB", 1e12}, {"B", 1},
}

func splitSize(s string) (float64, float64, bool) {
	for _, u := range sizeUnits {
		rest, found := strings.CutSuffix(s, u.name)
		if !found {
			continue
		}
		v, err := strconv.ParseFloat(strings.TrimSpace(rest), 64)
		if err != nil || v < 0 || math.IsNaN(v) || math.IsInf(v, 0) {
			return 0, 0, false
		}
		return v, u.multi, true
	}
	return 0, 0, false
}

// Bytes renders a byte count the way docker renders one, so a peak we computed
// sits in the same column as a reading docker formatted without looking
// invented.
func Bytes(n int64) string {
	if n < 0 {
		return "-"
	}
	v := float64(n)
	for _, u := range []struct {
		name  string
		multi float64
	}{{"TiB", 1 << 40}, {"GiB", 1 << 30}, {"MiB", 1 << 20}, {"KiB", 1 << 10}} {
		if v >= u.multi {
			return trimZero(v/u.multi) + u.name
		}
	}
	return strconv.FormatInt(n, 10) + "B"
}

func trimZero(v float64) string {
	s := strconv.FormatFloat(v, 'f', 2, 64)
	return strings.TrimSuffix(strings.TrimRight(s, "0"), ".")
}

// Round keeps a published number byte-stable across republishes of the same
// reading, which is what lets the API payload be compared for equality.
func Round(v float64) float64 { return math.Round(v*100) / 100 }
