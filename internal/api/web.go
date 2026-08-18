package api

import (
	"io/fs"
	"net/http"
	"strings"
)

// Assets is the built frontend, handed in by the command that has it
// embedded. Nil serves nothing: the API is complete without a page, and
// a build that skipped the frontend should say so honestly rather than
// serve a blank one.
type Assets fs.FS

// serveAssets answers everything outside /api with the frontend: the
// files it has, and index.html for anything else, because the page is a
// single document and a reload of any path has to reach it.
func (s *Server) serveAssets(w http.ResponseWriter, r *http.Request) {
	if s.assets == nil {
		writeError(w, http.StatusNotFound, "this build has no web client")
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/")
	if path == "" {
		path = "index.html"
	}
	if _, err := fs.Stat(s.assets, path); err != nil {
		path = "index.html"
	}
	// No caching for the document, so a rebuilt binary is not shadowed by
	// yesterday's page; the assets beside it are content-hashed and can
	// be cached hard.
	if path == "index.html" {
		w.Header().Set("Cache-Control", "no-store")
	} else {
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	}
	http.ServeFileFS(w, r, s.assets, path)
}
