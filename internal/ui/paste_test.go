package ui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

// A terminal paste is a message of its own under Bubble Tea v2, not a
// keystroke, so every field has to be reachable by that message or Cmd-V
// silently does nothing there.

func TestPasteFillsTaskBox(t *testing.T) {
	model := NewModel(stubBackend{})
	updated, _ := model.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	model = updated.(Model)
	model.mode = modeDispatch
	model.formFocus = dispatchTask
	model.focusForm()
	model.taskInput.SetValue("review ")

	updated, _ = model.Update(tea.PasteMsg{Content: "internal/ui/model.go"})
	model = updated.(Model)

	if got := model.taskInput.Value(); got != "review internal/ui/model.go" {
		t.Fatalf("task box = %q", got)
	}
}

func TestPasteKeepsTaskBoxMultiLine(t *testing.T) {
	model := NewModel(stubBackend{})
	updated, _ := model.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	model = updated.(Model)
	model.mode = modeDispatch
	model.formFocus = dispatchTask
	model.focusForm()

	updated, _ = model.Update(tea.PasteMsg{Content: "first\nsecond"})
	model = updated.(Model)

	if got := model.taskInput.Value(); got != "first\nsecond" {
		t.Fatalf("task box = %q", got)
	}
}

func TestPasteFillsComposer(t *testing.T) {
	model := NewModel(stubBackend{})
	updated, _ := model.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	model = updated.(Model)
	model.mode = modeCompose
	model.sendInput.Focus()
	model.sendInput.SetValue("see ")

	updated, _ = model.Update(tea.PasteMsg{Content: "line one\nline two"})
	model = updated.(Model)

	if got := model.sendInput.Value(); got != "see line one\nline two" {
		t.Fatalf("composer = %q", got)
	}
}

func TestPasteFillsNameFieldOnOneLine(t *testing.T) {
	model := NewModel(stubBackend{})
	updated, _ := model.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	model = updated.(Model)
	model.mode = modeDispatch
	model.formFocus = dispatchName
	model.focusForm()

	updated, _ = model.Update(tea.PasteMsg{Content: "fix\tthe\r\nparser"})
	model = updated.(Model)

	if got := model.nameInput.Value(); got != "fix the parser" {
		t.Fatalf("name field = %q", got)
	}
}

func TestPasteFillsRenameField(t *testing.T) {
	model := NewModel(stubBackend{})
	updated, _ := model.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	model = updated.(Model)
	model.mode = modeRename
	model.renameInput.SetValue("")
	model.renameInput.Focus()

	updated, _ = model.Update(tea.PasteMsg{Content: "stormlight"})
	model = updated.(Model)

	if got := model.renameInput.Value(); got != "stormlight" {
		t.Fatalf("rename field = %q", got)
	}
}

func TestPasteFillsPathPicker(t *testing.T) {
	model := NewModel(stubBackend{})
	updated, _ := model.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	model = updated.(Model)
	model.mode = modeDispatch
	model.formFocus = dispatchCustomPath
	model.startPathNav()

	updated, _ = model.Update(tea.PasteMsg{Content: "internal"})
	model = updated.(Model)

	if got := model.pathNav.filter.Value(); got != "internal" {
		t.Fatalf("path filter = %q", got)
	}
}

func TestPasteDrivesTranscriptSearch(t *testing.T) {
	model := NewModel(stubBackend{})
	updated, _ := model.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	model = updated.(Model)
	model.interactionContent = strings.Join([]string{
		"first line",
		"a needle here",
		"last line",
	}, "\n")
	model.mode = modeSearch
	model.search.input.SetValue("")
	model.search.input.Focus()

	updated, _ = model.Update(tea.PasteMsg{Content: "needle"})
	model = updated.(Model)

	if model.search.query != "needle" {
		t.Fatalf("search query = %q", model.search.query)
	}
	if len(model.search.matches) != 1 {
		t.Fatalf("matches = %v", model.search.matches)
	}
}

func TestFlattenPasteDropsControlBytes(t *testing.T) {
	got := string(flattenPaste("cd \x1b[200~/tmp\x07"))
	if got != "cd [200~/tmp" {
		t.Fatalf("flattened = %q", got)
	}
}
