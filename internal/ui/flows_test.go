package ui

import (
	"context"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/trentkm/stormlight/internal/agent"
	"github.com/trentkm/stormlight/internal/history"
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
	markedID         string
	mark             agent.Mark
	markCalls        int
}

func (b *flowBackend) SetMark(_ context.Context, id string, mark agent.Mark) error {
	b.markedID = id
	b.mark = mark
	b.markCalls++
	return nil
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
	// These flows exercise the transcript view; the PTY view boots live
	// terminal sessions no unit fixture wants.
	model.ptyEnabled = false
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

	updated, _ := model.updateNormal(tea.KeyPressMsg{Code: 'x', Mod: tea.ModCtrl})
	model = updated.(Model)
	if model.mode != modeDelete {
		t.Fatalf("ctrl+x mode = %d", model.mode)
	}
	next, _ := model.updateDelete(tea.KeyPressMsg{Code: tea.KeyEscape})
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

	next, _ := model.updateRename(tea.KeyPressMsg{Code: 'u', Mod: tea.ModCtrl})
	model = next.(Model)
	next, cmd := model.updateRename(tea.KeyPressMsg{Code: tea.KeyEnter})
	model = next.(Model)
	if cmd != nil {
		t.Fatal("empty name was accepted")
	}
	model.renameInput.SetValue("focused fixer")
	next, cmd = model.updateRename(tea.KeyPressMsg{Code: tea.KeyEnter})
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
	_, cmd = model.updateRename(tea.KeyPressMsg{Code: tea.KeyEnter})
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

	next, _ = model.updateAddWorkspace(tea.KeyPressMsg{Code: tea.KeyEscape})
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
	next, cmd := model.updateCompose(tea.KeyPressMsg{Code: tea.KeyEnter})
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
	resized, _ := model.Update(tea.WindowSizeMsg{Width: 100, Height: 50})
	model = resized.(Model)
	updated, _ := model.updateNormal(runeKey("?"))
	model = updated.(Model)
	if model.mode != modeHelp {
		t.Fatalf("? mode = %d", model.mode)
	}
	view := ansi.Strip(model.View().Content)
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
		t.Fatalf("after terminal grow: agents=%d terminal=%d (base %d/%d)",
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

func TestSpanreedNeverDimsAndListsAlwaysDoOutsideTheSelection(t *testing.T) {
	model := flowModelFixture(t, &flowBackend{})
	workspaceContext := workspace.DirectoryContext("/tmp/flows")
	model.agents = append(model.agents, agent.Agent{
		ID:          "agent-2",
		Name:        "agent-2",
		Provider:    agent.ProviderClaude,
		ProcessLive: true,
		Workspace:   workspaceContext,
	})
	model.rebuildGroups(workspaceContext.ID, "agent-1")

	// Whichever pane holds the cursor, the transcript reads at full strength
	// and the lists recede to everything but their selected row.
	for _, focus := range []pane{paneWorkspaces, paneAgents, paneInteraction} {
		model.activePane = focus
		workspaces, agents, interaction := model.paneDimmings(20)
		if interaction.dim {
			t.Fatalf("terminal dimmed with pane %d focused", focus)
		}
		if !workspaces.dim || !agents.dim {
			t.Fatalf("lists undimmed with pane %d focused: %+v %+v",
				focus, workspaces, agents)
		}
		if agents.keep.count == 0 {
			t.Fatalf("selected agent not kept lit with pane %d focused", focus)
		}
		if workspaces.keep.count == 0 {
			t.Fatalf("selected workspace not kept lit with pane %d focused", focus)
		}
	}

	// The keep range covers only the cursor row, so the second agent — the
	// one nothing points at — still recedes while the agent list is focused.
	model.activePane = paneAgents
	_, agents, _ := model.paneDimmings(20)
	rendered := dimPane(model.renderAgents(30, 19), agents.keep)
	rows := strings.Split(rendered, "\n")
	selected := rows[agents.keep.start]
	other := rows[agents.keep.start+agents.keep.count]
	if strings.Contains(selected, "\x1b[2m") {
		t.Fatalf("selected agent row was dimmed: %q", selected)
	}
	if !strings.HasPrefix(other, "\x1b[2m") {
		t.Fatalf("unselected agent row was not dimmed: %q", other)
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
	next, _ := model.updateCompose(tea.KeyPressMsg{Code: 'j', Mod: tea.ModCtrl})
	model = next.(Model)
	if !strings.Contains(model.sendInput.Value(), "\n") {
		t.Fatalf("ctrl+j did not insert a newline: %q", model.sendInput.Value())
	}
}

func TestComposerBackspaceLeavesOnlyWhenEmpty(t *testing.T) {
	model := flowModelFixture(t, &flowBackend{})
	updated, _ := model.updateNormal(runeKey("i"))
	model = updated.(Model)
	model.sendInput.SetValue("hi")

	next, _ := model.updateCompose(tea.KeyPressMsg{Code: tea.KeyBackspace})
	model = next.(Model)
	if model.mode != modeCompose {
		t.Fatalf("backspace over text left compose mode: %d", model.mode)
	}
	if model.sendInput.Value() != "h" {
		t.Fatalf("backspace did not delete a rune: %q", model.sendInput.Value())
	}

	next, _ = model.updateCompose(tea.KeyPressMsg{Code: tea.KeyBackspace})
	model = next.(Model)
	if model.mode != modeCompose || model.sendInput.Value() != "" {
		t.Fatalf("emptying the box left compose mode early: mode=%d value=%q",
			model.mode, model.sendInput.Value())
	}

	next, _ = model.updateCompose(tea.KeyPressMsg{Code: tea.KeyBackspace})
	model = next.(Model)
	if model.mode != modeNormal {
		t.Fatalf("backspace on an empty box stayed in mode %d", model.mode)
	}
	if model.sendInput.Focused() {
		t.Fatal("reply box kept focus after leaving")
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
		next, _ := model.updateDispatch(tea.KeyPressMsg{Code: tea.KeyEnter})
		model = next.(Model)
		if model.formFocus != want {
			t.Fatalf("enter landed on focus %d, want %d", model.formFocus, want)
		}
	}
}

func TestDispatchTabEscapesThePathPicker(t *testing.T) {
	model := dispatchFixture(t)
	selectCustomDirectory(t, &model)
	next, _ := model.updateDispatch(tea.KeyPressMsg{Code: tea.KeyEnter})
	model = next.(Model)
	if model.formFocus != dispatchCustomPath {
		t.Fatalf("enter did not open the path picker: focus %d", model.formFocus)
	}
	after, _ := model.updateDispatch(tea.KeyPressMsg{Code: tea.KeyTab})
	model = after.(Model)
	if model.formFocus != dispatchName {
		t.Fatalf("tab out of the picker landed on %d, want name", model.formFocus)
	}
}

func TestDispatchNameAcceptsTypedRunes(t *testing.T) {
	model := dispatchFixture(t)
	next, _ := model.updateDispatch(tea.KeyPressMsg{Code: tea.KeyEnter})
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
	next, _ := model.updateDispatch(tea.KeyPressMsg{Code: tea.KeyEnter})
	model = next.(Model)
	if hints := model.commandHints(); !strings.Contains(hints, "Tab name") {
		t.Fatalf("picker hints hide the way out: %q", hints)
	}
}

func TestMarkFlowRecordsTheHumansReading(t *testing.T) {
	backend := &flowBackend{}
	model := flowModelFixture(t, backend)
	model.activePane = paneAgents

	updated, _ := model.updateNormal(runeKey("m"))
	model = updated.(Model)
	if model.mode != modeMark || model.markAgentID != "agent-1" {
		t.Fatalf("m state: mode=%d agent=%q", model.mode, model.markAgentID)
	}
	rendered := ansi.Strip(model.renderMarkModal(80, 24))
	for _, want := range []string{"Mark", "In progress", "Needs attention"} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("mark modal lacks %q:\n%s", want, rendered)
		}
	}

	next, _ := model.updateMark(tea.KeyPressMsg{Code: tea.KeyEscape})
	model = next.(Model)
	if model.mode != modeNormal || backend.markCalls != 0 {
		t.Fatalf("esc did not cancel: mode=%d calls=%d", model.mode, backend.markCalls)
	}

	model.mode = modeMark
	next, cmd := model.updateMark(runeKey("a"))
	model = next.(Model)
	if cmd == nil {
		t.Fatal("a did not submit a mark")
	}
	// The row reports the human's reading before the backend write lands.
	if model.agents[0].Mark != agent.MarkAttention ||
		model.groups[0].agents[0].Mark != agent.MarkAttention {
		t.Fatalf("mark not applied locally: %#v", model.agents[0].Mark)
	}
	cmd()
	if backend.markedID != "agent-1" || backend.mark != agent.MarkAttention {
		t.Fatalf("marked %q as %q", backend.markedID, backend.mark)
	}

	// Reopening lands on the mark already set, and clearing hands the row
	// back to Stormlight's own reading.
	updated, _ = model.updateNormal(runeKey("m"))
	model = updated.(Model)
	if markChoices[model.markIndex].mark != agent.MarkAttention {
		t.Fatalf("picker opened on %q", markChoices[model.markIndex].mark)
	}
	next, cmd = model.updateMark(runeKey("c"))
	model = next.(Model)
	cmd()
	if backend.mark != agent.MarkNone || model.agents[0].Mark != agent.MarkNone {
		t.Fatalf("clear left mark %q (backend %q)", model.agents[0].Mark, backend.mark)
	}
}

func TestMarkedRowsReportTheMarkAndClearWhenSeen(t *testing.T) {
	working := agent.Agent{
		ID:          "one",
		Name:        "one",
		ProcessLive: true,
		Activity:    agent.ActivityIdle,
		Attention:   agent.AttentionQuestion,
		Mark:        agent.MarkWorking,
	}
	attention := agent.Agent{
		ID:          "two",
		Name:        "two",
		ProcessLive: true,
		Activity:    agent.ActivityIdle,
		Mark:        agent.MarkAttention,
	}

	row := ansi.Strip(renderAgentRow(working, false, false, 60))
	if !strings.Contains(row, "marked in progress") {
		t.Fatalf("in-progress row = %q", row)
	}
	row = ansi.Strip(renderAgentRow(attention, false, false, 60))
	if !strings.Contains(row, "marked needs attention") ||
		!strings.Contains(row, "◆") {
		t.Fatalf("attention row = %q", row)
	}

	stats := agent.Count([]agent.Agent{working, attention})
	if stats.Working != 1 || stats.Urgent != 0 || stats.Waiting != 1 {
		t.Fatalf("stats = %+v", stats)
	}

	ws := workspace.DirectoryContext("/workspace/marks")
	model := NewModel(stubBackend{})
	model.width = 120
	model.catalogWorkspaces = []workspace.Context{ws}
	attention.Workspace = ws
	model.agents = []agent.Agent{attention}
	model.rebuildGroups(ws.ID, "")
	model.activePane = paneWorkspaces

	updated, cmd := model.updateNormal(runeKey("M"))
	model = updated.(Model)
	if cmd == nil {
		t.Fatal("M did not clear the manual mark")
	}
	if model.agents[0].Mark != agent.MarkNone {
		t.Fatalf("mark after M = %q", model.agents[0].Mark)
	}
}

func TestSpanreedHeadingHonorsMarksOverDerivedState(t *testing.T) {
	ws := workspace.DirectoryContext("/workspace/marks")
	model := NewModel(stubBackend{})
	model.width = 120
	model.catalogWorkspaces = []workspace.Context{ws}
	base := agent.Agent{
		ID:          "one",
		Name:        "one",
		Provider:    agent.ProviderClaude,
		ProcessLive: true,
		Activity:    agent.ActivityIdle,
		Attention:   agent.AttentionWaiting,
		Workspace:   ws,
	}

	for _, testCase := range []struct {
		name   string
		mark   agent.Mark
		want   string
		absent string
	}{
		{
			name:   "unmarked keeps the derived reading",
			want:   "Unseen result",
			absent: "marked",
		},
		{
			name:   "in progress stands the unseen result down",
			mark:   agent.MarkWorking,
			want:   "marked in progress",
			absent: "Unseen result",
		},
		{
			name:   "needs attention says whose flag it is",
			mark:   agent.MarkAttention,
			want:   "Marked needs attention",
			absent: "Unseen result",
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			marked := base
			marked.Mark = testCase.mark
			model.agents = []agent.Agent{marked}
			model.rebuildGroups(ws.ID, marked.ID)
			model.interactionID = marked.ID
			// The meta words live in the window bar, which the layout
			// mounts as the Spanreed's title row; the interaction body
			// holds the grid. Together they are the Spanreed surface.
			rendered := ansi.Strip(model.renderTerminalBar(marked, 80) +
				"\n" + model.renderInteraction(80, 20))
			if !strings.Contains(rendered, testCase.want) {
				t.Fatalf("heading hides %q:\n%s", testCase.want, rendered)
			}
			if strings.Contains(rendered, testCase.absent) {
				t.Fatalf("heading still claims %q:\n%s", testCase.absent, rendered)
			}
		})
	}
}

type historyBackend struct {
	stubBackend
	records   []history.Record
	resumedID string
}

func (b *historyBackend) SessionHistory(
	context.Context,
) ([]history.Record, error) {
	return b.records, nil
}

func (b *historyBackend) Resume(
	_ context.Context,
	record history.Record,
) (agent.Agent, error) {
	b.resumedID = record.SessionID
	return agent.Agent{Name: record.Name, Task: record.Task}, nil
}

func TestHistoryFlowBrowsesAndResumes(t *testing.T) {
	backend := &historyBackend{records: []history.Record{
		{SessionID: "session-new", Provider: agent.ProviderClaude,
			Name: "cl-newer", Task: "newer work"},
		{SessionID: "session-old", Provider: agent.ProviderCodex,
			Name: "cx-older", Task: "older work"},
	}}
	model := flowModelFixture(t, backend)

	updated, cmd := model.updateNormal(tea.KeyPressMsg{Code: 'H', Text: "H"})
	model = updated.(Model)
	if model.mode != modeHistory || cmd == nil {
		t.Fatalf("H did not open history: mode=%d", model.mode)
	}
	// The load command's message carries the records into the model.
	next, _ := model.Update(cmd())
	model = next.(Model)
	if len(model.historyRecords) != 2 || model.historyLoading {
		t.Fatalf("history not loaded: %#v", model.historyRecords)
	}

	rendered := ansi.Strip(model.renderHistoryModal(100, 28))
	for _, want := range []string{"cl-newer", "cx-older", "codex"} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("history modal hides %q:\n%s", want, rendered)
		}
	}

	next, _ = model.updateHistory(tea.KeyPressMsg{Code: 'j', Text: "j"})
	model = next.(Model)
	next, resumeCmd := model.updateHistory(tea.KeyPressMsg{Code: tea.KeyEnter})
	model = next.(Model)
	if model.mode != modeNormal || resumeCmd == nil {
		t.Fatalf("enter did not leave the modal resuming: mode=%d", model.mode)
	}
	drainCmd(resumeCmd)
	if backend.resumedID != "session-old" {
		t.Fatalf("resumed %q, want the cursor's session", backend.resumedID)
	}

	// Esc closes without resuming.
	updated, _ = model.updateNormal(tea.KeyPressMsg{Code: 'H', Text: "H"})
	model = updated.(Model)
	next, _ = model.updateHistory(tea.KeyPressMsg{Code: tea.KeyEscape})
	model = next.(Model)
	if model.mode != modeNormal {
		t.Fatalf("esc left mode=%d", model.mode)
	}
}

func TestHistoryFilterNarrowsAndResumesTheMatch(t *testing.T) {
	backend := &historyBackend{records: []history.Record{
		{SessionID: "session-parser", Provider: agent.ProviderClaude,
			Name: "cl-parser", Task: "fix the parser"},
		{SessionID: "session-docs", Provider: agent.ProviderClaude,
			Name: "cl-docs", Task: "write the docs"},
		{SessionID: "session-tests", Provider: agent.ProviderCodex,
			Name: "cx-tests", Task: "extend parser tests"},
	}}
	model := flowModelFixture(t, backend)

	updated, cmd := model.updateNormal(tea.KeyPressMsg{Code: 'H', Text: "H"})
	model = updated.(Model)
	next, _ := model.Update(cmd())
	model = next.(Model)

	// / opens the filter; typed terms match anywhere in the record, and
	// every term must match.
	next, _ = model.updateHistory(tea.KeyPressMsg{Code: '/', Text: "/"})
	model = next.(Model)
	if !model.historyFiltering {
		t.Fatal("/ did not start filtering")
	}
	for _, key := range []rune("parser") {
		next, _ = model.updateHistory(tea.KeyPressMsg{Code: key, Text: string(key)})
		model = next.(Model)
	}
	if visible := model.visibleHistory(); len(visible) != 2 {
		t.Fatalf("filter kept %#v", visible)
	}
	rendered := ansi.Strip(model.renderHistoryModal(100, 28))
	if !strings.Contains(rendered, "cl-parser") ||
		strings.Contains(rendered, "cl-docs") {
		t.Fatalf("filtered modal shows the wrong rows:\n%s", rendered)
	}

	// Arrows move within the narrowed list, and Enter resumes the match
	// the cursor is on — the pathnav contract.
	next, _ = model.updateHistory(tea.KeyPressMsg{Code: tea.KeyDown})
	model = next.(Model)
	next, resumeCmd := model.updateHistory(tea.KeyPressMsg{Code: tea.KeyEnter})
	model = next.(Model)
	if model.mode != modeNormal || resumeCmd == nil {
		t.Fatalf("enter did not resume from the filter: mode=%d", model.mode)
	}
	drainCmd(resumeCmd)
	if backend.resumedID != "session-tests" {
		t.Fatalf("resumed %q, want the filtered cursor's session", backend.resumedID)
	}
}

func TestHistoryFilterEscClearsBeforeClosing(t *testing.T) {
	backend := &historyBackend{records: []history.Record{
		{SessionID: "session-a", Provider: agent.ProviderClaude, Name: "cl-a"},
	}}
	model := flowModelFixture(t, backend)
	updated, cmd := model.updateNormal(tea.KeyPressMsg{Code: 'H', Text: "H"})
	model = updated.(Model)
	next, _ := model.Update(cmd())
	model = next.(Model)

	next, _ = model.updateHistory(tea.KeyPressMsg{Code: '/', Text: "/"})
	model = next.(Model)
	next, _ = model.updateHistory(tea.KeyPressMsg{Code: 'z', Text: "z"})
	model = next.(Model)

	// First esc abandons the filter but keeps the modal open; the second
	// closes it.
	next, _ = model.updateHistory(tea.KeyPressMsg{Code: tea.KeyEscape})
	model = next.(Model)
	if model.mode != modeHistory || model.historyFilterActive() {
		t.Fatalf("esc state: mode=%d filter=%q",
			model.mode, model.historyFilter.Value())
	}
	next, _ = model.updateHistory(tea.KeyPressMsg{Code: tea.KeyEscape})
	model = next.(Model)
	if model.mode != modeNormal {
		t.Fatalf("second esc left mode=%d", model.mode)
	}
}

// drainCmd executes a command tree far enough to run its side effects,
// following tea.Batch all the way down so a test sees what the batched
// commands actually did.
func drainCmd(cmd tea.Cmd) {
	if cmd == nil {
		return
	}
	if batch, ok := cmd().(tea.BatchMsg); ok {
		for _, item := range batch {
			drainCmd(item)
		}
	}
}

