package api

import (
	"io/fs"
	"net/http"
	gopath "path"
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
	requested := strings.TrimPrefix(r.URL.Path, "/")
	if requested == "" {
		requested = "index.html"
	}
	_, err := fs.Stat(s.assets, requested)
	if err != nil {
		// A path that names a file is a file, and a missing one is a 404.
		// Answering it with the document instead turns a stale asset
		// reference into a MIME error in the console, which says nothing
		// about what actually went wrong.
		if strings.Contains(gopath.Base(requested), ".") {
			writeError(w, http.StatusNotFound, "no such file: "+requested)
			return
		}
		// Everything else is a route the client owns, and a reload of one
		// has to reach the document that knows how to render it.
		requested = "index.html"
	}

	w.Header().Set("X-Content-Type-Options", "nosniff")
	// Set only for what is actually being served: ServeFileFS may answer
	// with a redirect instead, and a redirect cached for a year is a
	// mistake that outlives the build that made it.
	if requested == "index.html" {
		// The document is never cached, so a rebuilt binary is not
		// shadowed by yesterday's page; the assets beside it are
		// content-hashed and can be cached hard.
		w.Header().Set("Cache-Control", "no-store")
	} else {
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	}
	http.ServeFileFS(w, r, s.assets, requested)
}
