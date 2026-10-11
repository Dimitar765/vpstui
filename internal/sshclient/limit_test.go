package sshclient

import (
	"errors"
	"io"
	"strings"
	"testing"
)

func TestLimitWriterUnderCap(t *testing.T) {
	w := newLimitWriter(64)
	if n, err := w.Write([]byte("hello")); err != nil || n != 5 {
		t.Fatalf("Write = (%d, %v), want (5, nil)", n, err)
	}
	if got := w.String(); got != "hello" {
		t.Fatalf("String = %q, want %q", got, "hello")
	}
	if w.full {
		t.Fatal("full set below cap")
	}
}

func TestLimitWriterStopsAtCap(t *testing.T) {
	const cap = 10
	w := newLimitWriter(cap)
	big := strings.Repeat("a", 25)
	if n, err := w.Write([]byte(big)); err != nil || n != len(big) {
		t.Fatalf("Write = (%d, %v), want (%d, nil)", n, err, len(big))
	}
	if got := w.String(); got != big[:cap] {
		t.Fatalf("String = %q, want %d-byte prefix", got, cap)
	}
	if !w.full {
		t.Fatal("full not set after overflow")
	}

	// Further writes are discarded but still reported as fully consumed.
	if n, err := w.Write([]byte("more")); err != nil || n != 4 {
		t.Fatalf("Write after full = (%d, %v), want (4, nil)", n, err)
	}
	if got := w.String(); len(got) != cap {
		t.Fatalf("String len = %d, want %d", len(got), cap)
	}
}

func TestLimitWriterExactFill(t *testing.T) {
	w := newLimitWriter(4)
	w.Write([]byte("abcd"))
	if w.full {
		t.Fatal("full set at exactly cap without overflow")
	}
	w.Write([]byte("x"))
	if !w.full {
		t.Fatal("full not set after overflowing write")
	}
	if got := w.String(); got != "abcd" {
		t.Fatalf("String = %q, want %q", got, "abcd")
	}
}

// The app layer keys the auto-retry on errors.Is(err, ErrConnLost), so the
// wrapping contract is: open conn passes foreign errors through, transport-
// shaped errors are typed, and a closed conn types everything — with the
// cause text preserved.
func TestConnLostWrapping(t *testing.T) {
	c := &Conn{}
	if err := c.connLost(nil); err != nil {
		t.Fatalf("connLost(nil) = %v", err)
	}
	plain := errors.New("exit status 1")
	if got := c.connLost(plain); got != plain {
		t.Fatalf("open conn must pass foreign errors through, got %v", got)
	}
	for _, shape := range []error{
		io.EOF,
		errors.New("write: broken pipe"),
		errors.New("use of closed network connection"),
		errors.New("ssh: connection lost"),
	} {
		got := c.connLost(shape)
		if !errors.Is(got, ErrConnLost) {
			t.Errorf("connLost(%v) not typed as ErrConnLost", shape)
		}
		if !strings.Contains(got.Error(), shape.Error()) {
			t.Errorf("wrapped error lost the cause text: %v", got)
		}
	}

	c.closed.Store(true)
	got := c.connLost(plain)
	if !errors.Is(got, ErrConnLost) {
		t.Error("closed conn must type every error as ErrConnLost")
	}
	if !strings.Contains(got.Error(), "exit status 1") {
		t.Errorf("wrapped error lost the cause text: %v", got)
	}
}
