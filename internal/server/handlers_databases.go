package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/robfig/cron/v3"

	"vpstui/internal/apitypes"
	"vpstui/internal/deploy"
	"vpstui/internal/models"
	"vpstui/internal/store"
)

// registerDatabases: the managed-database domain. Provisioning is a direct
// synchronous operation (not a queued deployment); the backup scheduler is
// started here once at boot.
func (s *Server) registerDatabases(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/databases", s.handleDatabasesList)
	mux.HandleFunc("POST /api/databases", s.handleDatabaseCreate)
	mux.HandleFunc("GET /api/databases/{id}", s.handleDatabaseGet)
	mux.HandleFunc("DELETE /api/databases/{id}", s.handleDatabaseDelete)
	mux.HandleFunc("POST /api/databases/{id}/backup", s.handleDatabaseBackup)
	mux.HandleFunc("GET /api/databases/{id}/connection", s.handleDatabaseConnection)
	deploy.NewBackupScheduler(s.engine, s.log).Start() // contained goroutine; cancel on shutdown is best-effort (not wired)
}

// ---------------------------------------------------------------- DTOs

type databaseReq struct {
	EnvironmentID string          `json:"environmentId"`
	ServerID      string          `json:"serverId"`
	DbType        string          `json:"dbType"`
	Name          string          `json:"name"`
	Publish       *bool           `json:"publish"`
	Port          int             `json:"port"`
	Backup        *backupSchedReq `json:"backup"`
}

type backupSchedReq struct {
	CronSpec  string `json:"cronSpec"`
	Retention int    `json:"retention"`
}

func (req *databaseReq) normalize() error {
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" {
		return errors.New("name is required")
	}
	if req.EnvironmentID == "" || req.ServerID == "" {
		return errors.New("environmentId and serverId are required")
	}
	switch req.DbType {
	case "postgres", "mysql", "redis", "mongo":
	default:
		return errors.New("dbType must be postgres, mysql, redis or mongo")
	}
	if req.Backup != nil {
		if req.Backup.CronSpec == "" {
			req.Backup.CronSpec = "0 4 * * *"
		}
		if _, err := cron.ParseStandard(req.Backup.CronSpec); err != nil {
			return errors.New("backup.cronSpec: " + err.Error())
		}
		if req.Backup.Retention < 1 {
			req.Backup.Retention = 7
		}
	}
	return nil
}

func toDatabaseResp(d store.Database, sched *store.BackupSchedule) apitypes.Database {
	resp := apitypes.Database{
		ID: d.ID, EnvironmentID: d.EnvironmentID, ServerID: d.ServerID,
		DbType: d.DbType, Name: d.Name, Config: d.Config,
		Status: d.Status, CreatedAt: d.CreatedAt,
	}
	if resp.Config == nil {
		resp.Config = map[string]any{}
	}
	if sched != nil {
		resp.Backup = &apitypes.BackupSchedule{
			DatabaseID: sched.DatabaseID, CronSpec: sched.CronSpec,
			Retention: sched.Retention, LastRunAt: sched.LastRunAt,
		}
	}
	return resp
}

// ---------------------------------------------------------------- handlers

func (s *Server) databaseRow(w http.ResponseWriter, r *http.Request) (store.Database, bool) {
	d, err := s.db.DatabaseByID(r.PathValue("id"))
	if errors.Is(err, store.ErrNotFound) {
		writeErr(w, http.StatusNotFound, "database not found")
		return store.Database{}, false
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return store.Database{}, false
	}
	return d, true
}

func (s *Server) handleDatabasesList(w http.ResponseWriter, r *http.Request) {
	rows, err := s.db.Databases()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	out := make([]apitypes.Database, 0, len(rows))
	for _, d := range rows {
		var sched *store.BackupSchedule
		if sc, serr := s.db.BackupScheduleByDatabase(d.ID); serr == nil {
			sched = &sc
		}
		out = append(out, toDatabaseResp(d, sched))
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleDatabaseCreate(w http.ResponseWriter, r *http.Request) {
	var req databaseReq
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
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
	cfg := map[string]any{}
	if req.Publish != nil {
		cfg["publish"] = *req.Publish
	}
	if req.Port != 0 {
		cfg["port"] = req.Port
	}
	row := store.Database{
		ID: models.NewID(), EnvironmentID: req.EnvironmentID, ServerID: req.ServerID,
		DbType: req.DbType, Name: req.Name, Config: cfg,
		Status: "undeployed", CreatedAt: time.Now().UTC(),
	}
	if err := s.db.CreateDatabase(row); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if req.Backup != nil {
		if err := s.db.UpsertBackupSchedule(store.BackupSchedule{
			DatabaseID: row.ID, CronSpec: req.Backup.CronSpec, Retention: req.Backup.Retention,
		}); err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	// Provision synchronously per spec — no queue.
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Minute)
	defer cancel()
	if err := s.engine.EnsureDatabaseSync(ctx, row.ID); err != nil {
		writeErr(w, http.StatusInternalServerError, "provision failed: "+err.Error())
		return
	}
	sched, _ := s.db.BackupScheduleByDatabase(row.ID)
	writeJSON(w, http.StatusCreated, toDatabaseResp(mustDatabase(s, row.ID), &sched))
}

// mustDatabase re-reads the row after provisioning so the response reflects
// the final status; falls back to the pre-provision copy on error.
func mustDatabase(s *Server, id string) store.Database {
	d, err := s.db.DatabaseByID(id)
	if err != nil {
		return store.Database{ID: id}
	}
	return d
}

func (s *Server) handleDatabaseGet(w http.ResponseWriter, r *http.Request) {
	d, ok := s.databaseRow(w, r)
	if !ok {
		return
	}
	var sched *store.BackupSchedule
	if sc, serr := s.db.BackupScheduleByDatabase(d.ID); serr == nil {
		sched = &sc
	}
	writeJSON(w, http.StatusOK, toDatabaseResp(d, sched))
}

func (s *Server) handleDatabaseDelete(w http.ResponseWriter, r *http.Request) {
	d, ok := s.databaseRow(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Minute)
	defer cancel()
	if err := s.engine.DestroyDatabase(ctx, d.ID); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

func (s *Server) handleDatabaseBackup(w http.ResponseWriter, r *http.Request) {
	d, ok := s.databaseRow(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Minute)
	defer cancel()
	file, err := s.engine.RunBackup(ctx, d.ID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"file": file})
}

func (s *Server) handleDatabaseConnection(w http.ResponseWriter, r *http.Request) {
	d, ok := s.databaseRow(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	uri, err := s.engine.DatabaseConnectionString(ctx, d.ID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"uri": uri})
}
