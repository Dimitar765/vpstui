package shell

import (
	"errors"
	"testing"

	"vpstui/internal/models"
)

func TestSanitizeTerm(t *testing.T) {
	tests := []struct{ in, want string }{
		{"xterm-256color", "xterm-256color"},
		{"", "xterm-256color"},
		{"we;ird$(rm)", "xterm-256color"},
		{"dumb", "dumb"},
		{"screen.xterm", "screen.xterm"},
	}
	for _, tt := range tests {
		if got := sanitizeTerm(tt.in); got != tt.want {
			t.Errorf("sanitizeTerm(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestParseShellArgs(t *testing.T) {
	h, c, err := parseShellArgs([]string{"--host", "abc", "--cid", "def"})
	if err != nil || h != "abc" || c != "def" {
		t.Fatalf("ok case: h=%q c=%q err=%v", h, c, err)
	}
	if _, _, err := parseShellArgs([]string{"--host", "abc"}); err == nil {
		t.Error("missing --cid accepted")
	}
	if _, _, err := parseShellArgs(nil); err == nil {
		t.Error("empty args accepted")
	}
}

func TestFindHost(t *testing.T) {
	hosts := []models.Host{{ID: "a", Name: "A"}, {ID: "b", Name: "B"}}
	h, err := FindHost(hosts, "b")
	if err != nil || h.Name != "B" {
		t.Fatalf("found %v err %v", h, err)
	}
	if _, err := FindHost(hosts, "zz"); err == nil {
		t.Fatal("missing host should error")
	}
}

func TestStoredSecretAgent(t *testing.T) {
	s, err := StoredSecret(models.Host{ID: "x", AuthType: models.AuthAgent})
	if err != nil || s != "" {
		t.Fatalf("agent: s=%q err=%v", s, err)
	}
}

func TestFailureMessage(t *testing.T) {
	if got := FailureMessage(nil, "  \n real error here \n more"); got != "real error here" {
		t.Errorf("stderr first line: %q", got)
	}
	if got := FailureMessage(errors.New("boom"), ""); got != "boom" {
		t.Errorf("plain err: %q", got)
	}
	if got := FailureMessage(nil, ""); got != "" {
		t.Errorf("no info: %q", got)
	}
}
