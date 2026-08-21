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
	// gate coalesces every terminal's output into one redraw stream; the
	// dashboard keeps a single listener on it however many terminals run.
	gate *pty.Gate

	mu      sync.Mutex
	entries map[string]pty.Model
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
		gate:     pty.NewGate(),
		entries:  make(map[string]pty.Model),
		starting: make(map[string]bool),
	}
}

// Wait hands the dashboard the herd's shared frame listener.
func (g *Manager) Wait() tea.Cmd {
	return g.gate.Wait()
}

// Gate exposes the shared gate for terminals the Manager does not own —
// the dashboard's overlay popup rides the same redraw stream.
func (g *Manager) Gate() *pty.Gate {
	return g.gate
}

// SetVisible marks which agents' terminals are on screen; only those
// request redraws when output arrives.
func (g *Manager) SetVisible(ids ...string) {
	visible := make(map[string]bool, len(ids))
	for _, id := range ids {
		visible[id] = true
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	for id, widget := range g.entries {
		widget.SetVisible(visible[id])
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
		if needsSize(widget, width, height) {
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

// needsSize reports whether a terminal is due an assertion: either its
// box moved, or the last one this widget sent never landed.
//
// The second half is the whole difference between a resize that is
// asserted and a resize that is *held*. An assertion can be refused,
// time out, or be dropped for a newer one that then loses the race to
// the daemon, and in every one of those cases the box is already the
// size the dashboard wants — so a reconcile comparing only the box sees
// nothing to do, and the agent keeps painting at whatever size the
// terminal was left holding (#155, #164).
//
// Comparing what the widget asserted, rather than the terminal's current
// size, is what keeps this from fighting other viewers. A browser that
// resizes the shared terminal moves the emulator and not this, so the
// dashboard follows it as before instead of flapping against it.
func needsSize(widget pty.Model, width, height int) bool {
	if cols, rows := widget.Size(); cols != width || rows != height {
		return true
	}
	cols, rows := widget.Asserted()
	return cols != width || rows != height
}

// open builds an agent's terminal over the runtime's attachment.
func (g *Manager) open(ctx context.Context, id string, width, height int) (pty.Model, error) {
	transport, err := g.backend.AttachTerminal(ctx, id, width, height)
	if err != nil {
		return pty.Model{}, err
	}
	return pty.New(transport, g.gate, width, height), nil
}

// Widget hands the UI an agent's terminal.
func (g *Manager) Widget(id string) (pty.Model, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	widget, ok := g.entries[id]
	return widget, ok
}

// WidgetByID resolves a widget's own id, for routing deferred messages
// (a coalesced wheel tick) to the terminal that scheduled them — which
// may no longer be the selected one by the time the tick lands.
func (g *Manager) WidgetByID(widgetID int64) (pty.Model, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	for _, widget := range g.entries {
		if widget.ID() == widgetID {
			return widget, true
		}
	}
	return pty.Model{}, false
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
	g.mu.Unlock()
	for _, widget := range widgets {
		widget.Close()
	}
	// Release the dashboard's last outstanding Wait so its goroutine does
	// not outlive the herd.
	g.gate.Notify()
}
