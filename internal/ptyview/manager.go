package ptyview

// The Manager is what makes the Spanreed a multiplexer rather than a
// viewer: one live Session per agent, running for the agent's whole life,
// so selecting an agent switches which terminal is rendered instead of
// starting one. tmux persists the panes themselves; the Manager persists
// the dashboard's view of them, and a deep capture seed bridges the gap a
// dashboard restart leaves.

import (
	"context"
	"sync"

	"github.com/trentkm/stormlight/internal/diagnostic"
)

type Manager struct {
	backend  Backend
	stateDir string

	mu       sync.Mutex
	sessions map[string]*Session
	// starting marks agents whose Start is in flight. A Start takes real
	// time (tmux calls, a capture, a FIFO), and Ensure fires on every
	// refresh — without this, two overlapping reconciles both start a
	// session for the same agent, and the pair then share one FIFO path
	// and one pane pipe, so the loser's teardown severs the winner's
	// stream.
	starting map[string]bool
	// draining is CloseAll having spoken: any Start still in flight hands
	// its session straight to teardown instead of the map, so nothing
	// outlives the dashboard because it was born during the shutdown.
	draining bool
	width    int
	height   int
}

func NewManager(backend Backend, stateDir string) *Manager {
	return &Manager{
		backend:  backend,
		stateDir: stateDir,
		sessions: make(map[string]*Session),
		starting: make(map[string]bool),
	}
}

// Ensure reconciles the running sessions against the roster: agents without
// a session get one, sessions whose agents are gone close, and any session
// rendering at the wrong size is moved. Errors are logged and skipped — one
// unstartable pane must not keep the rest of the herd dark; the next
// reconcile retries it.
func (g *Manager) Ensure(ctx context.Context, agentIDs []string, width, height int) {
	g.mu.Lock()
	if g.draining {
		g.mu.Unlock()
		return
	}
	g.width, g.height = width, height
	wanted := make(map[string]bool, len(agentIDs))
	for _, id := range agentIDs {
		wanted[id] = true
	}
	var closing []*Session
	for id, s := range g.sessions {
		if !wanted[id] {
			closing = append(closing, s)
			delete(g.sessions, id)
		}
	}
	existing := make([]*Session, 0, len(g.sessions))
	for _, s := range g.sessions {
		existing = append(existing, s)
	}
	missing := make([]string, 0, len(agentIDs))
	for _, id := range agentIDs {
		if _, ok := g.sessions[id]; !ok && !g.starting[id] {
			missing = append(missing, id)
			g.starting[id] = true
		}
	}
	g.mu.Unlock()

	for _, s := range closing {
		s.Close()
	}
	for _, s := range existing {
		if w, h := s.Size(); w == width && h == height {
			continue
		}
		if err := s.Resize(ctx, width, height); err != nil {
			diagnostic.Logger().Warn("pty resize",
				"agent_id", s.AgentID, "error", err)
		}
	}
	for _, id := range missing {
		s, err := Start(ctx, g.backend, id, g.stateDir, width, height)
		g.mu.Lock()
		delete(g.starting, id)
		surplus := err == nil && g.draining
		if err == nil && !g.draining {
			g.sessions[id] = s
		}
		g.mu.Unlock()
		if surplus {
			s.Close()
		}
		if err != nil {
			diagnostic.Logger().Warn("pty session start",
				"agent_id", id, "error", err)
		}
	}
}

// ResizeAll reasserts the current size on every window and emulator — the
// recovery move after an external attach let a client resize the windows.
func (g *Manager) ResizeAll(ctx context.Context) {
	g.mu.Lock()
	width, height := g.width, g.height
	sessions := make([]*Session, 0, len(g.sessions))
	for _, s := range g.sessions {
		sessions = append(sessions, s)
	}
	g.mu.Unlock()
	if width == 0 || height == 0 {
		return
	}
	for _, s := range sessions {
		if err := s.Resize(ctx, width, height); err != nil {
			diagnostic.Logger().Warn("pty resize",
				"agent_id", s.AgentID, "error", err)
		}
	}
}

func (g *Manager) Session(id string) *Session {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.sessions[id]
}

// CloseAll tears down every session and marks the Manager draining: a
// Start still in flight tears its session down on arrival, and no later
// Ensure rebuilds the herd. Quit-only, by design.
func (g *Manager) CloseAll() {
	g.mu.Lock()
	g.draining = true
	sessions := make([]*Session, 0, len(g.sessions))
	for _, s := range g.sessions {
		sessions = append(sessions, s)
	}
	g.sessions = make(map[string]*Session)
	g.mu.Unlock()
	for _, s := range sessions {
		s.Close()
	}
}
