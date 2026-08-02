package ui

// tea.Cmd constructors talking to the backend.
// Split from model.go; see #34.

import (
	"context"
	"time"

	tea "github.com/charmbracelet/bubbletea"
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

func tickCmd() tea.Cmd {
	return tea.Tick(2*time.Second, func(t time.Time) tea.Msg {
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
