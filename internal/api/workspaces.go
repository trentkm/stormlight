package api

import (
	"net/http"

	"github.com/trentkm/stormlight/internal/workspace"
)

func (s *Server) listWorkspaces(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := call(r)
	defer cancel()
	values, err := s.service.ListWorkspaces(ctx)
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, orEmpty(values))
}

type workspaceBody struct {
	Host string `json:"host,omitempty"`
	Path string `json:"path"`
}

func (s *Server) addWorkspace(w http.ResponseWriter, r *http.Request) {
	var body workspaceBody
	if err := decode(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	ctx, cancel := call(r)
	defer cancel()
	// Resolution runs on the machine that has the path, so host and path
	// travel together; the catalog then holds what the resolver said.
	added, err := s.service.AddWorkspace(ctx, body.Host, body.Path)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, added)
}

// workspaceRef identifies a catalogued workspace for the routes that act
// on one. It is the resolved context rather than a path: the catalog is
// keyed by what the resolver decided, and a client that just listed the
// workspaces already holds it.
type workspaceRef struct {
	Workspace workspace.Context `json:"workspace"`
	Name      string            `json:"name,omitempty"`
}

func (s *Server) removeWorkspace(w http.ResponseWriter, r *http.Request) {
	var body workspaceRef
	if err := decode(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	ctx, cancel := call(r)
	defer cancel()
	if err := s.service.RemoveWorkspace(ctx, body.Workspace); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusNoContent, nil)
}

func (s *Server) renameWorkspace(w http.ResponseWriter, r *http.Request) {
	var body workspaceRef
	if err := decode(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	ctx, cancel := call(r)
	defer cancel()
	if err := s.service.RenameWorkspace(ctx, body.Workspace, body.Name); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusNoContent, nil)
}
