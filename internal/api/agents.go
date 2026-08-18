package api

import (
	"context"
	"net/http"
	"time"

	"github.com/trentkm/stormlight/internal/agent"
	"github.com/trentkm/stormlight/internal/app"
)

// call gives a control-plane handler its bounded context. Every handler
// below is the same three steps — decode, one Service call, encode — and
// anything that wants to be cleverer than that belongs in internal/app,
// where the dashboard gets it too.
func call(r *http.Request) (context.Context, context.CancelFunc) {
	return context.WithTimeout(r.Context(), callTimeout)
}

func (s *Server) listAgents(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := call(r)
	defer cancel()
	agents, err := s.service.ListAgents(ctx)
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, orEmpty(agents))
}

type dispatchBody struct {
	Provider string `json:"provider"`
	Name     string `json:"name,omitempty"`
	Task     string `json:"task"`
	Cwd      string `json:"cwd"`
	Host     string `json:"host,omitempty"`
	Mode     string `json:"mode,omitempty"`
}

func (s *Server) dispatchAgent(w http.ResponseWriter, r *http.Request) {
	var body dispatchBody
	if err := decode(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	// Dispatch spawns a process and waits for the provider to answer for
	// it; the control plane's ordinary bound is too tight for that.
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	dispatched, err := s.service.Dispatch(ctx, app.DispatchRequest{
		Provider: agent.Provider(body.Provider),
		Name:     body.Name,
		Task:     body.Task,
		Cwd:      body.Cwd,
		Host:     body.Host,
		Mode:     agent.PermissionMode(body.Mode),
	})
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, dispatched)
}

type renameBody struct {
	Name string `json:"name"`
}

func (s *Server) renameAgent(w http.ResponseWriter, r *http.Request) {
	var body renameBody
	if err := decode(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	ctx, cancel := call(r)
	defer cancel()
	if err := s.service.Rename(ctx, r.PathValue("id"), body.Name); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusNoContent, nil)
}

func (s *Server) deleteAgent(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := call(r)
	defer cancel()
	// Delete hands the conversation to the history log on its way out —
	// the Service's own doing, so this route inherits it.
	if err := s.service.Delete(ctx, r.PathValue("id")); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusNoContent, nil)
}

type messageBody struct {
	Message string `json:"message"`
}

func (s *Server) sendMessage(w http.ResponseWriter, r *http.Request) {
	var body messageBody
	if err := decode(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	ctx, cancel := call(r)
	defer cancel()
	if err := s.service.Send(ctx, r.PathValue("id"), body.Message); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusNoContent, nil)
}

func (s *Server) interruptAgent(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := call(r)
	defer cancel()
	if err := s.service.Interrupt(ctx, r.PathValue("id")); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusNoContent, nil)
}

type markBody struct {
	Mark string `json:"mark"`
}

func (s *Server) markAgent(w http.ResponseWriter, r *http.Request) {
	var body markBody
	if err := decode(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	ctx, cancel := call(r)
	defer cancel()
	if err := s.service.SetMark(ctx, r.PathValue("id"), agent.Mark(body.Mark)); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusNoContent, nil)
}

func (s *Server) clearAttention(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := call(r)
	defer cancel()
	if err := s.service.ClearAttention(ctx, r.PathValue("id")); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusNoContent, nil)
}

func (s *Server) listProviders(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, orEmpty(s.service.Providers()))
}
