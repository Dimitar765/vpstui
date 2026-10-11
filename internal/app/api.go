package app

import (
	"time"

	"vpstui/internal/httpapi"
	"vpstui/internal/models"
)

// snapshot builds the published view of this dashboard's session. Only the
// Update goroutine calls it, so reading Dashboard fields needs no lock.
func (d *Dashboard) snapshot(host models.Host, connected bool) httpapi.SessionSnapshot {
	snap := httpapi.SessionSnapshot{
		ID:           host.ID,
		Name:         host.Name,
		Target:       host.Target(),
		Connected:    connected,
		FetchedAt:    time.Now(),
		HostStats:    d.hstats,
		HasHostStats: d.hasHostStats,
		Containers:   make([]httpapi.ContainerSnapshot, 0, len(d.containers)),
	}
	for _, c := range d.containers {
		cs := httpapi.ContainerSnapshot{
			ID:       c.ID,
			Name:     c.ShortName(),
			Image:    c.Image,
			State:    c.State,
			Status:   c.Status,
			CPUPerc:  "-",
			MemPerc:  "-",
			MemUsage: "-",
			Networks: c.Networks,
		}
		if st, ok := d.stats[c.ShortName()]; ok {
			cs.CPUPerc, cs.MemPerc, cs.MemUsage = st.CPUPerc, st.MemPerc, st.MemUsage
		}
		snap.Containers = append(snap.Containers, cs)
	}
	return snap
}

// publish snapshots session s into the API store, if it has a dashboard.
func (m *Model) publish(s *Session) {
	if m.api == nil || s.dash == nil || s.host.ID == "" {
		return
	}
	m.api.Publish(s.host.ID, s.dash.snapshot(s.host, s.conn != nil))
}
