package ui

import (
	"strings"
	"testing"
)

func TestTerminalControlsReplaceDashboardFooterHints(t *testing.T) {
	model := Model{
		mode:       modeNormal,
		activePane: paneInteraction,
		ptyEnabled: true,
		width:      120,
		keys:       defaultKeyBindings(),
	}
	hints := model.commandHints()
	for _, want := range []string{"ctrl+space out", "+j/k agents", "+n/p queue", "+z zoom"} {
		if !strings.Contains(hints, want) {
			t.Fatalf("terminal footer hints missing %q: %q", want, hints)
		}
	}
	if strings.Contains(hints, "transcript") {
		t.Fatalf("the transcript chord is gone; hints still name it: %q", hints)
	}
	if strings.Contains(hints, "i reply") {
		t.Fatalf("dashboard interaction hints remained in terminal footer: %q", hints)
	}
}

func TestGridCellMapsDockedAndZoomedOrigins(t *testing.T) {
	model := Model{width: 121, height: 40, ptyEnabled: true}
	workspaceWidth, agentWidth, _ := model.paneWidths(120)
	left := workspaceWidth + agentWidth + 1
	col, row, ok := model.gridCellAt(left+5, ptyGridTop+3)
	if !ok || col != 5 || row != 3 {
		t.Fatalf("docked cell = (%d,%d,%v)", col, row, ok)
	}
	if _, _, ok := model.gridCellAt(left-2, ptyGridTop); ok {
		t.Fatal("a sidebar column mapped into the grid")
	}
	model.ptyZoom = true
	col, row, ok = model.gridCellAt(5, ptyGridTop+1)
	if !ok || col != 5 || row != 1 {
		t.Fatalf("zoomed cell = (%d,%d,%v)", col, row, ok)
	}
}
