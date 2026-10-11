package app

import (
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"vpstui/internal/errsample"
)

// Error aggregation bounds. The sweep pulls a bounded window per container, so
// the payload a chatty host can hand back is finite before parsing even starts.
// The byte caps are vars so tests can drive the trim path without generating
// megabytes of fixture text.
var (
	errSweepBytesCap  = 4 << 20
	errSweepBytesKeep = 2 << 20
)

const (
	defaultErrWindow = 2 * time.Hour
	defaultErrTail   = 500
	maxErrTail       = 5000

	// errScanBytes bounds the phrase search, mirroring logLevelScanBytes: a
	// message body long enough to contain "connection refused" by accident
	// should not recolour and re-group its own line.
	errScanBytes = 360

	errSigMaxRunes = 160

	// errCorpusCap is how many lines one host keeps, errFlatCap how many the
	// flat feed shows, errExamplesPerGroup how many samples a group expands to,
	// maxErrorGroups the ceiling published to /api/errors.
	errCorpusCap        = 500
	errFlatCap          = 1000
	errExamplesPerGroup = errsample.Cap
	maxErrorGroups      = 200
)

// ErrLine is one kept log line, already classified and normalised.
type ErrLine struct {
	HostID   string
	HostName string
	ID       string // container id, docker's 12-char short form
	Name     string // container name
	Stamp    string // "HH:MM:SS", "" when the line had no timestamp
	At       time.Time
	Sev      logSev // lvlWarn or lvlError
	Sig      string // normalised signature
	Text     string // message, stamp and escapes removed — what the pane shows
}

// ErrGroup is one distinct problem, counted.
type ErrGroup struct {
	Sig        string
	Sev        logSev
	Count      int
	Hosts      []string
	Containers []string
	First      time.Time
	Last       time.Time
	Examples   []ErrLine
}

// sevName is the display label; it doubles as the ctable cell input, so
// sevCell stays free of logSev plumbing.
func sevName(s logSev) string {
	switch s {
	case lvlError:
		return "error"
	case lvlWarn:
		return "warn"
	}
	return ""
}

// sweepRow splits a "id<TAB>name<TAB>line" row on the first two tabs only: a
// log message may legitimately contain tabs.
func sweepRow(row string) (id, name, line string, ok bool) {
	a := strings.IndexByte(row, '\t')
	if a < 0 {
		return "", "", "", false
	}
	b := strings.IndexByte(row[a+1:], '\t')
	if b < 0 {
		return "", "", "", false
	}
	b += a + 1
	return row[:a], row[a+1 : b], row[b+1:], true
}

// parseErrSweep turns one host's payload into kept lines. It runs inside the
// command goroutine, never in Update, because a 4 MiB payload is far too much
// work for the UI thread. The second return reports that the payload was cut.
func parseErrSweep(payload, hostID, hostName string, sweptAt time.Time) ([]ErrLine, bool) {
	truncated := false
	if len(payload) > errSweepBytesCap {
		// Cut at a line boundary. A partial row would parse as a container
		// whose id is half a token, inventing a phantom host in the graph.
		cut := len(payload) - errSweepBytesKeep
		if i := strings.IndexByte(payload[cut:], '\n'); i >= 0 {
			cut += i + 1
		} else {
			cut = len(payload)
		}
		payload = payload[cut:]
		truncated = true
	}

	var out []ErrLine
	for _, row := range strings.Split(payload, "\n") {
		row = strings.TrimSuffix(row, "\r")
		if row == "" {
			continue
		}
		id, name, line, ok := sweepRow(row)
		if !ok {
			continue
		}
		stamp, at, rest := sweepStamp(line)
		// Escape sequences are decoration the container chose for a terminal
		// that is not this one. They would poison signature()'s digit and path
		// rules, and a truncated escape in a table cell leaks garbage, so the
		// corpus keeps the plain message.
		rest = stripANSI(rest)
		sev, keep := keepErrLine(rest)
		if !keep {
			continue
		}
		if at.IsZero() {
			at = sweptAt
		}
		out = append(out, ErrLine{
			HostID: hostID, HostName: hostName,
			ID: id, Name: name, Stamp: stamp, At: at,
			Sev: sev, Sig: signature(rest), Text: rest,
		})
	}
	return out, truncated
}

// sweepStamp splits docker's `--timestamps` prefix into the wall-clock display
// form and a parseable instant. splitLogStamp owns the display half; the full
// token is needed here because grouping wants an ordering, not a label.
func sweepStamp(line string) (stamp string, at time.Time, rest string) {
	stamp, rest = splitLogStamp(line)
	if stamp == "" {
		return "", time.Time{}, rest
	}
	i := strings.IndexByte(line, ' ')
	t, err := time.Parse(time.RFC3339Nano, line[:i])
	if err != nil {
		return stamp, time.Time{}, rest
	}
	// The token carries the daemon's offset; ordering and the LAST column are
	// meaningless next to a client clock in another zone.
	return stamp, t.Local(), rest
}

// failPhrases are matched as substrings: they only ever appear in a real
// failure. failWords are matched as whole words, because bare "killed" also
// turns up in a game server's happy path.
var (
	failPhrases = []string{
		"traceback (most recent call last)",
		"out of memory",
		"outofmemory",
		"oom-killer",
		"oomkilled",
		"killed process",
		"connection refused",
		"connection reset",
		// "timed out" covers connection/read/request timeouts. The bare word
		// "timeout" does not: it is also a config key, and a startup line
		// saying `timeout=30` would otherwise headline the aggregate.
		"timed out",
		"context deadline exceeded",
		"i/o timeout",
		"no space left on device",
		"permission denied",
		"authentication failed",
		"certificate has expired",
		"exit status",
		"core dumped",
		"segfault",
		"sigsegv",
		"sigabrt",
		"nil pointer",
		"stack trace",
	}
	failWords = []string{"exception", "uncaught", "aborted", "hung"}
)

// keepErrLine decides whether a line belongs in the aggregate. The log view's
// level scan already catches ERROR/WARN in every shape it knows ("ERROR:",
// "[error]", level=warn, "level":"error"); the heuristics below catch what has
// no level word at all — tracebacks, OOM notices, refused connections.
func keepErrLine(rest string) (logSev, bool) {
	switch sev := logLevel(rest); sev {
	case lvlError:
		return lvlError, true
	case lvlWarn:
		return lvlWarn, true
	}
	if failureHeuristic(rest) {
		return lvlError, true
	}
	return lvlNone, false
}

func failureHeuristic(s string) bool {
	if len(s) > errScanBytes {
		s = s[:errScanBytes]
	}
	low := strings.ToLower(s)
	// The classic shell notice for an OOM-killed process is a line saying
	// nothing but "Killed". Substring matching it would sweep up half the
	// world, so it gets its own exact case.
	if low == "killed" {
		return true
	}
	for _, p := range failPhrases {
		if strings.Contains(low, p) {
			return true
		}
	}
	return hasFailWord(low)
}

// hasFailWord matches failWords at letter boundaries, the same discipline
// logLevel uses so "information" never reads as info.
func hasFailWord(low string) bool {
	for i := 0; i < len(low); {
		if !isASCIILetter(low[i]) {
			i++
			continue
		}
		j := i
		for j < len(low) && isASCIILetter(low[j]) {
			j++
		}
		for _, w := range failWords {
			if low[i:j] == w {
				return true
			}
		}
		i = j
	}
	return false
}

// stripANSI drops ECMA-48 escape sequences. Required before signing: a
// coloured line's "[1;31m" would otherwise feed the digit and path rules and
// split one problem into a group per colour variant.
func stripANSI(s string) string {
	if !strings.ContainsRune(s, 0x1b) {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		if s[i] != 0x1b {
			b.WriteByte(s[i])
			i++
			continue
		}
		if i+1 >= len(s) || s[i+1] != '[' {
			b.WriteByte(s[i]) // a lone ESC is not a sequence; keep it
			i++
			continue
		}
		j := i + 2
		for j < len(s) && s[j] >= 0x30 && s[j] <= 0x3f {
			j++
		}
		for j < len(s) && s[j] >= 0x20 && s[j] <= 0x2f {
			j++
		}
		if j >= len(s) {
			break // truncated escape: drop the fragment
		}
		if s[j] >= 0x40 && s[j] <= 0x7f {
			i = j + 1 // final byte: the whole sequence goes
			continue
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}

// Signature placeholders, applied most-specific-first. Paths run before UUIDs,
// hex and digit runs so `/var/log/app2/current` collapses whole: replacing the
// digits first would leave `{n}` braces the path rule can no longer match, and
// a path containing an identifier would split into two half-replaced variants.
var (
	sigUUID = regexp.MustCompile(`[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}`)
	sigHex  = regexp.MustCompile(`(?:0[xX])?[0-9a-fA-F]{8,}`)
	sigPath = regexp.MustCompile(`/[A-Za-z0-9._+\-]+(?:/[A-Za-z0-9._+\-]+)+/?`)
	sigStr  = regexp.MustCompile(`'[^']*'|"[^"]*"`)
	sigNum  = regexp.MustCompile(`[0-9]+`)
	sigWS   = regexp.MustCompile(`\s+`)
)

// signature normalises a message into the key it groups under. Two lines of
// the same failure must land on one string while two different failures must
// not, so the rules are deliberately blunt: anything that looks like a path, an
// identifier, or a quoted value, and anything numeric, becomes a placeholder.
func signature(raw string) string {
	s := stripANSI(raw)
	if stamp, rest := splitLogStamp(s); stamp != "" {
		s = rest
	}
	s = sigPath.ReplaceAllString(s, "{path}")
	s = sigUUID.ReplaceAllString(s, "{id}")
	s = sigHex.ReplaceAllString(s, "{hex}")
	s = sigStr.ReplaceAllString(s, "{str}")
	s = sigNum.ReplaceAllString(s, "{n}")
	s = sigWS.ReplaceAllString(strings.ToLower(s), " ")
	s = strings.TrimSpace(s)
	if r := []rune(s); len(r) > errSigMaxRunes {
		s = string(r[:errSigMaxRunes])
	}
	return s
}

// groupErrors collapses a corpus into counted signatures. Output is fully
// ordered — count desc, then signature — so the same corpus always renders
// identically and the web page can skip repainting, exactly like toNetworkJSON.
func groupErrors(lines []ErrLine) []ErrGroup {
	type acc struct {
		hosts map[string]bool
		conts map[string]bool
		cands []errsample.Entry[ErrLine]
	}
	idx := make(map[string]int, len(lines))
	var accs []acc
	var out []ErrGroup

	for _, l := range lines {
		i, ok := idx[l.Sig]
		if !ok {
			i = len(out)
			idx[l.Sig] = i
			accs = append(accs, acc{hosts: map[string]bool{}, conts: map[string]bool{}})
			out = append(out, ErrGroup{Sig: l.Sig, Sev: l.Sev, First: l.At, Last: l.At})
		}
		g, a := &out[i], &accs[i]
		g.Count++
		// One error line in a sea of warnings is an error group: severity is
		// the worst thing this signature has ever said, not the first.
		if l.Sev > g.Sev {
			g.Sev = l.Sev
		}
		if l.At.Before(g.First) {
			g.First = l.At
		}
		if l.At.After(g.Last) {
			g.Last = l.At
		}
		a.hosts[l.HostName] = true
		a.conts[l.HostName+"/"+l.Name] = true
		a.cands = append(a.cands, errsample.Entry[ErrLine]{
			At:    l.At,
			Key:   errsample.Key{Text: l.Text, Container: l.Name, Host: l.HostName, Stamp: l.Stamp},
			Value: l,
		})
	}

	for i := range out {
		out[i].Hosts = sortedKeys(accs[i].hosts)
		out[i].Containers = sortedKeys(accs[i].conts)
		// The newest lines behind the signature, oldest first and newest last:
		// the same pick the web API makes, so Y and the page's copy agree.
		out[i].Examples = errsample.Newest(errExamplesPerGroup, accs[i].cands)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].Sig < out[j].Sig
	})
	return out
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// capCorpus keeps the newest lines when one host overshoots its budget.
func capCorpus(lines []ErrLine, n int) []ErrLine {
	if len(lines) <= n {
		return lines
	}
	return lines[len(lines)-n:]
}

// sortNewestFirst orders the flat feed. Slice sort is not stable by signature,
// so equal instants still fall out in corpus order.
func sortNewestFirst(lines []ErrLine) {
	sort.SliceStable(lines, func(i, j int) bool { return lines[i].At.After(lines[j].At) })
}

// parseErrWindow clamps VPSTUI_ERR_SINCE. A Go duration is parsed and then
// re-rendered in whole seconds, so the value reaching the remote shell is
// always numeric — and always in the one relative form every docker version
// understands, instead of whatever shorthand the user typed.
func parseErrWindow(s string) string {
	d, err := time.ParseDuration(coarseDuration(strings.TrimSpace(s)))
	if err != nil || d <= 0 {
		d = defaultErrWindow
	}
	d = min(d, 7*24*time.Hour)
	return strconv.FormatInt(int64(d.Seconds()), 10) + "s"
}

// coarseDuration rewrites the day and week suffixes Go's duration parser does
// not know: "2d" becomes "48h" rather than being rejected as garbage, because a
// sweep window is exactly the kind of thing people write "2d" for. Anything
// else passes through for ParseDuration to judge.
func coarseDuration(s string) string {
	if s == "" {
		return s
	}
	last := s[len(s)-1]
	if last != 'd' && last != 'D' && last != 'w' && last != 'W' {
		return s
	}
	n, err := strconv.Atoi(strings.TrimSpace(s[:len(s)-1]))
	if err != nil || n < 1 {
		return s
	}
	h := n * 24
	if last == 'w' || last == 'W' {
		h *= 7
	}
	return strconv.Itoa(h) + "h"
}

// parseErrTail clamps VPSTUI_ERR_TAIL, the same way parseLogTail protects the
// live stream: a typo cannot ask docker for the whole history.
func parseErrTail(s string) int {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil || n < 1 {
		return defaultErrTail
	}
	return min(n, maxErrTail)
}

func errWindow() string { return parseErrWindow(os.Getenv("VPSTUI_ERR_SINCE")) }
func errTail() int      { return parseErrTail(os.Getenv("VPSTUI_ERR_TAIL")) }
