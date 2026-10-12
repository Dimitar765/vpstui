package httpapi

import (
	"sort"
	"strings"
	"time"

	"vpstui/internal/errsample"
	"vpstui/internal/models"
)

type hostStatsJSON struct {
	Load1      float64 `json:"load1"`
	Load5      float64 `json:"load5"`
	Load15     float64 `json:"load15"`
	MemTotalMB int     `json:"memTotalMB"`
	MemUsedMB  int     `json:"memUsedMB"`
	NumCPU     int     `json:"numCPU"`
	UptimeSec  float64 `json:"uptimeSec"`
}

type containerJSON struct {
	ID       string   `json:"id"`
	Name     string   `json:"name"`
	Tag      string   `json:"tag"`
	TagColor string   `json:"tagColor"`
	Image    string   `json:"image"`
	State    string   `json:"state"`
	Status   string   `json:"status"`
	CPUPerc  string   `json:"cpu"`
	MemPerc  string   `json:"memPerc"`
	MemUsage string   `json:"memUsage"`
	CPUPct   *float64 `json:"cpuPct"`
	MemPct   *float64 `json:"memPct"`
}

type sessionSummaryJSON struct {
	ID             string    `json:"id"`
	Name           string    `json:"name"`
	Target         string    `json:"target"`
	Connected      bool      `json:"connected"`
	FetchedAt      time.Time `json:"fetchedAt"`
	ContainerCount int       `json:"containerCount"`
}

type sessionJSON struct {
	ID           string          `json:"id"`
	Name         string          `json:"name"`
	Target       string          `json:"target"`
	Connected    bool            `json:"connected"`
	FetchedAt    time.Time       `json:"fetchedAt"`
	HasHostStats bool            `json:"hasHostStats"`
	HostStats    *hostStatsJSON  `json:"hostStats,omitempty"`
	Containers   []containerJSON `json:"containers"`
	Perf         *perfJSON       `json:"perf,omitempty"`
}

// perfRowJSON is one container's ranked row. The rows arrive in the order the
// terminal ranks them, so the page never sorts or thresholds anything: a hog on
// the screen is a hog here.
type perfRowJSON struct {
	ID       string   `json:"id"`
	Name     string   `json:"name"`
	Tag      string   `json:"tag,omitempty"`
	TagColor string   `json:"tagColor,omitempty"`
	CPU      float64  `json:"cpu"`
	CPUAvg   float64  `json:"cpuAvg"`
	CPUPeak  float64  `json:"cpuPeak"`
	Share    float64  `json:"share"` // percent of the box, as the terminal prints it
	Spike    float64  `json:"spike"`
	Flags    []string `json:"flags"`

	Samples      int   `json:"samples"`
	MemBytes     int64 `json:"memBytes"`
	MemPeakBytes int64 `json:"memPeakBytes"`

	// The all-time counters are absent when the host exposes no cgroup peak, or
	// this container was not in the probe. Zero would be a measurement.
	AllTimeMemPeakBytes *int64   `json:"allTimeMemPeakBytes,omitempty"`
	CPUSeconds          *float64 `json:"cpuSeconds,omitempty"`
	CPUAvgCores         *float64 `json:"cpuAvgCores,omitempty"`
	Throttled           *int64   `json:"throttled,omitempty"`
	Periods             *int64   `json:"periods,omitempty"`

	// Series is the window already reduced to at most perf.MaxPoints values, so
	// one container can never grow a payload beyond a kilobyte or two.
	Series []float64 `json:"series"`
}

type perfJSON struct {
	WindowSec    float64       `json:"windowSec"`
	RetentionSec float64       `json:"retentionSec"`
	Samples      int           `json:"samples"`
	Probed       bool          `json:"probed"`
	ProbedAt     time.Time     `json:"probedAt"`
	Supported    bool          `json:"supported"`
	Rows         []perfRowJSON `json:"rows"`
}

func toPerfJSON(p *PerfSection) *perfJSON {
	if p == nil || len(p.Rows) == 0 {
		return nil
	}
	out := &perfJSON{
		WindowSec:    p.Window.Seconds(),
		RetentionSec: p.Retention.Seconds(),
		Samples:      p.Samples,
		Probed:       !p.ProbedAt.IsZero(),
		ProbedAt:     p.ProbedAt,
		Supported:    p.Supported,
		Rows:         make([]perfRowJSON, 0, len(p.Rows)),
	}
	for _, r := range p.Rows {
		row := perfRowJSON{
			ID: r.ID, Name: r.Name, Tag: r.Tag, TagColor: r.TagColor,
			CPU: r.CPU, CPUAvg: r.CPUAvg, CPUPeak: r.CPUPeak,
			Share: r.Share, Spike: r.Spike, Flags: r.Flags,
			Samples: r.Samples, MemBytes: r.MemBytes, MemPeakBytes: r.MemPeakBytes,
			AllTimeMemPeakBytes: r.AllTimeMemPeakBytes,
			CPUSeconds:          r.CPUSeconds,
			CPUAvgCores:         r.CPUAvgCores,
			Throttled:           r.Throttled,
			Periods:             r.Periods,
			Series:              r.Series,
		}
		// Never null: a page that iterates a list must not branch on absence for
		// a field that is always a list.
		if row.Flags == nil {
			row.Flags = []string{}
		}
		if row.Series == nil {
			row.Series = []float64{}
		}
		out.Rows = append(out.Rows, row)
	}
	return out
}

func toHostStatsJSON(h models.HostStats) hostStatsJSON {
	return hostStatsJSON{
		Load1:      h.Load1,
		Load5:      h.Load5,
		Load15:     h.Load15,
		MemTotalMB: h.MemTotalMB,
		MemUsedMB:  h.MemUsedMB,
		NumCPU:     h.NumCPU,
		UptimeSec:  h.UptimeSec,
	}
}

func toContainerJSON(c ContainerSnapshot) containerJSON {
	out := containerJSON{
		ID:       c.ID,
		Name:     c.Name,
		Tag:      c.Tag,
		TagColor: c.TagColor,
		Image:    c.Image,
		State:    c.State,
		Status:   c.Status,
		CPUPerc:  c.CPUPerc,
		MemPerc:  c.MemPerc,
		MemUsage: c.MemUsage,
	}
	if v, ok := parsePercent(c.CPUPerc); ok {
		out.CPUPct = &v
	}
	if v, ok := parsePercent(c.MemPerc); ok {
		out.MemPct = &v
	}
	return out
}

func toSessionSummaryJSON(s SessionSnapshot) sessionSummaryJSON {
	return sessionSummaryJSON{
		ID:             s.ID,
		Name:           s.Name,
		Target:         s.Target,
		Connected:      s.Connected,
		FetchedAt:      s.FetchedAt,
		ContainerCount: len(s.Containers),
	}
}

func toSessionJSON(s SessionSnapshot) sessionJSON {
	out := sessionJSON{
		ID:           s.ID,
		Name:         s.Name,
		Target:       s.Target,
		Connected:    s.Connected,
		FetchedAt:    s.FetchedAt,
		HasHostStats: s.HasHostStats,
		Containers:   make([]containerJSON, 0, len(s.Containers)),
	}
	if s.HasHostStats {
		h := toHostStatsJSON(s.HostStats)
		out.HostStats = &h
	}
	out.Perf = toPerfJSON(s.Perf)
	for _, c := range s.Containers {
		out.Containers = append(out.Containers, toContainerJSON(c))
	}
	return out
}

// Node ids are prefixed so a container and a network can never collide, and
// so the browser can join edges onto nodes without parsing names.
const (
	nodePrefixContainer = "c:"
	nodePrefixNetwork   = "n:"
)

// maxGraphNodes bounds one payload: a host with hundreds of containers would
// otherwise draw an unreadable graph. Networks come first in Nodes, so
// truncation drops containers rather than orphaning a cluster.
const maxGraphNodes = 200

type graphNodeJSON struct {
	ID    string `json:"id"`
	Kind  string `json:"kind"` // "container" | "network"
	Name  string `json:"name"`
	Tag   string `json:"tag,omitempty"`
	Color string `json:"tagColor,omitempty"`
	State string `json:"state,omitempty"`
}

type graphEdgeJSON struct {
	From string `json:"from"`
	To   string `json:"to"`
}

type networkJSON struct {
	ID        string          `json:"id"`
	Available bool            `json:"available"`
	FetchedAt time.Time       `json:"fetchedAt"`
	Truncated bool            `json:"truncated,omitempty"`
	Nodes     []graphNodeJSON `json:"nodes"`
	Edges     []graphEdgeJSON `json:"edges"`
	Isolated  []graphNodeJSON `json:"isolated"`
}

func containerNode(c ContainerSnapshot) graphNodeJSON {
	return graphNodeJSON{
		ID:    nodePrefixContainer + c.ID,
		Kind:  "container",
		Name:  c.Name,
		Tag:   c.Tag,
		Color: c.TagColor,
		State: c.State,
	}
}

// sortContainerNodes orders by name, falling back to id for equal names.
func sortContainerNodes(ns []graphNodeJSON) {
	sort.Slice(ns, func(i, j int) bool {
		if ns[i].Name != ns[j].Name {
			return ns[i].Name < ns[j].Name
		}
		return ns[i].ID < ns[j].ID
	})
}

// builtinNetwork reports the networks docker creates by default. Every
// container sits on bridge, so treating them as topology would connect
// everything to everything.
func builtinNetwork(name string) bool {
	switch name {
	case "bridge", "host", "none", "ingress":
		return true
	}
	return false
}

// toNetworkJSON maps the published container list onto a bipartite graph:
// containers and the user-defined networks they share, joined by one edge per
// membership. Network names come from `docker ps` itself, so ids cannot
// disagree with the nodes drawn beside them. Output is fully ordered, so an
// unchanged payload serialises identically and the page can skip repainting.
func toNetworkJSON(s SessionSnapshot) networkJSON {
	out := networkJSON{
		ID:        s.ID,
		Available: !s.FetchedAt.IsZero(),
		FetchedAt: s.FetchedAt,
		Nodes:     []graphNodeJSON{},
		Edges:     []graphEdgeJSON{},
		Isolated:  []graphNodeJSON{},
	}
	if !out.Available {
		return out
	}

	members := map[string][]string{} // network → container ids
	linked := map[string]bool{}
	for _, c := range s.Containers {
		var nets []string
		for _, n := range c.Networks {
			if !builtinNetwork(n) {
				nets = append(nets, n)
			}
		}
		if len(nets) == 0 {
			continue // on default networks only → Isolated
		}
		linked[c.ID] = true
		for _, n := range nets {
			members[n] = append(members[n], c.ID)
		}
	}

	netNames := make([]string, 0, len(members))
	for n, ids := range members {
		sort.Strings(ids)
		netNames = append(netNames, n)
	}
	sort.Strings(netNames)

	nodes := make([]graphNodeJSON, 0, len(netNames)+len(linked))
	for _, n := range netNames {
		nodes = append(nodes, graphNodeJSON{ID: nodePrefixNetwork + n, Kind: "network", Name: n})
	}
	var containers []graphNodeJSON
	for _, c := range s.Containers {
		if linked[c.ID] {
			containers = append(containers, containerNode(c))
		}
	}
	sortContainerNodes(containers)
	nodes = append(nodes, containers...)

	// Networks are emitted first, so a host too big to draw loses containers
	// rather than orphaning a cluster.
	if len(nodes) > maxGraphNodes {
		nodes = nodes[:maxGraphNodes]
		out.Truncated = true
	}
	out.Nodes = nodes
	kept := make(map[string]bool, len(nodes))
	for _, nd := range nodes {
		kept[nd.ID] = true
	}
	for _, n := range netNames {
		for _, id := range members[n] {
			from := nodePrefixContainer + id
			if !kept[from] {
				continue
			}
			out.Edges = append(out.Edges, graphEdgeJSON{From: from, To: nodePrefixNetwork + n})
		}
	}
	for _, c := range s.Containers {
		if !linked[c.ID] {
			out.Isolated = append(out.Isolated, containerNode(c))
		}
	}
	sortContainerNodes(out.Isolated)
	return out
}

// maxErrorGroups bounds one /api/errors payload. It mirrors app.maxErrorGroups:
// the TUI caps the same way, so the page and the screen list the same problems.
const maxErrorGroups = 200

// maxErrorExamples is the shared sample cap: the TUI trims a group's examples
// to this many by the same rule, so a pasted group is the same text from either
// end. See internal/errsample.
const maxErrorExamples = errsample.Cap

type errorHostJSON struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// Swept is the flag the page branches on; omitempty cannot express it for
	// a time.Time, so the timestamp is always present and meaningless when
	// false — exactly like the session payload's fetchedAt.
	Swept     bool      `json:"swept"`
	SweptAt   time.Time `json:"sweptAt"`
	LineCount int       `json:"lineCount"`
}

type errorSampleJSON struct {
	Host      string `json:"host"`
	Container string `json:"container"`
	Tag       string `json:"tag,omitempty"`
	// Sev is this line's own severity, not the group's: a warn line can be the
	// newest sample of a signature that once shouted at error level, and the
	// page's copy button has to reproduce the TUI's yank byte for byte.
	Sev   string `json:"sev"`
	Stamp string `json:"stamp,omitempty"`
	Text  string `json:"text"`
}

type errorGroupJSON struct {
	Sig        string           `json:"sig"`
	Sev        string           `json:"sev"`
	Count      int              `json:"count"`
	Hosts      []string         `json:"hosts"`
	Containers []string         `json:"containers"`
	FirstSeen  time.Time        `json:"firstSeen"`
	LastSeen   time.Time        `json:"lastSeen"`
	Sample     *errorSampleJSON `json:"sample,omitempty"`
	// Examples is the newest lines behind the signature, oldest first, so the
	// page's group copy writes the same clipboard body the TUI's Y does. Sample
	// stays for the single line the row shows.
	Examples []errorSampleJSON `json:"examples"`
}

type errorsJSON struct {
	Swept     int              `json:"swept"`
	Connected int              `json:"connected"`
	Truncated bool             `json:"truncated"`
	FetchedAt time.Time        `json:"fetchedAt"`
	LineCount int              `json:"lineCount"`
	Hosts     []errorHostJSON  `json:"hosts"`
	Groups    []errorGroupJSON `json:"groups"`
}

// sevRank orders the two severities the TUI publishes. A signature that has
// ever shouted at error level is an error, wherever the sample came from.
func sevRank(s string) int {
	switch s {
	case "error":
		return 2
	case "warn":
		return 1
	}
	return 0
}

// toErrorsJSON reduces every session's published corpus into one cross-host
// aggregate. It only counts and sorts: classification and signature
// normalisation belong to the TUI, so this reduce can be rerun on each request
// without a second SSH call and cannot disagree with the screen. Output is
// fully ordered, so an unchanged set of snapshots serialises identically and
// the page can skip repainting.
func toErrorsJSON(snaps []SessionSnapshot) errorsJSON {
	type acc struct {
		sev   string
		count int
		hosts map[string]bool
		conts map[string]bool
		first time.Time
		last  time.Time
		cands []errsample.Entry[errorSampleJSON]
	}
	out := errorsJSON{Hosts: []errorHostJSON{}, Groups: []errorGroupJSON{}}
	groups := map[string]*acc{}
	// contTag maps a group's "host/container" identity to the label the TUI gave
	// that container. Identity stays the docker name — relabelling must not
	// re-group history — so this only feeds the display strings.
	contTag := map[string]string{}

	for _, s := range snaps {
		if s.Connected {
			out.Connected++
		}
		swept := !s.ErrorsFetchedAt.IsZero()
		if swept {
			out.Swept++
			if s.ErrorsFetchedAt.After(out.FetchedAt) {
				out.FetchedAt = s.ErrorsFetchedAt
			}
		}
		if s.ErrorsTruncated {
			out.Truncated = true
		}
		out.Hosts = append(out.Hosts, errorHostJSON{
			ID: s.ID, Name: s.Name, Swept: swept,
			SweptAt: s.ErrorsFetchedAt, LineCount: len(s.Errors),
		})

		for _, l := range s.Errors {
			out.LineCount++
			a, ok := groups[l.Sig]
			if !ok {
				a = &acc{sev: l.Sev, hosts: map[string]bool{}, conts: map[string]bool{}, first: l.At, last: l.At}
				groups[l.Sig] = a
			}
			a.count++
			if sevRank(l.Sev) > sevRank(a.sev) {
				a.sev = l.Sev
			}
			if l.At.Before(a.first) {
				a.first = l.At
			}
			cand := errorSampleJSON{Host: s.Name, Container: l.Name, Tag: l.Tag, Sev: l.Sev, Stamp: l.Stamp, Text: l.Text}
			a.cands = append(a.cands, errsample.Entry[errorSampleJSON]{
				At:    l.At,
				Key:   errsample.Key{Text: l.Text, Container: l.Name, Host: s.Name, Stamp: l.Stamp},
				Value: cand,
			})
			if l.At.After(a.last) {
				a.last = l.At
			}
			a.hosts[s.Name] = true
			key := s.Name + "/" + l.Name
			a.conts[key] = true
			// Every line of one container carries the same label, because it comes
			// from a single store, so first-seen wins deterministically.
			if l.Tag != "" && contTag[key] == "" {
				contTag[key] = l.Tag
			}
		}
	}

	sort.Slice(out.Hosts, func(i, j int) bool {
		if out.Hosts[i].Name != out.Hosts[j].Name {
			return out.Hosts[i].Name < out.Hosts[j].Name
		}
		return out.Hosts[i].ID < out.Hosts[j].ID
	})

	sigs := make([]string, 0, len(groups))
	for sig := range groups {
		sigs = append(sigs, sig)
	}
	sort.Strings(sigs)
	for _, sig := range sigs {
		a := groups[sig]
		// One pick, shared with the TUI: the newest lines behind the signature,
		// oldest first, and the single sample is the last of them. A group always
		// has at least one line, so there is something to show.
		examples := errsample.Newest(maxErrorExamples, a.cands)
		sample := examples[len(examples)-1]
		out.Groups = append(out.Groups, errorGroupJSON{
			Sig:        sig,
			Sev:        a.sev,
			Count:      a.count,
			Hosts:      sortedNames(a.hosts),
			Containers: displayContainers(sortedNames(a.conts), contTag),
			FirstSeen:  a.first,
			LastSeen:   a.last,
			Sample:     &sample,
			Examples:   examples,
		})
	}

	// The page's ordering is the screen's: worst-first by volume, then by
	// signature so a tie never shuffles between polls.
	sort.SliceStable(out.Groups, func(i, j int) bool {
		if out.Groups[i].Count != out.Groups[j].Count {
			return out.Groups[i].Count > out.Groups[j].Count
		}
		return out.Groups[i].Sig < out.Groups[j].Sig
	})
	if len(out.Groups) > maxErrorGroups {
		out.Groups = out.Groups[:maxErrorGroups]
		out.Truncated = true
	}
	return out
}

func sortedNames(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// displayContainers turns a group's "host/container" identities into the labels
// the TUI shows, so the page and a yanked group read alike. Sorting happened on
// the docker names above, which is why a rename of the label cannot reorder or
// re-merge a group.
func displayContainers(names []string, tags map[string]string) []string {
	out := make([]string, 0, len(names))
	for _, n := range names {
		tag := tags[n]
		if tag == "" {
			out = append(out, n)
			continue
		}
		if i := strings.IndexByte(n, '/'); i >= 0 {
			out = append(out, n[:i]+"/"+tag+" "+n[i+1:])
			continue
		}
		out = append(out, tag+" "+n)
	}
	return out
}
