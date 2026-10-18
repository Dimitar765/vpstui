package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"regexp"

	"vpstui/internal/apitypes"
	"vpstui/internal/models"
	"vpstui/internal/store"
)

// registerAlerts: the alert-rule domain. Evaluation lives in the monitor;
// this file is CRUD + validation.
func (s *Server) registerAlerts(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/alerts", s.handleAlertsList)
	mux.HandleFunc("POST /api/alerts", s.handleAlertCreate)
	mux.HandleFunc("PUT /api/alerts/{id}", s.handleAlertUpdate)
	mux.HandleFunc("DELETE /api/alerts/{id}", s.handleAlertDelete)
}

func toAlertResp(a store.AlertRule) apitypes.AlertRule {
	return apitypes.AlertRule{
		ID: a.ID, ServerID: a.ServerID, ContainerPattern: a.ContainerPattern,
		Metric: a.Metric, Threshold: a.Threshold, DurationSec: a.DurationSec,
		Enabled: a.Enabled, State: a.State, LastFiredAt: a.LastFiredAt,
	}
}

func (s *Server) handleAlertsList(w http.ResponseWriter, r *http.Request) {
	rows, err := s.db.AlertRules()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	out := make([]apitypes.AlertRule, 0, len(rows))
	for _, a := range rows {
		out = append(out, toAlertResp(a))
	}
	writeJSON(w, http.StatusOK, out)
}

type alertReq struct {
	ServerID         string   `json:"serverId"`
	ContainerPattern string   `json:"containerPattern"`
	Metric           string   `json:"metric"`
	Threshold        *float64 `json:"threshold"`
	DurationSec      *int     `json:"durationSec"`
	Enabled          *bool    `json:"enabled"`
}

// normalize validates the rule request and fills defaults (duration 300 s,
// enabled true, threshold 0). A container pattern must compile — a rule that
// can never match would otherwise silently sit enabled forever; the monitor
// additionally disables such rows if it ever meets one out-of-band.
func (req *alertReq) normalize(s *Server) error {
	known := map[string]bool{
		apitypes.MetricLoad1: true, apitypes.MetricMemPct: true,
		apitypes.MetricContainerCPUPct: true, apitypes.MetricContainerMemPct: true,
		apitypes.MetricContainerDown: true,
	}
	if !known[req.Metric] {
		return errors.New("metric must be load1, mem_pct, container_cpu_pct, container_mem_pct or container_down")
	}
	if req.DurationSec != nil && *req.DurationSec < 0 {
		return errors.New("durationSec must be >= 0")
	}
	if req.Threshold != nil && *req.Threshold < 0 {
		return errors.New("threshold must be >= 0")
	}
	isContainer := req.Metric == apitypes.MetricContainerCPUPct ||
		req.Metric == apitypes.MetricContainerMemPct || req.Metric == apitypes.MetricContainerDown
	if !isContainer && req.ContainerPattern != "" {
		return errors.New("containerPattern only applies to container_* metrics")
	}
	if req.ContainerPattern != "" {
		if _, err := regexp.Compile(req.ContainerPattern); err != nil {
			return errors.New("containerPattern: " + err.Error())
		}
	}
	if req.ServerID != "" {
		if _, err := s.db.ServerByID(req.ServerID); err != nil {
			return errors.New("serverId: not found")
		}
	}
	return nil
}

func (req *alertReq) row(id string) store.AlertRule {
	var threshold float64
	if req.Threshold != nil {
		threshold = *req.Threshold
	}
	duration := 300
	if req.DurationSec != nil {
		duration = *req.DurationSec
	}
	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	return store.AlertRule{
		ID: id, ServerID: req.ServerID, ContainerPattern: req.ContainerPattern,
		Metric: req.Metric, Threshold: threshold, DurationSec: duration, Enabled: enabled,
	}
}

func (s *Server) handleAlertCreate(w http.ResponseWriter, r *http.Request) {
	var req alertReq
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad JSON body")
		return
	}
	if err := req.normalize(s); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	row := req.row(models.NewID())
	row.State = apitypes.AlertStateOK
	if err := s.db.CreateAlertRule(row); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, toAlertResp(row))
}

func (s *Server) alertRow(w http.ResponseWriter, r *http.Request) (store.AlertRule, bool) {
	a, err := s.db.AlertRuleByID(r.PathValue("id"))
	if errors.Is(err, store.ErrNotFound) {
		writeErr(w, http.StatusNotFound, "alert rule not found")
		return store.AlertRule{}, false
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return store.AlertRule{}, false
	}
	return a, true
}

// handleAlertUpdate replaces the rule's definition. State survives (only the
// evaluator transitions it); a definition change resets the monitor's
// sustain map on its next pass because the compiled shape changed.
func (s *Server) handleAlertUpdate(w http.ResponseWriter, r *http.Request) {
	cur, ok := s.alertRow(w, r)
	if !ok {
		return
	}
	var req alertReq
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad JSON body")
		return
	}
	if err := req.normalize(s); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	row := req.row(cur.ID)
	row.State = cur.State
	row.LastFiredAt = cur.LastFiredAt
	if err := s.db.UpdateAlertRule(row); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	updated, _ := s.db.AlertRuleByID(cur.ID)
	writeJSON(w, http.StatusOK, toAlertResp(updated))
}

func (s *Server) handleAlertDelete(w http.ResponseWriter, r *http.Request) {
	a, ok := s.alertRow(w, r)
	if !ok {
		return
	}
	if err := s.db.DeleteAlertRule(a.ID); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}
