// Package errsample picks the sample lines that represent one error signature.
// The TUI's y/Y and the web page's copy buttons must put the same text on the
// clipboard for the same corpus, so the choice and its tie-break live here once
// instead of in two comparators that can drift apart.
package errsample

import (
	"sort"
	"time"
)

// Cap is how many samples one group keeps, and therefore how many lines a
// pasted group can carry besides its header.
const Cap = 20

// Key totally orders two lines that share an instant, so which samples survive
// never depends on the order hosts answered in. Every field is a plain string:
// the same comparison works on a TUI log line and on a published sample.
type Key struct {
	Text      string
	Container string
	Host      string
	Stamp     string
}

func (k Key) Less(o Key) bool {
	if k.Text != o.Text {
		return k.Text < o.Text
	}
	if k.Container != o.Container {
		return k.Container < o.Container
	}
	if k.Host != o.Host {
		return k.Host < o.Host
	}
	return k.Stamp < o.Stamp
}

// Entry is one candidate sample: the line, when it happened, and how to break a
// tie with another line from a different host.
type Entry[T any] struct {
	At    time.Time
	Key   Key
	Value T
}

// Newest returns the n most recent entries, ties broken by Key, oldest first —
// so a pasted group reads like a log and its last element is the newest line,
// which is also the single sample a row shows. n <= 0 keeps everything.
func Newest[T any](n int, entries []Entry[T]) []T {
	if len(entries) == 0 {
		return nil
	}
	sorted := make([]Entry[T], len(entries))
	copy(sorted, entries)
	sort.SliceStable(sorted, func(i, j int) bool {
		if !sorted[i].At.Equal(sorted[j].At) {
			return sorted[i].At.After(sorted[j].At)
		}
		return sorted[i].Key.Less(sorted[j].Key)
	})
	if n > 0 && len(sorted) > n {
		sorted = sorted[:n]
	}
	out := make([]T, 0, len(sorted))
	for i := len(sorted) - 1; i >= 0; i-- {
		out = append(out, sorted[i].Value)
	}
	return out
}
