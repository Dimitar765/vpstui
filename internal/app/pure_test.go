package app

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"

	"vpstui/internal/models"
)

func TestLooksLikeConnLoss(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want bool
	}{
		{"connection lost", "ssh: connection lost", true},
		{"case insensitive connection lost", "CONNECTION LOST", true},
		{"use of closed", "write tcp: use of closed network connection", true},
		{"eof exact", "EOF", true},
		{"eof lowercase in sentence", "unexpected end of file: read EOF from server", true},
		{"broken pipe", "write: broken pipe", true},
		{"no such host", "dial tcp: lookup vps: no such host", true},
		{"tcp connect", "dial tcp connect: connection refused", true},
		{"docker error is not conn loss", "docker is not installed or not in PATH on this host", false},
		{"permission error is not conn loss", "permission denied accessing /var/run/docker.sock", false},
		{"empty string", "", false},
		{"timeout message", "command timed out", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := looksLikeConnLoss(tt.in); got != tt.want {
				t.Errorf("looksLikeConnLoss(%q) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}

func TestFmtDuration(t *testing.T) {
	tests := []struct {
		name string
		sec  float64
		want string
	}{
		{"zero", 0, "0s"},
		{"seconds truncated", 59.9, "59s"},
		{"just under a minute", 59, "59s"},
		{"exactly a minute", 60, "1m"},
		{"minutes", 90, "1m"},
		{"just under an hour", 3599, "59m"},
		{"exactly an hour", 3600, "1h 0m"},
		{"hours and minutes", 48600, "13h 30m"},
		{"just under a day", 86399, "23h 59m"},
		{"exactly a day", 86400, "1d 0h"},
		{"day and hours", 90061, "1d 1h"},
		{"negative stays seconds", -5, "-5s"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := fmtDuration(tt.sec); got != tt.want {
				t.Errorf("fmtDuration(%v) = %q, want %q", tt.sec, got, tt.want)
			}
		})
	}
}

func TestAuthIndex(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want int
	}{
		{"password", models.AuthPassword, 0},
		{"key", models.AuthKey, 1},
		{"agent", models.AuthAgent, 2},
		{"unknown falls back to password", "bogus", 0},
		{"empty falls back to password", "", 0},
		{"case sensitive", "Password", 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := authIndex(tt.in); got != tt.want {
				t.Errorf("authIndex(%q) = %d, want %d", tt.in, got, tt.want)
			}
		})
	}
}

func TestSplitLogStamp(t *testing.T) {
	tests := []struct {
		name      string
		in        string
		wantStamp string
		wantRest  string
	}{
		{
			name:      "docker nano stamp",
			in:        "2026-10-11T01:02:03.123456789Z hello world",
			wantStamp: "01:02:03",
			wantRest:  "hello world",
		},
		{
			name:      "second precision",
			in:        "2026-10-11T01:02:03Z msg",
			wantStamp: "01:02:03",
			wantRest:  "msg",
		},
		{
			name:      "zone offset",
			in:        "2026-10-11T01:02:03.5+02:00 msg",
			wantStamp: "01:02:03",
			wantRest:  "msg",
		},
		{name: "no stamp", in: "hello world", wantRest: "hello world"},
		{name: "date-looking word only", in: "Timestamped 2026 data", wantRest: "Timestamped 2026 data"},
		{name: "impossible time passes through", in: "2026-13-45T99:99:99Z x", wantRest: "2026-13-45T99:99:99Z x"},
		{name: "empty line", in: "", wantRest: ""},
		{name: "stamp with no message", in: "2026-10-11T01:02:03.1Z ", wantStamp: "01:02:03", wantRest: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stamp, rest := splitLogStamp(tt.in)
			if stamp != tt.wantStamp || rest != tt.wantRest {
				t.Errorf("splitLogStamp(%q) = %q, %q; want %q, %q", tt.in, stamp, rest, tt.wantStamp, tt.wantRest)
			}
		})
	}
}

func TestLogLevel(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want logSev
	}{
		{"uppercase prefix", "ERROR: boom", lvlError},
		{"lowercase prefix", "error boom", lvlError},
		{"bracketed nginx", "2026/10/11 01:02:03 [error] 123#0: *5 open() failed", lvlError},
		{"logfmt", "level=warn msg=retrying", lvlWarn},
		{"json", `{"level":"error","msg":"dial failed"}`, lvlError},
		{"panic", "panic: runtime error: index out of range", lvlError},
		{"critical", "critical failure", lvlError},
		{"uppercase info", "INFO starting server", lvlInfo},
		{"debug", "debug: cache miss", lvlDebug},
		{"trace", "TRACE x1", lvlDebug},
		{"warning word", "warning: deprecated", lvlWarn},
		{"no level", "listening on :8080", lvlNone},
		{"information is not info", "information theory topic", lvlNone},
		{"errors plural is not error", "connection errors are expected here", lvlNone},
		{
			// Past the scan window a severity word must not recolour the line.
			name: "token past the window",
			in:   strings.Repeat("x", logLevelScanBytes) + " ERROR",
			want: lvlNone,
		},
		{"warn beats later error", "[warn] retrying after error", lvlWarn},
		{"empty", "", lvlNone},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := logLevel(tt.in); got != tt.want {
				t.Errorf("logLevel(%q) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}

func TestParseLogTail(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want int
	}{
		{"unset falls back", "", defaultLogTail},
		{"garbage falls back", "many", defaultLogTail},
		{"zero falls back", "0", defaultLogTail},
		{"negative falls back", "-5", defaultLogTail},
		{"clamped to max", "99999999", maxLogTail},
		{"accepted", "500", 500},
		{"surrounding spaces", "  1200  ", 1200},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := parseLogTail(tt.in); got != tt.want {
				t.Errorf("parseLogTail(%q) = %d, want %d", tt.in, got, tt.want)
			}
		})
	}
}

func TestContainsFold(t *testing.T) {
	tests := []struct {
		name string
		s    string
		sub  string
		want bool
	}{
		{"plain", "GET /health 200", "health", true},
		{"case differs", "GET /Health 200", "health", true},
		{"absent", "GET /health 200", "500", false},
		{"empty sub never matches", "abc", "", false},
		{"sub longer than s", "ab", "abc", false},
		{"exact", "err", "err", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := containsFold(tt.s, tt.sub); got != tt.want {
				t.Errorf("containsFold(%q, %q) = %v, want %v", tt.s, tt.sub, got, tt.want)
			}
		})
	}
}

func TestStyleLineKeepsContainerANSI(t *testing.T) {
	// A line that already carries colour is passed through after the stamp
	// split: nesting our SGR inside the container's would corrupt both.
	in := "2026-10-11T01:02:03.1Z \x1b[31mERROR\x1b[0m boom"
	got := styleLine(in, "", false)
	if !strings.Contains(got, "\x1b[31mERROR\x1b[0m boom") {
		t.Errorf("container colours were altered: %q", got)
	}
	if strings.Contains(got, "2026-10-11") {
		t.Errorf("stamp should be trimmed to the clock: %q", got)
	}
	if !strings.Contains(got, "01:02:03") {
		t.Errorf("stamp missing: %q", got)
	}
}

func TestStyleLineColourByLevel(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		wantSeq string
		denySeq string
	}{
		{"error red", "ERROR boom", "\x1b[38;5;196m", ""},
		{"warn orange", "warning: slow", "\x1b[38;5;214m", ""},
		{"debug grey", "debug: miss", "\x1b[38;5;241m", ""},
		{"info uncoloured", "INFO up", "", "\x1b[38;5;196m"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := styleLine(tt.in, "", false)
			if tt.wantSeq != "" && !strings.Contains(got, tt.wantSeq) {
				t.Errorf("styleLine(%q) = %q, want %q in it", tt.in, got, tt.wantSeq)
			}
			if tt.denySeq != "" && strings.Contains(got, tt.denySeq) {
				t.Errorf("styleLine(%q) = %q, want no %q", tt.in, got, tt.denySeq)
			}
		})
	}
}

func TestHighlightLineBoldlyMarksEveryMatch(t *testing.T) {
	base := lipgloss.NewStyle()
	got := highlightLine("err in Err word", "err", base)
	if strings.Count(got, "\x1b[1m") != 2 {
		t.Errorf("expected two bold runs, got %q", got)
	}
	// Every rendered segment closes itself, so nothing bleeds downstream.
	if strings.Count(got, "\x1b[0m") != 2 {
		t.Errorf("expected each bold run to reset: %q", got)
	}
	if lipgloss.Width(got) != len("err in Err word") {
		t.Errorf("highlighting changed the visible width: %q", got)
	}
}
