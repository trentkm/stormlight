// Package ptyview keeps one live terminal per agent for the agent's whole
// life, so selecting an agent switches which terminal is rendered instead
// of starting one. Each terminal is an oathgate widget over the richest
// transport the backend offers: the runtime's native stream when it has
// one, the tmux tap otherwise.
package ptyview

import (
	"context"
	"fmt"
	"sync"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/trentkm/oathgate"

	"github.com/trentkm/stormlight/internal/diagnostic"
	"github.com/trentkm/stormlight/internal/session"
)

// Backend is the tap-shaped slice of the app service: pipe-pane into a
// FIFO, seeded by a raw capture, input through the runtime.
type Backend interface {
	PipePaneOpen(ctx context.Context, id, fifoPath string) error
	PipePaneClose(ctx context.Context, id string) error
	CaptureRaw(ctx context.Context, id string, history int) (string, error)
	PaneView(ctx context.Context, id string) (session.PaneView, error)
	ResizeAgentWindow(ctx context.Context, id string, width, height int) error
	SendRaw(ctx context.Context, id string, data []byte) error
}

// StreamBackend is the native shape: the runtime attaches directly and
// hands over an exact seed plus the live byte stream.
type StreamBackend interface {
	OpenTerminalStream(ctx context.Context, id string, cols, rows int) (session.TerminalStream, error)
}

// entry is one agent's terminal: the widget, and what the Manager needs
// to keep it honest.
type entry struct {
	agentID string
	widget  oathgate.Model
	// tap marks tmux-hosted terminals, whose real pane size an attached
	// client can hold apart from the box — the Manager re-adopts it.
	tap     bool
	backend Backend

	mu        sync.Mutex
	lastAdopt time.Time
}

// adoptPaneSize mirrors the pane's actual size into the widget's emulator
// (tap only; a native stream's terminal always follows the box). An
// attached client can refuse resizes and hold the pane wide, and replaying
// a wide pane through a narrow emulator re-wraps every row into soup.
func (e *entry) adoptPaneSize(ctx context.Context) {
	if !e.tap {
		return
	}
	e.mu.Lock()
	if time.Since(e.lastAdopt) < 2500*time.Millisecond {
		e.mu.Unlock()
		return
	}
	e.lastAdopt = time.Now()
	e.mu.Unlock()
	view, err := e.backend.PaneView(ctx, e.agentID)
	if err != nil || view.Cols < 2 || view.Rows < 2 {
		return
	}
	e.widget.SetTerminalSize(view.Cols, view.Rows)
}

type Manager struct {
	backend  any
	stateDir string
	// options styles every widget the Manager builds. A function rather
	// than a slice because the palette resolves light-or-dark only after
	// the terminal answers the background query — evaluating at open time
	// picks up the answer, evaluating at construction bakes in the guess.
	options func() []oathgate.Option

	mu       sync.Mutex
	entries  map[string]*entry
	byWidget map[int64]string
	// starting marks agents whose open is in flight, so overlapping
	// reconciles never build two terminals over one pane.
	starting map[string]bool
	draining bool
	width    int
	height   int
}

func NewManager(backend any, stateDir string, options func() []oathgate.Option) *Manager {
	if options == nil {
		options = func() []oathgate.Option { return nil }
	}
	return &Manager{
		backend:  backend,
		stateDir: stateDir,
		options:  options,
		entries:  make(map[string]*entry),
		byWidget: make(map[int64]string),
		starting: make(map[string]bool),
	}
}

// Ensure reconciles the terminals against the roster: agents without one
// get one, departed agents lose theirs, boxes follow the grid, and tap
// terminals re-adopt their pane's real size. The returned commands carry
// transport resizes; run them like any tea.Cmd. Errors are logged and
// retried on the next reconcile.
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
	var closing []*entry
	for id, e := range g.entries {
		if !wanted[id] {
			closing = append(closing, e)
			delete(g.byWidget, e.widget.ID())
			delete(g.entries, id)
		}
	}
	existing := make([]*entry, 0, len(g.entries))
	for _, e := range g.entries {
		existing = append(existing, e)
	}
	missing := make([]string, 0, len(agentIDs))
	for _, id := range agentIDs {
		if _, ok := g.entries[id]; !ok && !g.starting[id] {
			missing = append(missing, id)
			g.starting[id] = true
		}
	}
	g.mu.Unlock()

	for _, e := range closing {
		e.widget.Close()
	}
	var commands []tea.Cmd
	for _, e := range existing {
		e.adoptPaneSize(ctx)
		if cols, rows := e.widget.Size(); cols != width || rows != height {
			_, cmd := e.widget.SetSize(width, height)
			if cmd != nil {
				commands = append(commands, cmd)
			}
		}
	}
	for _, id := range missing {
		e, err := g.open(ctx, id, width, height)
		g.mu.Lock()
		delete(g.starting, id)
		surplus := err == nil && g.draining
		if err == nil && !g.draining {
			g.entries[id] = e
			g.byWidget[e.widget.ID()] = id
		}
		g.mu.Unlock()
		if surplus {
			e.widget.Close()
		}
		if err != nil {
			diagnostic.Logger().Warn("terminal open",
				"agent_id", id, "error", err)
		}
	}
	return commands
}

// open builds an agent's terminal over the richest transport available.
// The service exposes both method sets whichever runtime is behind it, so
// capability is discovered by trying: native stream first, tap on refusal.
func (g *Manager) open(ctx context.Context, id string, width, height int) (*entry, error) {
	if streamer, ok := g.backend.(StreamBackend); ok {
		stream, err := streamer.OpenTerminalStream(ctx, id, width, height)
		if err == nil {
			return &entry{
				agentID: id,
				widget:  oathgate.New(stream, width, height, g.options()...),
			}, nil
		}
		if _, tappable := g.backend.(Backend); !tappable {
			return nil, err
		}
	}
	tap, ok := g.backend.(Backend)
	if !ok {
		return nil, fmt.Errorf("ptyview: backend offers no terminal transport")
	}
	transport, err := openTap(ctx, tap, id, g.stateDir, width, height)
	if err != nil {
		return nil, err
	}
	e := &entry{
		agentID: id,
		widget:  oathgate.New(transport, width, height, g.options()...),
		tap:     true,
		backend: tap,
	}
	e.adoptPaneSize(ctx)
	return e, nil
}

// Widget hands the UI an agent's terminal.
func (g *Manager) Widget(id string) (oathgate.Model, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	e, ok := g.entries[id]
	if !ok {
		return oathgate.Model{}, false
	}
	return e.widget, true
}

// AgentForWidget resolves a FrameMsg's widget ID back to its agent.
func (g *Manager) AgentForWidget(widgetID int64) (string, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	id, ok := g.byWidget[widgetID]
	return id, ok
}

// ResizeAll reasserts the grid on every terminal — the recovery move
// after an external attach let a client resize the windows.
func (g *Manager) ResizeAll(ctx context.Context) []tea.Cmd {
	g.mu.Lock()
	width, height := g.width, g.height
	entries := make([]*entry, 0, len(g.entries))
	for _, e := range g.entries {
		entries = append(entries, e)
	}
	g.mu.Unlock()
	if width == 0 || height == 0 {
		return nil
	}
	var commands []tea.Cmd
	for _, e := range entries {
		_, cmd := e.widget.SetSize(width, height)
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
	entries := make([]*entry, 0, len(g.entries))
	for _, e := range g.entries {
		entries = append(entries, e)
	}
	g.entries = make(map[string]*entry)
	g.byWidget = make(map[int64]string)
	g.mu.Unlock()
	for _, e := range entries {
		e.widget.Close()
	}
}
