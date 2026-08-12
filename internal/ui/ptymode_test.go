package ui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/trentkm/stormlight/internal/agent"
	"github.com/trentkm/stormlight/internal/workspace"
)

func TestSelectionMovesInPTYMode(t *testing.T) {
	model := NewModel(stubBackend{})
	updated, _ := model.Update(tea.WindowSizeMsg{Width: 140, Height: 40})
	model = updated.(Model)
	ws := workspace.DirectoryContext("/tmp/sel")
	model.agents = []agent.Agent{
		{ID: "beta", Name: "beta", Workspace: ws, ProcessLive: true},
		{ID: "alpha", Name: "alpha", Workspace: ws, ProcessLive: true},
	}
	model.rebuildGroups(ws.ID, "alpha")
	model.activePane = paneAgents
	if got := model.selectedAgentID(); got != "alpha" {
		t.Fatalf("setup: selected %q", got)
	}
	next, _ := model.updateNormal(tea.KeyPressMsg{Code: 'k', Text: "k"})
	model = next.(Model)
	if got := model.selectedAgentID(); got != "beta" {
		t.Fatalf("k did not move selection: still %q", got)
	}
}

// The window bar must fill the pane exactly — a short bar breaks the band,
// an overflowing one wraps and shears the grid below it — and carry the
// agent's name and location at any width.
func TestTerminalBarFillsWidthExactly(t *testing.T) {
	model := NewModel(stubBackend{})
	updated, _ := model.Update(tea.WindowSizeMsg{Width: 140, Height: 40})
	model = updated.(Model)
	managedAgent := agent.Agent{
		ID:          "alpha",
		Name:        "alpha",
		Workspace:   workspace.DirectoryContext("/tmp/portal"),
		Cwd:         "/tmp/portal",
		Activity:    agent.ActivityWorking,
		ProcessLive: true,
	}
	for _, width := range []int{20, 56, 120} {
		bar := model.renderTerminalBar(managedAgent, width)
		if got := lipgloss.Width(bar); got != width {
			t.Errorf("width %d: bar renders %d cells", width, got)
		}
	}
	bar := model.renderTerminalBar(managedAgent, 80)
	if !strings.Contains(bar, "alpha") || !strings.Contains(bar, "portal") {
		t.Errorf("bar drops the agent facts: %q", bar)
	}
}
