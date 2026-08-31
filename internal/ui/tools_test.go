package ui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

func TestToolOverlayFormValidatesInputBeforeSave(t *testing.T) {
	model := flowModelFixture(t, stubBackend{})
	model.beginToolEdit(-1)
	if !strings.Contains(model.toolForm.validation, "ID: start") {
		t.Fatalf("initial validation = %q", model.toolForm.validation)
	}

	model.toolForm.id.SetValue("review")
	model.toolForm.binary.SetValue("review-tool")
	model.toolForm.hotkey.SetValue("c r")
	model.focusToolField(3)
	updated, _ := model.updateToolEdit(tea.KeyPressMsg{Text: "{", Code: '{'})
	model = updated.(Model)
	if !strings.Contains(model.toolForm.validation, "Arguments:") {
		t.Fatalf("live validation = %q", model.toolForm.validation)
	}
	if !strings.Contains(model.renderToolEditModal(80, 24), "Arguments:") {
		t.Fatal("validation is not rendered in the form")
	}

	updated, _ = model.updateToolEdit(tea.KeyPressMsg{Code: tea.KeyEnter})
	model = updated.(Model)
	if model.mode != modeToolEdit || len(model.toolOverlays) != 0 {
		t.Fatalf("invalid form was saved: mode=%v tools=%#v", model.mode, model.toolOverlays)
	}
}

func TestToolOverlayFormExplainsHost(t *testing.T) {
	model := flowModelFixture(t, stubBackend{})
	model.beginToolEdit(-1)

	model.focusToolField(5)
	if rendered := model.renderToolEditModal(100, 28); !strings.Contains(rendered, "Blank runs here") {
		t.Fatalf("host guidance missing:\n%s", rendered)
	}
}

func TestToolShortcutsAppearInHelpNotFooter(t *testing.T) {
	model := flowModelFixture(t, stubBackend{})
	model.toolOverlays = []ToolOverlay{{
		ID:     "review",
		Title:  "Review tool",
		Binary: "/opt/tools/review",
		Hotkey: []string{"c", "r"},
	}}

	footer := ansi.Strip(model.renderFooter())
	for _, unwanted := range []string{"T tools", "c r Review tool"} {
		if strings.Contains(footer, unwanted) {
			t.Fatalf("footer includes tool hint %q: %q", unwanted, footer)
		}
	}

	help := ansi.Strip(model.renderHelpModal(100, 50))
	for _, want := range []string{"Tools", "T", "configure tool overlays", "c r", "Review tool"} {
		if !strings.Contains(help, want) {
			t.Fatalf("help missing %q:\n%s", want, help)
		}
	}
}
