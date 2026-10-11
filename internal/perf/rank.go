package perf

import (
	"sort"
	"time"
)

// The thresholds a "hog" and a "spike" have to be argued about, so they are
// named here and quoted by the README rather than buried in a comparison.
const (
	// HogShareCPU is the fraction of the machine's total cores one container has
	// to be eating to be called a hog. Measured against the box, not against a
	// core, because 400% of one core on a 12-core host is not news.
	HogShareCPU = 0.10
	// HogShareMem is the same idea against physical RAM.
	HogShareMem = 0.10
	// SpikeRatio is how far above its own window mean the current reading has to
	// sit before the change is interesting.
	SpikeRatio = 3.0
	// SpikeFloor is the absolute CPU percentage below which a ratio is ignored:
	// 0.1% to 1.1% is eleven times nothing.
	SpikeFloor = 10.0
	// MinSamples is how many readings a container needs before its own mean
	// means anything, so a container vpstui just met cannot spike against an
	// empty baseline.
	MinSamples = 8
)

// Flag names, shared by the terminal badges and the published JSON.
const (
	FlagHog   = "hog"
	FlagSpike = "spike"
)

// Summary is what one container's window adds up to.
type Summary struct {
	Last     float64 // current CPU %, newest reading
	Peak     float64 // highest CPU % in the window
	Mean     float64 // average CPU % over the window
	MemBytes int64   // newest memory reading
	MemPeak  int64   // highest memory in the window
	MemPct   float64 // docker's own memory percentage
	Samples  int
}

// Summarize reduces a window of samples. An empty window yields a zero Summary
// with Samples 0, which callers render as "no data".
func Summarize(s []Sample) Summary {
	var out Summary
	if len(s) == 0 {
		return out
	}
	var cpuSum float64
	for _, x := range s {
		if x.CPU > out.Peak {
			out.Peak = x.CPU
		}
		if x.MemBytes > out.MemPeak {
			out.MemPeak = x.MemBytes
		}
		cpuSum += x.CPU
	}
	last := s[len(s)-1]
	out.Last, out.MemBytes, out.MemPct, out.Samples = last.CPU, last.MemBytes, last.MemPct, len(s)
	out.Mean = cpuSum / float64(len(s))
	return out
}

// Subject is one container to rank. Names come from the caller because the same
// id is labelled by a tag in the terminal and by a docker name in the JSON.
type Subject struct {
	ID   string
	Name string
}

// Rank is one container's verdict: its window, how much of the box it holds, and
// the flags that make it worth looking at.
type Rank struct {
	ID, Name string
	Sum      Summary
	CPUShare float64 // of the machine's total cores, 0..1
	MemShare float64 // of physical RAM, 0..1
	Spike    float64 // Last / Mean
	Flags    []string
}

// RankAll summarises every subject and orders the result by how much of the
// machine each one holds, most first. Ties break on id, so two hosts with
// identical readings publish the same order rather than the order their SSH
// round trips happened to finish in.
//
// nproc and hostMemMB are the denominators; either may be 0 on a host where the
// /proc probe failed, in which case the shares stay 0 and only the spike rule
// can flag a row.
func RankAll(now time.Time, b *Buffer, subs []Subject, window time.Duration, nproc, hostMemMB int) []Rank {
	out := make([]Rank, 0, len(subs))
	for _, s := range subs {
		r := Rank{ID: s.ID, Name: s.Name, Sum: Summarize(b.Series(now, s.ID, window))}
		if r.Sum.Samples == 0 {
			out = append(out, r)
			continue
		}
		if nproc > 0 {
			r.CPUShare = r.Sum.Last / 100 / float64(nproc)
		}
		if hostMemMB > 0 {
			r.MemShare = float64(r.Sum.MemBytes) / float64(hostMemMB<<20)
		}
		if r.Sum.Mean > 0 {
			r.Spike = r.Sum.Last / r.Sum.Mean
		}
		if r.CPUShare >= HogShareCPU || r.MemShare >= HogShareMem {
			r.Flags = append(r.Flags, FlagHog)
		}
		if r.Sum.Samples >= MinSamples && r.Sum.Last >= SpikeFloor && r.Spike >= SpikeRatio {
			r.Flags = append(r.Flags, FlagSpike)
		}
		out = append(out, r)
	}
	sort.SliceStable(out, func(i, j int) bool {
		x, y := shareOf(out[i]), shareOf(out[j])
		if x != y {
			return x > y
		}
		return out[i].ID < out[j].ID
	})
	return out
}

func shareOf(r Rank) float64 {
	if r.CPUShare > r.MemShare {
		return r.CPUShare
	}
	return r.MemShare
}

// Flagged reports whether a rank carries one of the flag constants.
func (r Rank) Flagged(want string) bool {
	for _, f := range r.Flags {
		if f == want {
			return true
		}
	}
	return false
}

// Points reduces a window to at most max CPU values, oldest first, always
// keeping the newest reading so a series never ends before the number beside it.
// Values are rounded, which is what lets an unchanged window publish identical
// bytes.
func Points(s []Sample, max int) []float64 {
	if len(s) == 0 || max <= 0 {
		return nil
	}
	if len(s) == 1 || max == 1 {
		return []float64{Round(s[len(s)-1].CPU)}
	}
	if len(s) <= max {
		out := make([]float64, 0, len(s))
		for _, x := range s {
			out = append(out, Round(x.CPU))
		}
		return out
	}
	out := make([]float64, 0, max)
	step := float64(len(s)-1) / float64(max-1)
	prev := -1
	for j := 0; j < max; j++ {
		i := int(float64(j)*step + 0.5)
		if i == prev {
			continue
		}
		out = append(out, Round(s[i].CPU))
		prev = i
	}
	return out
}

// sparkLevels is the eight-block ramp, shortest first.
const sparkLevels = "\u2581\u2582\u2583\u2584\u2585\u2586\u2587\u2588" // ▁▂▃▄▅▆▇█

// Spark draws a series as a block ramp scaled to its own maximum, so the shape
// of a spike survives a narrow terminal. An all-zero series still draws, because
// "flat at zero" is an answer.
func Spark(vals []float64, width int) string {
	if len(vals) == 0 || width <= 0 {
		return ""
	}
	if len(vals) > width {
		// Re-reduce to the cell budget rather than clipping, which would drop the
		// newest readings — the interesting end.
		step := float64(len(vals)-1) / float64(width-1)
		small := make([]float64, 0, width)
		for j := 0; j < width; j++ {
			small = append(small, vals[int(float64(j)*step+0.5)])
		}
		vals = small
	}
	max := vals[0]
	for _, v := range vals {
		if v > max {
			max = v
		}
	}
	ramp := []rune(sparkLevels)
	out := make([]rune, 0, len(vals))
	for _, v := range vals {
		if max <= 0 {
			out = append(out, ramp[0])
			continue
		}
		i := int(v / max * float64(len(ramp)-1))
		if i > len(ramp)-1 {
			i = len(ramp) - 1
		}
		out = append(out, ramp[i])
	}
	return string(out)
}
