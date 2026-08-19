package api

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"github.com/trentkm/stormlight/internal/agent"
	"github.com/trentkm/stormlight/internal/app"
	"github.com/trentkm/stormlight/internal/provider"
)

// call gives a control-plane handler its bounded context. Every handler
// below is the same three steps — decode, one Service call, encode — and
// anything that wants to be cleverer than that belongs in internal/app,
// where the dashboard gets it too.
func call(r *http.Request) (context.Context, context.CancelFunc) {
	return context.WithTimeout(r.Context(), callTimeout)
}

// agentView is an agent as this API reports it: the domain record plus
// the host it answered from.
//
// agent.Agent deliberately does not serialize Host — the field is the
// answering daemon's fact, and writing it into the agent's own document
// would be one dashboard's claim about the world. Over the wire it is
// exactly what a client needs, though: on a fleet, "which machine is this
// on" is not decoration. The outer field shadows the embedded one, which
// is why this reads as a plain agent with a host.
type agentView struct {
	agent.Agent
	Host string `json:"host,omitempty"`
}

func viewOf(agents []agent.Agent) []agentView {
	views := make([]agentView, 0, len(agents))
	for _, managedAgent := range agents {
		views = append(views, agentView{Agent: managedAgent, Host: managedAgent.Host})
	}
	return views
}

func (s *Server) listAgents(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := call(r)
	defer cancel()
	agents, err := s.service.ListAgents(ctx)
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, viewOf(agents))
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
	if err := decode(w, r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	// The same parser every other entry point uses. Casting the string
	// straight to a PermissionMode would accept "Auto" and then quietly
	// launch the agent in the provider's default mode instead of the one
	// asked for — a misspelled value silently ignored, which is exactly
	// what DisallowUnknownFields exists to prevent for keys.
	mode, err := agent.ParseMode(body.Mode)
	if err != nil {
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
		Mode:     mode,
	})
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, agentView{Agent: dispatched, Host: dispatched.Host})
}

// transcriptView is a conversation as this API serves it: parsed,
// unstyled entries the browser paints its own way. The terminal socket
// stays the live view; this is the history an alternate-screen agent
// never leaves in scrollback.
//
// The view is a delta. Conversations grow all day and a poll every few
// seconds must not re-ship the morning: ?after=N elides the first N
// entries, and Total tells the client where the conversation stands. A
// Total below the client's own count means the file shrank — replaced
// or compacted — and the client starts over. OK is false when there is
// no conversation to report *right now*: an agent between hooks, a
// tunnel mid-hiccup. The client keeps what it has rather than blanking
// a morning's reading over a two-second fault.
type transcriptView struct {
	Entries []provider.TranscriptEntry `json:"entries"`
	Total   int                        `json:"total"`
	OK      bool                       `json:"ok"`
}

func (s *Server) agentTranscript(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := call(r)
	defer cancel()
	entries, ok := s.service.Transcript(ctx, r.PathValue("id"))
	if !ok {
		writeJSON(w, http.StatusOK, transcriptView{Entries: []provider.TranscriptEntry{}})
		return
	}
	after := 0
	if raw := r.URL.Query().Get("after"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 0 {
			writeError(w, http.StatusBadRequest, "after must be a non-negative integer")
			return
		}
		after = parsed
	}
	delta := entries[min(after, len(entries)):]
	writeJSON(w, http.StatusOK, transcriptView{Entries: delta, Total: len(entries), OK: true})
}

type renameBody struct {
	Name string `json:"name"`
}

func (s *Server) renameAgent(w http.ResponseWriter, r *http.Request) {
	var body renameBody
	if err := decode(w, r, &body); err != nil {
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
	if err := decode(w, r, &body); err != nil {
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
	if err := decode(w, r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	mark, err := agent.ParseMark(body.Mark)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	ctx, cancel := call(r)
	defer cancel()
	if err := s.service.SetMark(ctx, r.PathValue("id"), mark); err != nil {
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
