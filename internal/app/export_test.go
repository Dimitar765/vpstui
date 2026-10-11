package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExportLogsWritesFile(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())

	m := New(nil, nil)
	s := &Session{host: host("a"), gen: 1}
	s.dash = NewDashboard(s.host, nil, 80, 24, s.gen)
	s.cv = NewContainerView("8f30766c1db2", "web-1", 80, 24)
	s.cv.hostID, s.cv.gen = s.host.ID, s.gen
	s.cv.Append("one")
	s.cv.Append("two")
	m.sessions = []*Session{s}
	m.active = 0
	m.view = viewContainer

	_, cmd := m.Update(key("E"))
	if cmd == nil {
		t.Fatal("E produced no export command")
	}
	if !s.cv.exporting || s.cv.note != "exporting…" {
		t.Fatalf("exporting=%v note=%q", s.cv.exporting, s.cv.note)
	}

	done, ok := cmd().(exportDoneMsg)
	if !ok || done.err != nil || done.path == "" {
		t.Fatalf("cmd() = %+v, want a path-bearing exportDoneMsg", done)
	}
	data, err := os.ReadFile(done.path)
	if err != nil {
		t.Fatalf("read %s: %v", done.path, err)
	}
	if string(data) != "one\ntwo\n" {
		t.Fatalf("file = %q, want %q", data, "one\ntwo\n")
	}
	wantDir := filepath.Join(os.Getenv("XDG_DATA_HOME"), "vpstui", "logs")
	if filepath.Dir(done.path) != wantDir {
		t.Fatalf("file at %q, want under %q", done.path, wantDir)
	}
	if base := filepath.Base(done.path); !strings.HasPrefix(base, "a-web-1-") {
		t.Fatalf("filename %q does not start with host-container", base)
	}

	m.Update(done)
	if s.cv.exporting {
		t.Fatal("exporting not cleared by exportDoneMsg")
	}
	if !strings.Contains(s.cv.note, "saved:") || !strings.Contains(s.cv.note, done.path) {
		t.Fatalf("note = %q, want saved: <path>", s.cv.note)
	}
}

func TestExportLogsEmptyIsANoop(t *testing.T) {
	m := New(nil, nil)
	s := &Session{host: host("a"), gen: 1}
	s.cv = NewContainerView("8f30766c1db2", "web-1", 80, 24)
	s.cv.hostID, s.cv.gen = s.host.ID, s.gen
	m.sessions = []*Session{s}
	m.active = 0
	m.view = viewContainer

	_, cmd := m.Update(key("E"))
	if cmd != nil {
		t.Fatal("empty buffer produced an export command")
	}
	if s.cv.note != "nothing to export" {
		t.Fatalf("note = %q, want nothing to export", s.cv.note)
	}
}

func TestSanitizeFileToken(t *testing.T) {
	tests := []struct{ in, want string }{
		{"web_1", "web_1"},
		{"a b/c", "a_b_c"},
		{"", "container"},
		{"höst:22", "h_st_22"},
	}
	for _, tt := range tests {
		if got := sanitizeFileToken(tt.in); got != tt.want {
			t.Errorf("sanitizeFileToken(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}
