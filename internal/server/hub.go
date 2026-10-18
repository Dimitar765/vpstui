package server

import (
	"encoding/json"
	"sync"
)

// Hub fans frames out to every live WS client. Broadcast marshals once and
// never blocks: a slow GUI or TUI drops frames rather than stalling the
// monitor that produced them.
type Hub struct {
	mu   sync.Mutex
	subs map[chan []byte]struct{}
}

// NewHub builds an empty broadcaster.
func NewHub() *Hub {
	return &Hub{subs: map[chan []byte]struct{}{}}
}

// Subscribe registers a client. The cancel func unregisters and closes the
// channel; it is safe to call more than once.
func (h *Hub) Subscribe() (<-chan []byte, func()) {
	ch := make(chan []byte, 64)
	h.mu.Lock()
	h.subs[ch] = struct{}{}
	h.mu.Unlock()
	cancel := func() {
		h.mu.Lock()
		if _, ok := h.subs[ch]; ok {
			delete(h.subs, ch)
			close(ch)
		}
		h.mu.Unlock()
	}
	return ch, cancel
}

// Broadcast marshals frame once and delivers it to every subscriber.
func (h *Hub) Broadcast(frame any) {
	b, err := json.Marshal(frame)
	if err != nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch := range h.subs {
		select {
		case ch <- b:
		default: // slow client: drop rather than block the producer
		}
	}
}
