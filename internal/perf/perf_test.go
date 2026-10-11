package perf

import (
	"strconv"
	"strings"
	"testing"
	"time"

	"vpstui/internal/models"
)

func TestPercent(t *testing.T) {
	cases := []struct {
		in   string
		want float64
		ok   bool
	}{
		{"12.34%", 12.34, true},
		{"0.00%", 0, true},
		{" 42% ", 42, true},
		{"42", 42, true},
		{"-", 0, false},
		{"--", 0, false},
		{"", 0, false},
		{"-5%", 0, false},  // a negative share would invert a ranking
		{"NaN%", 0, false}, // ParseFloat accepts these; a bar width must not
		{"+Inf%", 0, false},
		{"1.5GiB", 0, false},
	}
	for _, c := range cases {
		got, ok := Percent(c.in)
		if ok != c.ok || (c.ok && got != c.want) {
			t.Errorf("Percent(%q) = %v, %v; want %v, %v", c.in, got, ok, c.want, c.ok)
		}
	}
}

func TestMemUsed(t *testing.T) {
	cases := []struct {
		in   string
		want int64
		ok   bool
	}{
		{"1MiB / 2GiB", 1 << 20, true},
		{"1.5GiB / 16GiB", 1610612736, true},
		{"0B / 0B", 0, true},
		{"512KiB / 1GiB", 524288, true},
		{"12.3kB / 100kB", 12300, true},
		{"-", 0, false},
		{"", 0, false},
		{" / 1GiB", 0, false},
		{"nonsense", 0, false},
	}
	for _, c := range cases {
		got, ok := MemUsed(c.in)
		if ok != c.ok || got != c.want {
			t.Errorf("MemUsed(%q) = %d, %v; want %d, %v", c.in, got, ok, c.want, c.ok)
		}
	}
}

func TestBytes(t *testing.T) {
	cases := []struct {
		in   int64
		want string
	}{
		{-1, "-"},
		{0, "0B"},
		{1023, "1023B"},
		{1024, "1KiB"},
		{1610612736, "1.5GiB"},
		{1 << 40, "1TiB"},
	}
	for _, c := range cases {
		if got := Bytes(c.in); got != c.want {
			t.Errorf("Bytes(%d) = %q; want %q", c.in, got, c.want)
		}
	}
}

// stats is a shorthand for the shape docker emits.
func stats(id, cpu, mem string) models.ContainerStats {
	return models.ContainerStats{ID: id, Name: id, CPUPerc: cpu, MemUsage: mem, MemPerc: "1%"}
}

func TestRecordSkipsUnmeasured(t *testing.T) {
	b := New(Cap, Horizon)
	// "-" is what docker prints for a container it has no stats for; recording it
	// as zero would read as an idle container.
	b.Record(time.Now(), []models.ContainerStats{
		stats("aaa", "-", "-"),
		stats("bbb", "3.00%", "1MiB / 2GiB"),
		stats("", "3.00%", "1MiB / 2GiB"),
	})
	if got := b.Total(); got != 1 {
		t.Fatalf("Total() = %d; want 1", got)
	}
	if got := len(b.Series(time.Now(), "aaa", time.Hour)); got != 0 {
		t.Errorf("unmeasured container got %d samples; want 0", got)
	}
}

func TestSeriesWindowsByTime(t *testing.T) {
	base := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	b := New(Cap, Horizon)
	for i := 0; i < 5; i++ {
		b.Record(base.Add(time.Duration(i)*2*time.Second), []models.ContainerStats{
			stats("aaa", "1.00%", "1MiB / 2GiB"),
		})
	}
	// Window of 6 s ending at the last reading covers t=4s and t=8s and t=12s?
	// now = base+8s, from = base+2s, so samples at 2,4,6,8 s are in.
	got := b.Series(base.Add(8*time.Second), "aaa", 6*time.Second)
	if len(got) != 4 {
		t.Fatalf("Series len = %d; want 4", len(got))
	}
	if !got[0].At.Equal(base.Add(2*time.Second)) || !got[3].At.Equal(base.Add(8*time.Second)) {
		t.Errorf("window edges wrong: first %v last %v", got[0].At, got[3].At)
	}
	if s := b.Series(base, "missing", time.Hour); s != nil {
		t.Errorf("Series for unknown id = %v; want nil", s)
	}
}

func TestTrimHonoursCapAndHorizon(t *testing.T) {
	base := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)

	capped := New(3, time.Hour)
	for i := 0; i < 5; i++ {
		capped.Record(base.Add(time.Duration(i)*time.Second), []models.ContainerStats{
			stats("aaa", "1.00%", "1MiB / 2GiB"),
		})
	}
	got := capped.Series(base.Add(4*time.Second), "aaa", time.Hour)
	if len(got) != 3 {
		t.Fatalf("cap: kept %d samples; want 3", len(got))
	}

	aged := New(Cap, 10*time.Second)
	for i := 0; i < 5; i++ {
		aged.Record(base.Add(time.Duration(i)*5*time.Second), []models.ContainerStats{
			stats("aaa", "1.00%", "1MiB / 2GiB"),
		})
	}
	got = aged.Series(base.Add(20*time.Second), "aaa", time.Hour)
	// The horizon drops a reading only once it is strictly older than the
	// window, so t=10s survives exactly 10 s beside the newest sample.
	if len(got) != 3 {
		t.Fatalf("horizon: kept %d samples; want 3", len(got))
	}
}

func TestForgetDropsGoneContainers(t *testing.T) {
	b := New(Cap, Horizon)
	now := time.Now()
	b.Record(now, []models.ContainerStats{stats("aaa", "1%", "1MiB"), stats("bbb", "1%", "1MiB")})
	b.Forget(map[string]bool{"aaa": true})
	if b.Total() != 1 {
		t.Fatalf("Total after Forget = %d; want 1", b.Total())
	}
	if len(b.Series(now, "bbb", time.Hour)) != 0 {
		t.Error("Forgotten container kept its history")
	}
}

func TestSummarize(t *testing.T) {
	if got := Summarize(nil); got.Samples != 0 || got.Peak != 0 || got.Last != 0 {
		t.Errorf("Summarize(nil) = %+v; want zero", got)
	}
	base := time.Now()
	in := []Sample{
		{At: base, CPU: 10, MemBytes: 100, MemPct: 1},
		{At: base.Add(time.Second), CPU: 30, MemBytes: 900, MemPct: 5},
		{At: base.Add(2 * time.Second), CPU: 20, MemBytes: 400, MemPct: 3},
	}
	got := Summarize(in)
	if got.Last != 20 || got.Peak != 30 || got.MemPeak != 900 || got.MemBytes != 400 {
		t.Errorf("Summarize = %+v", got)
	}
	if got.Mean != 20 {
		t.Errorf("Mean = %v; want 20", got.Mean)
	}
	if got.Samples != 3 {
		t.Errorf("Samples = %d; want 3", got.Samples)
	}
}

// feed records cpu at 1 s intervals, newest last.
func feed(b *Buffer, id string, at time.Time, cpus []float64, mem string) {
	for i, c := range cpus {
		b.Record(at.Add(time.Duration(i)*time.Second), []models.ContainerStats{
			stats(id, formatPct(c), mem),
		})
	}
}

func formatPct(v float64) string {
	return strconv.FormatFloat(v, 'f', 2, 64) + "%"
}

func TestRankAllFlagsAndOrder(t *testing.T) {
	const hour = "1MiB / 2GiB"
	b := New(Cap, Horizon)
	now := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	start := now.Add(-11 * time.Second)

	// hog by memory: 200 MiB of a 1 GiB host is 19.5 %.
	feed(b, "hog", start, flat(11, 42), "200MiB / 1GiB")
	// spike: idle baseline, then a jump to 20 %.
	feed(b, "spike", start, append(flat(10, 3), 20), hour)
	// nothing: steady and small.
	feed(b, "quiet", start, flat(11, 0.1), hour)

	rows := RankAll(now, b, []Subject{{"hog", "hog"}, {"spike", "spike"}, {"quiet", "quiet"}},
		Window, 12, 1024)

	if len(rows) != 3 {
		t.Fatalf("rows = %d; want 3", len(rows))
	}
	if rows[0].ID != "hog" {
		t.Errorf("rank 1 = %q; want the memory hog", rows[0].ID)
	}
	if !rows[0].Flagged(FlagHog) {
		t.Errorf("hog not flagged: %+v", rows[0])
	}
	if rows[0].Flagged(FlagSpike) {
		t.Errorf("steady 42%% must not spike: %+v", rows[0])
	}
	if rows[1].ID != "spike" || !rows[1].Flagged(FlagSpike) {
		t.Errorf("rank 2 = %+v; want the spiking container", rows[1])
	}
	if rows[2].Flagged(FlagHog) || rows[2].Flagged(FlagSpike) {
		t.Errorf("quiet container was flagged: %+v", rows[2])
	}
	// 42 % of one core on a 12-core box is 3.5 % of the machine.
	if got := rows[0].CPUShare; got < 0.0349 || got > 0.0351 {
		t.Errorf("CPUShare = %v; want ~0.035", got)
	}
}

func TestRankAllWithoutDenominators(t *testing.T) {
	b := New(Cap, Horizon)
	now := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	feed(b, "aaa", now.Add(-11*time.Second), append(flat(10, 3), 20), "1MiB / 2GiB")

	rows := RankAll(now, b, []Subject{{"aaa", "aaa"}}, Window, 0, 0)
	if rows[0].CPUShare != 0 || rows[0].MemShare != 0 {
		t.Errorf("shares computed without a denominator: %+v", rows[0])
	}
	if !rows[0].Flagged(FlagSpike) {
		t.Error("a spike must still be found when the host probe failed")
	}
}

func TestSpikeGuards(t *testing.T) {
	b := New(Cap, Horizon)
	now := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	// Too few samples: a container vpstui has only just met.
	feed(b, "cold", now.Add(-2*time.Second), []float64{1, 1, 40}, "1MiB / 2GiB")
	// Enough samples, but the jump is under the absolute floor.
	feed(b, "tiny", now.Add(-11*time.Second), append(flat(10, 0.1), 1.1), "1MiB / 2GiB")
	rows := RankAll(now, b, []Subject{{"cold", "cold"}, {"tiny", "tiny"}}, Window, 12, 1024)
	for _, r := range rows {
		if r.Flagged(FlagSpike) {
			t.Errorf("%s flagged as a spike: %+v", r.ID, r)
		}
	}
}

func TestRankAllIsDeterministic(t *testing.T) {
	b := New(Cap, Horizon)
	now := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	for _, id := range []string{"zzz", "mmm", "aaa"} {
		feed(b, id, now.Add(-3*time.Second), flat(3, 5), "1MiB / 2GiB")
	}
	subs := []Subject{{"zzz", "z"}, {"aaa", "a"}, {"mmm", "m"}}
	first := ids(RankAll(now, b, subs, Window, 12, 1024))
	second := ids(RankAll(now, b, subs, Window, 12, 1024))
	if strings.Join(first, ",") != "aaa,mmm,zzz" || strings.Join(first, ",") != strings.Join(second, ",") {
		t.Errorf("tied shares ordered %v then %v; want id order both times", first, second)
	}
}

func TestPoints(t *testing.T) {
	if Points(nil, 10) != nil {
		t.Error("empty series must stay empty")
	}
	base := time.Now()
	var s []Sample
	for i := 0; i < 200; i++ {
		s = append(s, Sample{At: base.Add(time.Duration(i) * time.Second), CPU: float64(i)})
	}
	got := Points(s, MaxPoints)
	if len(got) > MaxPoints {
		t.Fatalf("Points returned %d values; cap is %d", len(got), MaxPoints)
	}
	if got[len(got)-1] != 199 {
		t.Errorf("Points dropped the newest reading: last = %v", got[len(got)-1])
	}
	if got[0] != 0 {
		t.Errorf("Points dropped the oldest reading: first = %v", got[0])
	}
	if len(Points(s[:3], 80)) != 3 {
		t.Error("a short series must pass through unresampled")
	}
	if got := Points(s, 1); len(got) != 1 || got[0] != 199 {
		t.Errorf("Points(max=1) = %v; want just the newest", got)
	}
}

func TestSpark(t *testing.T) {
	if Spark(nil, 10) != "" {
		t.Error("empty series must draw nothing")
	}
	if Spark([]float64{1, 2, 3}, 0) != "" {
		t.Error("zero width must draw nothing")
	}
	// An all-zero series still draws: "flat at zero" is an answer, not an absence.
	if got := Spark([]float64{0, 0, 0}, 8); len([]rune(got)) != 3 || strings.Trim(got, "\u2581") != "" {
		t.Errorf("Spark(zeros) = %q; want three floor blocks", got)
	}
	if got := Spark([]float64{0, 10}, 2); got != "\u2581\u2588" {
		t.Errorf("Spark(0..max) = %q; want floor then top", got)
	}
	if got := []rune(Spark([]float64{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}, 4)); len(got) != 4 {
		t.Errorf("Spark overran its width: %q", string(got))
	}
}

func TestRound(t *testing.T) {
	if got := Round(12.3456); got != 12.35 {
		t.Errorf("Round = %v; want 12.35", got)
	}
}

func flat(n int, v float64) []float64 {
	out := make([]float64, n)
	for i := range out {
		out[i] = v
	}
	return out
}

func ids(rows []Rank) []string {
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.ID)
	}
	return out
}
