package config

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"vpstui/internal/models"
)

func sampleTags() map[string]models.Tag {
	return map[string]models.Tag{
		TagKey("h1", "odysseus-postfix-1"): {HostID: "h1", Name: "odysseus-postfix-1", Tag: "mail", Color: "sky", ID: "11f4c9"},
		TagKey("h2", "web"):                {HostID: "h2", Name: "web", Tag: "front", Color: "violet", ID: "aa11bb"},
	}
}

func TestTagKeyShape(t *testing.T) {
	if got := TagKey("h1", "web"); got != "h1/web" {
		t.Errorf("TagKey = %q, want h1/web", got)
	}
	// The host id, not the host name, is the first half: renaming a host in the
	// table must not orphan the labels written for it.
	if TagKey("a", "web") == TagKey("b", "web") {
		t.Fatal("different hosts share a tag key")
	}
}

func TestSaveLoadTagsRoundTrip(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	want := sampleTags()
	if err := SaveTags(want); err != nil {
		t.Fatalf("SaveTags: %v", err)
	}
	got, err := LoadTags()
	if err != nil {
		t.Fatalf("LoadTags: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("round trip changed the map:\n got %+v\nwant %+v", got, want)
	}
}

func TestLoadTagsMissingFile(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	got, err := LoadTags()
	if err != nil {
		t.Fatalf("a missing tags file is the normal case, got error: %v", err)
	}
	if got != nil {
		t.Errorf("LoadTags = %+v, want nil", got)
	}
}

func TestSaveTagsEmptyIsNotAnError(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	for _, m := range []map[string]models.Tag{nil, {}} {
		if err := SaveTags(m); err != nil {
			t.Fatalf("SaveTags(%v): %v", m, err)
		}
	}
	got, err := LoadTags()
	if err != nil {
		t.Fatalf("LoadTags after empty save: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("LoadTags = %+v, want empty", got)
	}
}

func TestLoadTagsCorruptFile(t *testing.T) {
	tests := []struct {
		name string
		data string
	}{
		{"not json", "this is not json"},
		{"truncated object", `{"a":{"tag":`},
		{"wrong shape", `["mail"]`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("XDG_CONFIG_HOME", t.TempDir())
			p, err := tagsPath()
			if err != nil {
				t.Fatalf("tagsPath: %v", err)
			}
			if err := os.WriteFile(p, []byte(tt.data), 0600); err != nil {
				t.Fatalf("write: %v", err)
			}
			got, err := LoadTags()
			if err == nil {
				t.Fatalf("corrupt file loaded as %+v", got)
			}
			if !strings.Contains(err.Error(), "corrupt tags file") {
				t.Errorf("error %q does not name the file it could not read", err)
			}
		})
	}
}

// The file is hand-editable, so its bytes have to be stable: json sorts map keys,
// which is what makes a re-save of unchanged state a no-op on disk. Go randomises
// map iteration order, so saving the same map twice is the whole test.
func TestSaveTagsByteStable(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	p, err := tagsPath()
	if err != nil {
		t.Fatalf("tagsPath: %v", err)
	}

	tags := sampleTags()
	if err := SaveTags(tags); err != nil {
		t.Fatalf("SaveTags: %v", err)
	}
	A, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if err := SaveTags(tags); err != nil {
		t.Fatalf("SaveTags again: %v", err)
	}
	B, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !bytes.Equal(A, B) {
		t.Errorf("two saves of unchanged state differ:\n%s\n---\n%s", A, B)
	}
	if !strings.HasSuffix(string(B), "}\n") {
		t.Errorf("tags.json must end with a newline, got %q", string(B)[max(0, len(B)-8):])
	}
}

func TestSaveTagsAtomicAndPrivate(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)

	if err := SaveTags(sampleTags()); err != nil {
		t.Fatalf("SaveTags: %v", err)
	}
	p, err := tagsPath()
	if err != nil {
		t.Fatalf("tagsPath: %v", err)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(p), "tags.json.tmp")); !os.IsNotExist(err) {
		t.Errorf("tags.json.tmp left behind, stat err = %v", err)
	}
	fi, err := os.Stat(p)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := fi.Mode().Perm(); perm != 0600 {
		t.Errorf("tags.json mode = %o, want 600", perm)
	}
}
