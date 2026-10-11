package app

import (
	"testing"

	"vpstui/internal/models"
)

// trySubmit runs the form's own validation; these tests pin its contract: bad
// ports and key-auth-without-keypath are rejected with a visible error, the
// defaults fill in, and the secret is cleared from the form while the emitted
// message carries it.
func TestTrySubmitValidation(t *testing.T) {
	base := func() *HostForm {
		f := NewHostForm(models.Host{}, true)
		f.addr.SetValue("203.0.113.7")
		f.authIdx = authIndex(models.AuthPassword)
		f.secret.SetValue("hunter2")
		return f
	}

	tests := []struct {
		name    string
		port    string
		keypath string
		auth    string
		wantErr string // "" = submit must succeed
	}{
		{"port zero rejected", "0", "", models.AuthPassword, "invalid port"},
		{"port 65536 rejected", "65536", "", models.AuthPassword, "invalid port"},
		{"negative port rejected", "-1", "", models.AuthPassword, "invalid port"},
		{"non-numeric port rejected", "ssh", "", models.AuthPassword, "invalid port"},
		{"port 65535 accepted", "65535", "", models.AuthPassword, ""},
		{"empty port defaults to 22", "", "", models.AuthPassword, ""},
		{"key auth without key path rejected", "22", "", models.AuthKey, "private key path is required"},
		{"key auth with key path accepted", "22", "~/.ssh/id_ed25519", models.AuthKey, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := base()
			f.port.SetValue(tt.port)
			f.authIdx = authIndex(tt.auth)
			f.keypath.SetValue(tt.keypath)

			next, cmd, ok, _ := f.trySubmit()
			if tt.wantErr != "" {
				if ok {
					t.Fatalf("submit succeeded, want error %q", tt.wantErr)
				}
				if next.err != tt.wantErr {
					t.Fatalf("err = %q, want %q", next.err, tt.wantErr)
				}
				return
			}
			if !ok {
				t.Fatalf("submit failed with %q", next.err)
			}
			if cmd == nil {
				t.Fatal("accepted submit produced no message")
			}
			msg, isForm := cmd().(formSubmittedMsg)
			if !isForm {
				t.Fatalf("cmd produced %T, want formSubmittedMsg", cmd())
			}
			if !msg.isNew {
				t.Error("isNew lost")
			}
			if msg.secretV != "hunter2" {
				t.Errorf("message secret = %q, want the typed value", msg.secretV)
			}
			if next.SecretValue() != "" {
				t.Errorf("form secret = %q, want cleared after submit", next.SecretValue())
			}
			returned := msg.host
			if returned.Address != "203.0.113.7" {
				t.Errorf("address = %q", returned.Address)
			}
		})
	}
}

func TestTrySubmitDefaults(t *testing.T) {
	f := NewHostForm(models.Host{}, true)
	f.addr.SetValue("vps.example.com")
	f.authIdx = authIndex(models.AuthAgent)
	// port, user and name all left empty.
	next, cmd, ok, _ := f.trySubmit()
	if !ok {
		t.Fatalf("submit failed with %q", next.err)
	}
	h := cmd().(formSubmittedMsg).host
	if h.Port != 22 {
		t.Errorf("port = %d, want default 22", h.Port)
	}
	if h.User != "root" {
		t.Errorf("user = %q, want default root", h.User)
	}
	if h.Name != "vps.example.com" {
		t.Errorf("name = %q, want the address", h.Name)
	}
	if h.AuthType != models.AuthAgent {
		t.Errorf("auth = %q, want agent", h.AuthType)
	}
}
