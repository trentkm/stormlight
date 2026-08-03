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

func TestComposerRefusesToSendIntoAnActivePrompt(t *testing.T) {
	backend := &flowBackend{}
	model := flowModelFixture(t, backend)
	model.agents[0].Attention = agent.AttentionApproval
	model.rebuildGroups(model.agents[0].Workspace.ID, "agent-1")

	updated, _ := model.updateNormal(runeKey("i"))
	model = updated.(Model)
	model.sendInput.SetValue("oranges")
	next, cmd := model.updateCompose(tea.KeyMsg{Type: tea.KeyEnter})
	model = next.(Model)
	if cmd != nil {
		t.Fatal("send fired into an active prompt")
	}
	if model.err == nil || !strings.Contains(model.err.Error(), "terminal") {
		t.Fatalf("guard err = %v", model.err)
	}
	if model.sendInput.Value() != "oranges" {
		t.Fatal("guard discarded the typed message")
	}
}

func TestComposerYieldsWhenPromptArrivesMidCompose(t *testing.T) {
	backend := &flowBackend{}
	model := flowModelFixture(t, backend)
	updated, _ := model.updateNormal(runeKey("i"))
	model = updated.(Model)
	model.sendInput.SetValue("half a thought")

	agents := append([]agent.Agent(nil), model.agents...)
	agents[0].Attention = agent.AttentionApproval
	next, _ := model.Update(dashboardMsg{
		agents:     agents,
		workspaces: model.catalogWorkspaces,
	})
	model = next.(Model)
	if model.mode != modeNormal {
		t.Fatalf("composer did not yield: mode = %d", model.mode)
	}
	if model.sendInput.Value() != "half a thought" {
		t.Fatalf("draft was discarded: %q", model.sendInput.Value())
	}

	// After the prompt clears, i picks the draft back up.
	agents[0].Attention = agent.AttentionNone
	next, _ = model.Update(dashboardMsg{
		agents:     agents,
		workspaces: model.catalogWorkspaces,
	})
	model = next.(Model)
	updated, _ = model.updateNormal(runeKey("i"))
	model = updated.(Model)
	if model.mode != modeCompose || model.sendInput.Value() != "half a thought" {
		t.Fatalf("draft not restored: mode=%d value=%q",
			model.mode, model.sendInput.Value())
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

func TestColumnResizeAdjustsPersistsAndClamps(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	model := flowModelFixture(t, &flowBackend{})
	width := max(1, model.width-1)
	baseW, baseA, baseI := model.paneWidths(width)

	// Widen the focused Workspaces pane.
	updated, _ := model.updateNormal(runeKey(">"))
	model = updated.(Model)
	w, _, _ := model.paneWidths(width)
	if w != baseW+2 {
		t.Fatalf("workspace width %d, want %d", w, baseW+2)
	}

	// Growing Spanreed takes columns from Agents.
	model.activePane = paneInteraction
	updated, _ = model.updateNormal(runeKey(">"))
	model = updated.(Model)
	_, a, i := model.paneWidths(width)
	if a != baseA-2 || i != baseI {
		t.Fatalf("after spanreed grow: agents=%d spanreed=%d (base %d/%d)",
			a, i, baseA, baseI)
	}

	// The adjustment survives a reload through the prefs file.
	restored := LoadColumnPrefs()
	if restored != model.columns {
		t.Fatalf("prefs did not round-trip: %+v vs %+v", restored, model.columns)
	}

	// Shrinking against the clamp reverts instead of accruing debt.
	model.activePane = paneWorkspaces
	for range 30 {
		updated, _ = model.updateNormal(runeKey("<"))
		model = updated.(Model)
	}
	floorW, _, _ := model.paneWidths(width)
	updated, _ = model.updateNormal(runeKey(">"))
	model = updated.(Model)
	afterW, _, _ := model.paneWidths(width)
	if afterW != floorW+2 {
		t.Fatalf("one > after clamping should recover immediately: %d -> %d",
			floorW, afterW)
	}
}

func TestDimPaneLayersFaintWithoutBreakingColors(t *testing.T) {
	// A truecolor sequence whose components include 1 and 2 must survive
	// verbatim; standalone bold must drop; every sequence re-arms faint.
	line := "\x1b[1mBold\x1b[0m and \x1b[38;2;1;2;3mtruecolor\x1b[m tail"
	dimmed := dimPane(line, undimmedRows{})
	if !strings.Contains(dimmed, "\x1b[38;2;1;2;3;2m") {
		t.Fatalf("truecolor params were mangled: %q", dimmed)
	}
	if strings.Contains(dimmed, "\x1b[1m") || strings.Contains(dimmed, ";1m") {
		t.Fatalf("bold survived the dim layer: %q", dimmed)
	}
	if !strings.HasPrefix(dimmed, "\x1b[2m") || !strings.HasSuffix(dimmed, "\x1b[22m") {
		t.Fatalf("faint framing missing: %q", dimmed)
	}
	if got := dimParams(""); got != "0;2" {
		t.Fatalf("bare reset rewrote to %q", got)
	}
	if got := dimParams("38;5;1;1"); got != "38;5;1;2" {
		t.Fatalf("256-color guard failed: %q", got)
	}

	kept := dimPane("row zero\nrow one\nrow two", undimmedRows{start: 1, count: 1})
	rows := strings.Split(kept, "\n")
	if !strings.HasPrefix(rows[0], "\x1b[2m") || !strings.HasPrefix(rows[2], "\x1b[2m") {
		t.Fatalf("unselected rows were not dimmed: %q", kept)
	}
	if strings.Contains(rows[1], "\x1b[2m") {
		t.Fatalf("kept row was dimmed: %q", rows[1])
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

// dispatchFixture opens the new-agent form on a terminal roomy enough to
// draw the optional name row.
func dispatchFixture(t *testing.T) Model {
	t.Helper()
	model := flowModelFixture(t, &flowBackend{})
	updated, _ := model.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	model = updated.(Model)
	next, _ := model.beginDispatch(true)
	model = next.(Model)
	if !model.dispatchNameVisible() {
		t.Fatal("fixture terminal is too short to draw the name row")
	}
	return model
}

func selectCustomDirectory(t *testing.T, model *Model) {
	t.Helper()
	for index := range model.directories {
		if model.directories[index].kind == directoryCustom {
			model.selectDirectoryIndex(index)
			return
		}
	}
	t.Fatal("no custom-path row in the directory choices")
}

func TestDispatchEnterReachesTheNameField(t *testing.T) {
	model := dispatchFixture(t)
	for _, want := range []dispatchFocus{dispatchName, dispatchTask} {
		next, _ := model.updateDispatch(tea.KeyMsg{Type: tea.KeyEnter})
		model = next.(Model)
		if model.formFocus != want {
			t.Fatalf("enter landed on focus %d, want %d", model.formFocus, want)
		}
	}
}

func TestDispatchTabEscapesThePathPicker(t *testing.T) {
	model := dispatchFixture(t)
	selectCustomDirectory(t, &model)
	next, _ := model.updateDispatch(tea.KeyMsg{Type: tea.KeyEnter})
	model = next.(Model)
	if model.formFocus != dispatchCustomPath {
		t.Fatalf("enter did not open the path picker: focus %d", model.formFocus)
	}
	after, _ := model.updateDispatch(tea.KeyMsg{Type: tea.KeyTab})
	model = after.(Model)
	if model.formFocus != dispatchName {
		t.Fatalf("tab out of the picker landed on %d, want name", model.formFocus)
	}
}

func TestDispatchNameAcceptsTypedRunes(t *testing.T) {
	model := dispatchFixture(t)
	next, _ := model.updateDispatch(tea.KeyMsg{Type: tea.KeyEnter})
	model = next.(Model)
	for _, key := range []string{"m", "j", "e", "g"} {
		updated, _ := model.updateDispatch(runeKey(key))
		model = updated.(Model)
	}
	if model.nameInput.Value() != "mjeg" {
		t.Fatalf("name field value = %q", model.nameInput.Value())
	}
}

func TestDispatchHintsNameTheNextField(t *testing.T) {
	model := dispatchFixture(t)
	model.mode = modeDispatch
	if hints := model.commandHints(); !strings.Contains(hints, "Enter name") {
		t.Fatalf("directory hints hide the name field: %q", hints)
	}
	selectCustomDirectory(t, &model)
	next, _ := model.updateDispatch(tea.KeyMsg{Type: tea.KeyEnter})
	model = next.(Model)
	if hints := model.commandHints(); !strings.Contains(hints, "Tab name") {
		t.Fatalf("picker hints hide the way out: %q", hints)
	}
}
