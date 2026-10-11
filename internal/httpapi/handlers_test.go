package httpapi

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
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
	for _, path := range []string{"/api/sessions", "/api/sessions/a", "/api/sessions/a/network"} {
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
