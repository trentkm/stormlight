// Package api serves Stormlight's domain over HTTP and WebSockets: a
// second head on the same internal/app.Service the dashboard drives, so a
// browser client and the TUI are peers over one set of rules rather than
// two implementations of them.
//
// Two planes share the listener and never mix. The control plane is JSON
// over HTTP — roster, workspaces, dispatch, history — where a millisecond
// costs nothing. The data plane is one WebSocket per attached terminal,
// carrying raw bytes in both directions: keystrokes are never wrapped in
// JSON and never queued behind a request. That split is what keeps typing
// in a browser terminal feel like typing in a terminal.
package api

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/trentkm/stormlight/internal/app"
	"github.com/trentkm/stormlight/internal/diagnostic"
)

// callTimeout bounds a control-plane call. The data plane sets no
// deadline of its own: a terminal attachment is meant to outlive any
// request.
const callTimeout = 10 * time.Second

// Server is the HTTP surface. It owns no state the dashboard does not
// already own — every route is a decode, one Service call, and an encode.
type Server struct {
	service *app.Service
	token   string
	events  *hub
	mux     *http.ServeMux
}

// NewToken mints a fresh access token. One per run: nothing persists it,
// so a restart invalidates every URL that was handed out.
func NewToken() (string, error) {
	raw := make([]byte, 24)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("generate token: %w", err)
	}
	return hex.EncodeToString(raw), nil
}

// New builds the server. The token is required, not optional: these
// routes dispatch agents and stream terminals in every workspace the
// catalog knows, which is shell access to all of them. There is
// deliberately no unauthenticated mode to ship by accident.
func New(service *app.Service, token string) (*Server, error) {
	if service == nil {
		return nil, fmt.Errorf("api: a service is required")
	}
	if token == "" {
		return nil, fmt.Errorf("api: a token is required")
	}
	s := &Server{
		service: service,
		token:   token,
		events:  newHub(service),
		mux:     http.NewServeMux(),
	}
	s.routes()
	return s, nil
}

// Handler is the server as an http.Handler, with authentication in front
// of every route.
func (s *Server) Handler() http.Handler {
	return s.authenticated(s.mux)
}

// Run starts the background work the event stream needs and returns a
// stop function. Serving without it yields a working control plane and an
// event socket that never speaks.
func (s *Server) Run() (stop func()) {
	return s.events.run()
}

func (s *Server) routes() {
	s.mux.HandleFunc("GET /api/agents", s.listAgents)
	s.mux.HandleFunc("POST /api/agents", s.dispatchAgent)
	s.mux.HandleFunc("PATCH /api/agents/{id}", s.renameAgent)
	s.mux.HandleFunc("DELETE /api/agents/{id}", s.deleteAgent)
	s.mux.HandleFunc("POST /api/agents/{id}/message", s.sendMessage)
	s.mux.HandleFunc("POST /api/agents/{id}/interrupt", s.interruptAgent)
	s.mux.HandleFunc("POST /api/agents/{id}/mark", s.markAgent)
	s.mux.HandleFunc("POST /api/agents/{id}/clear-attention", s.clearAttention)
	s.mux.HandleFunc("GET /api/agents/{id}/terminal", s.terminal)

	s.mux.HandleFunc("GET /api/workspaces", s.listWorkspaces)
	s.mux.HandleFunc("POST /api/workspaces", s.addWorkspace)
	s.mux.HandleFunc("DELETE /api/workspaces", s.removeWorkspace)
	s.mux.HandleFunc("PATCH /api/workspaces", s.renameWorkspace)

	s.mux.HandleFunc("GET /api/providers", s.listProviders)
	s.mux.HandleFunc("GET /api/history", s.listHistory)
	s.mux.HandleFunc("POST /api/history/{id}/resume", s.resumeHistory)

	s.mux.HandleFunc("GET /api/events", s.eventStream)
}

// authenticated gates every route on the run's token and, for WebSocket
// upgrades, on the request's origin.
//
// The token may travel as a bearer header or as a query parameter,
// because a browser cannot set headers on a WebSocket handshake and the
// terminal socket is the whole point of this server.
func (s *Server) authenticated(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.tokenMatches(r) {
			// No detail: a wrong token learns nothing about the right one.
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		if isUpgrade(r) && !sameOrigin(r) {
			// A page the user is merely visiting must not be able to
			// reach this server just because it is on loopback.
			writeError(w, http.StatusForbidden, "cross-origin upgrade refused")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) tokenMatches(r *http.Request) bool {
	presented := r.URL.Query().Get("token")
	if header := r.Header.Get("Authorization"); header != "" {
		presented = strings.TrimSpace(strings.TrimPrefix(header, "Bearer"))
	}
	return subtle.ConstantTimeCompare([]byte(presented), []byte(s.token)) == 1
}

func isUpgrade(r *http.Request) bool {
	return strings.EqualFold(r.Header.Get("Upgrade"), "websocket")
}

// sameOrigin accepts a request with no Origin at all — that is a program,
// not a page, and programs are the other half of this API's audience —
// and otherwise requires the origin's host to be the one being served.
func sameOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	trimmed := origin
	for _, scheme := range []string{"http://", "https://"} {
		trimmed = strings.TrimPrefix(trimmed, scheme)
	}
	return trimmed == r.Host
}

// writeJSON encodes a successful response.
func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if payload == nil {
		return
	}
	if err := json.NewEncoder(w).Encode(payload); err != nil {
		// The status line is already out; all that is left is a record.
		diagnostic.Logger().Warn("api response truncated", "error", err)
	}
}

// orEmpty makes a nil slice encode as [] rather than null. An empty
// roster is a normal state — it is what a fresh install looks like — and
// a client should not have to special-case it before iterating.
func orEmpty[T any](values []T) []T {
	if values == nil {
		return []T{}
	}
	return values
}

// errorBody is the one shape every failure takes, so a client parses one
// thing.
type errorBody struct {
	Error string `json:"error"`
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, errorBody{Error: message})
}

// decode reads a JSON request body, refusing unknown fields so a
// misspelled key is an error rather than a silently ignored intention.
func decode(r *http.Request, target any) error {
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("malformed request body: %w", err)
	}
	return nil
}
