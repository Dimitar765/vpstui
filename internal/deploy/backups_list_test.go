package deploy

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestListBackupsOrdersNewestFirst drives the SFTP directory read through the
// in-process server against real files.
func TestListBackupsOrdersNewestFirst(t *testing.T) {
	cl := pipeSFTP(t)
	dir := t.TempDir()
	base := time.Now().UTC().Truncate(time.Second)
	files := map[string]time.Time{
		"20260101-000000.dump": base.Add(-48 * time.Hour),
		"20260103-000000.dump": base,
		"20260102-000000.dump": base.Add(-24 * time.Hour),
	}
	for name, mt := range files {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte("dumpdata"), 0o640); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(p, mt, mt); err != nil {
			t.Fatal(err)
		}
	}
	// A subdirectory must be skipped.
	if err := os.Mkdir(filepath.Join(dir, "sub"), 0o750); err != nil {
		t.Fatal(err)
	}

	got, err := listBackups(cl, dir)
	if err != nil {
		t.Fatalf("listBackups: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("entries = %+v", got)
	}
	if got[0].Name != "20260103-000000.dump" || got[1].Name != "20260102-000000.dump" || got[2].Name != "20260101-000000.dump" {
		t.Fatalf("order = %s,%s,%s", got[0].Name, got[1].Name, got[2].Name)
	}
	if got[0].Size != int64(len("dumpdata")) {
		t.Fatalf("size = %d", got[0].Size)
	}
	if !got[0].Modified.Equal(base) {
		t.Fatalf("modified = %v, want %v", got[0].Modified, base)
	}
}

// TestListBackupsMissingDir: a database with no dumps yet yields an empty
// list, not an error.
func TestListBackupsMissingDir(t *testing.T) {
	cl := pipeSFTP(t)
	got, err := listBackups(cl, filepath.Join(t.TempDir(), "nope"))
	if err != nil {
		t.Fatalf("missing dir: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("entries = %+v", got)
	}
}
