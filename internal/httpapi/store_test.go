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

	got, open, ok := s.GetIfOpen("a")
	if !open || !ok || got.Name != "alpha" {
		t.Fatalf("GetIfOpen(a) = %v, %v, %v; want alpha, true, true", got, open, ok)
	}
	if all, open := s.AllIfOpen(); !open || len(all) != 1 {
		t.Fatalf("AllIfOpen = %d snapshots, open %v; want 1, true", len(all), open)
	}
	s.Unpublish("a")
	if _, open, ok := s.GetIfOpen("a"); !open || ok {
		t.Fatalf("GetIfOpen(a) after Unpublish = open %v ok %v; want true, false", open, ok)
	}
	if all, open := s.AllIfOpen(); !open || len(all) != 0 {
		t.Fatalf("AllIfOpen after unpublish = %d snapshots, open %v; want 0, true", len(all), open)
	}
}

func TestStoreCloseRejectsPublishAndMarksClosed(t *testing.T) {
	s := NewStore()
	s.Publish("a", SessionSnapshot{ID: "a"})
	s.Close()
	if _, open, _ := s.GetIfOpen("a"); open {
		t.Fatal("GetIfOpen reports open after Close")
	}
	if _, open := s.AllIfOpen(); open {
		t.Fatal("AllIfOpen reports open after Close")
	}
	s.Publish("b", SessionSnapshot{ID: "b"})
	if all, open := s.AllIfOpen(); open || len(all) != 0 {
		t.Fatal("Publish after Close stored a snapshot")
	}
}

func TestStoreConcurrentAccess(t *testing.T) {
	s := NewStore()
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(2)
		go func() {
			defer wg.Done()
			for range 100 {
				s.Publish("x", SessionSnapshot{ID: "x", FetchedAt: time.Now()})
			}
		}()
		go func() {
			defer wg.Done()
			for range 100 {
				_, _, _ = s.GetIfOpen("x")
				_, _ = s.AllIfOpen()
			}
		}()
	}
	wg.Wait()
}
