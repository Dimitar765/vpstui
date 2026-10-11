package httpapi

import (
	"embed"
	"fmt"
	"net"
	"net/http"
	"os"
	"time"
)

//go:embed web/index.html
var web embed.FS

// indexHTML is read once at init: the page is embedded, so re-reading it per
// request only paid an embed.FS walk and an allocation on every poll.
var indexHTML = func() []byte {
	page, err := web.ReadFile("web/index.html")
	if err != nil {
		// embed.FS membership is a compile-time guarantee, so this is purely
		// defensive; serve a visible stub rather than a panic.
		return []byte("<!doctype html><title>vpstui</title><p>dashboard unavailable</p>")
	}
	return page
}()

const defaultAddr = "127.0.0.1:8712"

// loopbackOnly reports whether addr binds a loopback interface only, so a
// mis-set VPSTUI_HTTP_ADDR cannot expose the API on the LAN.
func loopbackOnly(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	if host == "" || host == "*" {
		return false
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// ListenAddr returns the configured bind address, defaulting to loopback.
// It rejects a non-loopback override with an error.
func ListenAddr() (string, error) {
	addr := os.Getenv("VPSTUI_HTTP_ADDR")
	if addr == "" {
		return defaultAddr, nil
	}
	if !loopbackOnly(addr) {
		return "", fmt.Errorf("VPSTUI_HTTP_ADDR %q is not loopback; refusing to expose the API", addr)
	}
	return addr, nil
}

// NewServer builds the read-only JSON API server over store.
func NewServer(store *Store, addr string) *http.Server {
	mux := http.NewServeMux()
	h := &handler{store: store}
	mux.HandleFunc("GET /", h.index)
	mux.HandleFunc("GET /healthz", h.healthz)
	mux.HandleFunc("GET /api/sessions", h.listSessions)
	mux.HandleFunc("GET /api/errors", h.getErrors)
	mux.HandleFunc("GET /api/sessions/{id}", h.getSession)
	mux.HandleFunc("GET /api/sessions/{id}/network", h.getNetwork)
	return &http.Server{
		Addr:         addr,
		Handler:      mux,
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 5 * time.Second,
		IdleTimeout:  60 * time.Second,
	}
}
