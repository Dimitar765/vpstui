package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"vpstui/internal/apitypes"
	"vpstui/internal/models"
	"vpstui/internal/store"
)

// registerApps: projects, environments and the app registry + deploy actions.
func (s *Server) registerApps(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/projects", s.handleProjectsList)
	mux.HandleFunc("POST /api/projects", s.handleProjectCreate)
	mux.HandleFunc("GET /api/projects/{id}", s.handleProjectGet)
	mux.HandleFunc("DELETE /api/projects/{id}", s.handleProjectDelete)
	mux.HandleFunc("POST /api/projects/{id}/environments", s.handleEnvironmentCreate)
	mux.HandleFunc("DELETE /api/environments/{id}", s.handleEnvironmentDelete)
	mux.HandleFunc("GET /api/apps", s.handleAppsList)
	mux.HandleFunc("POST /api/apps", s.handleAppCreate)
	mux.HandleFunc("GET /api/apps/{id}", s.handleAppGet)
	mux.HandleFunc("PUT /api/apps/{id}", s.handleAppUpdate)
	mux.HandleFunc("DELETE /api/apps/{id}", s.handleAppDelete)
	mux.HandleFunc("POST /api/apps/{id}/deploy", s.handleAppDeploy)
	mux.HandleFunc("POST /api/apps/{id}/rollback", s.handleAppRollback)
	mux.HandleFunc("GET /api/apps/{id}/deployments", s.handleAppDeployments)
}

// ---------------------------------------------------------------- DTOs

type projectReq struct {
	Name string `json:"name"`
}

type environmentReq struct {
	Name string `json:"name"`
}

type appReq struct {
	EnvironmentID string            `json:"environmentId"`
	ServerID      string            `json:"serverId"`
	Name          string            `json:"name"`
	SourceType    string            `json:"sourceType"`
	SourceConfig  map[string]any    `json:"sourceConfig"`
	EnvVars       map[string]string `json:"envVars"`
	Domains       []string          `json:"domains"`
	Port          int               `json:"port"`
	AutoDeploy    *bool             `json:"autoDeploy"`
}

func (req *appReq) normalize() error {
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" {
		return errors.New("name is required")
	}
	if req.EnvironmentID == "" || req.ServerID == "" {
		return errors.New("environmentId and serverId are required")
	}
	switch req.SourceType {
	case "image", "compose", "dockerfile", "static", "template":
	default:
		return errors.New("sourceType must be image, compose, dockerfile, static or template")
	}
	if req.SourceConfig == nil {
		req.SourceConfig = map[string]any{}
	}
	if req.EnvVars == nil {
		req.EnvVars = map[string]string{}
	}
	if req.Domains == nil {
		req.Domains = []string{}
	}
	return nil
}

func (req *appReq) row() store.App {
	auto := true
	if req.AutoDeploy != nil {
		auto = *req.AutoDeploy
	}
	return store.App{
		EnvironmentID: req.EnvironmentID,
		ServerID:      req.ServerID,
		Name:          req.Name,
		SourceType:    req.SourceType,
		SourceConfig:  req.SourceConfig,
		EnvVars:       req.EnvVars,
		Domains:       req.Domains,
		Port:          req.Port,
		AutoDeploy:    auto,
		Status:        apitypes.AppUndeployed,
		UpdatedAt:     time.Now().UTC(),
	}
}

func toAppResp(a store.App) apitypes.App {
	return apitypes.App{
		ID: a.ID, EnvironmentID: a.EnvironmentID, ServerID: a.ServerID, Name: a.Name,
		SourceType: a.SourceType, SourceConfig: a.SourceConfig, EnvVars: a.EnvVars,
		Domains: a.Domains, Port: a.Port, AutoDeploy: a.AutoDeploy,
		WebhookToken: a.WebhookToken, LastGoodDeployment: a.LastGoodDeployment,
		Status: a.Status, UpdatedAt: a.UpdatedAt,
	}
}

func toDeploymentResp(d store.Deployment) apitypes.Deployment {
	return apitypes.Deployment{
		ID: d.ID, AppID: d.AppID, Status: d.Status, Trigger: d.Trigger, Release: d.Release,
		CreatedAt: d.CreatedAt, StartedAt: d.StartedAt, FinishedAt: d.FinishedAt,
	}
}

// ---------------------------------------------------------------- projects

func (s *Server) handleProjectsList(w http.ResponseWriter, r *http.Request) {
	rows, err := s.db.Projects()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	out := make([]apitypes.Project, 0, len(rows))
	for _, p := range rows {
		out = append(out, apitypes.Project{ID: p.ID, Name: p.Name, CreatedAt: p.CreatedAt})
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleProjectCreate(w http.ResponseWriter, r *http.Request) {
	var req projectReq
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad JSON body")
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" {
		writeErr(w, http.StatusBadRequest, "name is required")
		return
	}
	p := store.Project{ID: models.NewID(), Name: req.Name, CreatedAt: time.Now().UTC()}
	if err := s.db.CreateProject(p); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, apitypes.Project{ID: p.ID, Name: p.Name, CreatedAt: p.CreatedAt})
}

func (s *Server) projectRow(w http.ResponseWriter, r *http.Request) (store.Project, bool) {
	p, err := s.db.ProjectByID(r.PathValue("id"))
	if errors.Is(err, store.ErrNotFound) {
		writeErr(w, http.StatusNotFound, "project not found")
		return store.Project{}, false
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return store.Project{}, false
	}
	return p, true
}

func (s *Server) handleProjectGet(w http.ResponseWriter, r *http.Request) {
	p, ok := s.projectRow(w, r)
	if !ok {
		return
	}
	envs, err := s.db.EnvironmentsByProject(p.ID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	out := make([]apitypes.Environment, 0, len(envs))
	for _, e := range envs {
		out = append(out, apitypes.Environment{ID: e.ID, ProjectID: e.ProjectID, Name: e.Name})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"id": p.ID, "name": p.Name, "createdAt": p.CreatedAt, "environments": out,
	})
}

func (s *Server) handleProjectDelete(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.projectRow(w, r); !ok {
		return
	}
	if err := s.db.DeleteProject(r.PathValue("id")); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

func (s *Server) handleEnvironmentCreate(w http.ResponseWriter, r *http.Request) {
	p, ok := s.projectRow(w, r)
	if !ok {
		return
	}
	var req environmentReq
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad JSON body")
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" {
		writeErr(w, http.StatusBadRequest, "name is required")
		return
	}
	e := store.Environment{ID: models.NewID(), ProjectID: p.ID, Name: req.Name}
	if err := s.db.CreateEnvironment(e); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, apitypes.Environment{ID: e.ID, ProjectID: e.ProjectID, Name: e.Name})
}

func (s *Server) handleEnvironmentDelete(w http.ResponseWriter, r *http.Request) {
	if err := s.db.DeleteEnvironment(r.PathValue("id")); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeErr(w, http.StatusNotFound, "environment not found")
			return
		}
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

// ---------------------------------------------------------------- apps

func (s *Server) handleAppsList(w http.ResponseWriter, r *http.Request) {
	rows, err := s.db.Apps()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	out := make([]apitypes.App, 0, len(rows))
	for _, a := range rows {
		out = append(out, toAppResp(a))
	}
	writeJSON(w, http.StatusOK, out)
}

// newWebhookToken mints the app's unique deploy-webhook token (S7 consumes
// it); 12 random bytes hex, collision-retried against the unique index.
func newWebhookToken() string {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	return "wh_" + hex.EncodeToString(b)
}

func (s *Server) handleAppCreate(w http.ResponseWriter, r *http.Request) {
	var req appReq
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<20)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad JSON body")
		return
	}
	if err := req.normalize(); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if _, err := s.db.EnvironmentByID(req.EnvironmentID); err != nil {
		writeErr(w, http.StatusBadRequest, "environment not found")
		return
	}
	if _, err := s.db.ServerByID(req.ServerID); err != nil {
		writeErr(w, http.StatusBadRequest, "server not found")
		return
	}
	row := req.row()
	row.ID = models.NewID()
	row.WebhookToken = newWebhookToken()
	if err := s.db.CreateApp(row); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, toAppResp(row))
}

func (s *Server) appRow(w http.ResponseWriter, r *http.Request) (store.App, bool) {
	a, err := s.db.AppByID(r.PathValue("id"))
	if errors.Is(err, store.ErrNotFound) {
		writeErr(w, http.StatusNotFound, "app not found")
		return store.App{}, false
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return store.App{}, false
	}
	return a, true
}

func (s *Server) handleAppGet(w http.ResponseWriter, r *http.Request) {
	a, ok := s.appRow(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, toAppResp(a))
}

func (s *Server) handleAppUpdate(w http.ResponseWriter, r *http.Request) {
	a, ok := s.appRow(w, r)
	if !ok {
		return
	}
	var req appReq
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<20)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad JSON body")
		return
	}
	// PUT replaces the mutable fields; identity fields stay pinned. Source
	// config must be present to be replaced (a name-only rename should not
	// wipe it).
	if req.SourceConfig != nil {
		a.SourceConfig = req.SourceConfig
	}
	if req.EnvVars != nil {
		a.EnvVars = req.EnvVars
	}
	if req.Domains != nil {
		a.Domains = req.Domains
	}
	a.Port = req.Port
	if req.Name != "" {
		a.Name = req.Name
	}
	if req.AutoDeploy != nil {
		a.AutoDeploy = *req.AutoDeploy
	}
	a.UpdatedAt = time.Now().UTC()
	if err := s.db.UpdateApp(a); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, toAppResp(a))
}

func (s *Server) handleAppDelete(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.appRow(w, r); !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Minute)
	defer cancel()
	if err := s.engine.Destroy(ctx, r.PathValue("id")); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

// ---------------------------------------------------------------- deploy actions

// deployResp mirrors the S7 webhook contract: the queued deployment id.
type deployResp struct {
	DeploymentID string `json:"deploymentId"`
}

func (s *Server) handleAppDeploy(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.appRow(w, r); !ok {
		return
	}
	depID, err := s.engine.Enqueue(r.PathValue("id"), apitypes.TriggerManual)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, deployResp{DeploymentID: depID})
}

func (s *Server) handleAppRollback(w http.ResponseWriter, r *http.Request) {
	a, ok := s.appRow(w, r)
	if !ok {
		return
	}
	if a.LastGoodDeployment == "" {
		writeErr(w, http.StatusConflict, "no known-good deployment to roll back to")
		return
	}
	last, err := s.db.DeploymentByID(a.LastGoodDeployment)
	if err != nil {
		writeErr(w, http.StatusConflict, "last good deployment no longer exists")
		return
	}
	depID, err := s.engine.EnqueuePinned(a.ID, apitypes.TriggerRollback, last.Release)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, deployResp{DeploymentID: depID})
}

func (s *Server) handleAppDeployments(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.appRow(w, r); !ok {
		return
	}
	deps, err := s.db.DeploymentsByApp(r.PathValue("id"), 100)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	out := make([]apitypes.Deployment, 0, len(deps))
	for _, d := range deps {
		out = append(out, toDeploymentResp(d))
	}
	writeJSON(w, http.StatusOK, out)
}
