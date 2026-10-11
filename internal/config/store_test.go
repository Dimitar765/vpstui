package config

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"vpstui/internal/models"
)

func sampleHosts() []models.Host {
	return []models.Host{
		{ID: "abc123", Name: "vps-1", Address: "203.0.113.7", Port: 22, User: "root", AuthType: models.AuthPassword},
		{ID: "def456", Name: "box two", Address: "host.example.com", Port: 2222, User: "deploy", AuthType: models.AuthKey, KeyPath: "~/.ssh/id_ed25519"},
	}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	want := sampleHosts()
	if err := Save(want); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(got) != len(want) {
		t.Fatalf("loaded %d hosts, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("host %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestSaveOverwrites(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	first := sampleHosts()[:1]
	if err := Save(first); err != nil {
		t.Fatalf("Save first: %v", err)
	}
	second := []models.Host{{ID: "z", Name: "replaced", Address: "10.0.0.1", Port: 22, User: "u", AuthType: models.AuthAgent}}
	if err := Save(second); err != nil {
		t.Fatalf("Save second: %v", err)
	}
	got, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(got) != 1 || got[0] != second[0] {
		t.Errorf("after overwrite Load() = %+v, want %+v", got, second)
	}
}

func TestSaveNilHosts(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	if err := Save(nil); err != nil {
		t.Fatalf("Save(nil): %v", err)
	}
	got, err := Load()
	if err != nil {
		t.Fatalf("Load after Save(nil): %v", err)
	}
	if len(got) != 0 {
		t.Errorf("Load() = %+v, want empty", got)
	}
}

func TestLoadMissingFile(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	got, err := Load()
	if err != nil {
		t.Fatalf("Load on missing file: %v", err)
	}
	if got != nil {
		t.Errorf("Load on missing file = %+v, want nil", got)
	}
}

// A corrupt file must never be silently destroyed by the next Save: Load moves
// it aside as <path>.bad, names that in the error, and Save writes fresh.
func TestLoadCorruptFileIsRenamedAsideNotOverwritten(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	p, err := hostsPath()
	if err != nil {
		t.Fatalf("hostsPath: %v", err)
	}
	garbage := []byte("this is not json at all")
	if err := os.WriteFile(p, garbage, 0600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "renamed") {
		t.Fatalf("Load error = %v, want it to mention renamed", err)
	}
	bad, err := os.ReadFile(p + ".bad")
	if err != nil {
		t.Fatalf("ReadFile(.bad): %v", err)
	}
	if !bytes.Equal(bad, garbage) {
		t.Fatalf(".bad = %q, want the original bytes %q", bad, garbage)
	}
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Fatalf("corrupt file still present after Load (stat err = %v)", err)
	}
	if err := Save(sampleHosts()); err != nil {
		t.Fatalf("Save: %v", err)
	}
	after, err := os.ReadFile(p + ".bad")
	if err != nil {
		t.Fatalf(".bad vanished after Save: %v", err)
	}
	if !bytes.Equal(after, garbage) {
		t.Fatalf(".bad changed after Save: %q", after)
	}
	if _, err := Load(); err != nil {
		t.Fatalf("Load after fresh Save: %v", err)
	}
}

func TestLoadCorruptJSON(t *testing.T) {
	tests := []struct {
		name string
		data string
	}{
		{"not json", "this is not json at all"},
		{"truncated array", `[{"id":"a","name":`},
		{"wrong shape", `{"hosts": []}`},
		{"empty object", `{}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("XDG_CONFIG_HOME", t.TempDir())

			p, err := hostsPath()
			if err != nil {
				t.Fatalf("hostsPath: %v", err)
			}
			if err := os.WriteFile(p, []byte(tt.data), 0600); err != nil {
				t.Fatalf("WriteFile: %v", err)
			}
			hosts, err := Load()
			if err == nil {
				t.Fatalf("Load on corrupt data (%q) returned no error, hosts=%+v", tt.data, hosts)
			}
			if !strings.Contains(err.Error(), "corrupt") {
				t.Errorf("error = %v, want it to mention corrupt", err)
			}
			if hosts != nil {
				t.Errorf("hosts = %+v, want nil on error", hosts)
			}
		})
	}
}

func TestSaveAtomicityAndPermissions(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)

	if err := Save(sampleHosts()); err != nil {
		t.Fatalf("Save: %v", err)
	}

	configDir := filepath.Join(dir, "vpstui")

	// The temporary file must not survive a successful Save.
	if _, err := os.Stat(filepath.Join(configDir, "hosts.json.tmp")); !os.IsNotExist(err) {
		t.Errorf("hosts.json.tmp left behind, stat err = %v", err)
	}

	// The final file must exist with 0600 permissions.
	fi, err := os.Stat(filepath.Join(configDir, "hosts.json"))
	if err != nil {
		t.Fatalf("stat hosts.json: %v", err)
	}
	if perm := fi.Mode().Perm(); perm != 0600 {
		t.Errorf("hosts.json mode = %o, want 0600", perm)
	}
}

func TestDirPermissions(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	dir, err := Dir()
	if err != nil {
		t.Fatalf("Dir: %v", err)
	}
	fi, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat dir: %v", err)
	}
	if !fi.IsDir() {
		t.Fatalf("Dir() %q is not a directory", dir)
	}
	if perm := fi.Mode().Perm(); perm != 0700 {
		t.Errorf("config dir mode = %o, want 0700", perm)
	}
}

func TestDataDir(t *testing.T) {
	base := t.TempDir()
	t.Setenv("XDG_DATA_HOME", base)

	dir, err := DataDir()
	if err != nil {
		t.Fatalf("DataDir: %v", err)
	}
	if want := filepath.Join(base, "vpstui"); dir != want {
		t.Fatalf("DataDir() = %q, want %q", dir, want)
	}
	logs := filepath.Join(dir, "logs")
	fi, err := os.Stat(logs)
	if err != nil {
		t.Fatalf("stat logs dir: %v", err)
	}
	if !fi.IsDir() {
		t.Fatalf("logs path %q is not a directory", logs)
	}
	if perm := fi.Mode().Perm(); perm != 0700 {
		t.Errorf("logs dir mode = %o, want 0700", perm)
	}
}

func TestDataDirHomeFallback(t *testing.T) {
	home := t.TempDir()
	t.Setenv("XDG_DATA_HOME", "")
	t.Setenv("HOME", home)

	dir, err := DataDir()
	if err != nil {
		t.Fatalf("DataDir: %v", err)
	}
	if want := filepath.Join(home, ".local", "share", "vpstui"); dir != want {
		t.Fatalf("DataDir() = %q, want %q", dir, want)
	}
	if _, err := os.Stat(filepath.Join(dir, "logs")); err != nil {
		t.Fatalf("logs dir not created: %v", err)
	}
}
