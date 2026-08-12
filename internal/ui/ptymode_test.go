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
	}
	hints := model.commandHints()
	for _, want := range []string{"ctrl+space out", "+j/k agents", "+z zoom", "+t transcript"} {
		if !strings.Contains(hints, want) {
			t.Fatalf("terminal footer hints missing %q: %q", want, hints)
		}
	}
	if strings.Contains(hints, "i reply") {
		t.Fatalf("dashboard interaction hints remained in terminal footer: %q", hints)
	}
}
