package server

import "net/http"

// routes registers every domain. Each register* method owns its path
// prefixes; later phases append new methods beside them.
func (s *Server) routes() {
	s.registerMisc(s.mux)
	s.registerAuth(s.mux)
	s.registerServers(s.mux)
	s.registerFleet(s.mux)
	s.registerApps(s.mux)
	s.registerDeployments(s.mux)
	s.registerDatabases(s.mux)
	s.registerDatabasesExtra(s.mux)
	s.registerTemplates(s.mux)
	s.registerNotifications(s.mux)
	s.registerAlerts(s.mux)
	s.registerHooks(s.mux)
	s.mux.Handle("/", s.static())
}

// registerMisc: healthz, me, keys, ws.
func (s *Server) registerMisc(mux *http.ServeMux) {
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("GET /api/me", s.handleMe)
	mux.HandleFunc("GET /api/keys", s.handleKeysList)
	mux.HandleFunc("POST /api/keys", s.handleKeysCreate)
	mux.HandleFunc("DELETE /api/keys/{id}", s.handleKeysDelete)
	mux.HandleFunc("GET /api/ws", s.handleWS)
}

// registerAuth: setup, login, logout, password change.
func (s *Server) registerAuth(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/setup", s.handleSetup)
	mux.HandleFunc("POST /api/auth/login", s.handleLogin)
	mux.HandleFunc("POST /api/auth/logout", s.handleLogout)
	mux.HandleFunc("POST /api/auth/password", s.handlePasswordChange)
}

// registerServers: the managed-server registry and its probes/actions.
func (s *Server) registerServers(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/servers", s.handleServersList)
	mux.HandleFunc("POST /api/servers", s.handleServersCreate)
	mux.HandleFunc("GET /api/servers/{id}", s.handleServerGet)
	mux.HandleFunc("PUT /api/servers/{id}", s.handleServerUpdate)
	mux.HandleFunc("DELETE /api/servers/{id}", s.handleServerDelete)
	mux.HandleFunc("POST /api/servers/{id}/test", s.handleServerTest)
	mux.HandleFunc("POST /api/servers/{id}/containers/{cid}/{action}", s.handleContainerAction)
}

// registerFleet: monitoring aggregation consumed by GUI and TUI hub mode.
func (s *Server) registerFleet(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/fleet", s.handleFleet)
	mux.HandleFunc("GET /api/fleet/errors", s.handleFleetErrors)
}
