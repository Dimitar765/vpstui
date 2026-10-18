package server

import (
	"context"
	"net/http"
	"time"

	"vpstui/internal/apitypes"
)

// registerDatabasesExtra: the backup-history fold-in. Kept apart from
// handlers_databases.go, which belongs to the databases phase.
func (s *Server) registerDatabasesExtra(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/databases/{id}/backups", s.handleDatabaseBackups)
}

// handleDatabaseBackups lists <base_dir>/backups/db-<id>/ newest first, so
// the GUI's history table can render what the scheduler and the manual
// backup endpoint produced.
func (s *Server) handleDatabaseBackups(w http.ResponseWriter, r *http.Request) {
	d, ok := s.databaseRow(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	entries, err := s.engine.ListBackups(ctx, d.ID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if entries == nil {
		entries = []apitypes.BackupEntry{} // never null for list endpoints
	}
	writeJSON(w, http.StatusOK, entries)
}
