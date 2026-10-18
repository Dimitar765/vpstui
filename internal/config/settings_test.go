package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func settingsTestDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("VPSTUI_LOG_TAIL", "")
	t.Setenv("VPSTUI_ERR_TAIL", "")
	t.Setenv("VPSTUI_ERR_SINCE", "")
	t.Setenv("VPSTUI_HTTP_ADDR", "")
	return dir
}

func TestSettingsMissingFileDefaults(t *testing.T) {
	settingsTestDir(t)
	s, err := LoadSettings()
	if err != nil {
		t.Fatal(err)
	}
	def := DefaultSettings()
	if s.Theme != def.Theme || s.LogTail != def.LogTail || s.ErrTail != def.ErrTail {
		t.Fatalf("got %+v, want defaults %+v", s, def)
	}
	if !s.MouseEnabled() {
		t.Error("absent mouse must default to enabled")
	}
}

func TestSettingsRoundTrip(t *testing.T) {
	dir := settingsTestDir(t)
	no := false
	in := Settings{Theme: "light", LogTail: 500, ErrSince: "24h", ErrTail: 50,
		HTTPAddr: "127.0.0.1:9999", Mouse: &no, LogWrap: true}
	if err := SaveSettings(in); err != nil {
		t.Fatal(err)
	}
	out, err := LoadSettings()
	if err != nil {
		t.Fatal(err)
	}
	if out.Theme != "light" || out.LogTail != 500 || out.ErrSince != "24h" ||
		out.ErrTail != 50 || out.HTTPAddr != "127.0.0.1:9999" || out.MouseEnabled() || !out.LogWrap {
		t.Fatalf("round trip mismatch: %+v", out)
	}
	if _, err := os.Stat(filepath.Join(dir, "vpstui", "settings.json")); err != nil {
		t.Fatalf("settings file missing: %v", err)
	}
}

func TestSettingsCorruptAside(t *testing.T) {
	dir := settingsTestDir(t)
	p := filepath.Join(dir, "vpstui", "settings.json")
	if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("{not json"), 0600); err != nil {
		t.Fatal(err)
	}
	s, err := LoadSettings()
	if err == nil {
		t.Fatal("corrupt settings must report an error")
	}
	if s != DefaultSettings() && (s.Theme != "dark" || s.LogTail != defaultLogTail) {
		t.Fatalf("corrupt load returned %+v, want defaults", s)
	}
	if _, err := os.Stat(p + ".bad"); err != nil {
		t.Fatalf("corrupt file not renamed aside: %v", err)
	}
	// A later save must succeed over the aside.
	if err := SaveSettings(DefaultSettings()); err != nil {
		t.Fatalf("save after corrupt aside: %v", err)
	}
}

// TestSettingsResolutionOrder pins env > file > default: the loader returns
// file values, and EnvDefaults only fills variables the user has not set.
func TestSettingsResolutionOrder(t *testing.T) {
	dir := settingsTestDir(t)
	if err := os.MkdirAll(filepath.Join(dir, "vpstui"), 0700); err != nil {
		t.Fatal(err)
	}
	in := Settings{LogTail: 77, ErrTail: 78, ErrSince: "2h", HTTPAddr: "127.0.0.1:9753"}
	data, _ := json.Marshal(in)
	if err := os.WriteFile(filepath.Join(dir, "vpstui", "settings.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	s, err := LoadSettings()
	if err != nil {
		t.Fatal(err)
	}
	if s.LogTail != 77 || s.ErrTail != 78 {
		t.Fatalf("file values lost: %+v", s)
	}
	EnvDefaults(s)
	if got := os.Getenv("VPSTUI_LOG_TAIL"); got != "77" {
		t.Errorf("VPSTUI_LOG_TAIL=%q, want 77 (file beats default)", got)
	}
	// env wins: an explicitly set variable is never overwritten.
	t.Setenv("VPSTUI_ERR_TAIL", "999")
	EnvDefaults(s)
	if got := os.Getenv("VPSTUI_ERR_TAIL"); got != "999" {
		t.Errorf("VPSTUI_ERR_TAIL=%q, want 999 (env beats file)", got)
	}
	if got := os.Getenv("VPSTUI_HTTP_ADDR"); got != "127.0.0.1:9753" {
		t.Errorf("VPSTUI_HTTP_ADDR=%q, want the file value", got)
	}
}
