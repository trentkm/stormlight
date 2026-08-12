package ui

import (
	"testing"

	tea "charm.land/bubbletea/v2"

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
