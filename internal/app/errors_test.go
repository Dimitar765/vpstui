package app

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
)

// sweepLine builds one payload row the way the remote awk does: the container's
// short id, its name, then the raw log line.
func sweepLine(id, name, line string) string {
	return id + "\t" + name + "\t" + line
}

func TestParseErrWindow(t *testing.T) {
	tests := []struct {
		name, in, want string
	}{
		{"unset falls back to the default", "", "7200s"},
		{"garbage falls back", "yesterday", "7200s"},
		{"zero falls back", "0", "7200s"},
		{"negative falls back", "-5m", "7200s"},
		{"day form re-renders in seconds", "1d", "86400s"},
		{"days", "2d", "172800s"},
		{"weeks clamp at the ceiling", "1w", "604800s"},
		{"fractional days are garbage", "1.5d", "7200s"},
		{"bare unit is garbage", "d", "7200s"},
		{"minutes pass through", "90m", "5400s"},
		{"hours pass through", "3h", "10800s"},
		{"whitespace tolerated", " 15m ", "900s"},
		{"beyond a week clamps", "3000h", "604800s"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := parseErrWindow(tt.in); got != tt.want {
				t.Errorf("parseErrWindow(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
	// The value reaching the shell must always be the numeric form docker takes.
	if !strings.HasSuffix(parseErrWindow("nonsense"), "s") {
		t.Error("window is not a seconds duration")
	}
}

func TestParseErrTail(t *testing.T) {
	tests := []struct {
		name, in string
		want     int
	}{
		{"unset falls back", "", defaultErrTail},
		{"garbage falls back", "many", defaultErrTail},
		{"zero falls back", "0", defaultErrTail},
		{"negative falls back", "-5", defaultErrTail},
		{"one is honoured", "1", 1},
		{"ceiling is honoured", "5000", maxErrTail},
		{"past the ceiling clamps", "999999", maxErrTail},
		{"whitespace tolerated", " 42 ", 42},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := parseErrTail(tt.in); got != tt.want {
				t.Errorf("parseErrTail(%q) = %d, want %d", tt.in, got, tt.want)
			}
		})
	}
}

func TestParseErrSweep(t *testing.T) {
	at := time.Date(2026, 10, 11, 9, 0, 0, 0, time.Local)
	stamped := sweepLine("8f30766c1db2", "web", "2026-10-11T01:02:03.511255428Z ERROR db is down")
	payload := strings.Join([]string{
		stamped,
		"", // blank line between containers
		sweepLine("c2", "api", "level=warn msg=slow query"),
		sweepLine("c3", "api", "INFO: everything is fine here"),
		"no-tabs-at-all", // fewer than three fields
		"c4\tweb",        // two fields only
		sweepLine("c5", "web", "ERROR\ta tab\tinside"),
		sweepLine("c6", "web", "ERROR no stamp"),
	}, "\n") + "\n"

	got, truncated := parseErrSweep(payload, "host1", "alpha", at)
	if truncated {
		t.Error("unexpected truncation")
	}
	if len(got) != 4 {
		t.Fatalf("kept %d lines, want 4: %+v", len(got), got)
	}

	first := got[0]
	if first.HostID != "host1" || first.HostName != "alpha" {
		t.Errorf("host fields = %q/%q", first.HostID, first.HostName)
	}
	if first.ID != "8f30766c1db2" || first.Name != "web" {
		t.Errorf("container fields = %q/%q", first.ID, first.Name)
	}
	if first.Stamp != "01:02:03" {
		t.Errorf("stamp = %q, want 01:02:03", first.Stamp)
	}
	wantAt := time.Date(2026, 10, 11, 1, 2, 3, 511255428, time.UTC).Local()
	if !first.At.Equal(wantAt) {
		t.Errorf("at = %v, want %v", first.At, wantAt)
	}
	if first.Sev != lvlError || first.Text != "ERROR db is down" {
		t.Errorf("sev/text = %v/%q", first.Sev, first.Text)
	}
	if got[1].Sev != lvlWarn {
		t.Errorf("level=warn classified as %v", got[1].Sev)
	}
	// The message keeps its own tabs: only the first two split the row.
	if got[2].Text != "ERROR\ta tab\tinside" {
		t.Errorf("tabs mangled: %q", got[2].Text)
	}
	// A line with no docker stamp still gets an instant, so ordering never sees zero.
	if got[3].Stamp != "" || !got[3].At.Equal(at) {
		t.Errorf("unstamped line: stamp=%q at=%v, want empty/%v", got[3].Stamp, got[3].At, at)
	}
}

func TestParseErrSweepStripsAnsi(t *testing.T) {
	coloured := sweepLine("c1", "web", "2026-10-11T01:02:03Z \x1b[1;31mERROR boom\x1b[0m")
	plain := sweepLine("c1", "web", "2026-10-11T01:02:03Z ERROR boom")
	a, _ := parseErrSweep(coloured, "h", "alpha", time.Now())
	b, _ := parseErrSweep(plain, "h", "alpha", time.Now())
	if len(a) != 1 || len(b) != 1 {
		t.Fatalf("kept %d and %d lines", len(a), len(b))
	}
	if strings.ContainsAny(a[0].Text, "\x1b") {
		t.Errorf("escape survived into Text: %q", a[0].Text)
	}
	if a[0].Sig != b[0].Sig {
		t.Errorf("coloured sig %q != plain sig %q", a[0].Sig, b[0].Sig)
	}
}

func TestParseErrSweepTrimsToLineBoundary(t *testing.T) {
	oldCap, oldKeep := errSweepBytesCap, errSweepBytesKeep
	t.Cleanup(func() { errSweepBytesCap, errSweepBytesKeep = oldCap, oldKeep })
	errSweepBytesCap, errSweepBytesKeep = 60, 40

	long := sweepLine("a1", "web", strings.Repeat("ERROR ", 40)+"first line is way too long to keep")
	keep := sweepLine("b2", "api", "ERROR second line")
	payload := long + "\n" + keep + "\n"

	got, truncated := parseErrSweep(payload, "h", "alpha", time.Now())
	if !truncated {
		t.Fatal("oversized payload did not report truncation")
	}
	if len(got) != 1 || got[0].ID != "b2" {
		t.Fatalf("kept %+v, want only the b2 row", got)
	}
	if got[0].Text != "ERROR second line" {
		t.Errorf("partial line survived: %q", got[0].Text)
	}
	// Under the cap nothing is trimmed and nothing is flagged.
	small, trunc := parseErrSweep(keep+"\n", "h", "alpha", time.Now())
	if trunc || len(small) != 1 {
		t.Errorf("small payload: trunc=%v kept=%d", trunc, len(small))
	}
}

func TestKeepErrLine(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		wantSev logSev
		want    bool
	}{
		{"uppercase prefix", "ERROR: db is down", lvlError, true},
		{"bracketed lower", "[error] boom", lvlError, true},
		{"logfmt level", "level=warn msg=slow", lvlWarn, true},
		{"json level", `{"level":"error","msg":"boom"}`, lvlError, true},
		{"warning word", "WARNING disk almost full", lvlWarn, true},
		{"panic is a level", "panic: runtime error: nil pointer", lvlError, true},
		{"fatal is a level", "fatal: cannot bind", lvlError, true},
		{"traceback", "Traceback (most recent call last):", lvlError, true},
		{"bare killed", "Killed", lvlError, true},
		{"oom killer", "Memory cgroup out of memory: Killed process 4321 (java)", lvlError, true},
		{"connection refused", "dial tcp 10.0.0.5:8080: connect: connection refused", lvlError, true},
		{"deadline", "context deadline exceeded while waiting", lvlError, true},
		{"io timeout", "read tcp 1.2.3.4:53: i/o timeout", lvlError, true},
		{"timed out", "net.Read: operation timed out", lvlError, true},
		{"no space", "write /data/x: no space left on device", lvlError, true},
		{"exit status", "command failed: exit status 1", lvlError, true},
		{"exception word", "Exception in thread \"main\"", lvlError, true},
		{"segfault", "segfault at 7fff8a7b ip 0x4", lvlError, true},

		{"plain info", "INFO: 172.23.0.1 - \"GET / HTTP/1.1\" 200 OK", lvlNone, false},
		{"information is not info", "information theory topic", lvlNone, false},
		{"errors plural is not error", "no errors here", lvlNone, false},
		{"killed inside a sentence", "the worker was killed by the scheduler on purpose", lvlNone, false},
		{"timeout as a setting", "timeout=30 connections=8", lvlNone, false},
		{"debug chatter", "DEBUG polling loop iteration", lvlNone, false},
		{"empty", "", lvlNone, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sev, keep := keepErrLine(tt.in)
			if keep != tt.want {
				t.Errorf("keepErrLine(%q) keep = %v, want %v (sev %v)", tt.in, keep, tt.want, sev)
				return
			}
			if keep && sev != tt.wantSev {
				t.Errorf("keepErrLine(%q) sev = %v, want %v", tt.in, sev, tt.wantSev)
			}
		})
	}
}

func TestFailureHeuristicScansABoundedPrefix(t *testing.T) {
	filler := strings.Repeat("x", errScanBytes*2)
	if failureHeuristic(filler + " connection refused") {
		t.Error("phrase past the scan window was matched")
	}
	if !failureHeuristic("connection refused " + filler) {
		t.Error("phrase inside the window was missed")
	}
}

func TestStripANSI(t *testing.T) {
	tests := []struct {
		name, in, want string
	}{
		{"no escapes", "plain", "plain"},
		{"sgr run", "\x1b[1;31mboom\x1b[0m", "boom"},
		{"256 colour", "\x1b[38;5;196mred\x1b[39m", "red"},
		{"cursor movement", "\x1b[2K\x1b[1Gline", "line"},
		{"private mode", "\x1b[?25lhidden", "hidden"},
		{"lone escape is kept", "a\x1b", "a\x1b"},
		{"truncated fragment dropped", "a\x1b[1;", "a"},
		{"escape mid-line", "one\x1b[31m two", "one two"},
		{"empty", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := stripANSI(tt.in); got != tt.want {
				t.Errorf("stripANSI(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestSignature(t *testing.T) {
	tests := []struct {
		name, in, want string
	}{
		{"lowercases", "ERROR Boom", "error boom"},
		{"digit runs", "user 42 failed", "user {n} failed"},
		{"hex runs", "segfault at 0x7fff8a7b", "segfault at {hex}"},
		{"bare hex run", "ip 5555aaaa offset", "ip {hex} offset"},
		{"uuid", "req 3f2504e0-4f89-11d3-9a0c-0305e82b6321 done", "req {id} done"},
		{"path", "open failed /var/log/app2/current", "open failed {path}"},
		{"path with identifier", "stat /data/3f2504e0-4f89-11d3-9a0c-0305e82b6321/x", "stat {path}"},
		{"double quoted", `say "hello world" now`, "say {str} now"},
		{"single quoted", "say 'hi there' now", "say {str} now"},
		{"collapses whitespace", "too   many\t spaces  here", "too many spaces here"},
		{"leading stamp", "2026-10-11T01:02:03.5Z ERROR boom", "error boom"},
		{"escapes", "\x1b[31mERROR boom\x1b[0m", "error boom"},
		{"single slash word", "and/or maybe", "and/or maybe"},
		{"port numbers", "dial 10.0.0.5:8080 refused", "dial {n}.{n}.{n}.{n}:{n} refused"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := signature(tt.in); got != tt.want {
				t.Errorf("signature(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}

	// The two directions that matter: same problem, one key; different
	// problems, different keys.
	if signature("ERROR db down at 12:34 on worker-7") != signature("ERROR db down at 09:01 on worker-8") {
		t.Error("same failure produced two signatures")
	}
	if signature("ERROR db down") == signature("ERROR disk full") {
		t.Error("different failures merged into one signature")
	}

	long := signature("x " + strings.Repeat("y", errSigMaxRunes*3))
	if len([]rune(long)) != errSigMaxRunes {
		t.Errorf("signature length = %d, want the %d-rune clamp", len([]rune(long)), errSigMaxRunes)
	}
}

func errFixture() []ErrLine {
	t1 := time.Date(2026, 10, 11, 1, 0, 0, 0, time.UTC)
	return []ErrLine{
		{HostID: "a", HostName: "alpha", ID: "c1", Name: "web", Sev: lvlWarn, Sig: "db down", At: t1, Text: "WARN db down"},
		{HostID: "a", HostName: "alpha", ID: "c1", Name: "web", Sev: lvlError, Sig: "db down", At: t1.Add(time.Minute), Text: "ERROR db down"},
		{HostID: "b", HostName: "beta", ID: "c2", Name: "api", Sev: lvlError, Sig: "no space left", At: t1.Add(2 * time.Minute), Text: "ERROR no space left"},
	}
}

func TestGroupErrors(t *testing.T) {
	lines := append(errFixture(), ErrLine{
		HostID: "b", HostName: "beta", ID: "c3", Name: "web",
		Sev: lvlError, Sig: "db down", At: time.Date(2026, 10, 11, 1, 30, 0, 0, time.UTC), Text: "ERROR db down",
	})
	groups := groupErrors(lines)
	if len(groups) != 2 {
		t.Fatalf("groups = %d, want 2: %+v", len(groups), groups)
	}
	// Count desc first.
	if groups[0].Sig != "db down" || groups[0].Count != 3 {
		t.Errorf("first group = %q ×%d, want db down ×3", groups[0].Sig, groups[0].Count)
	}
	if groups[1].Sig != "no space left" || groups[1].Count != 1 {
		t.Errorf("second group = %q ×%d", groups[1].Sig, groups[1].Count)
	}
	g := groups[0]
	// Severity is the worst thing the signature ever said, not the first.
	if g.Sev != lvlError {
		t.Errorf("sev = %v, want lvlError", g.Sev)
	}
	if want := []string{"alpha", "beta"}; !reflect.DeepEqual(g.Hosts, want) {
		t.Errorf("hosts = %v, want %v", g.Hosts, want)
	}
	if want := []string{"alpha/web", "beta/web"}; !reflect.DeepEqual(g.Containers, want) {
		t.Errorf("containers = %v, want %v", g.Containers, want)
	}
	if got, want := g.First.Format(time.RFC3339), "2026-10-11T01:00:00Z"; got != want {
		t.Errorf("first = %s, want %s", got, want)
	}
	if got, want := g.Last.Format(time.RFC3339), "2026-10-11T01:30:00Z"; got != want {
		t.Errorf("last = %s, want %s", got, want)
	}
	if len(g.Examples) != 3 {
		t.Errorf("examples = %d, want 3", len(g.Examples))
	}

	if groupErrors(nil) != nil {
		t.Error("empty corpus must give no groups")
	}
}

func TestGroupErrorsIsDeterministic(t *testing.T) {
	lines := errFixture()
	first := fmt.Sprintf("%+v", groupErrors(lines))
	second := fmt.Sprintf("%+v", groupErrors(lines))
	if first != second {
		t.Error("the same corpus serialised differently across runs")
	}
}

func TestGroupErrorsTiesFallBackToSignature(t *testing.T) {
	at := time.Now()
	lines := []ErrLine{
		{HostName: "alpha", Name: "web", Sev: lvlError, Sig: "zzz", At: at},
		{HostName: "alpha", Name: "web", Sev: lvlError, Sig: "aaa", At: at},
		{HostName: "alpha", Name: "web", Sev: lvlError, Sig: "mmm", At: at},
	}
	groups := groupErrors(lines)
	var got []string
	for _, g := range groups {
		got = append(got, g.Sig)
	}
	if want := []string{"aaa", "mmm", "zzz"}; !reflect.DeepEqual(got, want) {
		t.Errorf("order = %v, want %v", got, want)
	}
}

func TestGroupErrorsCapsExamples(t *testing.T) {
	at := time.Now()
	var lines []ErrLine
	for i := 0; i < errExamplesPerGroup+5; i++ {
		lines = append(lines, ErrLine{
			HostName: "alpha", Name: "web", Sev: lvlError, Sig: "boom",
			At: at.Add(time.Duration(i) * time.Second), Text: fmt.Sprintf("ERROR boom %d", i),
		})
	}
	g := groupErrors(lines)[0]
	if len(g.Examples) != errExamplesPerGroup {
		t.Fatalf("examples = %d, want %d", len(g.Examples), errExamplesPerGroup)
	}
	if g.Count != errExamplesPerGroup+5 {
		t.Errorf("count = %d, want %d — the cap must trim samples, not occurrences", g.Count, errExamplesPerGroup+5)
	}
	// The newest lines are kept, so the expand pane shows what is happening now.
	if want := fmt.Sprintf("ERROR boom %d", errExamplesPerGroup+4); g.Examples[len(g.Examples)-1].Text != want {
		t.Errorf("newest example = %q, want %q", g.Examples[len(g.Examples)-1].Text, want)
	}
}

func TestCapCorpusKeepsNewest(t *testing.T) {
	lines := make([]ErrLine, 10)
	for i := range lines {
		lines[i] = ErrLine{Sig: fmt.Sprintf("s%d", i), Text: fmt.Sprintf("line %d", i)}
	}
	got := capCorpus(lines, 3)
	if len(got) != 3 || got[0].Text != "line 7" || got[2].Text != "line 9" {
		t.Errorf("capCorpus kept %+v, want the last three", got)
	}
	if len(capCorpus(lines, 500)) != 10 {
		t.Error("cap above the length must not trim")
	}
}

func TestSortNewestFirst(t *testing.T) {
	at := time.Now()
	lines := []ErrLine{
		{Sig: "old", At: at.Add(-2 * time.Minute)},
		{Sig: "same-a", At: at},
		{Sig: "same-b", At: at},
		{Sig: "mid", At: at.Add(-time.Minute)},
	}
	sortNewestFirst(lines)
	if got := lines[0].Sig; got != "same-a" {
		t.Errorf("newest first = %q", got)
	}
	// Equal instants must keep corpus order, or the flat feed shuffles itself.
	if lines[1].Sig != "same-b" {
		t.Errorf("stable sort broken: %v %v", lines[0].Sig, lines[1].Sig)
	}
	if lines[3].Sig != "old" {
		t.Errorf("oldest last = %q", lines[3].Sig)
	}
}

func TestSevName(t *testing.T) {
	if sevName(lvlError) != "error" || sevName(lvlWarn) != "warn" || sevName(lvlInfo) != "" {
		t.Error("sevName labels drifted from what httpapi publishes")
	}
}

func TestSweepRowParser(t *testing.T) {
	id, name, line, ok := sweepRow("c1\tweb\t2026-10-11T01:02:03Z ERROR\there")
	if !ok || id != "c1" || name != "web" || line != "2026-10-11T01:02:03Z ERROR\there" {
		t.Errorf("sweepRow = %q/%q/%q/%v", id, name, line, ok)
	}
	if _, _, _, ok := sweepRow("only-one-field"); ok {
		t.Error("a row without two tabs must be rejected")
	}
	if _, _, _, ok := sweepRow("two\tfields"); ok {
		t.Error("a row with one tab must be rejected")
	}
}

func TestErrTimeFormatting(t *testing.T) {
	now := time.Now()
	if got := errTime(now); got != now.Format("15:04:05") {
		t.Errorf("today = %q, want the clock", got)
	}
	old := now.Add(-48 * time.Hour)
	if got := errTime(old); !strings.Contains(got, old.Format("01-02")) {
		t.Errorf("older line = %q, want a date", got)
	}
	if errTime(time.Time{}) != "—" {
		t.Error("zero instant must render as a dash, not a clock")
	}
}
