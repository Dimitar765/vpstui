package httpapi

import (
	"encoding/json"
	"net/http"
	"sort"
)

type handler struct{ store *Store }

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func (h *handler) healthz(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// index serves the bundled dashboard. Only "/" resolves; anything else the mux
// falls through to here is a plain 404.
func (h *handler) index(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(indexHTML)
}

func (h *handler) unavailable(w http.ResponseWriter) {
	writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "session store closed"})
}

func (h *handler) listSessions(w http.ResponseWriter, _ *http.Request) {
	snaps, open := h.store.AllIfOpen()
	if !open {
		h.unavailable(w)
		return
	}
	sort.Slice(snaps, func(i, j int) bool { return snaps[i].Name < snaps[j].Name })
	out := make([]sessionSummaryJSON, 0, len(snaps))
	for _, s := range snaps {
		out = append(out, toSessionSummaryJSON(s))
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *handler) getSession(w http.ResponseWriter, r *http.Request) {
	snap, open, ok := h.store.GetIfOpen(r.PathValue("id"))
	if !open {
		h.unavailable(w)
		return
	}
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "session not found"})
		return
	}
	writeJSON(w, http.StatusOK, toSessionJSON(snap))
}

// getNetwork serves the container↔network graph. It is a separate endpoint so
// the sessions poll never carries graph bytes while only the table is shown.
func (h *handler) getNetwork(w http.ResponseWriter, r *http.Request) {
	snap, open, ok := h.store.GetIfOpen(r.PathValue("id"))
	if !open {
		h.unavailable(w)
		return
	}
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "session not found"})
		return
	}
	writeJSON(w, http.StatusOK, toNetworkJSON(snap))
}

// getErrors serves the fleet-wide error aggregate. It reduces the corpora the
// TUI already published, so one request costs a read lock and a sort — never an
// SSH round trip, and never a second opinion about what counted as an error.
func (h *handler) getErrors(w http.ResponseWriter, _ *http.Request) {
	snaps, open := h.store.AllIfOpen()
	if !open {
		h.unavailable(w)
		return
	}
	writeJSON(w, http.StatusOK, toErrorsJSON(snaps))
}
