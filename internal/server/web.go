package server

import (
	"embed"
	"io/fs"
	"net/http"
	"strings"
)

//go:embed web/*
var webAssets embed.FS

// staticHandler serves the embedded web client.
// - GET /static/<file> → that file from the embedded FS
// - GET /            → index.html (the SPA)
// - GET /anything    → index.html (so deep links/refreshes work)
func (s *Server) staticHandler() http.Handler {
	sub, err := fs.Sub(webAssets, "web")
	if err != nil {
		panic("embed web: " + err.Error())
	}
	files := http.FileServer(http.FS(sub))

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// /static/<file> → strip the prefix and serve from the embedded FS.
		if strings.HasPrefix(r.URL.Path, "/static/") {
			r2 := r.Clone(r.Context())
			r2.URL.Path = strings.TrimPrefix(r.URL.Path, "/static")
			files.ServeHTTP(w, r2)
			return
		}
		// Anything else → SPA shell (no client-side routes today, but safe).
		b, err := fs.ReadFile(sub, "index.html")
		if err != nil {
			http.Error(w, "missing index", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		w.Write(b)
	})
}
