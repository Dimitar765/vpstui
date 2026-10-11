package httpapi

import (
	"sort"
	"time"

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
