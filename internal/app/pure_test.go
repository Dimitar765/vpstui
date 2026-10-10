package app

import (
	"testing"

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
