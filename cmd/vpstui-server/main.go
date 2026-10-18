// vpstui-server is the PaaS control-plane daemon: REST+WS API, embedded web
// GUI, SQLite state, and orchestration of managed servers over the existing
// agentless SSH transport. This main is the assembly point later phases
// extend (deploy queue, cron, notify) — keep it linear and boring.
package main

import (
	"context"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"vpstui/internal/config"
	"vpstui/internal/deploy"
	"vpstui/internal/notify"
	"vpstui/internal/orchestrator"
	"vpstui/internal/proxy"
	"vpstui/internal/server"
	"vpstui/internal/store"
)

func main() {
	log.SetFlags(log.LstdFlags | log.Lmsgprefix)
	log.SetPrefix("vpstui-server: ")

	var (
		listen    = flag.String("listen", "127.0.0.1:8713", "HTTP listen address")
		dataFlag  = flag.String("data", "", "data directory (default $XDG_DATA_HOME/vpstui-server)")
		verbose   = flag.Bool("verbose", false, "log every request")
		acmeEmail = flag.String("acme-email", "", "ACME account email for TLS (Phase S4 consumes it)")
	)
	flag.Parse()

	dataDir := *dataFlag
	if dataDir == "" {
		dir, err := config.ServerDataDir()
		if err != nil {
			log.Fatalf("resolve data dir: %v", err)
		}
		dataDir = dir
	}
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		log.Fatalf("create data dir %s: %v", dataDir, err)
	}
	logsDir := filepath.Join(dataDir, "logs")
	if err := os.MkdirAll(logsDir, 0o700); err != nil {
		log.Fatalf("create logs dir: %v", err)
	}

	db, err := store.Open(filepath.Join(dataDir, "vpstui.db"))
	if err != nil {
		log.Fatalf("open store: %v", err)
	}
	if err := db.Migrate(); err != nil {
		log.Fatalf("migrate store: %v", err)
	}

	hub := server.NewHub()
	pool := orchestrator.NewPool(db)
	orch := orchestrator.NewOrchestrator(db, pool, hub, log.Default())

	// Deploy engine: crash recovery first, then the 2-worker queue. S7 wires
	// the real notifier into the engine (deploys) and the orchestrator
	// (server.offline, alert transitions).
	engine := deploy.NewEngine(db, pool, hub, dataDir, log.Default())
	engine.EnsureProxy = proxy.Ensure
	nf := notify.NewDispatcher(db, log.Default())
	engine.SetNotifier(nf)
	orch.SetNotifier(nf)
	if err := engine.RecoverInterrupted(); err != nil {
		log.Printf("crash recovery: %v", err)
	}
	engine.Start()

	cfg := server.Config{
		Listen:    *listen,
		DataDir:   dataDir,
		ACMEEmail: *acmeEmail,
		Verbose:   *verbose,
		Log:       log.Default(),
	}
	srv, err := server.New(cfg, db, orch, hub, engine)
	// S4: TLS is opt-in via --acme-email. The engine uses it for app router
	// labels; proxy.Ensure (no email in its seam signature) reads the same
	// value from the environment.
	engine.AcmeEmail = cfg.ACMEEmail
	os.Setenv("VPSTUI_ACME_EMAIL", cfg.ACMEEmail)
	if err != nil {
		log.Fatalf("assemble server: %v", err)
	}
	if err := srv.EnsureSetupToken(); err != nil {
		log.Fatalf("setup token: %v", err)
	}

	if err := orch.Start(context.Background()); err != nil {
		log.Fatalf("start orchestrator: %v", err)
	}

	go func() {
		log.Printf("listening on %s (data %s)", *listen, dataDir)
		if err := srv.Serve(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("serve: %v", err)
		}
	}()

	// SIGTERM/SIGINT: stop accepting, drain 5 s, then shut the monitors down.
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop
	log.Printf("shutting down: draining 5s")
	drain, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = srv.Shutdown(drain)
	engine.Stop()
	orch.Stop()
	_ = db.Close()
	log.Printf("bye")
}
