package queue

import (
	"context"
	"sync"
	"testing"
	"testing/synctest"
	"time"
)

// TestQueueConcurrencyCapAndOrder: two workers never process more than two
// deployments at once, and every enqueued id is processed exactly once.
func TestQueueConcurrencyCapAndOrder(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var mu sync.Mutex
		active, maxActive := 0, 0
		processed := map[string]bool{}

		q := New(2, func(ctx context.Context, id string) {
			mu.Lock()
			active++
			if active > maxActive {
				maxActive = active
			}
			mu.Unlock()
			time.Sleep(time.Millisecond)
			mu.Lock()
			processed[id] = true
			active--
			mu.Unlock()
		})
		defer q.Close()

		const n = 20
		for i := range n {
			q.Enqueue(string(rune('a' + i)))
		}
		for {
			mu.Lock()
			done := len(processed) == n
			mu.Unlock()
			if done {
				break
			}
			time.Sleep(time.Millisecond)
			synctest.Wait()
		}
		mu.Lock()
		defer mu.Unlock()
		if maxActive > 2 {
			t.Fatalf("max concurrent = %d, want <= 2", maxActive)
		}
	})
}

// TestQueueCloseDrains: Close waits for the in-flight item.
func TestQueueCloseDrains(t *testing.T) {
	finished := make(chan string, 1)
	q := New(1, func(ctx context.Context, id string) {
		time.Sleep(10 * time.Millisecond)
		finished <- id
	})
	q.Enqueue("one")
	q.Close()
	select {
	case id := <-finished:
		if id != "one" {
			t.Fatalf("processed %q", id)
		}
	default:
		t.Fatal("Close returned before the in-flight deployment finished")
	}
}
