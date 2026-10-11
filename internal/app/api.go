package app

import (
	"time"

	"vpstui/internal/httpapi"
	"vpstui/internal/models"
)

// snapshot builds the published view of this dashboard's session. Only the
// Update goroutine calls it, so reading Dashboard fields needs no lock.
func (d *Dashboard) snapshot(host models.Host, connected bool) httpapi.SessionSnapshot {
	now := time.Now()
	snap := httpapi.SessionSnapshot{
		ID:              host.ID,
		Name:            host.Name,
		Target:          host.Target(),
		Connected:       connected,
		FetchedAt:       now,
		HostStats:       d.hstats,
		HasHostStats:    d.hasHostStats,
		Containers:      make([]httpapi.ContainerSnapshot, 0, len(d.containers)),
		Errors:          make([]httpapi.ErrorSnapshot, 0, len(d.errCorpus)),
		ErrorsFetchedAt: d.errFetchedAt,
		ErrorsTruncated: d.errTruncated,
		Perf:            d.perfSection(now),
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
		// The label comes from the same store the TUI table reads, so the page and
		// the terminal cannot disagree about a container's name.
		if tag, ok := d.tags.Get(host.ID, cs.Name); ok {
			cs.Tag, cs.TagColor = tag.Tag, tag.Color
		}
		if st, ok := d.stats[c.ShortName()]; ok {
			cs.CPUPerc, cs.MemPerc, cs.MemUsage = st.CPUPerc, st.MemPerc, st.MemUsage
		}
		snap.Containers = append(snap.Containers, cs)
	}
	// The corpus is already classified, signature-mapped and capped, so the
	// published rows carry the TUI's own verdicts rather than a second copy of
	// the raw lines.
	for _, l := range d.errCorpus {
		es := httpapi.ErrorSnapshot{
			ContainerID: l.ID,
			Name:        l.Name,
			// The stamp goes out as the cell the TUI yanks, not the raw docker
			// field: a line with no timestamp still needs a time the page can copy
			// and show, and JS must not reimplement the fallback.
			Stamp: errStampCell(l),
			At:    l.At,
			Sev:   sevName(l.Sev),
			Sig:   l.Sig,
			Text:  l.Text,
		}
		if tag, ok := d.tags.Get(host.ID, l.Name); ok {
			es.Tag = tag.Tag
		}
		snap.Errors = append(snap.Errors, es)
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
