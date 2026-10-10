package models

import (
	"encoding/hex"
	"strings"
	"testing"
)

func TestNewIDFormat(t *testing.T) {
	id := NewID()
	if len(id) != 16 {
		t.Errorf("NewID() = %q, want 16 characters", id)
	}
	if strings.ToLower(id) != id {
		t.Errorf("NewID() = %q, want lowercase hex", id)
	}
	if b, err := hex.DecodeString(id); err != nil {
		t.Errorf("NewID() = %q is not valid hex: %v", id, err)
	} else if len(b) != 8 {
		t.Errorf("NewID() decodes to %d bytes, want 8", len(b))
	}
}

func TestNewIDUnique(t *testing.T) {
	const samples = 1000
	seen := make(map[string]int, samples)
	for i := 0; i < samples; i++ {
		id := NewID()
		seen[id]++
	}
	if len(seen) != samples {
		t.Errorf("generated %d unique IDs from %d samples, want %d", len(seen), samples, samples)
	}
}

func TestContainerShortName(t *testing.T) {
	tests := []struct {
		name  string
		names string
		want  string
	}{
		{"single", "web", "web"},
		{"comma joined", "web,web-1,web-2", "web"},
		{"trailing comma", "web,", "web"},
		{"leading comma", ",web", ""},
		{"empty", "", ""},
		{"no comma with spaces", "my web app", "my web app"},
		{"internal commas only", "a,,b", "a"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := Container{Names: tt.names}
			if got := c.ShortName(); got != tt.want {
				t.Errorf("ShortName() for Names=%q = %q, want %q", tt.names, got, tt.want)
			}
		})
	}
}

func TestHostFormatting(t *testing.T) {
	tests := []struct {
		name              string
		host              Host
		wantTarget        string
		wantAddr          string
		wantPasswordRef   string
		wantPassphraseRef string
	}{
		{
			name:              "typical",
			host:              Host{ID: "abc123", User: "root", Address: "203.0.113.7", Port: 22},
			wantTarget:        "root@203.0.113.7:22",
			wantAddr:          "203.0.113.7:22",
			wantPasswordRef:   "abc123:password",
			wantPassphraseRef: "abc123:passphrase",
		},
		{
			name:              "custom port hostname",
			host:              Host{ID: "id2", User: "deploy", Address: "vps.example.com", Port: 2222},
			wantTarget:        "deploy@vps.example.com:2222",
			wantAddr:          "vps.example.com:2222",
			wantPasswordRef:   "id2:password",
			wantPassphraseRef: "id2:passphrase",
		},
		{
			name:              "zero port is printed verbatim",
			host:              Host{ID: "id3", User: "", Address: "h", Port: 0},
			wantTarget:        "@h:0",
			wantAddr:          "h:0",
			wantPasswordRef:   "id3:password",
			wantPassphraseRef: "id3:passphrase",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.host.Target(); got != tt.wantTarget {
				t.Errorf("Target() = %q, want %q", got, tt.wantTarget)
			}
			if got := tt.host.Addr(); got != tt.wantAddr {
				t.Errorf("Addr() = %q, want %q", got, tt.wantAddr)
			}
			if got := tt.host.PasswordRef(); got != tt.wantPasswordRef {
				t.Errorf("PasswordRef() = %q, want %q", got, tt.wantPasswordRef)
			}
			if got := tt.host.PassphraseRef(); got != tt.wantPassphraseRef {
				t.Errorf("PassphraseRef() = %q, want %q", got, tt.wantPassphraseRef)
			}
		})
	}
}
