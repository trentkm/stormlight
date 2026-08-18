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
	"net"
	"net/http"
	"net/url"
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
			// First, and with no detail: a wrong token learns nothing
			// about the right one, nor about what else would be accepted.
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		// A Host this server does not answer to means the name resolved
		// here from somewhere else — the rebinding trick that turns a
		// page the user is visiting into a client of their own machine.
		if !loopbackHost(r.Host) {
			writeError(w, http.StatusForbidden, "unrecognized host")
			return
		}
		// Every request that acts, not only the upgrades. A cross-origin
		// form post or a no-cors fetch is a *simple* request — it reaches
		// the handler with no preflight, and the side effect lands even
		// though the attacker cannot read the reply. Dispatching an agent
		// is exactly such a side effect.
		if !allowedOrigin(r) {
			writeError(w, http.StatusForbidden, "cross-origin request refused")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) tokenMatches(r *http.Request) bool {
	presented := r.URL.Query().Get("token")
	// A bearer header wins only when it actually carries one. Overwriting
	// a good query token with an unrelated Authorization header — a proxy
	// injecting Basic, a browser replaying cached credentials for
	// 127.0.0.1 — would reject a request that was perfectly authorized.
	if bearer, ok := bearerToken(r.Header.Get("Authorization")); ok {
		presented = bearer
	}
	return subtle.ConstantTimeCompare([]byte(presented), []byte(s.token)) == 1
}

// bearerToken pulls the credential out of an Authorization header. The
// scheme is case-insensitive per RFC 7235, and the space after it is not
// optional.
func bearerToken(header string) (string, bool) {
	scheme, credential, found := strings.Cut(header, " ")
	if !found || !strings.EqualFold(scheme, "Bearer") {
		return "", false
	}
	credential = strings.TrimSpace(credential)
	return credential, credential != ""
}

// loopbackHost reports whether a Host header names this machine. The
// server binds loopback, so anything else arrived by a name that resolved
// to it from outside — and matching an Origin against such a Host would
// be checking the attacker's claim against itself.
func loopbackHost(host string) bool {
	name, _, err := net.SplitHostPort(host)
	if err != nil {
		name = host
	}
	name = strings.Trim(name, "[]")
	if strings.EqualFold(name, "localhost") {
		return true
	}
	address := net.ParseIP(name)
	return address != nil && address.IsLoopback()
}

// allowedOrigin accepts a request with no Origin at all — that is a
// program, not a page, and programs are the other half of this API's
// audience — and otherwise requires an origin whose host is loopback and
// matches the one being served.
func allowedOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	parsed, err := url.Parse(origin)
	if err != nil || parsed.Host == "" {
		return false
	}
	return loopbackHost(parsed.Host) && parsed.Host == r.Host
}

// writeJSON encodes a successful response.
func writeJSON(w http.ResponseWriter, status int, payload any) {
	if payload == nil {
		// Nothing to describe; a content type would be a claim about a
		// body that is not there.
		w.WriteHeader(status)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
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

// maxRequestBody bounds a control-plane request. The largest legitimate
// one is a task description; anything approaching this is a mistake or an
// attempt to make the server hold memory on request.
const maxRequestBody = 1 << 20

// decode reads a JSON request body, refusing unknown fields so a
// misspelled key is an error rather than a silently ignored intention.
func decode(w http.ResponseWriter, r *http.Request, target any) error {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxRequestBody))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("malformed request body: %w", err)
	}
	return nil
}
