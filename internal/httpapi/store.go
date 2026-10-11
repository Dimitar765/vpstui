package httpapi

import (
	"sync"
	"time"

	"vpstui/internal/models"
)

// ContainerSnapshot is one container with its latest stats merged in.
type ContainerSnapshot struct {
	ID       string
	Name     string
	Image    string
	State    string
	Status   string
	CPUPerc  string
	MemPerc  string
	MemUsage string
	Networks []string // every attached network name, defaults included
}

// SessionSnapshot is the published view of one live session.
type SessionSnapshot struct {
	ID           string
	Name         string
	Target       string
	Connected    bool
	FetchedAt    time.Time
	HostStats    models.HostStats
	HasHostStats bool
	Containers   []ContainerSnapshot
}

// Store holds the snapshots the TUI publishes for the HTTP handlers to read.
// The Update goroutine writes; handler goroutines only read.
type Store struct {
	mu       sync.RWMutex
	sessions map[string]SessionSnapshot
	closed   bool
}

func NewStore() *Store {
	return &Store{sessions: map[string]SessionSnapshot{}}
}

// Publish records (or replaces) the snapshot for id.
func (s *Store) Publish(id string, snap SessionSnapshot) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	s.sessions[id] = snap
}

// Unpublish drops id, so a closed or reconnected session leaves no stale row.
func (s *Store) Unpublish(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.sessions, id)
}

// Get returns the snapshot for id.
func (s *Store) Get(id string) (SessionSnapshot, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	snap, ok := s.sessions[id]
	return snap, ok
}

// All returns every snapshot; callers own the returned slice.
func (s *Store) All() []SessionSnapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]SessionSnapshot, 0, len(s.sessions))
	for _, snap := range s.sessions {
		out = append(out, snap)
	}
	return out
}

// IsClosed reports whether the store was closed by the TUI exiting.
func (s *Store) IsClosed() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.closed
}

// Close marks the store closed so late requests return 503. Non-blocking.
func (s *Store) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	s.sessions = nil
}
