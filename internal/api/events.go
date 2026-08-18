package api

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"time"

	"github.com/coder/websocket"

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
	// latest is the last roster broadcast, handed to a client the moment
	// it subscribes so a fresh viewer paints without waiting for a tick.
	latest []byte

	// wake asks the poller for a roster now rather than at the next tick.
	wake chan struct{}
}

func newHub(service *app.Service) *hub {
	return &hub{
		service: service,
		clients: make(map[chan []byte]struct{}),
		wake:    make(chan struct{}, 1),
	}
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
		case <-h.wake:
			// A client just arrived and has nothing to paint; answer it
			// now rather than at the next tick.
			previous = ""
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
		payload, err := json.Marshal(rosterEvent{Type: "agents", Agents: viewOf(agents)})
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
	Type   string      `json:"type"`
	Agents []agentView `json:"agents"`
}

func (h *hub) hasClients() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.clients) > 0
}

// broadcast replaces what each client is holding rather than skipping the
// ones that are busy. Every message is the whole roster, so the newest is
// the only one worth having — but dropping it is not the same thing:
// polling suppresses an unchanged roster, so a message skipped here is
// one nobody resends, and a viewer whose socket stalled for a single tick
// would show a stale agent until the next real change. Which, for an
// agent that just went quiet, may be never.
func (h *hub) broadcast(payload []byte) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.latest = payload
	for client := range h.clients {
		send(client, payload)
	}
}

// send hands a client the latest roster, discarding whatever older one it
// had not read. The caller holds the hub lock, which is what makes this
// safe to do without blocking: every send happens under that lock, so
// nothing can refill the slot between the discard and the send.
func send(client chan []byte, payload []byte) {
	select {
	case <-client:
	default:
	}
	client <- payload
}

func (h *hub) subscribe() (<-chan []byte, func()) {
	client := make(chan []byte, 1)
	h.mu.Lock()
	h.clients[client] = struct{}{}
	if h.latest != nil {
		// Under the lock, so it cannot block; see send. Doing this after
		// unlocking is what would deadlock — a broadcast could fill the
		// one slot first, and the reader that would drain it is what this
		// function has not returned yet.
		send(client, h.latest)
	}
	h.mu.Unlock()
	// Ask for a fresh roster either way: the poll skips itself while
	// nobody is listening, so what was cached may be old.
	select {
	case h.wake <- struct{}{}:
	default:
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

	// The hub answers a new subscriber itself — with what it has, and by
	// asking the poller for something fresher. Fetching an opening roster
	// here instead is how the stale one ended up arriving after it.
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
	go keepalive(ctx, cancel, conn)

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
