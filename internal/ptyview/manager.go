// Package ptyview keeps one live terminal per agent for the agent's whole
// life, so selecting an agent switches which terminal is rendered instead
// of starting one. Each terminal is Stormlight's own widget over the
// runtime's native terminal attachment.
package ptyview

import (
	"context"
	"sync"

	tea "charm.land/bubbletea/v2"
	"github.com/trentkm/stormlight/internal/diagnostic"
	"github.com/trentkm/stormlight/internal/pty"
)

// Backend opens one live attachment to an agent's terminal: an exact
// snapshot seed, then the live byte stream, with input and resize flowing
// back.
type Backend interface {
	AttachTerminal(ctx context.Context, id string, cols, rows int) (pty.Transport, error)
}

type Manager struct {
	backend Backend

	mu       sync.Mutex
	entries  map[string]pty.Model
	byWidget map[int64]string
	// starting marks agents whose open is in flight, so overlapping
	// reconciles never build two terminals over one agent.
	starting map[string]bool
	draining bool
	width    int
	height   int
}

func NewManager(backend Backend) *Manager {
	return &Manager{
		backend:  backend,
		entries:  make(map[string]pty.Model),
		byWidget: make(map[int64]string),
		starting: make(map[string]bool),
	}
}

// Ensure reconciles the terminals against the roster: agents without one
// get one, departed agents lose theirs, and boxes follow the grid. The
// returned commands carry transport resizes; run them like any tea.Cmd.
// Errors are logged and retried on the next reconcile.
func (g *Manager) Ensure(ctx context.Context, agentIDs []string, width, height int) []tea.Cmd {
	g.mu.Lock()
	if g.draining {
		g.mu.Unlock()
		return nil
	}
	g.width, g.height = width, height
	wanted := make(map[string]bool, len(agentIDs))
	for _, id := range agentIDs {
		wanted[id] = true
	}
	var closing []pty.Model
	for id, widget := range g.entries {
		if !wanted[id] {
			closing = append(closing, widget)
			delete(g.byWidget, widget.ID())
			delete(g.entries, id)
		}
	}
	existing := make([]pty.Model, 0, len(g.entries))
	for _, widget := range g.entries {
		existing = append(existing, widget)
	}
	missing := make([]string, 0, len(agentIDs))
	for _, id := range agentIDs {
		if _, ok := g.entries[id]; !ok && !g.starting[id] {
			missing = append(missing, id)
			g.starting[id] = true
		}
	}
	g.mu.Unlock()

	for _, widget := range closing {
		widget.Close()
	}
	var commands []tea.Cmd
	for _, widget := range existing {
		if cols, rows := widget.Size(); cols != width || rows != height {
			_, cmd := widget.SetSize(width, height)
			if cmd != nil {
				commands = append(commands, cmd)
			}
		}
	}
	for _, id := range missing {
		widget, err := g.open(ctx, id, width, height)
		g.mu.Lock()
		delete(g.starting, id)
		surplus := err == nil && g.draining
		if err == nil && !g.draining {
			g.entries[id] = widget
			g.byWidget[widget.ID()] = id
		}
		g.mu.Unlock()
		if surplus {
			widget.Close()
		}
		if err != nil {
			diagnostic.Logger().Warn("terminal open",
				"agent_id", id, "error", err)
		}
	}
	return commands
}

// open builds an agent's terminal over the runtime's attachment.
func (g *Manager) open(ctx context.Context, id string, width, height int) (pty.Model, error) {
	transport, err := g.backend.AttachTerminal(ctx, id, width, height)
	if err != nil {
		return pty.Model{}, err
	}
	return pty.New(transport, width, height), nil
}

// Widget hands the UI an agent's terminal.
func (g *Manager) Widget(id string) (pty.Model, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	widget, ok := g.entries[id]
	return widget, ok
}

// AgentForWidget resolves a FrameMsg's widget ID back to its agent.
func (g *Manager) AgentForWidget(widgetID int64) (string, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	id, ok := g.byWidget[widgetID]
	return id, ok
}

// ResizeAll reasserts the grid on every terminal — the recovery move
// after an external attach let a client resize the terminals.
func (g *Manager) ResizeAll(ctx context.Context) []tea.Cmd {
	g.mu.Lock()
	width, height := g.width, g.height
	widgets := make([]pty.Model, 0, len(g.entries))
	for _, widget := range g.entries {
		widgets = append(widgets, widget)
	}
	g.mu.Unlock()
	if width == 0 || height == 0 {
		return nil
	}
	var commands []tea.Cmd
	for _, widget := range widgets {
		_, cmd := widget.SetSize(width, height)
		if cmd != nil {
			commands = append(commands, cmd)
		}
	}
	return commands
}

// CloseAll tears down every terminal and marks the Manager draining.
// Quit-only, by design.
func (g *Manager) CloseAll() {
	g.mu.Lock()
	g.draining = true
	widgets := make([]pty.Model, 0, len(g.entries))
	for _, widget := range g.entries {
		widgets = append(widgets, widget)
	}
	g.entries = make(map[string]pty.Model)
	g.byWidget = make(map[int64]string)
	g.mu.Unlock()
	for _, widget := range widgets {
		widget.Close()
	}
}
