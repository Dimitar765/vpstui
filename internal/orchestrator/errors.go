package orchestrator

import (
	"regexp"
	"sort"
	"strings"
	"time"

	"vpstui/internal/apitypes"
	"vpstui/internal/errsample"
)

// The cross-host error aggregate reuses internal/errsample for sample
// selection (the same pick the TUI's y/Y and the loopback page make). The
// sweep wire format — "id\tname\tline" from docker logs --timestamps — and
// the signature normalisation live here, mirroring the TUI's rules so a
// signature groups identically on both sides.

type errSev int

const (
	sevWarn errSev = iota
	sevError
)

func (s errSev) String() string {
	if s == sevError {
		return "error"
	}
	return "warn"
}

// errLine is one kept log line, classified and normalised.
type errLine struct {
	ServerID   string
	ServerName string
	Container  string
	Stamp      string
	At         time.Time
	Sev        errSev
	Sig        string
	Text       string
}

// errCorpus is one server's retained sample set, capped like the TUI's.
type errCorpus struct {
	lines   []errLine
	sweptAt time.Time
}

func (c *errCorpus) add(lines []errLine) {
	c.lines = append(c.lines, lines...)
	sort.SliceStable(c.lines, func(i, j int) bool { return c.lines[i].At.After(c.lines[j].At) })
	if len(c.lines) > errCorpusCap {
		c.lines = c.lines[:errCorpusCap]
	}
}

// parseErrSweep turns a SweepErrors payload into kept lines. `|| true` in the
// sweep command means a partial payload is still worth parsing.
func parseErrSweep(payload, serverID, serverName string, sweptAt time.Time) []errLine {
	var out []errLine
	for _, row := range strings.Split(payload, "\n") {
		_, name, line, ok := sweepRow(row)
		if !ok {
			continue
		}
		stamp, at, rest := sweepStamp(line)
		sev, keep := keepErrLine(rest)
		if !keep {
			continue
		}
		text := stripANSI(rest)
		out = append(out, errLine{
			ServerID: serverID, ServerName: serverName, Container: name,
			Stamp: stamp, At: at, Sev: sev,
			Sig: errSignature(text), Text: text,
		})
	}
	return out
}

func sweepRow(row string) (id, name, line string, ok bool) {
	parts := strings.SplitN(row, "\t", 3)
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" {
		return "", "", "", false
	}
	return parts[0], parts[1], parts[2], true
}

// sweepStamp splits docker's --timestamps RFC3339 prefix off a log line.
func sweepStamp(line string) (stamp string, at time.Time, rest string) {
	i := strings.IndexByte(line, ' ')
	if i < 0 {
		return "", time.Time{}, line
	}
	t, err := time.Parse(time.RFC3339Nano, line[:i])
	if err != nil {
		return "", time.Time{}, line
	}
	return t.Local().Format("15:04:05"), t.Local(), line[i+1:]
}

var (
	errPhrases = []string{
		"traceback (most recent call last)", "out of memory", "outofmemory",
		"oom-killed", "killed process", "connection refused", "connection reset",
		"timed out", "context deadline exceeded", "i/o timeout",
		"no space left on device", "permission denied", "authentication failed",
		"certificate has expired", "exit status", "core dumped", "segfault",
		"sigsegv", "sigabrt", "nil pointer", "stack trace",
	}
	errWords  = []string{"exception", "uncaught", "aborted", "error", "fatal", "warn"}
	sigUUID   = regexp.MustCompile(`[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}`)
	sigHex    = regexp.MustCompile(`(?:0[xX])?[0-9a-fA-F]{8,}`)
	sigPath   = regexp.MustCompile(`/[A-Za-z0-9._+\-]+(?:/[A-Za-z0-9._+\-]+)+/?`)
	sigStr    = regexp.MustCompile(`'[^']*'|"[^"]*"`)
	sigNum    = regexp.MustCompile(`[0-9]+`)
	sigWS     = regexp.MustCompile(`\s+`)
	sigMaxRun = 160
)

// keepErrLine classifies a line: explicit level words, then failure phrases,
// then bare failure words. Deliberately broader than the TUI's exact list —
// the daemon has no log pane to pre-filter for the user, so it keeps warn-
// level noise it would otherwise drop on the floor.
func keepErrLine(rest string) (errSev, bool) {
	low := strings.ToLower(rest)
	if len(low) > 360 {
		low = low[:360]
	}
	for _, w := range []string{"error", "fatal"} {
		if hasWord(low, w) {
			return sevError, true
		}
	}
	if hasWord(low, "warn") {
		return sevWarn, true
	}
	for _, p := range errPhrases {
		if strings.Contains(low, p) {
			return sevError, true
		}
	}
	for _, w := range errWords[1:4] { // exception, uncaught, aborted
		if hasWord(low, w) {
			return sevError, true
		}
	}
	return sevWarn, false
}

// hasWord matches whole ASCII words only, so "information" never reads as warn.
func hasWord(low, w string) bool {
	for i := 0; i+len(w) <= len(low); i++ {
		if i > 0 && isLetter(low[i-1]) {
			continue
		}
		if !strings.HasPrefix(low[i:], w) {
			continue
		}
		if j := i + len(w); j >= len(low) || !isLetter(low[j]) {
			return true
		}
	}
	return false
}

func isLetter(b byte) bool { return b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' }

// stripANSI drops ECMA-48 escape sequences before signing.
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
			break
		}
		if s[j] >= 0x40 && s[j] <= 0x7f {
			i = j + 1
			continue
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}

// errSignature normalises a line into a group key, most-specific rules first
// (paths before hex and digits, or "/log/app2/current" would split apart).
func errSignature(raw string) string {
	s := sigPath.ReplaceAllString(raw, "{path}")
	s = sigUUID.ReplaceAllString(s, "{id}")
	s = sigHex.ReplaceAllString(s, "{hex}")
	s = sigStr.ReplaceAllString(s, "{str}")
	s = sigNum.ReplaceAllString(s, "{n}")
	s = sigWS.ReplaceAllString(strings.ToLower(s), " ")
	s = strings.TrimSpace(s)
	if r := []rune(s); len(r) > sigMaxRun {
		s = string(r[:sigMaxRun])
	}
	return s
}

// groupErrors collapses a corpus into counted signatures: count desc, then
// signature, output fully ordered. Sample selection is errsample.Newest.
func groupErrors(lines []errLine, max int) []apitypes.ErrorGroup {
	type acc struct {
		hosts, conts map[string]bool
		cands        []errsample.Entry[errLine]
	}
	idx := map[string]int{}
	var out []apitypes.ErrorGroup
	var accs []acc
	for _, l := range lines {
		i, ok := idx[l.Sig]
		if !ok {
			i = len(out)
			idx[l.Sig] = i
			accs = append(accs, acc{hosts: map[string]bool{}, conts: map[string]bool{}})
			out = append(out, apitypes.ErrorGroup{Sig: l.Sig, Sev: l.Sev.String(), FirstSeen: l.At, LastSeen: l.At})
		}
		g, a := &out[i], &accs[i]
		g.Count++
		if l.Sev == sevError && g.Sev != sevError.String() {
			g.Sev = "error"
		}
		if l.At.Before(g.FirstSeen) {
			g.FirstSeen = l.At
		}
		if l.At.After(g.LastSeen) {
			g.LastSeen = l.At
		}
		a.hosts[l.ServerName] = true
		a.conts[l.ServerName+"/"+l.Container] = true
		a.cands = append(a.cands, errsample.Entry[errLine]{
			At:    l.At,
			Key:   errsample.Key{Text: l.Text, Container: l.Container, Host: l.ServerName, Stamp: l.Stamp},
			Value: l,
		})
	}
	for i := range out {
		out[i].Hosts = sortedSet(accs[i].hosts)
		out[i].Containers = sortedSet(accs[i].conts)
		for _, l := range errsample.Newest(errsample.Cap, accs[i].cands) {
			es := errorSample(l)
			out[i].Examples = append(out[i].Examples, es)
			if out[i].Sample == nil {
				s := es
				out[i].Sample = &s
			}
		}
		if out[i].Examples == nil {
			out[i].Examples = []apitypes.ErrorSample{}
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].Sig < out[j].Sig
	})
	if max > 0 && len(out) > max {
		out = out[:max]
	}
	return out
}

func sortedSet(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// errorSample adapts an internal errLine onto the wire DTO.
func errorSample(l errLine) apitypes.ErrorSample {
	return apitypes.ErrorSample{
		Host: l.ServerName, Container: l.Container, Sev: l.Sev.String(),
		Stamp: l.Stamp, Text: l.Text,
	}
}
