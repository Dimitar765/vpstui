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
	Tag      string // the user's own label, "" when unlabelled
	TagColor string // palette name, see app.tagColors
	Image    string
	State    string
	Status   string
	CPUPerc  string
	MemPerc  string
	MemUsage string
	Networks []string // every attached network name, defaults included
}

// ErrorSnapshot is one log line the TUI kept during its error sweep, already
// classified and reduced to a signature. The API never re-classifies: the
// heuristics live in one place, so the TUI and the page cannot disagree about
// what counted as an error.
type ErrorSnapshot struct {
	ContainerID string
	Name        string
	Tag         string // the label of the container that produced the line
	Stamp       string
	At          time.Time
	Sev         string // "error" | "warn"
	Sig         string
	Text        string
}

// PerfSnapshot is one container's window, as the TUI measured it. The numbers
// are already ranked, flagged and downsampled, so the page draws this list in the
// order it arrives and computes nothing.
type PerfSnapshot struct {
	ID      string
	Name    string
	CPU     float64 // newest reading
	CPUAvg  float64
	CPUPeak float64
	Share   float64 // percent of the box, whichever side dominates, 0..100
	Spike   float64 // newest / mean
	Flags   []string
	Samples int

	MemBytes     int64
	MemPeakBytes int64

	// The all-time counters are nil when the host exposes no cgroup peak, or when
	// this container was not in the probe. Zero would be a claim, not an absence.
	AllTimeMemPeakBytes *int64
	CPUSeconds          *float64
	// CPUAvgCores is CPUSeconds over the container's own lifetime — cumulative
	// CPU is not a cost until it is a rate, and 64 minutes means something
	// different for a week-old container than for a ten-minute-old one. Nil when
	// the start time is unknown, because then the denominator is a guess.
	CPUAvgCores *float64
	Throttled   *int64
	Periods     *int64

	// Series is the window reduced to at most perf.MaxPoints CPU values, oldest
	// first, which is what bounds the payload.
	Series []float64
}

// PerfSection is one session's published performance window.
type PerfSection struct {
	Rows      []PerfSnapshot
	Window    time.Duration
	Retention time.Duration
	Samples   int // readings held, across every container
	ProbedAt  time.Time
	Supported bool // the host answered the cgroup probe with a real counter
}

// SessionSnapshot is the published view of one live session.
type SessionSnapshot struct {
	ID              string
	Name            string
	Target          string
	Connected       bool
	FetchedAt       time.Time
	HostStats       models.HostStats
	HasHostStats    bool
	Containers      []ContainerSnapshot
	Errors          []ErrorSnapshot
	ErrorsFetchedAt time.Time
	ErrorsTruncated bool
	Perf            *PerfSection
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

// GetIfOpen resolves id under one read lock, so a Close racing the request
// cannot turn a live session into a 404 (the old IsClosed-then-Get pair
// could). open distinguishes the 503 case from a plain miss.
func (s *Store) GetIfOpen(id string) (snap SessionSnapshot, open, ok bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed {
		return SessionSnapshot{}, false, false
	}
	snap, ok = s.sessions[id]
	return snap, true, ok
}

// AllIfOpen returns every snapshot under one read lock, with the same shape as
// GetIfOpen. callers own the returned slice.
func (s *Store) AllIfOpen() (snaps []SessionSnapshot, open bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed {
		return nil, false
	}
	out := make([]SessionSnapshot, 0, len(s.sessions))
	for _, snap := range s.sessions {
		out = append(out, snap)
	}
	return out, true
}

// Close marks the store closed so late requests return 503. Non-blocking.
func (s *Store) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	s.sessions = nil
}
