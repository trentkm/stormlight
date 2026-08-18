package api

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"time"

	"github.com/coder/websocket"

	"github.com/trentkm/stormlight/internal/agent"
	"github.com/trentkm/stormlight/internal/app"
)

// pollInterval is how often the hub asks the runtime for the roster. It
// matches the dashboard's own refresh: the daemon is the source of truth
// for liveness and answers cheaply, and a second client polling at the
// same cadence adds one caller, not one per viewer.
const pollInterval = 700 * time.Millisecond

// hub turns the roster into an event stream. One poller serves every
// connected client, and only a roster that actually changed is broadcast,
// so an idle fleet is an idle socket.
//
// Polling is the v1 shape, not the destination: the daemon already
// publishes its own lifecycle and idle/busy feed, and moving to it later
// changes nothing a client can see — which is why the client contract is
// "here is the roster", never "here is what happened".
type hub struct {
	service *app.Service

	mu      sync.Mutex
	clients map[chan []byte]struct{}
	// latest is the last roster broadcast, kept so a client that connects
	// between polls is answered immediately instead of waiting.
	latest []byte
}

func newHub(service *app.Service) *hub {
	return &hub{service: service, clients: make(map[chan []byte]struct{})}
}

// run starts polling and returns the stop function.
func (h *hub) run() func() {
	ctx, cancel := context.WithCancel(context.Background())
	go h.poll(ctx)
	return cancel
}

func (h *hub) poll(ctx context.Context) {
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()
	var previous string
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		if !h.hasClients() {
			// Nobody is listening; do not spend a daemon round trip.
			continue
		}
		listCtx, cancel := context.WithTimeout(ctx, callTimeout)
		agents, err := h.service.ListAgents(listCtx)
		cancel()
		if err != nil {
			continue
		}
		payload, err := json.Marshal(rosterEvent{Type: "agents", Agents: agents})
		if err != nil {
			continue
		}
		if string(payload) == previous {
			continue
		}
		previous = string(payload)
		h.broadcast(payload)
	}
}

type rosterEvent struct {
	Type   string        `json:"type"`
	Agents []agent.Agent `json:"agents"`
}

func (h *hub) hasClients() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.clients) > 0
}

func (h *hub) broadcast(payload []byte) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.latest = payload
	for client := range h.clients {
		select {
		case client <- payload:
		default:
			// A client that cannot keep up with the roster is not worth
			// buffering for: the next broadcast carries the whole state
			// anyway, so dropping this one loses nothing.
		}
	}
}

func (h *hub) subscribe() (<-chan []byte, func()) {
	client := make(chan []byte, 1)
	h.mu.Lock()
	h.clients[client] = struct{}{}
	latest := h.latest
	h.mu.Unlock()
	if latest != nil {
		client <- latest
	}
	return client, func() {
		h.mu.Lock()
		delete(h.clients, client)
		h.mu.Unlock()
	}
}

// eventStream pushes the roster to one client for as long as it listens.
func (s *Server) eventStream(w http.ResponseWriter, r *http.Request) {
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		InsecureSkipVerify: true, // Origin is checked in the middleware.
	})
	if err != nil {
		return
	}
	ctx, cancel := context.WithCancel(context.WithoutCancel(r.Context()))
	defer cancel()
	defer conn.CloseNow()

	updates, unsubscribe := s.events.subscribe()
	defer unsubscribe()

	// The client speaks only by hanging up; notice when it does.
	go func() {
		defer cancel()
		for {
			if _, _, err := conn.Read(ctx); err != nil {
				return
			}
		}
	}()

	// An immediate roster, so a fresh client paints without waiting for
	// something to change.
	if err := s.sendRoster(ctx, conn); err != nil {
		return
	}
	for {
		select {
		case <-ctx.Done():
			return
		case payload := <-updates:
			if err := conn.Write(ctx, websocket.MessageText, payload); err != nil {
				return
			}
		}
	}
}

func (s *Server) sendRoster(ctx context.Context, conn *websocket.Conn) error {
	listCtx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	agents, err := s.service.ListAgents(listCtx)
	if err != nil {
		// A roster that cannot be read right now is not a reason to
		// refuse the stream; the next poll will carry one.
		return nil
	}
	payload, err := json.Marshal(rosterEvent{Type: "agents", Agents: agents})
	if err != nil {
		return err
	}
	return conn.Write(ctx, websocket.MessageText, payload)
}
