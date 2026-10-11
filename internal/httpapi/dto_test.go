package httpapi

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"testing"
	"time"

	"vpstui/internal/models"
)

func TestToSessionJSON(t *testing.T) {
	fetched := time.Unix(1000, 0)
	snap := SessionSnapshot{
		ID: "h1", Name: "web", Target: "root@1.2.3.4:22", Connected: true,
		FetchedAt:    fetched,
		HasHostStats: true,
		HostStats:    models.HostStats{Load1: 0.5, MemTotalMB: 2048, NumCPU: 2},
		Containers: []ContainerSnapshot{
			{ID: "abc", Name: "api", Image: "nginx", State: "running", CPUPerc: "1.2%", MemPerc: "3%"},
		},
	}

	got := toSessionJSON(snap)
	if got.HostStats == nil || got.HostStats.Load1 != 0.5 || got.HostStats.NumCPU != 2 {
		t.Fatalf("host stats not mapped: %+v", got.HostStats)
	}
	if len(got.Containers) != 1 || got.Containers[0].Name != "api" || got.Containers[0].CPUPerc != "1.2%" {
		t.Fatalf("containers not mapped: %+v", got.Containers)
	}
}

func TestToSessionJSONNoHostStats(t *testing.T) {
	got := toSessionJSON(SessionSnapshot{ID: "h2"})
	if got.HostStats != nil {
		t.Fatalf("HostStats = %+v; want nil when HasHostStats false", got.HostStats)
	}
	if got.Containers == nil {
		t.Fatal("Containers nil; want empty slice for stable JSON []")
	}
}

func TestToSessionSummaryJSON(t *testing.T) {
	snap := SessionSnapshot{ID: "h1", Name: "web", Containers: []ContainerSnapshot{{}, {}}}
	got := toSessionSummaryJSON(snap)
	if got.ContainerCount != 2 {
		t.Fatalf("ContainerCount = %d; want 2", got.ContainerCount)
	}
}

func graphFixture() SessionSnapshot {
	return SessionSnapshot{
		ID: "h1", FetchedAt: time.Unix(2000, 0),
		Containers: []ContainerSnapshot{
			{ID: "id1", Name: "api", State: "running", Networks: []string{"shared", "web_default"}},
			{ID: "id2", Name: "web", State: "running", Networks: []string{"web_default"}},
			// bridge rides along with most containers and must not be drawn
			{ID: "id3", Name: "db", State: "running", Networks: []string{"bridge", "web_default"}},
			{ID: "id4", Name: "solo", State: "exited", Networks: []string{"bridge"}},
		},
	}
}

func TestToNetworkJSONGraph(t *testing.T) {
	got := toNetworkJSON(graphFixture())
	if !got.Available || !got.FetchedAt.Equal(time.Unix(2000, 0)) {
		t.Fatalf("available/fetchedAt not mapped: %+v", got)
	}
	wantNodes := []string{"n:shared", "n:web_default", "c:id1", "c:id3", "c:id2"}
	if len(got.Nodes) != len(wantNodes) {
		t.Fatalf("nodes = %+v, want ids %v", got.Nodes, wantNodes)
	}
	for i, id := range wantNodes {
		if got.Nodes[i].ID != id {
			t.Errorf("node %d = %q, want %q", i, got.Nodes[i].ID, id)
		}
	}
	if got.Nodes[0].Kind != "network" || got.Nodes[2].Kind != "container" || got.Nodes[2].State != "running" {
		t.Errorf("node kinds/states wrong: %+v", got.Nodes)
	}
	wantEdges := []graphEdgeJSON{
		{From: "c:id1", To: "n:shared"},
		{From: "c:id1", To: "n:web_default"},
		{From: "c:id2", To: "n:web_default"},
		{From: "c:id3", To: "n:web_default"},
	}
	if !reflect.DeepEqual(got.Edges, wantEdges) {
		t.Errorf("edges = %+v, want %+v", got.Edges, wantEdges)
	}
	if len(got.Isolated) != 1 || got.Isolated[0].ID != "c:id4" || got.Isolated[0].Name != "solo" {
		t.Errorf("isolated = %+v, want solo", got.Isolated)
	}
	if got.Truncated {
		t.Errorf("small graph reported truncated")
	}
}

func TestToNetworkJSONUnavailable(t *testing.T) {
	got := toNetworkJSON(SessionSnapshot{ID: "h2"})
	if got.Available {
		t.Error("available true before the first container fetch")
	}
	if got.Nodes == nil || got.Edges == nil || got.Isolated == nil {
		t.Errorf("nil slices serialise as null; want empty arrays: %+v", got)
	}
}

func TestToNetworkJSONFiltersBuiltinNetworks(t *testing.T) {
	snap := SessionSnapshot{
		ID: "h4", FetchedAt: time.Unix(3000, 0),
		Containers: []ContainerSnapshot{
			{ID: "aaa111", Name: "on-bridge", State: "running", Networks: []string{"bridge"}},
			{ID: "bbb222", Name: "multi", State: "running", Networks: []string{"bridge", "app_net", "ingress"}},
		},
	}
	got := toNetworkJSON(snap)
	wantNodes := []string{"n:app_net", "c:bbb222"}
	if len(got.Nodes) != 2 || got.Nodes[0].ID != wantNodes[0] || got.Nodes[1].ID != wantNodes[1] {
		t.Fatalf("nodes = %+v, want %v", got.Nodes, wantNodes)
	}
	wantEdges := []graphEdgeJSON{{From: "c:bbb222", To: "n:app_net"}}
	if !reflect.DeepEqual(got.Edges, wantEdges) {
		t.Errorf("edges = %+v, want %+v", got.Edges, wantEdges)
	}
	if len(got.Isolated) != 1 || got.Isolated[0].ID != "c:aaa111" {
		t.Errorf("isolated = %+v, want only on-bridge", got.Isolated)
	}
}

func TestToNetworkJSONDeterministic(t *testing.T) {
	snap := graphFixture()
	snap.Containers[1].Networks = append(snap.Containers[1].Networks, "shared")
	first, err := json.Marshal(toNetworkJSON(snap))
	if err != nil {
		t.Fatal(err)
	}
	for range 20 {
		again, err := json.Marshal(toNetworkJSON(snap))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(first, again) {
			t.Fatalf("payload changed between identical snapshots:\n%s\n%s", first, again)
		}
	}
}

func TestToNetworkJSONTruncates(t *testing.T) {
	snap := SessionSnapshot{ID: "h3", FetchedAt: time.Unix(4000, 0)}
	for i := range maxGraphNodes + 50 {
		id := fmt.Sprintf("c%04d", i)
		snap.Containers = append(snap.Containers, ContainerSnapshot{ID: id, Name: id, State: "running", Networks: []string{"one"}})
	}
	got := toNetworkJSON(snap)
	if !got.Truncated {
		t.Fatal("over-limit graph not marked truncated")
	}
	if len(got.Nodes) != maxGraphNodes {
		t.Fatalf("nodes = %d, want %d", len(got.Nodes), maxGraphNodes)
	}
	if len(got.Edges) != maxGraphNodes-1 { // network node plus the kept containers
		t.Fatalf("edges = %d, want %d", len(got.Edges), maxGraphNodes-1)
	}
}

func i64(v int64) *int64     { return &v }
func f64(v float64) *float64 { return &v }

// perfFixture is one session after a probe: the hog carries the kernel's
// all-time counters, the idling container was never probed.
func perfFixture() SessionSnapshot {
	return SessionSnapshot{
		ID: "h1", Name: "web", FetchedAt: time.Unix(5000, 0),
		Perf: &PerfSection{
			Window:    20 * time.Minute,
			Retention: 30 * time.Minute,
			Samples:   620,
			ProbedAt:  time.Unix(4900, 0).UTC(),
			Supported: true,
			Rows: []PerfSnapshot{
				{
					ID: "bbb222222222", Name: "hog", CPU: 42, CPUAvg: 3.8, CPUPeak: 61.2,
					// 42% of one core on a 12-core box is 3.5% of the box: share is a
					// percentage, printed exactly as the terminal prints it.
					Share: 3.5, Spike: 11.1, Flags: []string{"hog", "spike"}, Samples: 310,
					MemBytes: 1298234, MemPeakBytes: 1503234,
					AllTimeMemPeakBytes: i64(1503234), CPUSeconds: f64(961.9),
					// 961.9 CPU-seconds over the container's own life, divided in Go:
					// the page never computes a rate of its own.
					CPUAvgCores: f64(0.26),
					Throttled:   i64(0), Periods: i64(0),
					Series: []float64{0.1, 42},
				},
				{
					ID: "aaa111111111", Name: "idle", CPU: 1, CPUAvg: 1, CPUPeak: 1,
					Samples: 310, Series: []float64{1},
				},
			},
		},
	}
}

func TestToPerfJSONAbsentWithoutRows(t *testing.T) {
	cases := map[string]*PerfSection{
		"nil":      nil,
		"no rows":  {Window: 20 * time.Minute},
		"empty id": {Rows: []PerfSnapshot{}},
	}
	for name, p := range cases {
		if got := toPerfJSON(p); got != nil {
			t.Errorf("%s: perf emitted for a window with nothing in it: %+v", name, got)
		}
		if got := toSessionJSON(SessionSnapshot{ID: "h", Perf: p}); got.Perf != nil {
			t.Errorf("%s: session carries a perf object: %+v", name, got.Perf)
		}
	}
	body, err := json.Marshal(toSessionJSON(SessionSnapshot{ID: "h"}))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(body, []byte(`"perf"`)) {
		t.Errorf("unprobed session serialises a perf key: %s", body)
	}
}

func TestToPerfJSONMapsWindow(t *testing.T) {
	got := toPerfJSON(perfFixture().Perf)
	if got == nil {
		t.Fatal("perf nil with rows present")
	}
	if got.WindowSec != 1200 || got.RetentionSec != 1800 || got.Samples != 620 {
		t.Errorf("window/retention/samples = %v/%v/%v, want 1200/1800/620",
			got.WindowSec, got.RetentionSec, got.Samples)
	}
	if !got.Probed || !got.ProbedAt.Equal(time.Unix(4900, 0).UTC()) || !got.Supported {
		t.Errorf("probe metadata = probed %v at %v supported %v", got.Probed, got.ProbedAt, got.Supported)
	}
	// The rank order the terminal computed, untouched.
	if len(got.Rows) != 2 || got.Rows[0].ID != "bbb222222222" || got.Rows[1].ID != "aaa111111111" {
		t.Fatalf("rows reordered: %+v", got.Rows)
	}
	if !reflect.DeepEqual(got.Rows[0].Flags, []string{"hog", "spike"}) {
		t.Errorf("flags = %v, want hog spike", got.Rows[0].Flags)
	}
	if got.Rows[0].AllTimeMemPeakBytes == nil || *got.Rows[0].AllTimeMemPeakBytes != 1503234 {
		t.Errorf("all-time peak dropped: %+v", got.Rows[0])
	}
	if got.Rows[0].CPUAvgCores == nil || *got.Rows[0].CPUAvgCores != 0.26 {
		t.Errorf("the cumulative-seconds rate was dropped: %+v", got.Rows[0])
	}
	if got.Rows[1].AllTimeMemPeakBytes != nil || got.Rows[1].CPUSeconds != nil ||
		got.Rows[1].CPUAvgCores != nil ||
		got.Rows[1].Throttled != nil || got.Rows[1].Periods != nil {
		t.Errorf("unprobed container claims counters: %+v", got.Rows[1])
	}
}

func TestToPerfJSONNoNullArrays(t *testing.T) {
	sec := perfFixture().Perf
	// A container read but never sampled: both lists arrive nil from the buffer.
	sec.Rows = append(sec.Rows, PerfSnapshot{ID: "ccc333333333", Name: "fresh"})
	got := toPerfJSON(sec)
	if got.Rows[1].Flags == nil || got.Rows[2].Flags == nil || got.Rows[2].Series == nil {
		t.Errorf("nil lists kept nil, so they would serialise as null: %+v", got.Rows)
	}
	body, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	// A page iterates flags and series unconditionally, so neither may be null.
	for _, want := range []string{`"flags":[]`, `"series":[1]`, `"series":[]`} {
		if !bytes.Contains(body, []byte(want)) {
			t.Errorf("payload missing %s: %s", want, body)
		}
	}
	if bytes.Contains(body, []byte("null")) {
		t.Errorf("payload contains a null: %s", body)
	}
}

func TestToPerfJSONDeterministic(t *testing.T) {
	snap := perfFixture()
	first, err := json.Marshal(toSessionJSON(snap))
	if err != nil {
		t.Fatal(err)
	}
	for range 20 {
		again, err := json.Marshal(toSessionJSON(snap))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(first, again) {
			t.Fatalf("payload changed between identical snapshots:\n%s\n%s", first, again)
		}
	}
	// A zero flag list and a nil one must not be two different payloads.
	withEmpty := perfFixture()
	withEmpty.Perf.Rows[1].Flags = []string{}
	b, err := json.Marshal(toSessionJSON(withEmpty))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, b) {
		t.Errorf("empty flags changed the payload:\n%s\n%s", first, b)
	}
}
