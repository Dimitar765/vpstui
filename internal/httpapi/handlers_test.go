package httpapi

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"vpstui/internal/models"
)

func newTestServer(t *testing.T) (*Store, *httptest.Server) {
	t.Helper()
	store := NewStore()
	srv := httptest.NewServer(NewServer(store, "").Handler)
	t.Cleanup(srv.Close)
	return store, srv
}

func TestHealthz(t *testing.T) {
	_, hs := newTestServer(t)
	res, err := http.Get(hs.URL + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d; want 200", res.StatusCode)
	}
	if ct := res.Header.Get("Content-Type"); ct != "application/json" {
		t.Fatalf("content-type = %q", ct)
	}
	var body map[string]bool
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if !body["ok"] {
		t.Fatalf("healthz body = %v", body)
	}
}

func TestListSessions(t *testing.T) {
	store, hs := newTestServer(t)
	store.Publish("a", SessionSnapshot{ID: "a", Name: "alpha", Containers: []ContainerSnapshot{{}}})
	res, err := http.Get(hs.URL + "/api/sessions")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var list []sessionSummaryJSON
	if err := json.NewDecoder(res.Body).Decode(&list); err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].ID != "a" || list[0].ContainerCount != 1 {
		t.Fatalf("list = %+v", list)
	}
}

func TestGetSessionFoundAndMissing(t *testing.T) {
	store, hs := newTestServer(t)
	store.Publish("a", SessionSnapshot{ID: "a", Name: "alpha"})

	res, err := http.Get(hs.URL + "/api/sessions/a")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("found status = %d; want 200", res.StatusCode)
	}
	var got sessionJSON
	if err := json.NewDecoder(res.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got.Name != "alpha" {
		t.Fatalf("name = %q", got.Name)
	}

	res2, err := http.Get(hs.URL + "/api/sessions/zz")
	if err != nil {
		t.Fatal(err)
	}
	defer res2.Body.Close()
	if res2.StatusCode != http.StatusNotFound {
		t.Fatalf("missing status = %d; want 404", res2.StatusCode)
	}
}

func TestClosedStoreReturns503(t *testing.T) {
	store, hs := newTestServer(t)
	store.Close()
	for _, path := range []string{"/api/sessions", "/api/sessions/a", "/api/sessions/a/network", "/api/errors"} {
		res, err := http.Get(hs.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != http.StatusServiceUnavailable {
			t.Fatalf("%s status = %d; want 503", path, res.StatusCode)
		}
	}
}

func TestIndexServesDashboard(t *testing.T) {
	_, hs := newTestServer(t)
	res, err := http.Get(hs.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d; want 200", res.StatusCode)
	}
	if ct := res.Header.Get("Content-Type"); ct != "text/html; charset=utf-8" {
		t.Fatalf("content-type = %q", ct)
	}
	body, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(body, []byte("<template id=\"tpl-session\"")) {
		t.Fatal("index does not embed the session template")
	}
}

// The #perf tab's contract with the payload is text the page can be grepped for:
// the tab, the panel, the templates the script clones, and the field names it
// prints. A rename that leaves the script looking for a template that is gone has
// to fail a test here, not a browser.
func TestIndexPagePerfContract(t *testing.T) {
	_, hs := newTestServer(t)
	res, err := http.Get(hs.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	body, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	page := string(body)
	for _, want := range []string{
		`<button type="button" data-tab="perf">`,
		`<div id="perf" hidden></div>`,
		`<template id="tpl-perf">`,
		`<template id="tpl-perf-row">`,
		"svgEl('polyline', { points: pts }",
		"perfTd.appendChild(flagCells(ranked.flags))",
		// The rate is Go's, printed here and never divided again in the browser.
		"rate + ' cores averaged over its life'",
		"'<0.01' : trimZero(r.cpuAvgCores)",
		`allTimeMemPeakBytes`,
	} {
		if !strings.Contains(page, want) {
			t.Errorf("page no longer contains %q", want)
		}
	}
	// The published share is a percentage; the page must not divide or rescale it.
	if strings.Contains(page, "share / 100") || strings.Contains(page, "share * 100") {
		t.Error("page rescales the published share instead of printing it")
	}
	// The one standing rule: remote text reaches the DOM as text only.
	if assignment := regexp.MustCompile(`\.innerHTML\s*[+]?=`); assignment.MatchString(page) {
		t.Error("page assigns innerHTML: container names and log lines are remote text")
	}
}

func TestUnknownPathIs404(t *testing.T) {
	_, hs := newTestServer(t)
	res, err := http.Get(hs.URL + "/nope")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d; want 404", res.StatusCode)
	}
}

func TestHandlerRejectsNonGET(t *testing.T) {
	_, hs := newTestServer(t)
	res, err := http.Post(hs.URL+"/api/sessions", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("POST status = %d; want 405", res.StatusCode)
	}
}

func TestGetNetworkEndpoint(t *testing.T) {
	store, hs := newTestServer(t)
	snap := graphFixture()
	store.Publish(snap.ID, snap)

	res, err := http.Get(hs.URL + "/api/sessions/h1/network")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d; want 200", res.StatusCode)
	}
	body, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"id", "available", "fetchedAt", "nodes", "edges", "isolated"} {
		if _, ok := raw[key]; !ok {
			t.Errorf("response is missing %q: %s", key, body)
		}
	}
	if raw["available"] != true {
		t.Errorf("available = %v; want true", raw["available"])
	}
	var got networkJSON
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Nodes) != 5 || len(got.Edges) != 4 || len(got.Isolated) != 1 {
		t.Errorf("graph shape = %d nodes / %d edges / %d isolated, want 5/4/1",
			len(got.Nodes), len(got.Edges), len(got.Isolated))
	}

	res2, err := http.Get(hs.URL + "/api/sessions/zz/network")
	if err != nil {
		t.Fatal(err)
	}
	defer res2.Body.Close()
	if res2.StatusCode != http.StatusNotFound {
		t.Fatalf("missing status = %d; want 404", res2.StatusCode)
	}
}

func TestGetNetworkNotFetchedYet(t *testing.T) {
	store, hs := newTestServer(t)
	store.Publish("h9", SessionSnapshot{ID: "h9", Name: "solo"})
	res, err := http.Get(hs.URL + "/api/sessions/h9/network")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d; want 200", res.StatusCode)
	}
	var got networkJSON
	if err := json.NewDecoder(res.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got.Available {
		t.Error("available true before the first container fetch")
	}
}

func errHostSnap(id, name string, connected bool, at time.Time, lines ...ErrorSnapshot) SessionSnapshot {
	return SessionSnapshot{
		ID: id, Name: name, Connected: connected, FetchedAt: at,
		Containers:      []ContainerSnapshot{{ID: "c", Name: name + "-c"}},
		Errors:          lines,
		ErrorsFetchedAt: at,
	}
}

func TestErrorsEndpoint(t *testing.T) {
	store, hs := newTestServer(t)
	t1 := time.Unix(1700000000, 0).UTC()
	store.Publish("h1", errHostSnap("h1", "alpha", true, t1.Add(time.Minute),
		ErrorSnapshot{ContainerID: "c1", Name: "web", Stamp: "01:00:00", At: t1, Sev: "warn", Sig: "db down", Text: "WARN db down"},
		ErrorSnapshot{ContainerID: "c1", Name: "web", Stamp: "01:01:00", At: t1.Add(time.Minute), Sev: "error", Sig: "db down", Text: "ERROR db down"},
	))
	store.Publish("h2", errHostSnap("h2", "beta", false, t1.Add(2*time.Minute),
		ErrorSnapshot{ContainerID: "c2", Name: "api", At: t1.Add(3 * time.Minute), Sev: "error", Sig: "no space left", Text: "ERROR no space left"},
	))
	// Connected but never swept: it belongs in the coverage counts, not the groups.
	store.Publish("h3", SessionSnapshot{ID: "h3", Name: "gamma", Connected: true})

	res, err := http.Get(hs.URL + "/api/errors")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d; want 200", res.StatusCode)
	}
	body, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}

	// The key set is the contract the page is written against.
	var top map[string]any
	if err := json.Unmarshal(body, &top); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"swept", "connected", "truncated", "fetchedAt", "lineCount", "hosts", "groups"} {
		if _, ok := top[key]; !ok {
			t.Errorf("payload is missing %q: %s", key, body)
		}
	}
	if len(top) != 7 {
		t.Errorf("payload has %d top-level keys, want 7: %s", len(top), body)
	}
	group := top["groups"].([]any)[0].(map[string]any)
	for _, key := range []string{"sig", "sev", "count", "hosts", "containers", "firstSeen", "lastSeen", "sample", "examples"} {
		if _, ok := group[key]; !ok {
			t.Errorf("group is missing %q: %v", key, group)
		}
	}
	host := top["hosts"].([]any)[0].(map[string]any)
	for _, key := range []string{"id", "name", "swept", "sweptAt", "lineCount"} {
		if _, ok := host[key]; !ok {
			t.Errorf("host is missing %q: %v", key, host)
		}
	}

	var got errorsJSON
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	if got.Swept != 2 || got.Connected != 2 || got.LineCount != 3 || got.Truncated {
		t.Errorf("coverage = swept %d connected %d lines %d truncated %v, want 2/2/3/false",
			got.Swept, got.Connected, got.LineCount, got.Truncated)
	}
	// The newest sweep stamps the payload, not the newest line.
	if !got.FetchedAt.Equal(t1.Add(2 * time.Minute)) {
		t.Errorf("fetchedAt = %v, want %v", got.FetchedAt, t1.Add(2*time.Minute))
	}
	if want := []string{"alpha", "beta", "gamma"}; len(got.Hosts) != 3 {
		t.Errorf("hosts = %d, want 3", len(got.Hosts))
	} else {
		for i, name := range want {
			if got.Hosts[i].Name != name {
				t.Errorf("hosts[%d] = %q, want %q", i, got.Hosts[i].Name, name)
			}
		}
		if got.Hosts[2].Swept || got.Hosts[2].LineCount != 0 {
			t.Errorf("gamma reported as swept: %+v", got.Hosts[2])
		}
	}

	// Count desc, then signature — the same order the TUI screen uses.
	if len(got.Groups) != 2 {
		t.Fatalf("groups = %d, want 2: %+v", len(got.Groups), got.Groups)
	}
	first := got.Groups[0]
	if first.Sig != "db down" || first.Count != 2 || first.Sev != "error" {
		t.Errorf("first group = %+v, want db down ×2 error", first)
	}
	if len(first.Hosts) != 1 || first.Hosts[0] != "alpha" {
		t.Errorf("first hosts = %v, want [alpha]", first.Hosts)
	}
	if len(first.Containers) != 1 || first.Containers[0] != "alpha/web" {
		t.Errorf("first containers = %v, want [alpha/web]", first.Containers)
	}
	if !first.FirstSeen.Equal(t1) || !first.LastSeen.Equal(t1.Add(time.Minute)) {
		t.Errorf("first window = %v..%v", first.FirstSeen, first.LastSeen)
	}
	// The sample is the newest line, so the page never shows a stale warning
	// beside a signature the TUI calls an error.
	if first.Sample == nil || first.Sample.Text != "ERROR db down" ||
		first.Sample.Host != "alpha" || first.Sample.Container != "web" || first.Sample.Stamp != "01:01:00" {
		t.Errorf("first sample = %+v", first.Sample)
	}
	if got.Groups[1].Sig != "no space left" || got.Groups[1].Count != 1 {
		t.Errorf("second group = %+v", got.Groups[1])
	}
	// examples is the clipboard body the page's copy button writes: every line of
	// the group, oldest first, so it reads like a log and ends on the sample.
	if len(first.Examples) != 2 {
		t.Fatalf("first examples = %+v, want both lines", first.Examples)
	}
	if first.Examples[0].Text != "WARN db down" || first.Examples[1].Text != "ERROR db down" {
		t.Errorf("examples order = %q, %q, want warn then error",
			first.Examples[0].Text, first.Examples[1].Text)
	}
	if first.Examples[len(first.Examples)-1] != *first.Sample {
		t.Errorf("newest example %+v disagrees with the sample %v",
			first.Examples[len(first.Examples)-1], *first.Sample)
	}
}

// A tag is TUI-authored state the page only mirrors: it must reach the table, the
// graph and the error rows, and an unlabelled container must publish nothing for
// the page to colour.
func TestTagFieldsEndpoint(t *testing.T) {
	store, hs := newTestServer(t)
	at := time.Unix(1700000000, 0).UTC()
	store.Publish("h1", SessionSnapshot{
		ID: "h1", Name: "alpha", Connected: true, FetchedAt: at,
		Containers: []ContainerSnapshot{
			{ID: "c1", Name: "odysseus-postfix-1", Tag: "mail", TagColor: "sky", Image: "postfix", State: "running", Networks: []string{"appnet"}},
			{ID: "c2", Name: "redis", Image: "redis:7", State: "running", Networks: []string{"appnet"}},
		},
		ErrorsFetchedAt: at,
		Errors: []ErrorSnapshot{
			{ContainerID: "c1", Name: "odysseus-postfix-1", Tag: "mail", Stamp: "01:00:00", At: at, Sev: "error", Sig: "queue stalled", Text: "ERROR queue stalled"},
		},
	})

	var sess sessionJSON
	getJSON(t, hs.URL+"/api/sessions/h1", &sess)
	if len(sess.Containers) != 2 {
		t.Fatalf("containers = %+v", sess.Containers)
	}
	if sess.Containers[0].Tag != "mail" || sess.Containers[0].TagColor != "sky" {
		t.Errorf("tagged container = %+v", sess.Containers[0])
	}
	if sess.Containers[1].Tag != "" || sess.Containers[1].TagColor != "" {
		t.Errorf("unlabelled container published %q/%q", sess.Containers[1].Tag, sess.Containers[1].TagColor)
	}

	var net networkJSON
	getJSON(t, hs.URL+"/api/sessions/h1/network", &net)
	var labelled, plain bool
	for _, n := range net.Nodes {
		switch n.Name {
		case "odysseus-postfix-1":
			labelled = n.Tag == "mail" && n.Color == "sky"
			if !labelled {
				t.Errorf("graph node = %+v, want the label and its colour", n)
			}
		case "redis":
			plain = n.Tag == "" && n.Color == ""
		}
	}
	if !labelled || !plain {
		t.Errorf("graph nodes: labelled=%v plain=%v", labelled, plain)
	}

	var errs errorsJSON
	getJSON(t, hs.URL+"/api/errors", &errs)
	if len(errs.Groups) != 1 {
		t.Fatalf("groups = %+v", errs.Groups)
	}
	g := errs.Groups[0]
	// Identity stays the docker name, so relabelling cannot re-merge history;
	// only the display string carries the label.
	if len(g.Containers) != 1 || g.Containers[0] != "alpha/mail odysseus-postfix-1" {
		t.Errorf("group containers = %v, want host/label name", g.Containers)
	}
	if g.Sample == nil || g.Sample.Tag != "mail" {
		t.Errorf("group sample = %+v, want the published tag", g.Sample)
	}
	// A group with more lines than the cap keeps the newest ones, because those
	// are what a paste is for.
	store.Publish("h2", manyErrSnap("h2", "beta", "disk full", maxErrorExamples+5, at))
	getJSON(t, hs.URL+"/api/errors", &errs)
	var big errorGroupJSON
	for _, gr := range errs.Groups {
		if gr.Sig == "disk full" {
			big = gr
		}
	}
	if len(big.Examples) != maxErrorExamples {
		t.Fatalf("examples = %d, want the cap %d", len(big.Examples), maxErrorExamples)
	}
	if big.Examples[0].Text != "ERROR disk full 5" {
		t.Errorf("oldest kept example = %q, want the newest window, not the oldest", big.Examples[0].Text)
	}
}

func manyErrSnap(id, name, sig string, n int, at time.Time) SessionSnapshot {
	snap := SessionSnapshot{ID: id, Name: name, Connected: true, FetchedAt: at, ErrorsFetchedAt: at}
	for i := range n {
		snap.Errors = append(snap.Errors, ErrorSnapshot{
			ContainerID: "c9", Name: "api", Stamp: "01:00:00",
			At: at.Add(time.Duration(i) * time.Second), Sev: "error", Sig: sig,
			Text: "ERROR " + sig + " " + strconv.Itoa(i),
		})
	}
	return snap
}

func getJSON(t *testing.T, url string, into any) {
	t.Helper()
	res, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer res.Body.Close()
	body, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatalf("read %s: %v", url, err)
	}
	if res.StatusCode != http.StatusOK {
		t.Fatalf("GET %s -> %d: %s", url, res.StatusCode, body)
	}
	if err := json.Unmarshal(body, into); err != nil {
		t.Fatalf("decode %s: %v\n%s", url, err, body)
	}
}

// Two polls of an unchanged fleet must produce byte-identical JSON, or the
// page's skip-repaint comparison churns on every request.
func TestErrorsEndpointIsStable(t *testing.T) {
	store, hs := newTestServer(t)
	at := time.Unix(1700000100, 0).UTC()
	mk := func(hostID, name, cont string, n int) SessionSnapshot {
		snap := errHostSnap(hostID, name, true, at)
		for i := 0; i < n; i++ {
			snap.Errors = append(snap.Errors, ErrorSnapshot{
				ContainerID: cont, Name: cont, At: at.Add(time.Duration(i) * time.Second),
				Sev: "error", Sig: "boom " + cont, Text: "ERROR boom",
			})
		}
		return snap
	}
	// Same count and same instant on both hosts: the only tie-break available
	// is a total order, so this is the shape that could shuffle.
	store.Publish("h1", mk("h1", "alpha", "c1", 2))
	store.Publish("h2", mk("h2", "beta", "c2", 2))
	store.Publish("h3", mk("h3", "gamma", "c3", 2))

	var first []byte
	for i := 0; i < 12; i++ {
		res, err := http.Get(hs.URL + "/api/errors")
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(res.Body)
		res.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
		if first == nil {
			first = body
			continue
		}
		if !bytes.Equal(first, body) {
			t.Fatalf("poll %d differs:\n%s\n%s", i, first, body)
		}
	}
}

func TestErrorsEndpointTruncatesGroups(t *testing.T) {
	store, hs := newTestServer(t)
	at := time.Unix(1700000000, 0).UTC()
	snap := errHostSnap("h1", "alpha", true, at)
	for i := 0; i < maxErrorGroups+5; i++ {
		snap.Errors = append(snap.Errors, ErrorSnapshot{
			ContainerID: "c1", Name: "web", At: at,
			Sev: "error", Sig: string(rune('a'+i%26)) + "-" + strconv.Itoa(i), Text: "ERROR boom",
		})
	}
	store.Publish("h1", snap)

	res, err := http.Get(hs.URL + "/api/errors")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var got errorsJSON
	if err := json.NewDecoder(res.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if len(got.Groups) != maxErrorGroups {
		t.Fatalf("groups = %d, want the %d cap", len(got.Groups), maxErrorGroups)
	}
	if !got.Truncated {
		t.Error("truncated false while groups were dropped")
	}
	// The host still reports the corpus it published, cap or no cap.
	if got.Hosts[0].LineCount != maxErrorGroups+5 || got.LineCount != maxErrorGroups+5 {
		t.Errorf("host lines = %d, payload lines = %d, want %d",
			got.Hosts[0].LineCount, got.LineCount, maxErrorGroups+5)
	}
}

// A payload with nothing swept is still a valid, empty aggregate.
func TestErrorsEndpointEmpty(t *testing.T) {
	store, hs := newTestServer(t)
	store.Publish("h1", SessionSnapshot{ID: "h1", Name: "alpha", Connected: true,
		Containers: []ContainerSnapshot{{ID: "c", Name: "web"}},
		HostStats:  models.HostStats{NumCPU: 2}})

	res, err := http.Get(hs.URL + "/api/errors")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	body, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	var got errorsJSON
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	if got.Swept != 0 || got.Connected != 1 || got.LineCount != 0 || len(got.Groups) != 0 {
		t.Errorf("empty aggregate = %+v", got)
	}
	// Never null arrays: the page iterates both without guards.
	if !json.Valid(body) || bytes.Contains(body, []byte("null")) {
		t.Errorf("payload contains a null: %s", body)
	}
}

// The perf key set is the contract the #perf tab is written against: the page
// sorts nothing and thresholds nothing, so it may not have to guess a name.
func TestSessionEndpointPerfContract(t *testing.T) {
	store, hs := newTestServer(t)
	store.Publish("h1", perfFixture())
	store.Publish("h2", SessionSnapshot{ID: "h2", Name: "raw"})

	res, err := http.Get(hs.URL + "/api/sessions/h1")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	body, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	var top map[string]any
	if err := json.Unmarshal(body, &top); err != nil {
		t.Fatal(err)
	}
	// Eight, because hostStats is omitted with none — the same rule the
	// sessions tab already relies on.
	if len(top) != 8 {
		t.Errorf("session has %d top-level keys, want 8: %s", len(top), body)
	}
	perfObj, ok := top["perf"].(map[string]any)
	if !ok {
		t.Fatalf("published window missing from payload: %s", body)
	}
	if len(perfObj) != 7 {
		t.Errorf("perf has %d keys, want 7: %v", len(perfObj), perfObj)
	}
	for _, key := range []string{"windowSec", "retentionSec", "samples", "probed", "probedAt", "supported", "rows"} {
		if _, ok := perfObj[key]; !ok {
			t.Errorf("perf is missing %q: %v", key, perfObj)
		}
	}

	rows := perfObj["rows"].([]any)
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want 2: %v", len(rows), rows)
	}
	probed := rows[0].(map[string]any)
	if probed["id"] != "bbb222222222" {
		t.Errorf("rank 1 = %v, want the hog: the terminal's order must survive the wire", probed["id"])
	}
	if len(probed) != 17 {
		t.Errorf("probed row has %d keys, want 17: %v", len(probed), probed)
	}
	if len(rows[1].(map[string]any)) != 12 {
		t.Errorf("unprobed row has %d keys, want 12 (no all-time claims): %v",
			len(rows[1].(map[string]any)), rows[1])
	}

	res2, err := http.Get(hs.URL + "/api/sessions/h2")
	if err != nil {
		t.Fatal(err)
	}
	defer res2.Body.Close()
	body2, err := io.ReadAll(res2.Body)
	if err != nil {
		t.Fatal(err)
	}
	var bare map[string]any
	if err := json.Unmarshal(body2, &bare); err != nil {
		t.Fatal(err)
	}
	if _, ok := bare["perf"]; ok {
		t.Errorf("session without samples publishes perf: %s", body2)
	}
	if len(bare) != 7 {
		t.Errorf("bare session has %d keys, want 7: %s", len(bare), body2)
	}
}
