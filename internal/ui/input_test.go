package ui

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/trentkm/stormlight/internal/agent"
	"github.com/trentkm/stormlight/internal/app"
	"github.com/trentkm/stormlight/internal/provider"
	"github.com/trentkm/stormlight/internal/surface"
	"github.com/trentkm/stormlight/internal/workspace"
)

func TestLineInputEditsAtCursor(t *testing.T) {
	input := newLineInput("")
	input.SetValue("ac")
	input.Focus()
	input = input.Update(tea.KeyMsg{Type: tea.KeyLeft})
	input = input.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("b")})

	if got := input.Value(); got != "abc" {
		t.Fatalf("got %q", got)
	}
}

func TestLineInputDeletesPreviousWord(t *testing.T) {
	input := newLineInput("")
	input.SetValue("hello brave world")
	input.Focus()
	input = input.Update(tea.KeyMsg{Type: tea.KeyCtrlW})

	if got := input.Value(); got != "hello brave " {
		t.Fatalf("got %q", got)
	}
}

func TestLineInputAcceptsSpaceKey(t *testing.T) {
	input := newLineInput("")
	input.SetValue("review")
	input.Focus()
	input = input.Update(tea.KeyMsg{Type: tea.KeySpace})
	input = input.Update(runeKey("this repo"))

	if got := input.Value(); got != "review this repo" {
		t.Fatalf("got %q", got)
	}
}

func TestLineInputShowsFocusedPlaceholderAndPreservesSpaces(t *testing.T) {
	input := newLineInput("Describe the task")
	input.Focus()
	if view := ansi.Strip(input.View()); !strings.Contains(view, "Describe the task") {
		t.Fatalf("focused placeholder is missing: %q", view)
	}

	input = input.Update(runeKey("review this repo"))
	if got := input.Value(); got != "review this repo" {
		t.Fatalf("input value = %q", got)
	}
}

func TestCleanInteractionCompactsBlankRuns(t *testing.T) {
	got := cleanInteraction("result\n\n\n\nPane is dead\n", 80, agent.Provider("custom"))
	want := "result\n\nPane is dead"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestCleanInteractionNormalizesTerminalFramesAndWraps(t *testing.T) {
	content := strings.Join([]string{
		"╭────────────────────────────────╮",
		"│ >_ OpenAI Codex                │",
		"│ directory: /a/very/long/path   │",
		"╰────────────────────────────────╯",
		"",
		"› Explain this codebase",
	}, "\n")

	got := cleanInteraction(content, 18, agent.Provider("custom"))
	plain := ansi.Strip(got)
	if strings.ContainsAny(plain, "╭╮╰╯│") {
		t.Fatalf("terminal frame was not normalized:\n%s", got)
	}
	for index, line := range strings.Split(plain, "\n") {
		if width := lipgloss.Width(line); width > 18 {
			t.Fatalf("line %d is %d columns wide: %q", index+1, width, line)
		}
	}
	if !strings.Contains(plain, "OpenAI Codex") || !strings.Contains(plain, "Explain this") {
		t.Fatalf("interaction content was lost:\n%s", got)
	}
}

func TestCleanInteractionPreservesColorAndFocusesConversation(t *testing.T) {
	content := strings.Join([]string{
		"\x1b[35m╭─ Claude Code ─╮\x1b[0m",
		"\x1b[35m│\x1b[0m Welcome back \x1b[35m│\x1b[0m",
		"\x1b[35m╰────────────────╯\x1b[0m",
		"Usage policy banner",
		"",
		"\x1b[48;5;237m\x1b[37m❯ hello\x1b[0m",
		"",
		"\x1b[90mThought for 2s\x1b[0m",
		"",
		"\x1b[36m⏺\x1b[0m Hello! How can I help?",
		"",
		"\x1b[36m──────────────── session ────────\x1b[0m",
		"\x1b[48;5;237m❯ \x1b[0m",
		"────────────────────────────────",
		"model status",
	}, "\n")

	got := cleanInteraction(content, 40, agent.ProviderClaude)
	plain := ansi.Strip(got)
	if !strings.Contains(got, "\x1b[") {
		t.Fatalf("ANSI styling was removed: %q", got)
	}
	for index, row := range strings.Split(got, "\n") {
		if left := activeSGR(row); left != "" {
			t.Fatalf("row %d leaves styling open (%q): %q", index, left, row)
		}
	}
	if strings.Contains(plain, "Welcome back") ||
		strings.Contains(plain, "Usage policy") ||
		strings.Contains(plain, "model status") {
		t.Fatalf("terminal chrome remained in interaction:\n%s", plain)
	}
	if !strings.Contains(plain, "❯ hello") ||
		!strings.Contains(plain, "Hello! How can I help?") {
		t.Fatalf("conversation content was lost:\n%s", plain)
	}
}

func TestCleanInteractionCarriesStyleAcrossWrappedRows(t *testing.T) {
	content := "\x1b[36m" + strings.Repeat("word ", 12) + "\x1b[0m"

	rows := strings.Split(cleanInteraction(content, 20, agent.Provider("custom")), "\n")
	if len(rows) < 3 {
		t.Fatalf("line did not wrap into enough rows: %q", rows)
	}
	for index, row := range rows {
		if !strings.HasPrefix(row, "\x1b[36m") {
			t.Fatalf("row %d lost the color it continues: %q", index, row)
		}
		if left := activeSGR(row); left != "" {
			t.Fatalf("row %d leaves styling open (%q): %q", index, left, row)
		}
	}
}

func TestCleanInteractionKeepsIndentationWhenWrapping(t *testing.T) {
	line := "    ⎿ " + strings.Repeat("word ", 12)
	got := cleanInteraction(line, 30, agent.Provider("custom"))
	rows := strings.Split(ansi.Strip(got), "\n")
	if len(rows) < 2 {
		t.Fatalf("line did not wrap: %q", got)
	}
	for index, row := range rows {
		if !strings.HasPrefix(row, "    ") {
			t.Fatalf("row %d lost the hanging indent: %q", index, row)
		}
		if width := lipgloss.Width(row); width > 30 {
			t.Fatalf("row %d is %d columns wide: %q", index, width, row)
		}
	}
}

func TestCleanInteractionKeepsIndentationInsideFrames(t *testing.T) {
	content := strings.Join([]string{
		"╭──────────────────────────╮",
		"│ plain line               │",
		"│    indented code         │",
		"╰──────────────────────────╯",
	}, "\n")
	got := ansi.Strip(cleanInteraction(content, 60, agent.Provider("custom")))
	if !strings.Contains(got, "\n   indented code") {
		t.Fatalf("frame interior lost its indentation:\n%s", got)
	}
	if strings.Contains(got, " plain line") {
		t.Fatalf("frame padding space survived:\n%s", got)
	}
}

func TestCleanInteractionDropsNonStyleEscapeSequences(t *testing.T) {
	content := "\x1b]0;unexpected title\x07\x1b[31mresult\x1b[0m"
	got := cleanInteraction(content, 80, agent.Provider("custom"))
	if strings.Contains(got, "]0;") || !strings.Contains(got, "\x1b[31m") {
		t.Fatalf("unexpected sanitized output: %q", got)
	}
}

func TestCleanInteractionRemovesPopulatedCodexComposer(t *testing.T) {
	content := strings.Join([]string{
		"╭─ OpenAI Codex ─╮",
		"Tip: startup content",
		"",
		"\x1b[1m› \x1b[0mExplain this codebase",
		"",
		"\x1b[2m• \x1b[0mHere is the explanation.",
		"",
		"\x1b[1m›\x1b[0m unsent draft",
		"",
		"model-name · /tmp/project",
		"",
		"",
	}, "\n")

	plain := ansi.Strip(cleanInteraction(content, 80, agent.ProviderCodex))
	if strings.Contains(plain, "startup") ||
		strings.Contains(plain, "unsent draft") ||
		strings.Contains(plain, "model-name") {
		t.Fatalf("Codex chrome remained:\n%s", plain)
	}
	if !strings.Contains(plain, "Explain this codebase") ||
		!strings.Contains(plain, "Here is the explanation") {
		t.Fatalf("Codex conversation was lost:\n%s", plain)
	}
}

func TestViewFitsEightyColumnTmuxPane(t *testing.T) {
	model := NewModel(stubBackend{})
	updated, _ := model.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	model = updated.(Model)
	model.agents = []agent.Agent{{
		ID:        "12345678",
		Provider:  agent.ProviderCodex,
		Name:      "cx-layout-check",
		Task:      "printf layout-check",
		Cwd:       "/tmp/project",
		CreatedAt: time.Now(),
		Activity:  agent.ActivityWorking,
		Attention: agent.AttentionApproval,
		Workspace: workspace.Context{
			ID:            "git:/tmp/project/.git",
			Kind:          workspace.KindGit,
			Name:          "long-project-name",
			Root:          "/tmp/project",
			ExecutionRoot: "/tmp/project",
			ComponentName: "long-feature-worktree",
			ComponentRoot: "/tmp/project",
		},
	}}
	model.rebuildGroups("git:/tmp/project/.git", "12345678")
	model.interactionID = "12345678"
	model.interaction.SetContent("layout-check\n\nPane is dead")

	assertViewFitsPane(t, model, 79, 23)
}

func TestDispatchViewFitsEightyColumnTmuxPane(t *testing.T) {
	backend := &recordingBackend{
		providers: []provider.Info{
			{ID: agent.ProviderCodex, Label: "Codex", Available: true},
			{ID: agent.ProviderClaude, Label: "Claude", Available: true},
		},
	}
	model := NewModel(backend)
	model.yaziPath = "/opt/homebrew/bin/yazi"
	model.prepareDirectoryChoices(model.initialCwd)
	updated, _ := model.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	model = updated.(Model)
	model.mode = modeDispatch
	model.chooseDispatchDirectory = true

	view := ansi.Strip(model.View())
	if !strings.Contains(view, "Codex") ||
		!strings.Contains(view, "Claude") {
		t.Fatalf("provider selector is incomplete:\n%s", view)
	}
	if !strings.Contains(view, "Working directory") ||
		!strings.Contains(view, "Browse with Yazi") ||
		!strings.Contains(view, "Enter a path") {
		t.Fatalf("directory selector is incomplete:\n%s", view)
	}
	assertViewFitsPane(t, model, 79, 23)
}

func TestDispatchCustomPathFitsShortPane(t *testing.T) {
	for _, size := range []struct {
		width  int
		height int
	}{
		{width: 80, height: 16},
		{width: 40, height: 16},
	} {
		model := NewModel(stubBackend{})
		model.yaziPath = "/opt/homebrew/bin/yazi"
		model.prepareDirectoryChoices(model.initialCwd)
		for index, choice := range model.directories {
			if choice.kind == directoryCustom {
				model.directoryIndex = index
				break
			}
		}
		model.mode = modeDispatch
		model.chooseDispatchDirectory = true
		model.formFocus = dispatchCustomPath
		updated, _ := model.Update(tea.WindowSizeMsg{
			Width:  size.width,
			Height: size.height,
		})
		model = updated.(Model)

		assertViewFitsPane(t, model, size.width-1, size.height-1)
	}
}

func TestDispatchViewFitsCompactPaneWithUnavailableProviders(t *testing.T) {
	backend := &recordingBackend{
		providers: []provider.Info{
			{ID: agent.ProviderCodex, Label: "Codex"},
			{ID: agent.ProviderClaude, Label: "Claude"},
		},
	}
	model := NewModel(backend)
	model.mode = modeDispatch
	updated, _ := model.Update(tea.WindowSizeMsg{Width: 40, Height: 16})
	model = updated.(Model)

	view := ansi.Strip(model.View())
	if !strings.Contains(view, "Codex") ||
		!strings.Contains(view, "Claude") ||
		!strings.Contains(view, "not found") {
		t.Fatalf("provider availability is unclear:\n%s", view)
	}
	if !strings.Contains(view, "Coding agent") ||
		!strings.Contains(view, "Task") ||
		strings.Contains(view, "Working directory") {
		t.Fatalf("contextual new-agent form is incorrect:\n%s", view)
	}
	assertViewFitsPane(t, model, 39, 15)
}

func TestNewAgentModalPreservesDashboardContext(t *testing.T) {
	workspaceContext := workspace.DirectoryContext("/workspace/repo")
	model := NewModel(stubBackend{})
	model.width = 120
	model.height = 30
	model.catalogWorkspaces = []workspace.Context{workspaceContext}
	model.agents = []agent.Agent{{
		ID:        "agent-one",
		Name:      "agent-one",
		Workspace: workspaceContext,
	}}
	model.rebuildGroups(workspaceContext.ID, "agent-one")
	model.activePane = paneAgents
	updated, _ := model.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
	model = updated.(Model)

	updated, _ = model.beginDispatch(false)
	model = updated.(Model)
	view := ansi.Strip(model.View())
	for _, label := range []string{
		"Workspaces",
		"Agents",
		"Spanreed",
		"New agent",
		"Coding agent",
		"Task",
	} {
		if !strings.Contains(view, label) {
			t.Fatalf("modal view is missing %q:\n%s", label, view)
		}
	}
	assertViewFitsPane(t, model, 119, 29)
}

func TestModalRendersCompleteBorder(t *testing.T) {
	rendered := ansi.Strip(renderModal("content", 32, 8))
	lines := strings.Split(rendered, "\n")
	if len(lines) != 8 {
		t.Fatalf("modal height = %d, want 8:\n%s", len(lines), rendered)
	}
	if !strings.HasPrefix(lines[0], "┌") ||
		!strings.HasSuffix(lines[0], "┐") ||
		!strings.HasPrefix(lines[len(lines)-1], "└") ||
		!strings.HasSuffix(lines[len(lines)-1], "┘") {
		t.Fatalf("modal border is incomplete:\n%s", rendered)
	}
	for index, line := range lines {
		if width := lipgloss.Width(line); width != 32 {
			t.Fatalf("modal line %d width = %d, want 32", index+1, width)
		}
	}
}

func TestAddWorkspaceOnlyOffersNewDirectoryActions(t *testing.T) {
	active := workspace.DirectoryContext(t.TempDir())
	model := NewModel(stubBackend{})
	model.yaziPath = "/opt/homebrew/bin/yazi"
	model.catalogWorkspaces = []workspace.Context{active}
	model.rebuildGroups(active.ID, "")
	updated, _ := model.Update(tea.WindowSizeMsg{Width: 100, Height: 28})
	model = updated.(Model)

	updated, _ = model.beginAddWorkspace()
	model = updated.(Model)
	if len(model.directories) != 2 ||
		model.directories[0].kind != directoryYazi ||
		model.directories[1].kind != directoryCustom {
		t.Fatalf("add-workspace choices = %#v", model.directories)
	}
	for _, choice := range model.directories {
		if choice.kind == directoryPath {
			t.Fatalf("active directory is selectable: %#v", choice)
		}
	}
	view := ansi.Strip(model.View())
	if !strings.Contains(view, "Add workspace") ||
		!strings.Contains(view, "Browse with Yazi") ||
		!strings.Contains(view, "Enter a path") ||
		!strings.Contains(view, "Active workspaces") ||
		!strings.Contains(view, "read only") ||
		!strings.Contains(view, active.Name) {
		t.Fatalf("add-workspace modal lacks context:\n%s", view)
	}
	assertViewFitsPane(t, model, 99, 27)
}

func TestFooterKeepsCommandsVisibleWithStatus(t *testing.T) {
	model := NewModel(stubBackend{})
	model.width = 100
	model.status = "Workspace added"

	footer := ansi.Strip(model.renderFooter())
	if !strings.Contains(footer, "Workspace added") ||
		!strings.Contains(footer, "j/k select") ||
		!strings.Contains(footer, "n add") {
		t.Fatalf("status displaced contextual commands: %q", footer)
	}
}

func TestFooterShowsOnlyChordOptionsWhilePending(t *testing.T) {
	model := NewModel(stubBackend{})
	model.width = 100

	updated, _ := model.updateNormal(runeKey(","))
	model = updated.(Model)
	footer := ansi.Strip(model.renderFooter())
	if !strings.Contains(footer, "Sort:") ||
		!strings.Contains(footer, "a attention") ||
		!strings.Contains(footer, "n name") ||
		!strings.Contains(footer, "c newest") {
		t.Fatalf("pending sort chord lacks its options: %q", footer)
	}
	if strings.Contains(footer, "j/k") || strings.Contains(footer, "q quit") {
		t.Fatalf("normal hints shown while chord is pending: %q", footer)
	}

	updated, _ = model.updateNormal(runeKey("n"))
	model = updated.(Model)
	footer = ansi.Strip(model.renderFooter())
	if !strings.Contains(footer, "Sorted by name") ||
		!strings.Contains(footer, "j/k select") {
		t.Fatalf("resolved chord did not restore normal footer: %q", footer)
	}

	updated, _ = model.updateNormal(runeKey("g"))
	model = updated.(Model)
	footer = ansi.Strip(model.renderFooter())
	if !strings.Contains(footer, "Go:") || !strings.Contains(footer, "g top") {
		t.Fatalf("pending g chord lacks its options: %q", footer)
	}
	if strings.Contains(footer, "j/k") {
		t.Fatalf("normal hints shown while g chord is pending: %q", footer)
	}

	updated, _ = model.updateNormal(tea.KeyMsg{Type: tea.KeyEsc})
	model = updated.(Model)
	footer = ansi.Strip(model.renderFooter())
	if strings.Contains(footer, "Go:") || !strings.Contains(footer, "j/k select") {
		t.Fatalf("cancelled chord did not restore normal footer: %q", footer)
	}
}

func TestWideDashboardRendersThreePaneHierarchy(t *testing.T) {
	workspaceContext := workspace.DirectoryContext("/workspace")
	model := NewModel(stubBackend{})
	model.catalogWorkspaces = []workspace.Context{workspaceContext}
	model.agents = []agent.Agent{{
		ID:        "one",
		Name:      "one",
		Workspace: workspaceContext,
	}}
	model.rebuildGroups(workspaceContext.ID, "one")
	model.interactionID = "one"
	updated, _ := model.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
	model = updated.(Model)
	model.interaction.SetContent("agent response")

	view := ansi.Strip(model.View())
	for _, label := range []string{"Workspaces", "Agents", "Spanreed", "agent response"} {
		if !strings.Contains(view, label) {
			t.Fatalf("dashboard is missing %q:\n%s", label, view)
		}
	}
	assertViewFitsPane(t, model, 119, 29)
}

func TestWideDashboardRendersPhysicalPaneDividers(t *testing.T) {
	model := NewModel(stubBackend{})
	model.width = 120
	model.height = 30

	body := ansi.Strip(model.renderBody())
	for _, line := range strings.Split(body, "\n") {
		if strings.Contains(line, "Workspaces") &&
			strings.Contains(line, "Agents") &&
			strings.Contains(line, "Spanreed") {
			if strings.Count(line, "│") < 2 {
				t.Fatalf("pane headers have no physical dividers: %q", line)
			}
			return
		}
	}
	t.Fatalf("pane header row not found:\n%s", body)
}

func TestAgentPaneHeaderShowsSelectedWorkspace(t *testing.T) {
	workspaceContext := workspace.DirectoryContext("/workspace/meshclaw")
	model := NewModel(stubBackend{})
	model.width = 120
	model.height = 30
	model.activePane = paneAgents
	model.catalogWorkspaces = []workspace.Context{workspaceContext}
	model.rebuildGroups(workspaceContext.ID, "")

	header := strings.Split(ansi.Strip(model.renderBody()), "\n")[0]
	if !strings.Contains(header, "Agents") ||
		!strings.Contains(header, "meshclaw") ||
		strings.Index(header, "meshclaw") < strings.Index(header, "Agents") {
		t.Fatalf("agent header lacks right-aligned workspace context: %q", header)
	}

	rendered := ansi.Strip(renderPaneHeader("Agents", "a-very-long-workspace-name", 24, true))
	if width := lipgloss.Width(rendered); width != 24 {
		t.Fatalf("contextual header width = %d, want 24: %q", width, rendered)
	}
}

func TestPaneHeaderKeepsLabelAlignmentAcrossFocusStates(t *testing.T) {
	active := ansi.Strip(renderPaneHeader("Workspaces", "", 24, true))
	inactive := ansi.Strip(renderPaneHeader("Workspaces", "", 24, false))
	if !strings.HasPrefix(active, "Workspaces") ||
		!strings.HasPrefix(inactive, "Workspaces") {
		t.Fatalf("labels shifted between focus states:\nactive:   %q\ninactive: %q",
			active, inactive)
	}
	if lipgloss.Width(active) != 24 || lipgloss.Width(inactive) != 24 {
		t.Fatalf("header width changed with focus: %d vs %d",
			lipgloss.Width(active), lipgloss.Width(inactive))
	}
}

func TestRowDensityTogglesWithoutHidingPanes(t *testing.T) {
	model := NewModel(stubBackend{})
	model.width = 120
	model.height = 30
	if model.rowsExpanded {
		t.Fatal("rows are expanded by default")
	}

	updated, _ := model.updateNormal(runeKey("z"))
	model = updated.(Model)
	if !model.rowsExpanded {
		t.Fatal("z did not expand rows")
	}
	body := ansi.Strip(model.renderBody())
	header := strings.Split(body, "\n")[0]
	if !strings.Contains(header, "Workspaces") ||
		!strings.Contains(header, "Agents") ||
		!strings.Contains(header, "Spanreed") {
		t.Fatalf("density toggle hid dashboard panes: %q", header)
	}
	if !strings.Contains(model.commandHints(), "z compact rows") {
		t.Fatalf("expanded hint = %q", model.commandHints())
	}

	updated, _ = model.updateNormal(runeKey("z"))
	model = updated.(Model)
	if model.rowsExpanded {
		t.Fatal("second z did not compact rows")
	}
}

func TestNarrowLayoutHonorsRowDensityAndShowsNavigationCue(t *testing.T) {
	model := NewModel(stubBackend{})
	model.width = 40
	model.height = 20
	model.rowsExpanded = false

	if model.expandedRows() {
		t.Fatal("narrow layout overrode the compact preference")
	}
	updated, _ := model.updateNormal(runeKey("z"))
	model = updated.(Model)
	if !model.rowsExpanded {
		t.Fatal("narrow-layout z did not toggle row density")
	}
	updated, _ = model.updateNormal(runeKey("z"))
	model = updated.(Model)
	header := strings.Split(ansi.Strip(model.renderBody()), "\n")[0]
	if !strings.Contains(header, "Workspaces") ||
		!strings.Contains(header, "Agents ›") {
		t.Fatalf("narrow workspace header lacks navigation cue: %q", header)
	}
}

func TestRowDensityChangesListHeight(t *testing.T) {
	workspaceContext := workspace.DirectoryContext("/workspace")
	model := NewModel(stubBackend{})
	model.width = 120
	model.activePane = paneAgents
	model.agents = []agent.Agent{
		{ID: "one", Name: "one", Workspace: workspaceContext},
		{ID: "two", Name: "two", Workspace: workspaceContext},
	}
	model.rebuildGroups(workspaceContext.ID, "one")

	model.rowsExpanded = true
	expanded := ansi.Strip(model.renderAgents(40, 12))
	model.rowsExpanded = false
	compact := ansi.Strip(model.renderAgents(40, 12))
	if strings.Count(expanded, "\n") <= strings.Count(compact, "\n") {
		t.Fatalf(
			"expanded rows are not taller:\nexpanded:\n%s\ncompact:\n%s",
			expanded,
			compact,
		)
	}
	if strings.Contains(compact, "directory") {
		t.Fatalf("compact rows contain secondary details:\n%s", compact)
	}
}

func assertViewFitsPane(t *testing.T, model Model, maxWidth, maxLines int) {
	t.Helper()
	lines := strings.Split(ansi.Strip(model.View()), "\n")
	if len(lines) > maxLines {
		t.Fatalf("rendered %d lines; maximum is %d", len(lines), maxLines)
	}
	for index, line := range lines {
		if width := lipgloss.Width(line); width > maxWidth {
			t.Fatalf("line %d is %d columns wide: %q", index+1, width, line)
		}
	}
}

func TestProviderSelectorDispatchesHighlightedProvider(t *testing.T) {
	backend := &recordingBackend{
		providers: []provider.Info{
			{ID: agent.ProviderCodex, Label: "Codex", Available: true},
			{ID: agent.ProviderClaude, Label: "Claude", Available: true},
		},
	}
	model := NewModel(backend)
	model.mode = modeDispatch

	updated, _ := model.updateDispatch(runeKey("j"))
	model = updated.(Model)
	if model.providerIndex != 1 {
		t.Fatalf("provider index after j = %d, want 1", model.providerIndex)
	}

	updated, _ = model.updateDispatch(runeKey("k"))
	model = updated.(Model)
	if model.providerIndex != 0 {
		t.Fatalf("provider index after k = %d, want 0", model.providerIndex)
	}
	selector := ansi.Strip(strings.Join(model.renderProviderRows(56), "\n"))
	if !strings.Contains(selector, "Codex") ||
		!strings.Contains(selector, "Claude") {
		t.Fatalf("unexpected selector %q", selector)
	}

	model.formFocus = dispatchTask
	model.taskInput.SetValue("check provider routing")
	updated, cmd := model.updateDispatch(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(Model)
	if cmd == nil {
		t.Fatal("dispatch command was not created")
	}
	result := cmd()
	if action, ok := result.(actionMsg); !ok || action.err != nil {
		t.Fatalf("unexpected dispatch result: %#v", result)
	}
	if backend.request.Provider != agent.ProviderCodex {
		t.Fatalf("dispatched provider = %q, want codex", backend.request.Provider)
	}
	if model.status != "Dispatching Codex" {
		t.Fatalf("status = %q", model.status)
	}
}

func TestTaskComposerWrapsLongPrompts(t *testing.T) {
	model := NewModel(&recordingBackend{
		providers: []provider.Info{
			{ID: agent.ProviderCodex, Label: "Codex", Available: true},
		},
	})
	model.formFocus = dispatchTask
	model.focusForm()
	model.taskInput.SetValue(
		"Review the workspace architecture and identify any unsafe " +
			"runtime boundaries before making changes. final-marker",
	)

	rendered := ansi.Strip(model.renderTaskComposer(44, 4))
	if !strings.Contains(rendered, "Review the workspace") ||
		!strings.Contains(rendered, "final-marker") ||
		strings.Contains(rendered, "…") {
		t.Fatalf("task composer truncated the prompt:\n%s", rendered)
	}
	lines := strings.Split(rendered, "\n")
	if len(lines) != 6 {
		t.Fatalf("task composer height = %d, want 6:\n%s", len(lines), rendered)
	}
	for index, line := range lines {
		if width := lipgloss.Width(line); width != 44 {
			t.Fatalf("line %d width = %d, want 44: %q", index+1, width, line)
		}
	}
}

func TestTaskEditorDelegatesPresentationToSurface(t *testing.T) {
	start := t.TempDir()
	current := &recordingSurface{popups: true}
	command, err := taskEditorCmd(
		current,
		"/usr/local/bin/nvim",
		start,
		"Review this workspace",
	)
	if err != nil {
		t.Fatal(err)
	}

	if current.request.Command.Path != "/usr/local/bin/nvim" ||
		current.request.Command.Dir != start ||
		len(current.request.Command.Args) != 1 ||
		!strings.HasSuffix(current.request.Command.Args[0], ".md") {
		t.Fatalf("editor command request = %#v", current.request.Command)
	}
	if current.request.Popup == nil ||
		current.request.Popup.Width != "82%" ||
		current.request.Popup.Height != "76%" ||
		!strings.Contains(current.request.Popup.Title, "Edit task") {
		t.Fatalf("editor popup request = %#v", current.request.Popup)
	}

	message := command()
	result, ok := message.(taskEditedMsg)
	if !ok || result.err != nil || result.task != "Review this workspace" {
		t.Fatalf("editor result = %#v", message)
	}
}

func TestNewAgentUsesSelectedWorkspaceWithoutDirectoryStep(t *testing.T) {
	current := t.TempDir()
	root := t.TempDir()
	executionRoot := filepath.Join(root, "feature")
	componentRoot := filepath.Join(executionRoot, "package")
	if err := os.MkdirAll(componentRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	workspaceContext := workspace.Context{
		ID:            "git:" + root,
		Kind:          workspace.KindGit,
		Name:          "repo",
		Root:          root,
		ExecutionRoot: executionRoot,
		ComponentName: "package",
		ComponentRoot: componentRoot,
	}

	backend := &recordingBackend{
		providers: []provider.Info{
			{ID: agent.ProviderCodex, Label: "Codex", Available: true},
			{ID: agent.ProviderClaude, Label: "Claude", Available: true},
		},
	}
	model := NewModel(backend)
	model.initialCwd = current
	model.yaziPath = "/opt/homebrew/bin/yazi"
	model.catalogWorkspaces = []workspace.Context{workspaceContext}
	model.agents = []agent.Agent{{
		ID:        "feature-agent",
		Name:      "feature-agent",
		Cwd:       componentRoot,
		Workspace: workspaceContext,
	}}
	model.rebuildGroups(workspaceContext.ID, "feature-agent")
	model.activePane = paneAgents

	updated, _ := model.updateNormal(runeKey("n"))
	model = updated.(Model)
	if model.mode != modeDispatch {
		t.Fatalf("mode = %v, want dispatch", model.mode)
	}
	if model.chooseDispatchDirectory {
		t.Fatal("contextual new agent unexpectedly enables directory selection")
	}
	if model.formFocus != dispatchProvider {
		t.Fatalf("focus = %v, want coding agent", model.formFocus)
	}
	if got := model.cwdInput.Value(); got != executionRoot {
		t.Fatalf("working directory = %q", got)
	}
	rendered := ansi.Strip(model.renderDispatch(80))
	if !strings.Contains(rendered, "Coding agent") ||
		!strings.Contains(rendered, "Task") ||
		strings.Contains(rendered, "Working directory") ||
		strings.Contains(rendered, "Browse with Yazi") {
		t.Fatalf("unexpected contextual new-agent form:\n%s", rendered)
	}

	updated, _ = model.updateDispatch(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(Model)
	if model.formFocus != dispatchTask {
		t.Fatalf("Enter from coding agent focused %v, want input", model.formFocus)
	}
	model.taskInput.SetValue("review this workspace")
	updated, cmd := model.updateDispatch(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(Model)
	if cmd == nil {
		t.Fatal("dispatch command was not created")
	}
	if result := cmd(); result.(actionMsg).err != nil {
		t.Fatalf("dispatch failed: %#v", result)
	}
	if backend.request.Cwd != executionRoot {
		t.Fatalf("dispatched cwd = %q, want %q", backend.request.Cwd, executionRoot)
	}
}

func TestDispatchLocationUsesVimNavigation(t *testing.T) {
	model := NewModel(stubBackend{})
	model.yaziPath = "/opt/homebrew/bin/yazi"
	model.prepareDirectoryChoices(model.initialCwd)
	model.mode = modeDispatch
	model.chooseDispatchDirectory = true
	model.formFocus = dispatchDirectory

	updated, _ := model.updateDispatch(runeKey("G"))
	model = updated.(Model)
	selected, _ := model.selectedDirectory()
	if selected.kind != directoryCustom {
		t.Fatalf("G selected %#v, want custom path", selected)
	}

	updated, _ = model.updateDispatch(runeKey("g"))
	model = updated.(Model)
	updated, _ = model.updateDispatch(runeKey("g"))
	model = updated.(Model)
	if model.directoryIndex != 0 {
		t.Fatalf("gg selected index %d", model.directoryIndex)
	}

	updated, _ = model.updateDispatch(runeKey("e"))
	model = updated.(Model)
	selected, _ = model.selectedDirectory()
	if selected.kind != directoryCustom ||
		model.formFocus != dispatchCustomPath {
		t.Fatalf("edit selection = %#v focus=%v", selected, model.formFocus)
	}
}

func TestDirectorySelectorUpdatesPathAndPickerResult(t *testing.T) {
	current := t.TempDir()
	selected := t.TempDir()
	model := NewModel(stubBackend{})
	model.initialCwd = current
	model.yaziPath = "/opt/homebrew/bin/yazi"
	model.prepareDirectoryChoices(current)
	model.mode = modeDispatch
	model.chooseDispatchDirectory = true
	model.formFocus = dispatchDirectory

	yaziIndex := -1
	for index, choice := range model.directories {
		if choice.kind == directoryYazi {
			yaziIndex = index
			break
		}
	}
	if yaziIndex < 0 {
		t.Fatal("Yazi choice is missing")
	}
	model.directoryIndex = yaziIndex
	updated, _ := model.Update(directoryPickedMsg{path: selected})
	model = updated.(Model)
	if model.cwdInput.Value() != selected || model.formFocus != dispatchTask {
		t.Fatalf("picker result: cwd=%q focus=%d", model.cwdInput.Value(), model.formFocus)
	}
	choice, ok := model.selectedDirectory()
	if !ok || choice.path != selected {
		t.Fatalf("picker choice = %#v", choice)
	}
}

func TestResolveYaziDirectory(t *testing.T) {
	cwd := t.TempDir()
	choice := t.TempDir()
	file := filepath.Join(choice, "selected.go")
	if err := os.WriteFile(file, []byte("package selected"), 0600); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name   string
		choice string
		cwd    string
		want   string
	}{
		{
			name:   "selected directory",
			choice: choice + "\n",
			cwd:    cwd,
			want:   choice,
		},
		{
			name:   "selected file uses parent",
			choice: file + "\n",
			cwd:    cwd,
			want:   choice,
		},
		{
			name: "quit uses current directory",
			cwd:  cwd + "\n",
			want: cwd,
		},
		{
			name: "cancel leaves directory unchanged",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := resolveYaziDirectory(
				[]byte(test.choice),
				[]byte(test.cwd),
			)
			if err != nil {
				t.Fatal(err)
			}
			if got != test.want {
				t.Fatalf("got %q, want %q", got, test.want)
			}
		})
	}
}

type recordingSurface struct {
	request surface.Request
	popups  bool
}

func (s *recordingSurface) Capabilities() surface.Capabilities {
	return surface.Capabilities{Popups: s.popups}
}

func (s *recordingSurface) Present(
	request surface.Request,
) (surface.Presentation, error) {
	s.request = request
	return surface.Presentation{
		Command: exec.Command("true"),
		Mode:    surface.PresentationOverlay,
	}, nil
}

func TestYaziPickerDelegatesPresentationToSurface(t *testing.T) {
	start := t.TempDir()
	current := &recordingSurface{popups: true}
	command, err := yaziPickerCmd(current, "/usr/local/bin/yazi", start)
	if err != nil {
		t.Fatal(err)
	}

	if current.request.Command.Path != "/usr/local/bin/yazi" ||
		current.request.Command.Dir != start {
		t.Fatalf("command request = %#v", current.request.Command)
	}
	if current.request.Popup == nil ||
		current.request.Popup.Width != "78%" ||
		current.request.Popup.Height != "76%" {
		t.Fatalf("popup request = %#v", current.request.Popup)
	}
	args := strings.Join(current.request.Command.Args, " ")
	if !strings.Contains(args, "--chooser-file") ||
		!strings.Contains(args, "--cwd-file") {
		t.Fatalf("Yazi handoff args = %#v", current.request.Command.Args)
	}

	message := command()
	result, ok := message.(directoryPickedMsg)
	if !ok || result.err != nil {
		t.Fatalf("picker result = %#v", message)
	}
}

func TestYaziPickerOmitsPopupForDirectSurface(t *testing.T) {
	current := &recordingSurface{}
	command, err := yaziPickerCmd(current, "/usr/local/bin/yazi", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if current.request.Popup != nil {
		t.Fatalf("unsupported popup was requested: %#v", current.request.Popup)
	}
	if result, ok := command().(directoryPickedMsg); !ok || result.err != nil {
		t.Fatalf("picker did not complete cleanly: %#v", result)
	}
}

func TestVimPaneNavigation(t *testing.T) {
	firstWorkspace := workspace.DirectoryContext("/workspace/one")
	secondWorkspace := workspace.DirectoryContext("/workspace/two")
	model := NewModel(stubBackend{})
	model.height = 24
	model.catalogWorkspaces = []workspace.Context{
		firstWorkspace,
		secondWorkspace,
	}
	model.agents = []agent.Agent{
		{ID: "one", Workspace: firstWorkspace},
		{ID: "two", Workspace: firstWorkspace},
		{ID: "three", Workspace: firstWorkspace},
	}
	model.rebuildGroups(secondWorkspace.ID, "")

	updated, _ := model.updateNormal(runeKey("g"))
	model = updated.(Model)
	if model.workspaceCursor != 1 {
		t.Fatalf("single g moved workspace cursor to %d", model.workspaceCursor)
	}
	updated, _ = model.updateNormal(runeKey("g"))
	model = updated.(Model)
	if model.workspaceCursor != 0 {
		t.Fatalf("gg moved workspace cursor to %d, want 0", model.workspaceCursor)
	}
	updated, _ = model.updateNormal(runeKey("G"))
	model = updated.(Model)
	if model.workspaceCursor != 1 {
		t.Fatalf("G moved workspace cursor to %d, want 1", model.workspaceCursor)
	}

	model.workspaceCursor = 0
	updated, _ = model.updateNormal(runeKey("l"))
	model = updated.(Model)
	if model.activePane != paneAgents {
		t.Fatalf("l selected pane %d, want agents", model.activePane)
	}
	updated, _ = model.updateNormal(runeKey("G"))
	model = updated.(Model)
	if model.agentCursor != 2 {
		t.Fatalf("G moved agent cursor to %d, want 2", model.agentCursor)
	}
	updated, _ = model.updateNormal(runeKey("g"))
	model = updated.(Model)
	updated, _ = model.updateNormal(runeKey("g"))
	model = updated.(Model)
	if model.agentCursor != 0 {
		t.Fatalf("gg moved agent cursor to %d, want 0", model.agentCursor)
	}
	updated, _ = model.updateNormal(runeKey("l"))
	model = updated.(Model)
	if model.activePane != paneInteraction {
		t.Fatalf("second l selected pane %d, want interaction", model.activePane)
	}
	updated, _ = model.updateNormal(runeKey("h"))
	model = updated.(Model)
	if model.activePane != paneAgents {
		t.Fatalf("h selected pane %d, want agents", model.activePane)
	}
}

func TestWorkspaceGroupingDrivesAgentPane(t *testing.T) {
	gitWorkspace := workspace.Context{
		ID:            "git:/repo/.git",
		Kind:          workspace.KindGit,
		Name:          "repo",
		Root:          "/repo",
		ExecutionRoot: "/repo",
	}
	otherWorkspace := workspace.Context{
		ID:            "directory:/other",
		Kind:          workspace.KindDirectory,
		Name:          "other",
		Root:          "/other",
		ExecutionRoot: "/other",
	}
	model := NewModel(stubBackend{})
	model.agents = []agent.Agent{
		{ID: "one", Name: "one", Workspace: gitWorkspace},
		{ID: "two", Name: "two", Workspace: gitWorkspace},
		{ID: "three", Name: "three", Workspace: otherWorkspace},
	}
	model.catalogWorkspaces = []workspace.Context{gitWorkspace, otherWorkspace}
	model.rebuildGroups(gitWorkspace.ID, "one")
	if len(model.groups) != 2 {
		t.Fatalf("workspace count = %d, want 2", len(model.groups))
	}
	if got := len(model.agentsForSelectedWorkspace()); got != 2 {
		t.Fatalf("selected workspace agent count = %d, want 2", got)
	}
	rendered := ansi.Strip(model.renderAgents(52, 20))
	if !strings.Contains(rendered, "one") ||
		!strings.Contains(rendered, "two") ||
		strings.Contains(rendered, "three") {
		t.Fatalf("agent pane does not match selected workspace:\n%s", rendered)
	}

	model.moveSelection(1)
	if selected, _ := model.selectedWorkspace(); selected.ID != otherWorkspace.ID {
		t.Fatalf("selected workspace = %#v", selected)
	}
	if got := len(model.agentsForSelectedWorkspace()); got != 1 {
		t.Fatalf("second workspace agent count = %d, want 1", got)
	}
}

func TestWorkspaceDetailPrioritizesPathInCompactPane(t *testing.T) {
	value := workspace.Context{
		Kind: "custom",
		Root: "/Volumes/repos/shared/alpha-service",
	}

	narrow := workspaceDetail(value, 24)
	if !strings.Contains(narrow, "custom") ||
		!strings.HasSuffix(narrow, "alpha-service") ||
		lipgloss.Width(narrow) > 24 {
		t.Fatalf("narrow detail lost its distinguishing tail: %q", narrow)
	}

	wide := workspaceDetail(value, 60)
	if wide != "custom · /Volumes/repos/shared/alpha-service" {
		t.Fatalf("wide detail = %q", wide)
	}

	named := workspace.Context{
		Kind: "git",
		Name: "alpha-service",
		Root: "/Volumes/repos/shared/alpha-service",
	}
	if detail := workspaceDetail(named, 60); detail != "git · /Volumes/repos/shared" {
		t.Fatalf("parent-only detail = %q", detail)
	}
	if got := abbreviatePath("/Volumes/repos/alpha-service"); got != "/V/r/alpha-service" {
		t.Fatalf("abbreviated path = %q", got)
	}

	worktree := workspace.Context{
		Kind:          "git",
		Name:          "alpha-service",
		Root:          "/Volumes/repos/shared/alpha-service",
		ExecutionRoot: "/Volumes/repos/shared/alpha-service-worktrees/fix-auth",
	}
	if detail := workspaceDetail(worktree, 60); !strings.HasSuffix(detail, " · fix-auth") {
		t.Fatalf("worktree detail lost its tail: %q", detail)
	}

	mainCheckout := workspace.Context{
		Kind:          "git",
		Name:          "alpha-service",
		Root:          "/Volumes/repos/shared/alpha-service",
		ExecutionRoot: "/Volumes/repos/shared/alpha-service",
	}
	if detail := workspaceDetail(mainCheckout, 60); detail != "git · /Volumes/repos/shared" {
		t.Fatalf("main-checkout detail = %q", detail)
	}
}

func TestFirstRefreshLandsOnRequestedWorkspace(t *testing.T) {
	first := workspace.DirectoryContext("/workspace/first")
	second := workspace.DirectoryContext("/workspace/second")
	model := NewModelWithOptions(stubBackend{}, nil, Options{
		SelectWorkspaceID: second.ID,
	})

	updated, _ := model.Update(dashboardMsg{
		workspaces: []workspace.Context{first, second},
	})
	model = updated.(Model)
	if selected, ok := model.selectedWorkspace(); !ok || selected.ID != second.ID {
		t.Fatalf("selected workspace = %#v", selected)
	}

	// The request is one-shot: later refreshes follow the user's cursor.
	model.moveSelection(-1)
	updated, _ = model.Update(dashboardMsg{
		workspaces: []workspace.Context{first, second},
	})
	model = updated.(Model)
	if selected, _ := model.selectedWorkspace(); selected.ID != first.ID {
		t.Fatalf("refresh stole the cursor: %#v", selected)
	}
}

func TestInfoModalCollapsesDegenerateRows(t *testing.T) {
	plain := workspace.Context{
		ID:            "git:/repo/.git",
		Kind:          workspace.KindGit,
		Name:          "repo",
		Root:          "/repo",
		ExecutionRoot: "/repo",
	}
	model := NewModel(stubBackend{})
	model.catalogWorkspaces = []workspace.Context{plain}
	model.rebuildGroups(plain.ID, "")

	rendered := ansi.Strip(model.renderInfoModal(80, 24))
	if strings.Contains(rendered, "Execution root") ||
		strings.Contains(rendered, "Component") {
		t.Fatalf("degenerate rows survived:\n%s", rendered)
	}

	worktree := plain
	worktree.ExecutionRoot = "/repo-worktrees/fix-auth"
	model.catalogWorkspaces = []workspace.Context{worktree}
	model.rebuildGroups(worktree.ID, "")

	rendered = ansi.Strip(model.renderInfoModal(80, 24))
	if !strings.Contains(rendered, "Execution root") {
		t.Fatalf("distinct execution root is missing:\n%s", rendered)
	}
}

func TestWorkspaceShimmerSweepsWithStableWidth(t *testing.T) {
	value := workspace.DirectoryContext("/workspace/project")
	model := NewModel(stubBackend{})
	model.width = 120
	model.shimmerRunning = true
	group := workspaceGroup{
		context: value,
		agents: []agent.Agent{{
			Activity: agent.ActivityWorking,
		}},
	}

	model.shimmerPhase = 4
	early := model.renderWorkspaceRow(group, true, true, 30, false)
	model.shimmerPhase = 7
	late := model.renderWorkspaceRow(group, true, true, 30, false)
	if !strings.Contains(ansi.Strip(early), "▌") {
		t.Fatalf("focus bar missing:\n%s", ansi.Strip(early))
	}
	quiet := ansi.Strip(model.renderWorkspaceRow(group, false, false, 30, false))
	if !strings.HasPrefix(quiet, "●") {
		t.Fatalf("active glyph missing on quiet row:\n%s", quiet)
	}
	// Colors are unavailable under the test color profile, so the sweep
	// itself is asserted via the band math.
	if shimmerBand(7, 4) == shimmerBand(7, 7) {
		t.Fatal("shimmer band did not move between phases")
	}
	if band := shimmerBand(7, -1); band > -4 {
		t.Fatalf("resting band %d still touches the text", band)
	}
	earlyLines := strings.Split(ansi.Strip(early), "\n")
	lateLines := strings.Split(ansi.Strip(late), "\n")
	if len(earlyLines) != len(lateLines) {
		t.Fatalf("shimmer changed row height: %d != %d", len(earlyLines), len(lateLines))
	}
	for index := range earlyLines {
		if lipgloss.Width(earlyLines[index]) != lipgloss.Width(lateLines[index]) {
			t.Fatalf("shimmer changed line %d width", index+1)
		}
	}
}

func TestHierarchyConnectorRowsFollowDensity(t *testing.T) {
	model := NewModel(stubBackend{})
	model.width = 120
	model.groups = []workspaceGroup{
		{context: workspace.DirectoryContext("/workspace/one")},
		{
			context: workspace.DirectoryContext("/workspace/two"),
			agents: []agent.Agent{
				{ID: "one"},
				{ID: "two"},
				{ID: "three"},
			},
		},
	}
	model.workspaceCursor = 1
	model.agentCursor = 2

	workspaceRow, agentRow, ok := model.hierarchyConnectorRows(20)
	if !ok || workspaceRow != 2 || agentRow != 3 {
		t.Fatalf(
			"compact connector rows = %d -> %d, ok=%v",
			workspaceRow,
			agentRow,
			ok,
		)
	}

	model.rowsExpanded = true
	workspaceRow, agentRow, ok = model.hierarchyConnectorRows(20)
	if !ok || workspaceRow != 4 || agentRow != 7 {
		t.Fatalf(
			"expanded connector rows = %d -> %d, ok=%v",
			workspaceRow,
			agentRow,
			ok,
		)
	}
}

func TestSortModesOrderAgentsExplicitly(t *testing.T) {
	older := time.Now().Add(-time.Hour)
	newer := time.Now()
	agents := []agent.Agent{
		{ID: "b", Name: "bravo", CreatedAt: newer},
		{ID: "a", Name: "alpha", CreatedAt: older, Attention: agent.AttentionApproval},
	}

	sortAgentList(agents, sortByCreated)
	if agents[0].ID != "b" {
		t.Fatalf("newest-first order = %s,%s", agents[0].ID, agents[1].ID)
	}
	sortAgentList(agents, sortByAttention)
	if agents[0].ID != "a" {
		t.Fatalf("attention-first order = %s,%s", agents[0].ID, agents[1].ID)
	}
	sortAgentList(agents, sortByName)
	if agents[0].ID != "a" {
		t.Fatalf("name order = %s,%s", agents[0].ID, agents[1].ID)
	}
}

func TestSortChordChangesMode(t *testing.T) {
	model := NewModel(stubBackend{})
	updated, _ := model.updateNormal(runeKey(","))
	model = updated.(Model)
	updated, _ = model.updateNormal(runeKey("a"))
	model = updated.(Model)
	if model.sortMode != sortByAttention {
		t.Fatalf("sort mode = %v, want attention", model.sortMode)
	}
	if !strings.Contains(model.status, "attention") {
		t.Fatalf("status = %q", model.status)
	}
}

func TestHierarchyConnectorBridgesDifferentRows(t *testing.T) {
	pane := strings.Join([]string{
		"header │",
		"one    │",
		"two    │",
		"three  │",
	}, "\n")
	rendered := ansi.Strip(paintHierarchyConnector(pane, 8, 1, 3))
	lines := strings.Split(rendered, "\n")
	if !strings.HasSuffix(lines[0], " │") ||
		!strings.HasSuffix(lines[1], "╮│") ||
		!strings.HasSuffix(lines[2], "││") ||
		!strings.HasSuffix(lines[3], "╰│") {
		t.Fatalf("hierarchy path is incomplete:\n%s", rendered)
	}
	for index, line := range lines {
		if width := lipgloss.Width(line); width != 8 {
			t.Fatalf("line %d width = %d, want 8: %q", index+1, width, line)
		}
	}
}

func TestHierarchyKeepsParentRowsSelected(t *testing.T) {
	workspaceContext := workspace.DirectoryContext("/workspace/project")
	model := NewModel(stubBackend{})
	model.width = 120
	model.activePane = paneInteraction
	model.catalogWorkspaces = []workspace.Context{workspaceContext}
	model.agents = []agent.Agent{{
		ID:        "agent-one",
		Name:      "agent-one",
		Workspace: workspaceContext,
	}}
	model.rebuildGroups(workspaceContext.ID, "agent-one")

	workspaces := ansi.Strip(model.renderWorkspaces(30, 12))
	agents := ansi.Strip(model.renderAgents(40, 12))
	if !strings.Contains(workspaces, "›") ||
		!strings.Contains(agents, "›") ||
		strings.Contains(workspaces, "▌") ||
		strings.Contains(agents, "▌") {
		t.Fatalf(
			"hierarchy selection is unclear:\nworkspaces:\n%s\nagents:\n%s",
			workspaces,
			agents,
		)
	}
}

func TestAgentRowsShowWorkspaceComponent(t *testing.T) {
	model := NewModel(stubBackend{})
	model.height = 24
	model.agents = []agent.Agent{{
		ID:       "one",
		Provider: agent.ProviderCodex,
		Name:     "feature-agent",
		Task:     "Fix the parser",
		Workspace: workspace.Context{
			ID:            "monorepo:/workspace",
			Kind:          "monorepo",
			Name:          "Example",
			Root:          "/workspace",
			ExecutionRoot: "/workspace",
			ComponentName: "ParserPackage",
			ComponentRoot: "/workspace/src/ParserPackage",
		},
	}}
	model.rebuildGroups("monorepo:/workspace", "one")
	model.rowsExpanded = true
	rendered := ansi.Strip(model.renderAgents(52, 20))
	if !strings.Contains(rendered, "ParserPackage") {
		t.Fatalf("workspace component missing from agent pane:\n%s", rendered)
	}
}

func TestListRowsHaveVisualSeparation(t *testing.T) {
	workspaceContext := workspace.DirectoryContext("/workspace")
	model := NewModel(stubBackend{})
	model.activePane = paneAgents
	model.agents = []agent.Agent{
		{ID: "one", Name: "one", Workspace: workspaceContext},
		{ID: "two", Name: "two", Workspace: workspaceContext},
	}
	model.rebuildGroups(workspaceContext.ID, "one")
	model.rowsExpanded = true

	rendered := ansi.Strip(model.renderAgents(52, 20))
	if !strings.Contains(rendered, "\n\n") {
		t.Fatalf("agent rows run together:\n%s", rendered)
	}
}

func TestFocusedAgentRowUsesTaskFirstTitleAndSelectionRail(t *testing.T) {
	rendered := ansi.Strip(renderAgentRow(agent.Agent{
		Provider: agent.ProviderCodex,
		Name:     "cx-fix-parser-behavior",
		Task:     "Fix parser behavior",
		Activity: agent.ActivityWorking,
		Workspace: workspace.Context{
			ComponentName: "parser-worktree",
		},
	}, true, true, 52))

	if !strings.Contains(rendered, "Fix parser behavior") ||
		!strings.Contains(rendered, "codex") ||
		strings.Contains(rendered, "cx-fix-parser") {
		t.Fatalf("agent row does not use task-first labeling:\n%s", rendered)
	}
	if strings.Count(rendered, "▌") != 2 || strings.Contains(rendered, ">") {
		t.Fatalf("agent selection rail is unclear:\n%s", rendered)
	}
	for index, line := range strings.Split(rendered, "\n") {
		if width := lipgloss.Width(line); width != 52 {
			t.Fatalf("line %d width = %d, want 52: %q", index+1, width, line)
		}
	}
}

func TestContextualNewAction(t *testing.T) {
	workspaceContext := workspace.DirectoryContext(t.TempDir())
	model := NewModel(stubBackend{})
	model.catalogWorkspaces = []workspace.Context{workspaceContext}
	model.rebuildGroups(workspaceContext.ID, "")

	updated, _ := model.updateNormal(runeKey("n"))
	workspacesModel := updated.(Model)
	if workspacesModel.mode != modeAddWorkspace {
		t.Fatalf("n in workspaces mode = %d, want add workspace", workspacesModel.mode)
	}

	model.activePane = paneAgents
	updated, _ = model.updateNormal(runeKey("n"))
	agentsModel := updated.(Model)
	if agentsModel.mode != modeDispatch {
		t.Fatalf("n in agents mode = %d, want dispatch", agentsModel.mode)
	}
	if agentsModel.chooseDispatchDirectory {
		t.Fatal("n in agents enabled directory selection")
	}

	model.activePane = paneInteraction
	updated, _ = model.updateNormal(runeKey("n"))
	interactionModel := updated.(Model)
	if interactionModel.mode != modeDispatch {
		t.Fatalf("n in interaction mode = %d, want dispatch", interactionModel.mode)
	}
	if interactionModel.chooseDispatchDirectory {
		t.Fatal("n in interaction enabled directory selection")
	}

	model.activePane = paneAgents
	updated, _ = model.updateNormal(runeKey("o"))
	advancedModel := updated.(Model)
	if advancedModel.mode != modeDispatch ||
		!advancedModel.chooseDispatchDirectory {
		t.Fatal("o did not open explicit directory selection")
	}

	emptyModel := NewModel(stubBackend{})
	emptyModel.activePane = paneAgents
	updated, _ = emptyModel.updateNormal(runeKey("n"))
	emptyModel = updated.(Model)
	if !emptyModel.chooseDispatchDirectory {
		t.Fatal("n without a workspace hid the required directory selection")
	}
}

func TestInteractionComposerSendsToSelectedAgent(t *testing.T) {
	backend := &recordingBackend{
		providers: []provider.Info{
			{ID: agent.ProviderCodex, Label: "Codex", Available: true},
		},
	}
	workspaceContext := workspace.DirectoryContext(t.TempDir())
	model := NewModel(backend)
	model.catalogWorkspaces = []workspace.Context{workspaceContext}
	model.agents = []agent.Agent{{
		ID:        "agent-one",
		Name:      "agent-one",
		Provider:  agent.ProviderCodex,
		Workspace: workspaceContext,
	}}
	model.rebuildGroups(workspaceContext.ID, "agent-one")
	model.activePane = paneInteraction

	updated, _ := model.updateNormal(runeKey("i"))
	model = updated.(Model)
	if model.mode != modeCompose {
		t.Fatalf("mode = %d, want compose", model.mode)
	}
	updated, _ = model.updateCompose(runeKey("please run the tests"))
	model = updated.(Model)
	updated, cmd := model.updateCompose(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(Model)
	if cmd == nil {
		t.Fatal("send command was not created")
	}
	result := cmd()
	action, ok := result.(actionMsg)
	if !ok || action.err != nil {
		t.Fatalf("unexpected send result: %#v", result)
	}
	if backend.sentID != "agent-one" ||
		backend.sentMessage != "please run the tests" {
		t.Fatalf("sent id=%q message=%q", backend.sentID, backend.sentMessage)
	}
}

func TestEnterOpensSelectedAgentTerminal(t *testing.T) {
	backend := &recordingBackend{}
	workspaceContext := workspace.DirectoryContext("/workspace")
	model := NewModel(backend)
	model.catalogWorkspaces = []workspace.Context{workspaceContext}
	model.agents = []agent.Agent{{
		ID:        "agent-one",
		Name:      "agent-one",
		Workspace: workspaceContext,
	}}
	model.rebuildGroups(workspaceContext.ID, "agent-one")
	model.activePane = paneAgents

	updated, cmd := model.updateNormal(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(Model)
	if cmd == nil || model.status != "Opening agent-one" {
		t.Fatalf("terminal open was not started: status=%q", model.status)
	}
	message := cmd()
	attached, ok := message.(attachMsg)
	if !ok || attached.err != nil {
		t.Fatalf("unexpected attach result: %#v", message)
	}
	if backend.attachedID != "agent-one" {
		t.Fatalf("attached agent = %q", backend.attachedID)
	}
}

func TestInteractionHidesPreviousAgentTranscript(t *testing.T) {
	workspaceContext := workspace.DirectoryContext("/workspace")
	model := NewModel(stubBackend{})
	model.catalogWorkspaces = []workspace.Context{workspaceContext}
	model.agents = []agent.Agent{
		{ID: "one", Name: "one", Workspace: workspaceContext},
		{ID: "two", Name: "two", Workspace: workspaceContext},
	}
	model.rebuildGroups(workspaceContext.ID, "two")
	model.interactionID = "one"
	model.interaction.SetContent("private output from one")

	rendered := ansi.Strip(model.renderInteraction(60, 20))
	if strings.Contains(rendered, "private output from one") ||
		!strings.Contains(rendered, "Loading interaction") {
		t.Fatalf("stale transcript is visible:\n%s", rendered)
	}
}

func TestInteractionRefreshPreservesScrollPosition(t *testing.T) {
	workspaceContext := workspace.DirectoryContext("/workspace")
	model := NewModel(stubBackend{})
	model.catalogWorkspaces = []workspace.Context{workspaceContext}
	model.agents = []agent.Agent{{
		ID:        "one",
		Name:      "one",
		Workspace: workspaceContext,
	}}
	model.rebuildGroups(workspaceContext.ID, "one")
	model.interaction = viewport.New(40, 2)
	model.interaction.SetContent("one\ntwo\nthree\nfour")
	model.interaction.GotoTop()
	model.interactionID = "one"

	updated, _ := model.Update(interactionMsg{
		id:      "one",
		content: "one\ntwo\nthree\nfour\nfive",
	})
	model = updated.(Model)
	if model.interaction.YOffset != 0 {
		t.Fatalf("refresh moved viewport to offset %d", model.interaction.YOffset)
	}

	model.interaction.GotoBottom()
	updated, _ = model.Update(interactionMsg{
		id:      "one",
		content: "one\ntwo\nthree\nfour\nfive\nsix",
	})
	model = updated.(Model)
	if !model.interaction.AtBottom() {
		t.Fatalf("viewport stopped following output at offset %d", model.interaction.YOffset)
	}
}

func TestAddWorkspaceCommandUpdatesDashboard(t *testing.T) {
	backend := &recordingBackend{}
	path := t.TempDir()
	model := NewModel(backend)

	updated, cmd := model.submitAddWorkspace(path)
	model = updated.(Model)
	if cmd == nil {
		t.Fatal("add workspace command was not created")
	}
	message := cmd()
	added, ok := message.(workspaceAddedMsg)
	if !ok || added.err != nil {
		t.Fatalf("unexpected add result: %#v", message)
	}
	updated, _ = model.Update(message)
	model = updated.(Model)
	if model.mode != modeNormal ||
		model.selectedWorkspaceID() != added.value.ID {
		t.Fatalf("workspace was not selected after add: %#v", model.groups)
	}
	if backend.addedPath != path {
		t.Fatalf("added path = %q, want %q", backend.addedPath, path)
	}
}

func TestActionErrorSurvivesSuccessfulRefresh(t *testing.T) {
	model := NewModel(stubBackend{})
	model.err = errors.New("attach failed")
	model.status = "Action failed"

	updated, _ := model.Update(dashboardMsg{})
	model = updated.(Model)
	if model.err == nil || model.err.Error() != "attach failed" {
		t.Fatalf("refresh cleared action error: %#v", model.err)
	}

	updated, _ = model.Update(runeKey("j"))
	model = updated.(Model)
	if model.err != nil || model.status != "Ready" {
		t.Fatalf("key did not clear action error: err=%v status=%q", model.err, model.status)
	}
}

func runeKey(value string) tea.KeyMsg {
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(value)}
}

type stubBackend struct{}

func (stubBackend) ListAgents(context.Context) ([]agent.Agent, error) {
	return nil, nil
}

func (stubBackend) ListWorkspaces(context.Context) ([]workspace.Context, error) {
	return nil, nil
}

func (stubBackend) AddWorkspace(_ context.Context, path string) (workspace.Context, error) {
	return workspace.DirectoryContext(path), nil
}

func (stubBackend) RemoveWorkspace(context.Context, workspace.Context) error {
	return nil
}

func (stubBackend) Dispatch(context.Context, app.DispatchRequest) (agent.Agent, error) {
	return agent.Agent{}, nil
}

func (stubBackend) Capture(context.Context, string, int) (string, error) {
	return "", nil
}

func (stubBackend) Attach(context.Context, string) (app.AttachResult, error) {
	return app.AttachResult{}, nil
}

func (stubBackend) Send(context.Context, string, string) error {
	return nil
}

func (stubBackend) Interrupt(context.Context, string) error {
	return nil
}

func (stubBackend) Rename(context.Context, string, string) error {
	return nil
}

func (stubBackend) ClearAttention(context.Context, string) error {
	return nil
}

func (stubBackend) RenameWorkspace(context.Context, workspace.Context, string) error {
	return nil
}

func (stubBackend) Delete(context.Context, string) error {
	return nil
}

func (stubBackend) SyncAgentWindows(context.Context, int, int) error {
	return nil
}

func (stubBackend) Providers() []provider.Info {
	return nil
}

type recordingBackend struct {
	stubBackend
	providers   []provider.Info
	request     app.DispatchRequest
	addedPath   string
	sentID      string
	sentMessage string
	attachedID  string
}

func (b *recordingBackend) Dispatch(_ context.Context, request app.DispatchRequest) (agent.Agent, error) {
	b.request = request
	return agent.Agent{
		Provider: request.Provider,
		Name:     string(request.Provider) + "-test",
	}, nil
}

func (b *recordingBackend) Providers() []provider.Info {
	return b.providers
}

func (b *recordingBackend) AddWorkspace(
	_ context.Context,
	path string,
) (workspace.Context, error) {
	b.addedPath = path
	return workspace.DirectoryContext(path), nil
}

func (b *recordingBackend) Send(_ context.Context, id, message string) error {
	b.sentID = id
	b.sentMessage = message
	return nil
}

func (b *recordingBackend) Attach(
	_ context.Context,
	id string,
) (app.AttachResult, error) {
	b.attachedID = id
	return app.AttachResult{}, nil
}

func TestSeenClearingMarksSelectedAgentOnPresence(t *testing.T) {
	ws := workspace.DirectoryContext("/workspace/project")
	model := NewModel(stubBackend{})
	model.width = 120
	model.height = 30
	model.ready = true
	model.catalogWorkspaces = []workspace.Context{ws}
	model.agents = []agent.Agent{{
		ID:          "unread",
		Name:        "sh-unread",
		ProcessLive: true,
		Activity:    agent.ActivityIdle,
		Attention:   agent.AttentionWaiting,
		Workspace:   ws,
	}}
	model.rebuildGroups(ws.ID, "unread")
	model.activePane = paneAgents
	model.interactionID = "unread"

	updated, _ := model.Update(runeKey("k"))
	model = updated.(Model)
	if model.agents[0].Attention != agent.AttentionNone {
		t.Fatalf("attention = %q, want cleared on presence", model.agents[0].Attention)
	}
}

func TestClearAttentionHotkeyClearsWholeWorkspace(t *testing.T) {
	ws := workspace.DirectoryContext("/workspace/project")
	model := NewModel(stubBackend{})
	model.width = 120
	model.catalogWorkspaces = []workspace.Context{ws}
	model.agents = []agent.Agent{
		{ID: "one", ProcessLive: true, Attention: agent.AttentionWaiting, Workspace: ws},
		{ID: "two", ProcessLive: true, Attention: agent.AttentionQuestion, Workspace: ws},
	}
	model.rebuildGroups(ws.ID, "")
	model.activePane = paneWorkspaces

	updated, cmd := model.updateNormal(runeKey("M"))
	model = updated.(Model)
	if cmd == nil {
		t.Fatal("expected a backend clear command")
	}
	for _, managedAgent := range model.agents {
		if managedAgent.Attention != agent.AttentionNone {
			t.Fatalf("agent %s attention = %q", managedAgent.ID, managedAgent.Attention)
		}
	}
}
