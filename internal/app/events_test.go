package app

import (
	"testing"

	"vpstui/internal/docker"
)

func eventsModel(t *testing.T) (*Model, *Session) {
	t.Helper()
	m := New(nil, nil)
	s := &Session{host: host("a"), gen: 1}
	s.dock = docker.New(nil)
	s.dash = NewDashboard(s.host, s.dock, 80, 24, s.gen)
	m.sessions = []*Session{s}
	m.active = 0
	m.view = viewDashboard
	return m, s
}

func TestEventsArmAndCoalesce(t *testing.T) {
	m, s := eventsModel(t)

	// The first successful container list arms exactly one events stream.
	_, cmd := m.Update(statsLoadedMsg{msgTag: msgTag{hostID: "a", gen: 1}, listOK: true})
	if !s.dash.evArmed {
		t.Fatal("first listOK did not arm the events stream")
	}
	if cmd == nil {
		t.Fatal("arming produced no batch (startEvents command lost)")
	}
	// A second listOK must not re-arm.
	_, cmd2 := m.Update(statsLoadedMsg{msgTag: msgTag{hostID: "a", gen: 1}, listOK: true})
	if cmd2 != nil {
		t.Fatal("second listOK re-armed or emitted a stray command")
	}

	// While a snapshot is in flight, an event only sets the pending flag.
	s.dash.fetchBusy = true
	_, cmd = m.Update(eventsMsg{msgTag: msgTag{hostID: "a", gen: 1}, ev: docker.Event{Action: "die", ID: "8f30766c1db2", Name: "web"}})
	if cmd != nil {
		t.Fatal("event during in-flight fetch produced a command")
	}
	if !s.dash.evPending {
		t.Fatal("event during in-flight fetch did not set evPending")
	}

	// The next statsLoadedMsg flushes the pending burst as one refetch.
	_, cmd = m.Update(statsLoadedMsg{msgTag: msgTag{hostID: "a", gen: 1}, listOK: true})
	if s.dash.evPending {
		t.Fatal("evPending not cleared by the flush")
	}
	if cmd == nil {
		t.Fatal("flush did not propagate a fetch command")
	}

	// Stale-tag events are dropped without state changes.
	s.dash.evPending = false
	s.dash.fetchBusy = false
	_, cmd = m.Update(eventsMsg{msgTag: msgTag{hostID: "a", gen: 9}, ev: docker.Event{Action: "start"}})
	if cmd != nil {
		t.Fatal("stale event produced a command")
	}
	if s.dash.evPending || s.dash.fetchBusy {
		t.Fatal("stale event changed state")
	}

	// Background sessions never fetch and never set pending on themselves.
	m2, s2 := eventsModel(t)
	m2.active = 0
	s2.dash.fetchBusy = false
	// Make a second session active so s2 is background.
	other := &Session{host: host("b"), gen: 1}
	other.dash = NewDashboard(other.host, docker.New(nil), 80, 24, 1)
	m2.sessions = append(m2.sessions, other)
	m2.active = 1
	_, cmd = m2.Update(eventsMsg{msgTag: msgTag{hostID: "a", gen: 1}, ev: docker.Event{Action: "start"}})
	if cmd != nil {
		t.Fatal("background session fetched on an event")
	}
	if s2.dash.evPending {
		t.Fatal("background session recorded pending")
	}
	if s2.dash.fetchBusy {
		t.Fatal("background session started a fetch")
	}
}
