// Package queue is the deploy worker pool: an unbuffered channel of
// deployment ids drained by N workers. Durability lives one layer up —
// Enqueue callers insert the deployment row (status queued) BEFORE signaling,
// so a daemon crash loses at most the signal, never the record; boot-time
// crash recovery fails those rows.
package queue

import (
	"context"
	"sync"
)

type Queue struct {
	ch     chan string
	fn     func(ctx context.Context, id string)
	wg     sync.WaitGroup
	cancel context.CancelFunc
}

// New starts n workers calling fn for every enqueued id, in order of
// arrival. fn must be safe for concurrent use (the engine serializes per
// app itself).
func New(n int, fn func(ctx context.Context, id string)) *Queue {
	ctx, cancel := context.WithCancel(context.Background())
	q := &Queue{ch: make(chan string), fn: fn, cancel: cancel}
	q.wg.Add(n)
	for range n {
		go func() {
			defer q.wg.Done()
			for id := range q.ch {
				fn(ctx, id)
			}
		}()
	}
	return q
}

// Enqueue signals a worker. Never blocks indefinitely: the channel is
// effectively unbuffered capacity, so a caller contending with two busy
// workers waits its turn — deployment rows are already durable.
func (q *Queue) Enqueue(id string) {
	q.ch <- id
}

// Close stops accepting work and waits for in-flight deployments to drain.
func (q *Queue) Close() {
	q.cancel()
	close(q.ch)
	q.wg.Wait()
}
