package server

import (
	"embed"
	"io/fs"
	"net/http"
	"strings"
)

// webout is the built web GUI, copied here by `make web` (pnpm build →
// rsync web/out).
//
//go:embed all:webout
var webout embed.FS

// static serves the embedded SPA: real files by path, unknown non-API paths
// fall back to index.html so client-side routing works.
func (s *Server) static() http.Handler {
	sub, err := fs.Sub(webout, "webout")
	if err != nil {
		return http.NotFoundHandler()
	}
	file := http.FileServerFS(sub)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := strings.TrimPrefix(r.URL.Path, "/")
		if p == "" {
			p = "index.html"
		}
		// Directory-style routes (trailingSlash export): /deployments/view/
		// must serve that directory's index.html, not the SPA root fallback.
		if strings.HasSuffix(p, "/") {
			p += "index.html"
		}
		if _, err := fs.Stat(sub, p); err != nil {
			// Unknown path: SPA fallback, or a plain 404 while only .keep exists.
			if _, err := fs.Stat(sub, "index.html"); err != nil {
				http.NotFound(w, r)
				return
			}
			r.URL.Path = "/"
		}
		file.ServeHTTP(w, r)
	})
}
