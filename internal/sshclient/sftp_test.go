package sshclient

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// TestSFTPRoundTrip dials the in-process SSH server, opens an SFTP client and
// uploads then downloads a file — the exact moves the deploy engine's
// WriteFileAtomic/UploadDir make against a real server.
func TestSFTPRoundTrip(t *testing.T) {
	ts := startTestServer(t, "password", func(cmd string) (string, string, uint32) { return "", "", 0 })
	ctx, cancel := dialCtx()
	defer cancel()

	conn, err := Dial(ctx, ts.host(), "pw")
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { conn.Close() })

	cl, err := conn.SFTP()
	if err != nil {
		t.Fatalf("sftp: %v", err)
	}
	t.Cleanup(func() { cl.Close() })

	dir := t.TempDir()
	remote := filepath.Join(dir, "compose.yaml")

	payload := []byte("services:\n  app:\n    image: nginx\n")
	f, err := cl.OpenFile(remote, os.O_WRONLY|os.O_CREATE|os.O_TRUNC)
	if err != nil {
		t.Fatalf("open remote: %v", err)
	}
	if _, err := f.Write(payload); err != nil {
		t.Fatalf("write remote: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close remote: %v", err)
	}
	if err := cl.Chmod(remote, 0o600); err != nil {
		t.Fatalf("chmod: %v", err)
	}

	got, err := os.ReadFile(remote)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("round trip mismatch: got %q", got)
	}

	info, err := os.Stat(remote)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v, want 0600", info.Mode().Perm())
	}
	if err := cl.Remove(remote); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if _, err := os.Stat(remote); !os.IsNotExist(err) {
		t.Fatalf("file still present after remove: %v", err)
	}
}
