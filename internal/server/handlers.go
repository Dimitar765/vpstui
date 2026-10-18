package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"vpstui/internal/apitypes"
	"vpstui/internal/models"
	"vpstui/internal/orchestrator"
	"vpstui/internal/secret"
	"vpstui/internal/sshclient"
	"vpstui/internal/store"
)

// ---------------------------------------------------------------- DTOs

type serverReq struct {
	Name               string `json:"name"`
	Address            string `json:"address"`
	Port               int    `json:"port"`
	User               string `json:"user"`
	AuthType           string `json:"authType"`
	KeyPath            string `json:"keyPath"`
	BaseDir            string `json:"baseDir"`
	MonitorIntervalSec int    `json:"monitorIntervalSec"`
	ProxyHTTPPort      int    `json:"proxyHttpPort"`
	ProxyHTTPSPort     int    `json:"proxyHttpsPort"`
	Password           string `json:"password"`   // secret: straight into secret.Set, never stored
	Passphrase         string `json:"passphrase"` // secret: key auth only
}

type serverResp struct {
	ID                 string     `json:"id"`
	Name               string     `json:"name"`
	Address            string     `json:"address"`
	Port               int        `json:"port"`
	User               string     `json:"user"`
	AuthType           string     `json:"authType"`
	KeyPath            string     `json:"keyPath,omitempty"`
	BaseDir            string     `json:"baseDir"`
	MonitorIntervalSec int        `json:"monitorIntervalSec"`
	ProxyHTTPPort      int        `json:"proxyHttpPort"`
	ProxyHTTPSPort     int        `json:"proxyHttpsPort"`
	Status             string     `json:"status"`
	CreatedAt          time.Time  `json:"createdAt"`
	LastSeenAt         *time.Time `json:"lastSeenAt,omitempty"`
	HasPassword        bool       `json:"hasPassword"`
	HasPassphrase      bool       `json:"hasPassphrase"`
}

func toServerResp(s store.Server) serverResp {
	r := serverResp{
		ID: s.ID, Name: s.Name, Address: s.Address, Port: s.Port, User: s.User,
		AuthType: s.AuthType, KeyPath: s.KeyPath, BaseDir: s.BaseDir,
		MonitorIntervalSec: s.MonitorIntervalSec,
		ProxyHTTPPort:      s.ProxyHTTPPort, ProxyHTTPSPort: s.ProxyHTTPSPort,
		Status: s.Status, CreatedAt: s.CreatedAt, LastSeenAt: s.LastSeenAt,
	}
	if s.AuthType == models.AuthPassword {
		_, err := secret.Get(s.ID + ":password")
		r.HasPassword = err == nil
	} else if s.AuthType == models.AuthKey {
		_, err := secret.Get(s.ID + ":passphrase")
		r.HasPassphrase = err == nil
	}
	return r
}

// applySecrets stores credentials under "<serverID>:password"/
// "<serverID>:passphrase". Empty fields leave existing values alone, so a
// PUT that only renames a server cannot wipe its credentials.
func applySecrets(id string, req *serverReq) error {
	if req.Password != "" {
		if err := secret.Set(id+":password", req.Password); err != nil {
			return fmt.Errorf("store password: %w", err)
		}
	}
	if req.Passphrase != "" {
		if err := secret.Set(id+":passphrase", req.Passphrase); err != nil {
			return fmt.Errorf("store passphrase: %w", err)
		}
	}
	return nil
}

func (req *serverReq) normalize() error {
	req.Name = strings.TrimSpace(req.Name)
	req.Address = strings.TrimSpace(req.Address)
	req.User = strings.TrimSpace(req.User)
	req.KeyPath = strings.TrimSpace(req.KeyPath)
	req.BaseDir = strings.TrimSpace(req.BaseDir)
	if req.Name == "" || req.Address == "" || req.User == "" {
		return errors.New("name, address and user are required")
	}
	if req.Port == 0 {
		req.Port = 22
	}
	switch req.AuthType {
	case "", models.AuthKey, models.AuthPassword, models.AuthAgent:
	default:
		return fmt.Errorf("authType must be %s|%s|%s", models.AuthKey, models.AuthPassword, models.AuthAgent)
	}
	if req.AuthType == "" {
		req.AuthType = models.AuthKey
	}
	if req.BaseDir == "" {
		req.BaseDir = "/opt/vpstui"
	}
	if req.MonitorIntervalSec == 0 {
		req.MonitorIntervalSec = 15
	}
	if req.ProxyHTTPPort == 0 {
		req.ProxyHTTPPort = 80
	}
	if req.ProxyHTTPSPort == 0 {
		req.ProxyHTTPSPort = 443
	}
	return nil
}

func (req *serverReq) row() store.Server {
	return store.Server{
		Name: req.Name, Address: req.Address, Port: req.Port, User: req.User,
		AuthType: req.AuthType, KeyPath: req.KeyPath, BaseDir: req.BaseDir,
		MonitorIntervalSec: req.MonitorIntervalSec,
		ProxyHTTPPort:      req.ProxyHTTPPort, ProxyHTTPSPort: req.ProxyHTTPSPort,
	}
}

// ---------------------------------------------------------------- handlers

func (s *Server) handleServersList(w http.ResponseWriter, r *http.Request) {
	rows, err := s.db.Servers()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	out := make([]serverResp, 0, len(rows))
	for _, row := range rows {
		out = append(out, toServerResp(row))
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleServersCreate(w http.ResponseWriter, r *http.Request) {
	var req serverReq
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad JSON body")
		return
	}
	if err := req.normalize(); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	row := req.row()
	row.ID = models.NewID()
	row.Status = apitypes.StatusUnknown
	if err := applySecrets(row.ID, &req); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := s.db.CreateServer(row); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	// A new row needs its monitor; reconcile in the background so a slow
	// registry write never blocks the request.
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_ = s.orch.Reapply(ctx)
	}()
	writeJSON(w, http.StatusCreated, toServerResp(row))
}

func (s *Server) serverRow(w http.ResponseWriter, r *http.Request) (store.Server, bool) {
	row, err := s.db.ServerByID(r.PathValue("id"))
	if errors.Is(err, store.ErrNotFound) {
		writeErr(w, http.StatusNotFound, "server not found")
		return store.Server{}, false
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return store.Server{}, false
	}
	return row, true
}

func (s *Server) handleServerGet(w http.ResponseWriter, r *http.Request) {
	row, ok := s.serverRow(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, toServerResp(row))
}

func (s *Server) handleServerUpdate(w http.ResponseWriter, r *http.Request) {
	row, ok := s.serverRow(w, r)
	if !ok {
		return
	}
	var req serverReq
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad JSON body")
		return
	}
	if err := req.normalize(); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	updated := req.row()
	updated.ID = row.ID
	updated.CreatedAt = row.CreatedAt
	updated.Status = row.Status
	updated.LastSeenAt = row.LastSeenAt
	if err := applySecrets(row.ID, &req); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := s.db.UpdateServer(updated); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	// Dial settings may have changed: drop the pooled connection and let the
	// reconciler restart the monitor.
	s.orch.PoolRemove(row.ID)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_ = s.orch.Reapply(ctx)
	}()
	writeJSON(w, http.StatusOK, toServerResp(updated))
}

func (s *Server) handleServerDelete(w http.ResponseWriter, r *http.Request) {
	row, ok := s.serverRow(w, r)
	if !ok {
		return
	}
	s.orch.PoolRemove(row.ID)
	if err := s.db.DeleteServer(row.ID); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	_ = secret.Delete(row.ID + ":password")
	_ = secret.Delete(row.ID + ":passphrase")
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_ = s.orch.Reapply(ctx)
	}()
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

// handleServerTest dials fresh (bypassing the pool) and probes docker plus
// the compose plugin, reporting a friendly message on failure.
func (s *Server) handleServerTest(w http.ResponseWriter, r *http.Request) {
	row, ok := s.serverRow(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	defer cancel()
	if err := probeServer(ctx, row); err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// probeServer dials and runs the docker ps probe plus a compose version
// check (compose missing is reported as its own failure: monitoring would
// still work, deploys would not).
func probeServer(ctx context.Context, row store.Server) error {
	sec, err := orchestrator.SecretFor(row)
	if err != nil {
		return fmt.Errorf("resolve credentials: %w", err)
	}
	conn, err := sshclient.Dial(ctx, orchestrator.HostRow(row), sec)
	if err != nil {
		return fmt.Errorf("dial %s: %w", row.Name, err)
	}
	defer conn.Close()
	if _, errOut, err := conn.Exec(ctx, "docker ps --format '{{.ID}}' | head -1"); err != nil {
		msg := strings.TrimSpace(errOut)
		if msg == "" {
			msg = err.Error()
		}
		return fmt.Errorf("docker probe failed: %s", msg)
	}
	cv, _, err := conn.Exec(ctx, "docker compose version --short")
	if err != nil || strings.TrimSpace(cv) == "" {
		return fmt.Errorf("docker reachable, but the compose plugin is missing — install docker-compose-plugin v2")
	}
	return nil
}

func (s *Server) handleContainerAction(w http.ResponseWriter, r *http.Request) {
	action := r.PathValue("action")
	switch action {
	case "start", "stop", "restart", "remove":
	default:
		writeErr(w, http.StatusBadRequest, "action must be start|stop|restart|remove")
		return
	}
	row, ok := s.serverRow(w, r)
	if !ok {
		return
	}
	if err := s.orch.ContainerAction(r.Context(), row.ID, r.PathValue("cid"), action); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// ---------------------------------------------------------------- fleet

func (s *Server) handleFleet(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, apitypes.FleetResponse{Servers: s.orch.Fleet()})
}

func (s *Server) handleFleetErrors(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.orch.FleetErrors(200))
}
