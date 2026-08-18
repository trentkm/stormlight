package ui

// tea.Cmd constructors talking to the backend.
// Split from model.go; see #34.

import (
	"context"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/trentkm/stormlight/internal/agent"
	"github.com/trentkm/stormlight/internal/app"
	"github.com/trentkm/stormlight/internal/diagnostic"
)

func (m Model) refreshCmd() tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		agents, err := m.backend.ListAgents(ctx)
		if err != nil {
			return dashboardMsg{err: err}
		}
		workspaces, err := m.backend.ListWorkspaces(ctx)
		if err != nil {
			return dashboardMsg{agents: agents, err: err}
		}
		roots, err := m.backend.ListWorkspaceRoots(ctx)
		if err != nil {
			return dashboardMsg{
				agents:     agents,
				workspaces: workspaces,
				err:        err,
			}
		}
		return dashboardMsg{
			agents:     agents,
			workspaces: workspaces,
			roots:      roots,
		}
	}
}

// interactionCaptureLines asks for the pane's entire history (negative
// budgets mean "from the beginning"): the transcript, and / search over it,
// should reach everything the agent ever printed. A worst-case 50k-line
// capture measures ~100ms and runs in a background command.
const interactionCaptureLines = -1

// shouldReloadInteraction keeps the fast poll cheap: the transcript is
// recaptured only when the selected agent's story changed, while it is
// streaming, or after a two-second staleness bound — not on every tick.
func (m Model) shouldReloadInteraction(previous agent.Agent) bool {
	selected, ok := m.selectedAgent()
	if !ok {
		return false
	}
	if selected.ID != previous.ID || selected.ID != m.interactionID {
		return true
	}
	if selected.Activity == agent.ActivityWorking ||
		selected.Activity == agent.ActivityStarting {
		return true
	}
	if selected.Activity != previous.Activity ||
		selected.Attention != previous.Attention ||
		selected.Summary != previous.Summary {
		return true
	}
	return time.Since(m.interactionLoadedAt) >= 2*time.Second
}

func (m Model) loadInteractionCmd() tea.Cmd {
	id := m.selectedAgentID()
	if id == "" {
		return nil
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		content, err := m.backend.Capture(ctx, id, interactionCaptureLines)
		return interactionMsg{id: id, content: content, err: err}
	}
}

// The state poll is fast so attention (the amber band) lands quickly; the
// expensive work — workspace resolution, transcript capture — is cached or
// gated so the quick cadence stays cheap.
func tickCmd() tea.Cmd {
	return tea.Tick(700*time.Millisecond, func(t time.Time) tea.Msg {
		return tickMsg(t)
	})
}

func shimmerTickCmd() tea.Cmd {
	return tea.Tick(90*time.Millisecond, func(t time.Time) tea.Msg {
		return shimmerTickMsg(t)
	})
}

// actionCmd runs a backend action; label names it in the failure log only —
// the dashboard does not announce successes.
func actionCmd(label string, action func(context.Context) error) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		err := action(ctx)
		if err != nil {
			diagnostic.Logger().Error("dashboard command failed",
				"action", label,
				"error", err,
			)
		}
		return actionMsg{err: err}
	}
}

// addWorkspaceCmd adds a directory the human picked, on the machine they
// picked it from.
func addWorkspaceCmd(backend Backend, host, path string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		value, err := backend.AddWorkspace(ctx, host, path)
		return workspaceAddedMsg{value: value, err: err}
	}
}

func attachCmd(backend Backend, id, name string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		result, err := backend.Attach(ctx, id)
		return attachMsg{result: result, name: name, err: err}
	}
}

func dispatchCmd(backend Backend, request app.DispatchRequest) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_, err := backend.Dispatch(ctx, request)
		if err != nil {
			diagnostic.Logger().Error("dashboard dispatch failed",
				"provider", request.Provider,
				"error", err,
			)
			return actionMsg{err: err}
		}
		return actionMsg{}
	}
}
