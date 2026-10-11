package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
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
	hosts, err := config.Load()
	if err != nil {
		fmt.Fprintln(os.Stderr, "warning:", err)
	}
	m := app.New(hosts)
	p := tea.NewProgram(m, tea.WithAltScreen())
	m.Attach(p.Send)

	srv, _ := startAPI(m.HTTPStore())

	if _, err := p.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}

	if srv != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer cancel()
		_ = srv.Shutdown(ctx)
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
