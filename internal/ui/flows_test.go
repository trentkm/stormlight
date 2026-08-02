package ui

import (
	"context"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"github.com/trentkm/stormlight/internal/agent"
	"github.com/trentkm/stormlight/internal/workspace"
)

// flowBackend records the mutating calls the modal flows make.
type flowBackend struct {
	stubBackend
	deletedIDs       []string
	removedRoot      string
	renamedID        string
	renamedName      string
	renamedWorkspace string
}

func (b *flowBackend) Delete(_ context.Context, id string) error {
	b.deletedIDs = append(b.deletedIDs, id)
	return nil
}

func (b *flowBackend) RemoveWorkspace(_ context.Context, value workspace.Context) error {
	b.removedRoot = value.Root
	return nil
}

func (b *flowBackend) Rename(_ context.Context, id, name string) error {
	b.renamedID = id
	b.renamedName = name
	return nil
}

func (b *flowBackend) RenameWorkspace(
	_ context.Context, value workspace.Context, name string,
) error {
	b.renamedWorkspace = value.Root
	b.renamedName = name
	return nil
}

func flowModelFixture(t *testing.T, backend Backend) Model {
	t.Helper()
	model := NewModel(backend)
	updated, _ := model.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	model = updated.(Model)
	workspaceContext := workspace.DirectoryContext("/tmp/flows")
	model.agents = []agent.Agent{{
		ID:          "agent-1",
		Name:        "agent-1",
		Provider:    agent.ProviderClaude,
		Task:        "flow testing",
		ProcessLive: true,
		Workspace:   workspaceContext,
	}}
	model.rebuildGroups(workspaceContext.ID, "agent-1")
	return model
}

func TestDeleteFlowDeletesAgentAndCancels(t *testing.T) {
	backend := &flowBackend{}
	model := flowModelFixture(t, backend)
	model.activePane = paneAgents

	updated, _ := model.updateNormal(tea.KeyMsg{Type: tea.KeyCtrlX})
	model = updated.(Model)
	if model.mode != modeDelete {
		t.Fatalf("ctrl+x mode = %d", model.mode)
	}
	next, _ := model.updateDelete(tea.KeyMsg{Type: tea.KeyEscape})
	model = next.(Model)
	if model.mode != modeNormal || len(backend.deletedIDs) != 0 {
		t.Fatalf("esc did not cancel cleanly: mode=%d deleted=%v",
			model.mode, backend.deletedIDs)
	}

	model.mode = modeDelete
	next, cmd := model.updateDelete(runeKey("x"))
	model = next.(Model)
	if model.mode != modeNormal || cmd == nil {
		t.Fatal("x did not confirm the delete")
	}
	cmd()
	if len(backend.deletedIDs) != 1 || backend.deletedIDs[0] != "agent-1" {
		t.Fatalf("deleted = %v", backend.deletedIDs)
	}
}

func TestDeleteFlowGuardsWorkspaceWithAgents(t *testing.T) {
	backend := &flowBackend{}
	model := flowModelFixture(t, backend)
	model.activePane = paneWorkspaces
	model.mode = modeDelete

	next, _ := model.updateDelete(runeKey("x"))
	model = next.(Model)
	if model.mode != modeDelete || !strings.Contains(model.status, "press X") {
		t.Fatalf("x skipped the agent guard: mode=%d status=%q",
			model.mode, model.status)
	}

	next, cmd := model.updateDelete(runeKey("X"))
	model = next.(Model)
	if cmd == nil {
		t.Fatal("X did not confirm")
	}
	cmd()
	if len(backend.deletedIDs) != 1 || backend.removedRoot == "" {
		t.Fatalf("X deleted=%v removed=%q", backend.deletedIDs, backend.removedRoot)
	}
}

func TestRenameFlowRenamesAgentAndWorkspace(t *testing.T) {
	backend := &flowBackend{}
	model := flowModelFixture(t, backend)
	model.activePane = paneAgents

	updated, _ := model.updateNormal(runeKey("R"))
	model = updated.(Model)
	if model.mode != modeRename || model.renameAgentID != "agent-1" {
		t.Fatalf("R state: mode=%d agent=%q", model.mode, model.renameAgentID)
	}
	if !strings.Contains(ansi.Strip(model.renderRenameModal(80, 24)), "Rename agent") {
		t.Fatal("rename modal lacks its title")
	}

	next, _ := model.updateRename(tea.KeyMsg{Type: tea.KeyCtrlU})
	model = next.(Model)
	next, cmd := model.updateRename(tea.KeyMsg{Type: tea.KeyEnter})
	model = next.(Model)
	if cmd != nil {
		t.Fatal("empty name was accepted")
	}
	model.renameInput.SetValue("focused fixer")
	next, cmd = model.updateRename(tea.KeyMsg{Type: tea.KeyEnter})
	model = next.(Model)
	if cmd == nil {
		t.Fatal("rename was not submitted")
	}
	cmd()
	if backend.renamedID != "agent-1" || backend.renamedName != "focused fixer" {
		t.Fatalf("renamed %q -> %q", backend.renamedID, backend.renamedName)
	}

	model.activePane = paneWorkspaces
	updated, _ = model.updateNormal(runeKey("R"))
	model = updated.(Model)
	model.renameInput.SetValue("better name")
	_, cmd = model.updateRename(tea.KeyMsg{Type: tea.KeyEnter})
	cmd()
	if backend.renamedWorkspace != "/tmp/flows" || backend.renamedName != "better name" {
		t.Fatalf("workspace rename = %q -> %q",
			backend.renamedWorkspace, backend.renamedName)
	}
}

func TestAddWorkspaceFlowNavigatesAndSubmits(t *testing.T) {
	root := t.TempDir()
	backend := &recordingBackend{}
	model := NewModel(backend)
	updated, _ := model.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	model = updated.(Model)
	workspaceContext := workspace.DirectoryContext(root)
	model.catalogWorkspaces = []workspace.Context{workspaceContext}
	model.rebuildGroups(workspaceContext.ID, "")
	model.activePane = paneWorkspaces

	updated, _ = model.updateNormal(runeKey("n"))
	model = updated.(Model)
	if model.mode != modeAddWorkspace || len(model.directories) == 0 {
		t.Fatalf("n state: mode=%d choices=%d", model.mode, len(model.directories))
	}

	next, _ := model.updateAddWorkspace(runeKey("j"))
	model = next.(Model)
	next, _ = model.updateAddWorkspace(runeKey("k"))
	model = next.(Model)

	next, cmd := model.submitAddWorkspace(root)
	model = next.(Model)
	if cmd == nil {
		t.Fatal("submit did not produce a command")
	}
	cmd()
	if backend.addedPath != root {
		t.Fatalf("workspace path = %q, want %q", backend.addedPath, root)
	}
	if _, cmd := model.submitAddWorkspace("/definitely/not/a/dir"); cmd != nil {
		t.Fatal("missing directory was submitted")
	}

	next, _ = model.updateAddWorkspace(tea.KeyMsg{Type: tea.KeyEscape})
	model = next.(Model)
	if model.mode != modeNormal {
		t.Fatalf("esc left mode %d", model.mode)
	}
}

func TestHelpModalOpensRendersAndDismisses(t *testing.T) {
	model := flowModelFixture(t, &flowBackend{})
	resized, _ := model.Update(tea.WindowSizeMsg{Width: 100, Height: 45})
	model = resized.(Model)
	updated, _ := model.updateNormal(runeKey("?"))
	model = updated.(Model)
	if model.mode != modeHelp {
		t.Fatalf("? mode = %d", model.mode)
	}
	view := ansi.Strip(model.View())
	for _, want := range []string{"Keys", "Navigate", "Act", "any key closes"} {
		if !strings.Contains(view, want) {
			t.Fatalf("help modal missing %q", want)
		}
	}
	next, _ := model.Update(runeKey("x"))
	model = next.(Model)
	if model.mode != modeNormal {
		t.Fatal("help modal did not dismiss")
	}
}

func TestNarrowLayoutFocusesOnePane(t *testing.T) {
	model := flowModelFixture(t, &flowBackend{})
	updated, _ := model.Update(tea.WindowSizeMsg{Width: 60, Height: 20})
	model = updated.(Model)
	for _, pane := range []pane{paneWorkspaces, paneAgents, paneInteraction} {
		model.activePane = pane
		assertViewFitsPane(t, model, 59, 19)
	}
}

func TestDispatchModeCyclesWithM(t *testing.T) {
	model := flowModelFixture(t, &flowBackend{})
	model.mode = modeDispatch
	seen := map[agent.PermissionMode]bool{model.dispatchMode: true}
	for range 3 {
		updated, _ := model.updateDispatch(runeKey("m"))
		model = updated.(Model)
		seen[model.dispatchMode] = true
	}
	if len(seen) < 3 {
		t.Fatalf("m cycled through %d modes: %v", len(seen), seen)
	}
}

func TestComposerCtrlJInsertsNewline(t *testing.T) {
	model := flowModelFixture(t, &flowBackend{})
	updated, _ := model.updateNormal(runeKey("i"))
	model = updated.(Model)
	model.sendInput.SetValue("first")
	next, _ := model.updateCompose(tea.KeyMsg{Type: tea.KeyCtrlJ})
	model = next.(Model)
	if !strings.Contains(model.sendInput.Value(), "\n") {
		t.Fatalf("ctrl+j did not insert a newline: %q", model.sendInput.Value())
	}
}

func TestComposerRuleAndVisibleRows(t *testing.T) {
	if width := len([]rune(ansi.Strip(renderComposerRule(24)))); width != 24 {
		t.Fatalf("composer rule width = %d", width)
	}
	model := flowModelFixture(t, &flowBackend{})
	if rows := model.visibleRows(); rows < 1 {
		t.Fatalf("visible rows = %d", rows)
	}
}
