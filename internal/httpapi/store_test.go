package httpapi

import (
	"sync"
	"testing"
	"time"
)

func TestStorePublishGetAllUnpublish(t *testing.T) {
	s := NewStore()
	snap := SessionSnapshot{ID: "a", Name: "alpha", FetchedAt: time.Unix(1, 0)}
	s.Publish("a", snap)

	got, ok := s.Get("a")
	if !ok || got.Name != "alpha" {
		t.Fatalf("Get(a) = %v, %v; want alpha, true", got, ok)
	}
	if all := s.All(); len(all) != 1 {
		t.Fatalf("All len = %d; want 1", len(all))
	}
	s.Unpublish("a")
	if _, ok := s.Get("a"); ok {
		t.Fatal("Get(a) after Unpublish returned ok")
	}
	if all := s.All(); len(all) != 0 {
		t.Fatalf("All len after unpublish = %d; want 0", len(all))
	}
}

func TestStoreCloseRejectsPublishAndMarksClosed(t *testing.T) {
	s := NewStore()
	s.Publish("a", SessionSnapshot{ID: "a"})
	s.Close()
	if !s.IsClosed() {
		t.Fatal("IsClosed false after Close")
	}
	s.Publish("b", SessionSnapshot{ID: "b"})
	if len(s.All()) != 0 {
		t.Fatal("Publish after Close stored a snapshot")
	}
}

func TestStoreConcurrentAccess(t *testing.T) {
	s := NewStore()
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				s.Publish("x", SessionSnapshot{ID: "x", FetchedAt: time.Now()})
			}
		}()
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				_ = s.All()
				_, _ = s.Get("x")
			}
		}()
	}
	wg.Wait()
}
