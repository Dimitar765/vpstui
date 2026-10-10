package secret

import (
	"os"
	"testing"
)

// The package caches keyring availability in the package-level keyringOK var
// and never re-probes it, so the whole suite lives in one test function that
// first pins the cache to "no keyring" and deterministically exercises the
// file fallback regardless of the machine it runs on.
func TestFileFallback(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	mu.Lock()
	off := false
	keyringOK = &off
	mu.Unlock()

	t.Run("set get delete round trip", func(t *testing.T) {
		if err := Set("alice:password", "s3cret"); err != nil {
			t.Fatalf("Set: %v", err)
		}
		got, err := Get("alice:password")
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if got != "s3cret" {
			t.Fatalf("Get = %q, want %q", got, "s3cret")
		}
		if err := Delete("alice:password"); err != nil {
			t.Fatalf("Delete: %v", err)
		}
		got, err = Get("alice:password")
		if err != nil {
			t.Fatalf("Get after Delete: %v", err)
		}
		if got != "" {
			t.Errorf("Get after Delete = %q, want empty", got)
		}
	})

	t.Run("set overwrites", func(t *testing.T) {
		if err := Set("k", "v1"); err != nil {
			t.Fatalf("Set v1: %v", err)
		}
		if err := Set("k", "v2"); err != nil {
			t.Fatalf("Set v2: %v", err)
		}
		got, err := Get("k")
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if got != "v2" {
			t.Errorf("Get = %q, want %q", got, "v2")
		}
		if err := Delete("k"); err != nil {
			t.Fatalf("Delete: %v", err)
		}
	})

	t.Run("empty value round trips", func(t *testing.T) {
		if err := Set("blank", ""); err != nil {
			t.Fatalf("Set: %v", err)
		}
		got, err := Get("blank")
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if got != "" {
			t.Errorf("Get = %q, want empty", got)
		}
		if err := Delete("blank"); err != nil {
			t.Fatalf("Delete: %v", err)
		}
	})

	t.Run("unicode and special characters", func(t *testing.T) {
		const val = "pässwörd \"quote\" '  spaces '"
		if err := Set("uni", val); err != nil {
			t.Fatalf("Set: %v", err)
		}
		got, err := Get("uni")
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if got != val {
			t.Errorf("Get = %q, want %q", got, val)
		}
		if err := Delete("uni"); err != nil {
			t.Fatalf("Delete: %v", err)
		}
	})

	t.Run("get missing returns empty nil", func(t *testing.T) {
		got, err := Get("does-not-exist")
		if err != nil {
			t.Fatalf("Get missing: %v", err)
		}
		if got != "" {
			t.Errorf("Get missing = %q, want empty", got)
		}
	})

	t.Run("delete missing is a no-op", func(t *testing.T) {
		if err := Delete("never-stored"); err != nil {
			t.Errorf("Delete missing: %v", err)
		}
	})

	t.Run("file permissions and atomicity", func(t *testing.T) {
		if err := Set("mode", "x"); err != nil {
			t.Fatalf("Set: %v", err)
		}
		p, err := filePath()
		if err != nil {
			t.Fatalf("filePath: %v", err)
		}
		fi, err := os.Stat(p)
		if err != nil {
			t.Fatalf("stat secrets.json: %v", err)
		}
		if perm := fi.Mode().Perm(); perm != 0600 {
			t.Errorf("secrets.json mode = %o, want 0600", perm)
		}
		if _, err := os.Stat(p + ".tmp"); !os.IsNotExist(err) {
			t.Errorf("secrets.json.tmp left behind, stat err = %v", err)
		}
		if err := Delete("mode"); err != nil {
			t.Fatalf("Delete: %v", err)
		}
	})
}
