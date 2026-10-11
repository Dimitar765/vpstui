package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"vpstui/internal/app"
	"vpstui/internal/config"
	"vpstui/internal/httpapi"
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

	hosts, err := config.Load()
	if err != nil {
		fmt.Fprintln(os.Stderr, "warning:", err)
	}
	tags, err := config.LoadTags()
	if err != nil {
		fmt.Fprintln(os.Stderr, "warning:", err)
	}
	m := app.New(hosts, tags)
	p := tea.NewProgram(m, tea.WithAltScreen(), tea.WithContext(ctx))
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
