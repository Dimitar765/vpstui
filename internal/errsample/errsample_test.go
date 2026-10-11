package errsample

import (
	"testing"
	"time"
)

// spec is one corpus line in the shape a test wants to write it: a second
// offset, a text, a container. Value identity is the text and container joined,
// so a reordering is visible without threading an id through the ordering keys.
type spec struct {
	at   int64
	text string
	cont string
}

func entries(in ...spec) []Entry[string] {
	out := make([]Entry[string], 0, len(in))
	for _, s := range in {
		out = append(out, Entry[string]{
			At:    time.Unix(s.at, 0).UTC(),
			Key:   Key{Text: s.text, Container: s.cont},
			Value: s.text + "@" + s.cont,
		})
	}
	return out
}

func TestNewest(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		n    int
		in   []Entry[string]
		want []string
	}{
		{"empty", 3, nil, nil},
		{"unordered input comes out oldest first", 0, entries(
			spec{3, "c", "x"}, spec{1, "a", "x"}, spec{2, "b", "x"},
		), []string{"a@x", "b@x", "c@x"}},
		{"cap keeps the newest and drops the oldest", 2, entries(
			spec{1, "a", "x"}, spec{2, "b", "x"}, spec{3, "c", "x"},
		), []string{"b@x", "c@x"}},
		{"equal instants break by key, not by arrival", 3, entries(
			spec{5, "z", "beta"}, spec{5, "a", "alpha"}, spec{5, "m", "alpha"}, spec{1, "old", "x"},
		), []string{"z@beta", "m@alpha", "a@alpha"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := Newest(tc.n, tc.in)
			if len(got) != len(tc.want) {
				t.Fatalf("got %d entries, want %d: %v", len(got), len(tc.want), got)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("entry %d = %q, want %q", i, got[i], tc.want[i])
				}
			}
		})
	}
}

// TestNewestLastIsSingleSample pins the invariant the API relies on: the one
// line a row shows is the last of its Examples, so y and Y never disagree.
func TestNewestLastIsSingleSample(t *testing.T) {
	t.Parallel()

	out := Newest(Cap, entries(spec{1, "old", "x"}, spec{9, "new", "x"}))
	if len(out) != 2 || out[len(out)-1] != "new@x" {
		t.Fatalf("newest last broken: %v", out)
	}
}

// TestNewestLeavesInputAlone guards the corpus, which is read again on every
// repaint: a selection that sorted in place would change what the TUI shows.
func TestNewestLeavesInputAlone(t *testing.T) {
	t.Parallel()

	in := entries(spec{1, "a", "x"}, spec{7, "b", "x"})
	Newest(1, in)
	if in[0].Value != "a@x" || in[1].Value != "b@x" {
		t.Fatalf("input reordered: %v", in)
	}
}
