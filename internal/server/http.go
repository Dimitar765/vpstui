package server

import (
	"context"
	"log"
	"net/http"

	"vpstui/internal/deploy"
	"vpstui/internal/orchestrator"
	"vpstui/internal/store"
)

// Config is the daemon's assembly-time configuration. Later phases extend
// it; cmd/vpstui-server/main.go is the only constructor.
type Config struct {
	Listen    string
	DataDir   string
	ACMEEmail string // consumed by the Phase-S4 proxy/TLS work
	Verbose   bool
	Log       *log.Logger
}

// Server is the daemon's HTTP layer: auth, the server registry, fleet
// monitoring, the app/deploy domains and the embedded GUI.
type Server struct {
	cfg    Config
	db     *store.DB
	orch   *orchestrator.Orchestrator
	hub    *Hub
	engine *deploy.Engine
	log    *log.Logger
	mux    *http.ServeMux
	httpd  *http.Server

	// setupTok is the first-boot setup token; empty once used or when an
	// admin already exists.
	setupTok string
}

// New assembles the daemon: store, orchestrator, hub, deploy engine, routes
// and the setup token for a fresh install. Callers own the orchestrator and
// engine lifecycles (Start/Stop); the returned http.Server is driven by
// Serve/Shutdown.
func New(cfg Config, db *store.DB, orch *orchestrator.Orchestrator, hub *Hub, engine *deploy.Engine) (*Server, error) {
	if cfg.Log == nil {
		cfg.Log = log.Default()
	}
	if hub == nil {
		hub = NewHub()
	}
	s := &Server{cfg: cfg, db: db, orch: orch, hub: hub, engine: engine, log: cfg.Log, mux: http.NewServeMux()}
	s.routes()
	s.httpd = &http.Server{Addr: cfg.Listen, Handler: s.handler()}
	return s, nil
}

// Serve runs the HTTP server until Shutdown.
func (s *Server) Serve() error { return s.httpd.ListenAndServe() }

// Shutdown drains connections with the caller's context (5 s in main).
func (s *Server) Shutdown(ctx context.Context) error { return s.httpd.Shutdown(ctx) }

// handler wraps the mux in the middleware chain: recover outermost (every
// panic becomes a 500, never a dead daemon), then request-log, then auth.
func (s *Server) handler() http.Handler {
	return recoverMW(logMW(s.cfg.Verbose, s.log, s.authMW(s.mux)))
}
