package api

import (
	"errors"
	"net/http"
	"strings"

	"github.com/trentkm/stormlight/internal/link"
)

// Links over HTTP: the arrows the canvas draws, and the one verb a
// person has on them beyond drawing — fire now. Firing on a turn end is
// not a route; it happens in the provider hook, whether or not this
// server is running.

type linkBody struct {
	From  string `json:"from"`
	To    string `json:"to"`
	Label string `json:"label"`
	Auto  bool   `json:"auto"`
}

type linkPatch struct {
	Label *string `json:"label"`
	Auto  *bool   `json:"auto"`
}

func (s *Server) listLinks(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := call(r)
	defer cancel()
	links, err := s.service.Links(ctx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, orEmpty(links))
}

func (s *Server) addLink(w http.ResponseWriter, r *http.Request) {
	var body linkBody
	if err := decode(w, r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	ctx, cancel := call(r)
	defer cancel()
	added, err := s.service.AddLink(ctx, body.From, body.To, body.Label, body.Auto)
	if err != nil {
		writeError(w, linkStatus(err), err.Error())
		return
	}
	s.events.poke()
	writeJSON(w, http.StatusCreated, added)
}

func (s *Server) updateLink(w http.ResponseWriter, r *http.Request) {
	var body linkPatch
	if err := decode(w, r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	ctx, cancel := call(r)
	defer cancel()
	updated, err := s.service.UpdateLink(ctx, r.PathValue("id"), link.Patch{
		Label: body.Label,
		Auto:  body.Auto,
	})
	if err != nil {
		writeError(w, linkStatus(err), err.Error())
		return
	}
	s.events.poke()
	writeJSON(w, http.StatusOK, updated)
}

func (s *Server) removeLink(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := call(r)
	defer cancel()
	if err := s.service.RemoveLink(ctx, r.PathValue("id")); err != nil {
		writeError(w, linkStatus(err), err.Error())
		return
	}
	s.events.poke()
	writeJSON(w, http.StatusNoContent, nil)
}

func (s *Server) fireLink(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := call(r)
	defer cancel()
	fired, err := s.service.FireLink(ctx, r.PathValue("id"))
	if err != nil {
		writeError(w, linkStatus(err), err.Error())
		return
	}
	s.events.poke()
	writeJSON(w, http.StatusOK, fired)
}

// linkStatus is the status a link failure deserves: a link that cannot
// exist is a conflict with the ones that do, a link nobody has is not
// found, and an agent nobody has is not found either.
func linkStatus(err error) int {
	switch {
	case errors.Is(err, link.ErrSelf),
		errors.Is(err, link.ErrCycle),
		errors.Is(err, link.ErrNothingToSend),
		errors.Is(err, link.ErrHopLimit):
		return http.StatusConflict
	case errors.Is(err, link.ErrNotFound),
		strings.Contains(err.Error(), "not found"):
		return http.StatusNotFound
	default:
		return http.StatusBadRequest
	}
}
