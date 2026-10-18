package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"vpstui/internal/app"
	"vpstui/internal/config"
	"vpstui/internal/httpapi"
	"vpstui/internal/secret"
	"vpstui/internal/shell"
)

func main() {
	// Child mode invoked by the TUI for interactive container shells.
	if len(os.Args) > 1 && os.Args[1] == "_shell" {
		os.Exit(shell.Main(os.Args[2:]))
	}
	// SIGINT/SIGTERM cancel the program context: Bubble Tea unwinds the UI and
	// Run returns, so the API server gets a clean shutdown instead of a kill.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	hubURL := hubFlag(os.Args[1:])

	hosts, err := config.Load()
	if err != nil {
		fmt.Fprintln(os.Stderr, "warning:", err)
	}
	tags, err := config.LoadTags()
	if err != nil {
		fmt.Fprintln(os.Stderr, "warning:", err)
	}
	cfg, err := config.LoadSettings()
	if err != nil {
		fmt.Fprintln(os.Stderr, "warning:", err)
	}
	// Resolution order env > file > default: the env-backed settings only land
	// in the environment when the user has not set the variable themselves.
	config.EnvDefaults(cfg)
	m := app.NewWithSettings(hosts, tags, cfg)
	if hubURL != "" {
		// Hub mode: connect now when a token is stored for this URL, else let
		// the hub form ask for one (the URL stays prefilled).
		tok, _ := secret.Get(app.HubTokenRef(hubURL))
		m.SetStartHub(hubURL, tok)
	}
	opts := []tea.ProgramOption{tea.WithAltScreen(), tea.WithContext(ctx)}
	if cfg.MouseEnabled() {
		opts = append(opts, tea.WithMouseAllMotion())
	}
	p := tea.NewProgram(m, opts...)
	m.Attach(p.Send)

	srv, ln := startAPI(m.HTTPStore())
	defer func() {
		if ln != nil {
			_ = ln.Close()
		}
	}()

	if _, err := p.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}

	if srv != nil {
		// Background, not the signal context: on ctrl-c that one is already
		// canceled and would abort the drain before it started.
		sctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(sctx)
	}
}

// hubFlag extracts --hub <url> / --hub=<url> from the raw args. Everything
// else is left to the settings/env readers, so new flags never come through
// two parsers.
func hubFlag(args []string) string {
	for i := range args {
		switch {
		case args[i] == "--hub" && i+1 < len(args):
			return strings.TrimSpace(args[i+1])
		case strings.HasPrefix(args[i], "--hub="):
			return strings.TrimSpace(strings.TrimPrefix(args[i], "--hub="))
		}
	}
	return ""
}

// startAPI binds the loopback JSON API and serves it in the background. The
// TUI must keep working regardless, so any bind error only warns and yields
// a nil server.
func startAPI(store *httpapi.Store) (*http.Server, net.Listener) {
	addr, err := httpapi.ListenAddr()
	if err != nil {
		fmt.Fprintln(os.Stderr, "warning:", err)
		return nil, nil
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		fmt.Fprintln(os.Stderr, "warning: http api disabled:", err)
		return nil, nil
	}
	srv := httpapi.NewServer(store, addr)
	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			fmt.Fprintln(os.Stderr, "warning: http api:", err)
		}
	}()
	return srv, ln
}
