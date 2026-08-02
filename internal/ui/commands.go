package ui

// tea.Cmd constructors talking to the backend.
// Split from model.go; see #34.

import (
	"context"
	"time"

	tea "github.com/charmbracelet/bubbletea"
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
		return dashboardMsg{
			agents:     agents,
			workspaces: workspaces,
		}
	}
}

// interactionCaptureLines asks for the pane's entire history (negative
// budgets mean "from the beginning"): the transcript, and / search over it,
// should reach everything the agent ever printed. A worst-case 50k-line
// capture measures ~100ms and runs in a background command.
const interactionCaptureLines = -1

// syncAgentWindowsCmd sizes detached agent windows to the transcript
// viewport, so agents render at exactly the width Spanreed displays and
// captured lines never need re-wrapping. The window is taller than the
// viewport: alternate-screen agents (Claude Code) expose only their visible
// screen to tmux, so extra rows become extra reachable history.
func (m Model) syncAgentWindowsCmd() tea.Cmd {
	if !m.ready {
		return nil
	}
	width, height := m.interactionDimensions()
	rows := clamp(height*4, 24, 160)
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := m.backend.SyncAgentWindows(ctx, width, rows); err != nil {
			diagnostic.Logger().Warn("sync agent window sizes", "error", err)
		}
		return nil
	}
}

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

func actionCmd(status string, action func(context.Context) error) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		err := action(ctx)
		if err != nil {
			diagnostic.Logger().Error("dashboard command failed",
				"action", status,
				"error", err,
			)
		}
		return actionMsg{status: status, err: err}
	}
}

func addWorkspaceCmd(backend Backend, path string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		value, err := backend.AddWorkspace(ctx, path)
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
		managedAgent, err := backend.Dispatch(ctx, request)
		if err != nil {
			diagnostic.Logger().Error("dashboard dispatch failed",
				"provider", request.Provider,
				"error", err,
			)
			return actionMsg{err: err}
		}
		return actionMsg{status: "Dispatched " + agentDisplayTitle(managedAgent)}
	}
}
