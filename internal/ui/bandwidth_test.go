package ui

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"

	"github.com/trentkm/stormlight/internal/agent"
	"github.com/trentkm/stormlight/internal/workspace"
)

// The strip is the one row that reaches the terminal's true right edge:
// composed width is m.width, one column past the body.
func TestTitleBandReachesTheTerminalEdge(t *testing.T) {
	model := NewModel(stubBackend{})
	model.width = 100
	model.height = 24
	width := model.width - 1
	workspaceWidth, agentWidth, interactionWidth := model.paneWidths(width)
	band := model.renderTitleBand(workspaceWidth, agentWidth, interactionWidth)
	if got := lipgloss.Width(band); got != model.width {
		t.Fatalf("band width = %d, want %d", got, model.width)
	}
}

// TestWorkspaceCountMatchesTheRows: the pane lists catalogued workspaces
// and any that merely hold an agent, and a workspace on another machine
// is never in this machine's catalogue. A count taken from the catalogue
// would then disagree with what is on screen.
func TestWorkspaceCountMatchesTheRows(t *testing.T) {
	local := workspace.Context{
		ID: "git:/here/.git", Kind: "git", Name: "here",
		Root: "/here", ExecutionRoot: "/here",
	}
	remote := workspace.Context{
		Host: "devbox", ID: "devbox:git:/there/.git", Kind: "git", Name: "there",
		Root: "/there", ExecutionRoot: "/there",
	}
	model := Model{
		catalogWorkspaces: []workspace.Context{local},
		agents: []agent.Agent{
			{ID: "aaa", Workspace: local},
			{ID: "bbb", Host: "devbox", Workspace: remote},
		},
	}
	model.rebuildGroups("", "")

	if len(model.groups) != 2 {
		t.Fatalf("both workspaces should be listed: %+v", model.groups)
	}
	if got := model.renderWorkspacesBar("", 40); !strings.Contains(got, "2") {
		t.Fatalf("the count should be of the rows, got %q", got)
	}
}
