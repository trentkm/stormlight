package ui

import (
	"github.com/charmbracelet/x/ansi"
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

func TestTerminalSelectionCopiesTmuxStyleSpans(t *testing.T) {
	model := Model{
		ptySelAnchor: gridCell{row: 0, col: 6},
		ptySelHead:   gridCell{row: 2, col: 3},
	}
	start, end := model.selectionSpan()
	if start != (gridCell{0, 6}) || end != (gridCell{2, 3}) {
		t.Fatalf("span = %v..%v", start, end)
	}
	// Reversed drags normalize.
	model.ptySelAnchor, model.ptySelHead = model.ptySelHead, model.ptySelAnchor
	start, end = model.selectionSpan()
	if start != (gridCell{0, 6}) || end != (gridCell{2, 3}) {
		t.Fatalf("reversed span = %v..%v", start, end)
	}
}

func TestPaintTerminalSelectionReversesTheSpanOnly(t *testing.T) {
	view := "alpha beta\ngamma delta\nepsilon zeta"
	painted := paintTerminalSelection(view,
		gridCell{row: 0, col: 6}, gridCell{row: 2, col: 2}, 12)
	lines := strings.Split(painted, "\n")
	if !strings.HasPrefix(lines[0], "alpha ") {
		t.Fatalf("text before the anchor was painted: %q", lines[0])
	}
	if !strings.Contains(lines[0], searchMatchSGR+"beta") {
		t.Fatalf("anchor line span not reversed: %q", lines[0])
	}
	if !strings.Contains(lines[1], searchMatchSGR) {
		t.Fatalf("middle line not reversed: %q", lines[1])
	}
	if !strings.Contains(lines[2], searchMatchSGR+"eps") ||
		strings.Contains(strings.SplitN(lines[2], searchResetSGR, 2)[0], "ilon") {
		t.Fatalf("head line reversed past its column: %q", lines[2])
	}
}

func TestSelectionCopiesByCellsNotRunes(t *testing.T) {
	// "你好 world": the two wide glyphs occupy cells 0-3; "world" starts
	// at cell 5. A rune-indexed slice would land inside "好 wor".
	line := "你好 world"
	got := strings.TrimRight(ansi.Cut(line, 5, 10), " ")
	if got != "world" {
		t.Fatalf("cell slice = %q, want %q", got, "world")
	}
}
