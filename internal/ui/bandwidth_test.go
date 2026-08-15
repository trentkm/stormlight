package ui

import (
	"testing"

	"charm.land/lipgloss/v2"
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
