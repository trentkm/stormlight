package ui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/trentkm/stormlight/internal/agent"
	"github.com/trentkm/stormlight/internal/app"
	"github.com/trentkm/stormlight/internal/history"
	"github.com/trentkm/stormlight/internal/provider"
	"github.com/trentkm/stormlight/internal/pty"
	"github.com/trentkm/stormlight/internal/resurrect"
	"github.com/trentkm/stormlight/internal/surface"
	"github.com/trentkm/stormlight/internal/workspace"
)

func TestLineInputEditsAtCursor(t *testing.T) {
	input := newLineInput("")
	input.SetValue("ac")
	input.Focus()
	input = input.Update(tea.KeyPressMsg{Code: tea.KeyLeft})
	input = input.Update(tea.KeyPressMsg{Code: []rune("b")[0], Text: "b"})

	if got := input.Value(); got != "abc" {
		t.Fatalf("got %q", got)
	}
}

func TestLineInputDeletesPreviousWord(t *testing.T) {
	input := newLineInput("")
	input.SetValue("hello brave world")
	input.Focus()
	input = input.Update(tea.KeyPressMsg{Code: 'w', Mod: tea.ModCtrl})

	if got := input.Value(); got != "hello brave " {
		t.Fatalf("got %q", got)
	}
}

func TestLineInputAcceptsSpaceKey(t *testing.T) {
	input := newLineInput("")
	input.SetValue("review")
	input.Focus()
	input = input.Update(tea.KeyPressMsg{Code: tea.KeySpace, Text: " "})
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

	view := ansi.Strip(model.View().Content)
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

	view := ansi.Strip(model.View().Content)
	if !strings.Contains(view, "Codex") ||
		!strings.Contains(view, "Claude") ||
		!strings.Contains(view, "not found") {
		t.Fatalf("provider availability is unclear:\n%s", view)
	}
	// A box this short cannot hold the whole form, so the section label above
	// the provider rows is one of the rows that yields. What may not yield is
	// the composer and the hint line: the rows the reader types into and
	// reads to get out. This used to fit only because the form overran the
	// modal and the hints were the part quietly cut off.
	if !strings.Contains(view, "Task") ||
		strings.Contains(view, "Working directory") {
		t.Fatalf("contextual new-agent form is incorrect:\n%s", view)
	}
	if strings.Count(view, "╭") != strings.Count(view, "╰") {
		t.Fatalf("the form was cut off inside the composer:\n%s", view)
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
	view := ansi.Strip(model.View().Content)
	for _, label := range []string{
		"Workspaces",
		"Agents",
		// The Spanreed's title row is its window bar in terminal view,
		// so the context it preserves is the selected agent's name.
		"agent-one",
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

func TestDispatchOptionalNameReachesTheRequest(t *testing.T) {
	directory := t.TempDir()
	backend := &recordingBackend{
		providers: []provider.Info{{ID: agent.ProviderClaude, Label: "Claude"}},
	}
	model := NewModel(backend)
	updated, _ := model.Update(tea.WindowSizeMsg{Width: 120, Height: 34})
	model = updated.(Model)
	model.initialCwd = directory
	updated, _ = model.beginDispatch(false)
	model = updated.(Model)

	view := ansi.Strip(model.View().Content)
	if !strings.Contains(view, "Name") || !strings.Contains(view, "optional") {
		t.Fatalf("new-agent form has no optional name field:\n%s", view)
	}

	// Tab from the provider list lands on the name before the task, so the
	// field is reachable without leaving the keyboard home row.
	updated, _ = model.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	model = updated.(Model)
	if model.formFocus != dispatchName {
		t.Fatalf("tab focus = %v, want the name field", model.formFocus)
	}
	for _, key := range []string{"j", "u", "d", "g", "e"} {
		updated, _ = model.Update(runeKey(key))
		model = updated.(Model)
	}
	if got := model.nameInput.Value(); got != "judge" {
		t.Fatalf("name field value = %q, want %q", got, "judge")
	}

	// Enter moves on to the task rather than launching a nameless agent.
	updated, _ = model.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	model = updated.(Model)
	if model.formFocus != dispatchTask {
		t.Fatalf("focus after naming = %v, want the task field", model.formFocus)
	}
	model.taskInput.SetValue("score the rewrite")

	updated, command := model.submitDispatch()
	model = updated.(Model)
	if command != nil {
		command()
	}
	if backend.request.Name != "judge" {
		t.Fatalf("dispatch name = %q, want %q", backend.request.Name, "judge")
	}
	if backend.request.Task != "score the rewrite" {
		t.Fatalf("dispatch task = %q", backend.request.Task)
	}
	if model.nameInput.Value() != "" {
		t.Fatalf("name survived the launch: %q", model.nameInput.Value())
	}
}

func TestDispatchWithoutANameLaunchesUnnamed(t *testing.T) {
	directory := t.TempDir()
	backend := &recordingBackend{
		providers: []provider.Info{{ID: agent.ProviderClaude, Label: "Claude"}},
	}
	model := NewModel(backend)
	updated, _ := model.Update(tea.WindowSizeMsg{Width: 120, Height: 34})
	model = updated.(Model)
	model.initialCwd = directory
	updated, _ = model.beginDispatch(false)
	model = updated.(Model)

	// Enter stops on the optional name; leaving it blank is what makes the
	// launch unnamed.
	updated, _ = model.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	model = updated.(Model)
	if model.formFocus != dispatchName {
		t.Fatalf("Enter focus = %v, want the name field", model.formFocus)
	}
	updated, _ = model.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	model = updated.(Model)
	if model.formFocus != dispatchTask {
		t.Fatalf("Enter focus = %v, want the task field", model.formFocus)
	}
	model.taskInput.SetValue("investigate the flake")

	updated, command := model.submitDispatch()
	if command != nil {
		command()
	}
	if backend.request.Name != "" {
		t.Fatalf("unnamed dispatch carried name %q", backend.request.Name)
	}
}

// dispatchTaskFixture opens the new-agent form on a terminal of the given
// size with the cursor in the task box.
func dispatchTaskFixture(t *testing.T, width, height int) Model {
	t.Helper()
	backend := &recordingBackend{
		providers: []provider.Info{
			{ID: agent.ProviderCodex, Label: "Codex", Available: true},
			{ID: agent.ProviderClaude, Label: "Claude", Available: true},
		},
	}
	model := NewModel(backend)
	model.yaziPath = "/opt/homebrew/bin/yazi"
	sized, _ := model.Update(tea.WindowSizeMsg{Width: width, Height: height})
	model = sized.(Model)
	opened, _ := model.beginDispatch(true)
	model = opened.(Model)
	model.formFocus = dispatchTask
	model.focusForm()
	return model
}

// taskComposerRows returns the rows the task box draws, border stripped.
func taskComposerRows(t *testing.T, model Model) []string {
	t.Helper()
	width, height := model.dispatchContentDimensions()
	layout := model.dispatchLayout(width, height)
	box := strings.Split(
		ansi.Strip(model.renderTaskComposer(max(1, width-4), layout.taskHeight)),
		"\n",
	)
	if len(box) != layout.taskHeight+2 {
		t.Fatalf("box drew %d rows, want %d plus its border:\n%s",
			len(box), layout.taskHeight, strings.Join(box, "\n"))
	}
	return box[1 : len(box)-1]
}

// typeTask types into the task box the way the event loop does, drawing
// after every key — the textarea scrolls against what was last drawn.
func typeTask(t *testing.T, model Model, keys ...tea.KeyPressMsg) Model {
	t.Helper()
	for _, key := range keys {
		updated, _ := model.updateDispatch(key)
		model = updated.(Model)
		model.View()
	}
	return model
}

// Enter launches the agent, so the task box needs its own newline key.
func TestDispatchTaskCtrlJInsertsNewline(t *testing.T) {
	model := dispatchTaskFixture(t, 120, 40)
	model = typeTask(t, model,
		runeKey("first"),
		tea.KeyPressMsg{Code: 'j', Mod: tea.ModCtrl},
		runeKey("second"),
	)
	if got := model.taskInput.Value(); got != "first\nsecond" {
		t.Fatalf("task = %q, want both lines", got)
	}
	if rows := taskComposerRows(t, model); !strings.Contains(rows[0], "first") ||
		!strings.Contains(rows[1], "second") {
		t.Fatalf("the box does not show both lines:\n%s", strings.Join(rows, "\n"))
	}
}

// Typing past the bottom of the box must keep the cursor's line in view.
// The textarea wraps and scrolls against its own width and height, so a
// persisted size that disagrees with the drawn box parks the scroll above
// what is being typed and nothing brings it back.
func TestDispatchTaskComposerKeepsTypingInView(t *testing.T) {
	for _, size := range [][2]int{{80, 24}, {100, 30}, {160, 50}} {
		model := dispatchTaskFixture(t, size[0], size[1])
		for index := range 40 {
			model = typeTask(t, model, runeKey(fmt.Sprintf("word%02d ", index)))
		}
		rows := taskComposerRows(t, model)
		if !strings.Contains(strings.Join(rows, " "), "word39") {
			t.Fatalf("%dx%d: the last word typed is out of view:\n%s",
				size[0], size[1], strings.Join(rows, "\n"))
		}
		if strings.TrimSpace(rows[0]) == "" {
			t.Fatalf("%dx%d: the box opens on a blank row:\n%s",
				size[0], size[1], strings.Join(rows, "\n"))
		}
	}
}

// The form's rows are a fixed budget: the composer takes what is left, and
// the hint line below it is the first thing a miscount pushes off the
// bottom of the modal. The path picker is the case that miscounted — it
// arrives as a block of rows rather than as one line.
func TestDispatchFormFitsItsModal(t *testing.T) {
	for _, size := range [][2]int{{80, 24}, {100, 30}, {120, 34}, {160, 50}} {
		for _, picker := range []bool{false, true} {
			// A 24-row terminal cannot hold the picker and the form both;
			// every other combination has to fit whole.
			if picker && size[1] < 30 {
				continue
			}
			model := dispatchTaskFixture(t, size[0], size[1])
			if picker {
				selectCustomDirectory(t, &model)
				model.startPathNav()
			}
			model = typeTask(t, model, runeKey(strings.Repeat("task ", 40)))
			modal := ansi.Strip(model.renderDispatchModal(model.bodyDimensions()))
			if !strings.Contains(modal, "Esc cancel") {
				t.Fatalf("%dx%d picker=%v: the hint line was clipped:\n%s",
					size[0], size[1], picker, modal)
			}
			// What the composer shows depends on how many rows it got. Given
			// two or more it shows the wrapped task from the top; given the
			// single row a cramped form leaves it, it shows the row the cursor
			// is on — which is the tail, since the task was just typed. Both
			// are the box doing its job, so the assertion is that the typed
			// text reached it, not which end of it is visible.
			width, height := model.dispatchContentDimensions()
			layout := model.dispatchLayout(width, height)
			composer := ansi.Strip(
				model.renderTaskComposer(max(1, width-4), layout.taskHeight),
			)
			want := "task task"
			if layout.taskHeight < 2 {
				want = "task"
			}
			if !strings.Contains(composer, want) {
				t.Fatalf("%dx%d picker=%v: the task box was clipped:\n%s\n%s",
					size[0], size[1], picker, composer, modal)
			}
			assertViewFitsPane(t, model, size[0]-1, size[1]-1)
		}
	}
}

// The form must fit the modal it is drawn in, at every height a terminal
// can be, and it must never buy that fit by cutting off the composer or the
// hint line. A twenty-row terminal used to render fourteen rows into a
// twelve-row box: the modal cut the overflow from the bottom, so the task
// box lost its lower border and the hints vanished entirely, leaving no
// on-screen way to tell how to submit or escape. See #79.
func TestDispatchFormFitsEveryHeight(t *testing.T) {
	for _, picker := range []bool{false, true} {
		for width := 60; width <= 160; width += 20 {
			for height := 12; height <= 50; height++ {
				model := dispatchTaskFixture(t, width, height)
				model.chooseDispatchDirectory = picker
				if picker {
					model.prepareDirectoryChoices(model.initialCwd)
					model.directoryIndex = 0
				}
				model.syncTaskComposerSize()

				boxWidth, boxHeight := model.dispatchContentDimensions()
				form := ansi.Strip(model.renderDispatchAt(boxWidth, boxHeight))
				rows := strings.Split(form, "\n")
				if len(rows) > boxHeight {
					t.Fatalf("%dx%d picker=%v: form is %d rows in a %d-row box:\n%s",
						width, height, picker, len(rows), boxHeight, form)
				}
				// The composer is drawn as a bordered box, so an intact one
				// opens and closes; a clipped form loses the closing edge.
				if strings.Count(form, "\u256d") != strings.Count(form, "\u2570") {
					t.Fatalf("%dx%d picker=%v: the composer border is cut:\n%s",
						width, height, picker, form)
				}
				// The hint row is the last thing drawn, and a narrow box
				// truncates its text — so what is checked is that a row
				// follows the composer at all, not what it says.
				last := rows[len(rows)-1]
				if strings.TrimSpace(last) == "" || strings.Contains(last, "\u2570") {
					t.Fatalf("%dx%d picker=%v: nothing follows the composer:\n%s",
						width, height, picker, form)
				}
			}
		}
	}
}

// One more row in the directory list must not cost the user a field.
//
// The list grows by a row when Yazi is installed, and that single row used to
// tip an ordinary 100x30 terminal out of the roomy form — which took the name
// field with it, out of the drawing and out of the focus order both, so an
// agent could not be named at all. CI never saw it because the runners have
// no Yazi. See #73.
func TestDispatchNameSurvivesAnExtraDirectoryRow(t *testing.T) {
	for _, yazi := range []string{"", "/opt/homebrew/bin/yazi"} {
		model := dispatchTaskFixture(t, 100, 30)
		model.chooseDispatchDirectory = true
		model.yaziPath = yazi
		model.prepareDirectoryChoices(model.initialCwd)
		model.syncTaskComposerSize()

		if !model.dispatchNameVisible() {
			t.Fatalf("yazi=%q: the name field is not drawn at 100x30", yazi)
		}
		order := model.dispatchFocusOrder()
		if !slices.Contains(order, dispatchName) {
			t.Fatalf("yazi=%q: name missing from the focus order %v", yazi, order)
		}
		// The composer is the field the form exists to fill, so buying the
		// name row back must not starve it below its own floor.
		width, height := model.dispatchContentDimensions()
		if task := model.dispatchLayout(width, height).taskHeight; task < minTaskHeight {
			t.Fatalf("yazi=%q: composer starved to %d rows", yazi, task)
		}
	}
}

// A short pane drops the optional name row, so focus must not stop there.
func TestDispatchFocusSkipsHiddenNameField(t *testing.T) {
	backend := &recordingBackend{
		providers: []provider.Info{{ID: agent.ProviderClaude, Label: "Claude"}},
	}
	model := NewModel(backend)
	model.mode = modeDispatch
	updated, _ := model.Update(tea.WindowSizeMsg{Width: 40, Height: 16})
	model = updated.(Model)

	if model.dispatchNameVisible() {
		t.Fatal("compact form claims room for the name field")
	}
	updated, _ = model.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	model = updated.(Model)
	if model.formFocus == dispatchName {
		t.Fatal("focus landed on a name field that is not drawn")
	}
}

// Shrinking the terminal while the name field is focused must move focus
// out of the field the next render will drop.
func TestDispatchShrinkReleasesHiddenNameField(t *testing.T) {
	backend := &recordingBackend{
		providers: []provider.Info{{ID: agent.ProviderClaude, Label: "Claude"}},
	}
	model := NewModel(backend)
	model.mode = modeDispatch
	updated, _ := model.Update(tea.WindowSizeMsg{Width: 120, Height: 34})
	model = updated.(Model)
	model.formFocus = dispatchName
	model.focusForm()

	updated, _ = model.Update(tea.WindowSizeMsg{Width: 40, Height: 16})
	model = updated.(Model)
	if model.formFocus != dispatchTask {
		t.Fatalf("focus after shrink = %v, want the task field", model.formFocus)
	}
}

func TestModalRendersCompleteBorder(t *testing.T) {
	rendered := ansi.Strip(renderModal("content", 32, 8))
	lines := strings.Split(rendered, "\n")
	if len(lines) != 8 {
		t.Fatalf("modal height = %d, want 8:\n%s", len(lines), rendered)
	}
	if !strings.HasPrefix(lines[0], "╭") ||
		!strings.HasSuffix(lines[0], "╮") ||
		!strings.HasPrefix(lines[len(lines)-1], "╰") ||
		!strings.HasSuffix(lines[len(lines)-1], "╯") {
		t.Fatalf("modal border is incomplete or not curved:\n%s", rendered)
	}
	for index, line := range lines {
		if width := lipgloss.Width(line); width != 32 {
			t.Fatalf("modal line %d width = %d, want 32", index+1, width)
		}
	}
}

// The composer sits inside the dispatch modal, so it curves with it. A square
// box nested in a rounded one reads as a rendering slip.
func TestTaskComposerCurvesWithItsModal(t *testing.T) {
	box := strings.Split(
		ansi.Strip(NewModel(stubBackend{}).renderTaskComposer(40, 3)),
		"\n",
	)
	if !strings.HasPrefix(box[0], "╭") ||
		!strings.HasSuffix(box[0], "╮") ||
		!strings.HasPrefix(box[len(box)-1], "╰") ||
		!strings.HasSuffix(box[len(box)-1], "╯") {
		t.Fatalf("task composer is not curved:\n%s", strings.Join(box, "\n"))
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
	view := ansi.Strip(model.View().Content)
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

	updated, _ = model.updateNormal(tea.KeyPressMsg{Code: tea.KeyEscape})
	model = updated.(Model)
	footer = ansi.Strip(model.renderFooter())
	if strings.Contains(footer, "Go:") || !strings.Contains(footer, "j/k select") {
		t.Fatalf("cancelled chord did not restore normal footer: %q", footer)
	}
}

func TestWideDashboardRendersThreePaneHierarchy(t *testing.T) {
	workspaceContext := workspace.DirectoryContext("/workspace")
	model := NewModel(stubBackend{})
	model.ptyEnabled = false
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

	view := ansi.Strip(model.View().Content)
	for _, label := range []string{"Workspaces", "Agents", "agent response"} {
		if !strings.Contains(view, label) {
			t.Fatalf("dashboard is missing %q:\n%s", label, view)
		}
	}
	assertViewFitsPane(t, model, 119, 29)
}

// The dashboard's two seams divide different kinds of thing, so they are not
// drawn alike: a hairline in the header band's own faded gradient splits the
// catalog's columns, while a heavy accent rule trailed by a column of air
// splits the whole catalog from the Spanreed. Both hang from caps under the
// band, and both keep air on either side.
func TestWideDashboardSeamsSeparateCatalogFromControlPlane(t *testing.T) {
	model := NewModel(stubBackend{})
	model.width = 120
	model.height = 30

	body := ansi.Strip(model.renderBody())
	lines := strings.Split(body, "\n")
	header := -1
	for index, line := range lines {
		if strings.Contains(line, "Workspaces") &&
			strings.Contains(line, "Agents") {
			header = index
			break
		}
	}
	if header < 0 {
		t.Fatalf("pane header row not found:\n%s", body)
	}
	// The header row carries the seams' caps, not the seams: half-strokes
	// that begin at the band and descend out of it.
	hairline := strings.Index(lines[header], "╷")
	plane := strings.Index(lines[header], "╻")
	if hairline < 0 || plane < 0 || plane < hairline {
		t.Fatalf("seams are not capped under the band: %q", lines[header])
	}
	if strings.ContainsAny(lines[header], "│┃") {
		t.Fatalf("uncapped seam collides with the header band: %q", lines[header])
	}

	firstRow := lines[header+1]
	if strings.Index(firstRow, "│") != hairline ||
		strings.Index(firstRow, "┃") != plane {
		t.Fatalf("seams do not hang from their caps:\n%q\n%q",
			lines[header], firstRow)
	}

	for _, line := range lines[header+1:] {
		runes := []rune(line)
		if column := runeIndex(runes, '│'); column >= 0 {
			if column+1 < len(runes) && runes[column+1] != ' ' {
				t.Fatalf("catalog rule has no air behind it: %q", line)
			}
			if column > 0 && runes[column-1] != ' ' {
				t.Fatalf("catalog rule has no air before it: %q", line)
			}
		}
		column := runeIndex(runes, '┃')
		if column < 0 {
			t.Fatalf("body row has no plane seam: %q", line)
		}
		if column+1 < len(runes) && runes[column+1] != ' ' {
			t.Fatalf("plane seam has no gutter behind it: %q", line)
		}
	}
}

// The wide layout already says which workspace is selected four ways — the ›
// marker, the undimmed row, the connector arc, and the agent list's contents —
// so the header does not spell it out a fifth time. The narrow layout shows
// one pane at a time, where none of those four are on screen, and there the
// context label is the only thing naming the workspace.
func TestAgentPaneHeaderNamesWorkspaceOnlyWhenNarrow(t *testing.T) {
	workspaceContext := workspace.DirectoryContext("/workspace/meshclaw")
	model := NewModel(stubBackend{})
	model.width = 120
	model.height = 30
	model.activePane = paneAgents
	model.catalogWorkspaces = []workspace.Context{workspaceContext}
	model.rebuildGroups(workspaceContext.ID, "")

	header := strings.Split(ansi.Strip(model.renderBody()), "\n")[0]
	if !strings.Contains(header, "Agents") {
		t.Fatalf("agent header is missing its label: %q", header)
	}
	if strings.Contains(header, "meshclaw") {
		t.Fatalf("wide agent header repeats the workspace: %q", header)
	}

	model.width = 60
	narrow := strings.Split(ansi.Strip(model.renderBody()), "\n")[0]
	if !strings.Contains(narrow, "meshclaw") ||
		strings.Index(narrow, "meshclaw") < strings.Index(narrow, "Agents") {
		t.Fatalf("narrow agent header lacks its workspace context: %q", narrow)
	}

	rendered := ansi.Strip(renderPaneHeader("Agents", "a-very-long-workspace-name", 24, true, headerBand{total: 24}))
	if width := lipgloss.Width(rendered); width != 24 {
		t.Fatalf("contextual header width = %d, want 24: %q", width, rendered)
	}
}

func TestPaneHeaderKeepsLabelAlignmentAcrossFocusStates(t *testing.T) {
	active := ansi.Strip(renderPaneHeader("Workspaces", "", 24, true, headerBand{total: 24}))
	inactive := ansi.Strip(renderPaneHeader("Workspaces", "", 24, false, headerBand{total: 24}))
	// The label is set into the band with a column of padding on each side,
	// and that padding must not move when focus does.
	if !strings.HasPrefix(active, " Workspaces ") ||
		!strings.HasPrefix(inactive, " Workspaces ") {
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
		!strings.Contains(header, "Agents") {
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
	lines := strings.Split(ansi.Strip(model.View().Content), "\n")
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
	updated, cmd := model.updateDispatch(tea.KeyPressMsg{Code: tea.KeyEnter})
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
	sized, _ := model.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	model = sized.(Model)

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
	rendered := ansi.Strip(model.renderDispatchModal(model.bodyDimensions()))
	if !strings.Contains(rendered, "Coding agent") ||
		!strings.Contains(rendered, "Task") ||
		strings.Contains(rendered, "Working directory") ||
		strings.Contains(rendered, "Browse with Yazi") {
		t.Fatalf("unexpected contextual new-agent form:\n%s", rendered)
	}

	for range len(model.dispatchFocusOrder()) {
		if model.formFocus == dispatchTask {
			break
		}
		updated, _ = model.updateDispatch(tea.KeyPressMsg{Code: tea.KeyEnter})
		model = updated.(Model)
	}
	if model.formFocus != dispatchTask {
		t.Fatalf("Enter never reached the task field: %v", model.formFocus)
	}
	model.taskInput.SetValue("review this workspace")
	updated, cmd := model.updateDispatch(tea.KeyPressMsg{Code: tea.KeyEnter})
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
			Activity:    agent.ActivityWorking,
			ProcessLive: true,
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
	if !strings.Contains(quiet, "● 1") {
		t.Fatalf("active count chip missing on quiet row:\n%s", quiet)
	}
	if strings.HasPrefix(strings.TrimSpace(quiet), "●") {
		t.Fatalf("state glyph is still doubled in the left column:\n%s", quiet)
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
	if !ok || workspaceRow != 3 || agentRow != 4 {
		t.Fatalf(
			"compact connector rows = %d -> %d, ok=%v",
			workspaceRow,
			agentRow,
			ok,
		)
	}

	model.rowsExpanded = true
	workspaceRow, agentRow, ok = model.hierarchyConnectorRows(20)
	if !ok || workspaceRow != 5 || agentRow != 8 {
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
	if lines[0] != "header │" ||
		!strings.HasSuffix(lines[1], "╮ │") ||
		!strings.HasSuffix(lines[2], "│ │") ||
		!strings.HasSuffix(lines[3], "╰ │") {
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
	// The focus bar belongs to the pane holding the cursor, and the cursor is
	// in the Spanreed, so neither list claims it.
	if strings.Contains(workspaces, "▌") || strings.Contains(agents, "▌") {
		t.Fatalf(
			"a list claimed the focus bar it does not hold:\nworkspaces:\n%s\nagents:\n%s",
			workspaces,
			agents,
		)
	}
	if !strings.Contains(agents, "›") {
		t.Fatalf("selected agent is unmarked:\n%s", agents)
	}
	// The workspace row marks nothing: the pane keeps it undimmed while its
	// neighbours recede, which is signal enough without an arrow.
	if strings.Contains(workspaces, "›") {
		t.Fatalf("workspace row still carries a selection arrow:\n%s", workspaces)
	}
	lit := model.selectedRowRange(len(model.groups), model.workspaceCursor, 11)
	if lit.count == 0 {
		t.Fatalf("no workspace row is held at full strength to mark selection")
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

// Selecting a row must not change what it reports. The selection background
// used to repaint the title flat, so putting the cursor on a working agent
// made it read exactly like an idle one — which is what "the blue pulse
// clears as soon as I navigate left" describes. See #63.
func TestSelectedAgentRowKeepsWorkingAndUrgentState(t *testing.T) {
	// No profile to force: v2 dropped the global renderer that used to
	// downsample to whatever it had detected, so a Style emits full-fidelity
	// ANSI whether or not a terminal is attached.
	base := agent.Agent{
		Provider:    agent.ProviderCodex,
		Name:        "cx-fix-parser",
		Task:        "Fix parser behavior",
		ProcessLive: true,
	}
	withState := func(activity agent.Activity, attention agent.Attention) agent.Agent {
		value := base
		value.Activity = activity
		value.Attention = attention
		return value
	}
	render := func(value agent.Agent, phase int) string {
		return renderAgentRowWithDensity(value, true, true, 52, true, false, phase)
	}
	// The subtitle spells the state out in words either way; the claim here
	// is about the title line, which is what carries the color.
	titleLine := func(rendered string) string {
		return strings.SplitN(rendered, "\n", 2)[0]
	}

	idle := render(withState(agent.ActivityIdle, agent.AttentionNone), 4)
	working := render(withState(agent.ActivityWorking, agent.AttentionNone), 4)
	urgent := render(withState(agent.ActivityIdle, agent.AttentionQuestion), 4)
	if titleLine(working) == titleLine(idle) {
		t.Fatal("selected working title renders identically to an idle one")
	}
	if titleLine(urgent) == titleLine(idle) {
		t.Fatal("selected urgent title renders identically to an idle one")
	}

	// The sweep has to keep moving under the cursor, not freeze at a shade.
	late := render(withState(agent.ActivityWorking, agent.AttentionNone), 7)
	if titleLine(late) == titleLine(working) {
		t.Fatal("selected working title does not shimmer between phases")
	}

	for _, rendered := range []string{idle, working, urgent, late} {
		for index, line := range strings.Split(ansi.Strip(rendered), "\n") {
			if width := lipgloss.Width(line); width != 52 {
				t.Fatalf("line %d width = %d, want 52: %q", index+1, width, line)
			}
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
	model.ptyEnabled = false
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
	updated, cmd := model.updateCompose(tea.KeyPressMsg{Code: tea.KeyEnter})
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
	// In the terminal view Enter walks into the embedded terminal
	// instead; the external attach is transcript-view behavior (and F).
	model.ptyEnabled = false
	model.catalogWorkspaces = []workspace.Context{workspaceContext}
	model.agents = []agent.Agent{{
		ID:        "agent-one",
		Name:      "agent-one",
		Workspace: workspaceContext,
	}}
	model.rebuildGroups(workspaceContext.ID, "agent-one")
	model.activePane = paneAgents

	updated, cmd := model.updateNormal(tea.KeyPressMsg{Code: tea.KeyEnter})
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
	model.ptyEnabled = false
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

func TestInteractionHeadingNamesTheCheckout(t *testing.T) {
	checkout := workspace.Context{
		ID:            "git:/repo/.git",
		Kind:          workspace.KindGit,
		Name:          "repo",
		Root:          "/repo",
		ExecutionRoot: "/repo",
	}
	worktree := checkout
	worktree.ExecutionRoot = "/repo/.claude/worktrees/fix-auth"
	loose := workspace.DirectoryContext("/scratch")

	model := NewModel(stubBackend{})
	model.catalogWorkspaces = []workspace.Context{checkout, loose}
	model.agents = []agent.Agent{
		{ID: "main", Name: "main", Cwd: "/repo", Workspace: checkout},
		{
			ID:        "linked",
			Name:      "linked",
			Cwd:       "/repo/.claude/worktrees/fix-auth",
			Workspace: worktree,
		},
		{ID: "loose", Name: "loose", Cwd: "/scratch", Workspace: loose},
	}

	for _, testCase := range []struct {
		name      string
		workspace string
		agent     string
		want      string
		absent    []string
	}{
		{
			name:      "linked worktree wears its own name",
			workspace: checkout.ID,
			agent:     "linked",
			want:      "worktree fix-auth",
			absent:    []string{"main checkout"},
		},
		{
			name:      "primary checkout says so",
			workspace: checkout.ID,
			agent:     "main",
			want:      "main checkout",
			absent:    []string{"worktree"},
		},
		{
			name:      "outside git names no checkout",
			workspace: loose.ID,
			agent:     "loose",
			absent:    []string{"worktree", "main checkout"},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			model.rebuildGroups(testCase.workspace, testCase.agent)
			model.interactionID = testCase.agent
			selected, ok := model.selectedAgent()
			if !ok {
				t.Fatal("no selected agent")
			}
			// The checkout words live in the window bar, the Spanreed's
			// title row in terminal view.
			rendered := ansi.Strip(model.renderTerminalBar(selected, 80) +
				"\n" + model.renderInteraction(80, 20))
			if testCase.want != "" && !strings.Contains(rendered, testCase.want) {
				t.Fatalf("heading hides %q:\n%s", testCase.want, rendered)
			}
			for _, absent := range testCase.absent {
				if strings.Contains(rendered, absent) {
					t.Fatalf("heading claims %q:\n%s", absent, rendered)
				}
			}
		})
	}
}

// The accent marks the checkout worth picking out of several; the primary one
// is stated, not flagged, because in most repositories it is simply where the
// work happens.
func TestCheckoutBadgeColors(t *testing.T) {
	linked := checkoutStyle(checkoutBadge{text: "worktree fix-auth"})
	if linked.GetForeground() != colorAccent() {
		t.Fatalf("linked worktree lost its accent: %v", linked.GetForeground())
	}
	primary := checkoutStyle(checkoutBadge{text: "main checkout", primary: true})
	if primary.GetForeground() != colorMuted() {
		t.Fatalf("primary checkout is not muted: %v", primary.GetForeground())
	}
}

// A narrow pane drops the path before the badge: the path restates the
// checkout, the badge is the only token that names it. Tokens fall from the
// tail, so the state and the provider outlive both.
func TestInteractionMetaKeepsCheckoutOverPath(t *testing.T) {
	agentIn := func(root, executionRoot string) agent.Agent {
		return agent.Agent{
			Provider:    agent.ProviderClaude,
			Activity:    agent.ActivityWorking,
			ProcessLive: true,
			Cwd:         "~/repos/stormlight",
			Workspace: workspace.Context{
				ID:            "w",
				Name:          "stormlight",
				Kind:          workspace.KindGit,
				Root:          root,
				ExecutionRoot: executionRoot,
			},
		}
	}
	for _, testCase := range []struct {
		name   string
		agent  agent.Agent
		narrow int
		keeps  string
	}{
		{
			name:   "linked worktree",
			agent:  agentIn("/repo", "/repo/.claude/worktrees/fix-auth"),
			narrow: 40,
			keeps:  "worktree fix-auth",
		},
		{
			name:   "primary checkout",
			agent:  agentIn("/repo", "/repo"),
			narrow: 36,
			keeps:  "main checkout",
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			wide := ansi.Strip(interactionMeta(testCase.agent, 90))
			if !strings.Contains(wide, testCase.keeps) ||
				!strings.Contains(wide, "stormlight") {
				t.Fatalf("wide meta line lost a token: %q", wide)
			}
			if !strings.Contains(wide, "working") ||
				!strings.Contains(wide, "claude") {
				t.Fatalf("wide meta line lost its state or provider: %q", wide)
			}

			narrow := ansi.Strip(interactionMeta(testCase.agent, testCase.narrow))
			if !strings.Contains(narrow, testCase.keeps) {
				t.Fatalf("narrow meta line dropped the checkout: %q", narrow)
			}
			if strings.Contains(narrow, "stormlight") {
				t.Fatalf("narrow meta line kept the path: %q", narrow)
			}
			if width := ansi.StringWidth(narrow); width > testCase.narrow {
				t.Fatalf("narrow meta line is %d wide, limit %d: %q",
					width, testCase.narrow, narrow)
			}
		})
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
	model.interaction = viewport.New(viewport.WithWidth(40), viewport.WithHeight(2))
	model.interaction.SetContent("one\ntwo\nthree\nfour")
	model.interaction.GotoTop()
	model.interactionID = "one"

	updated, _ := model.Update(interactionMsg{
		id:      "one",
		content: "one\ntwo\nthree\nfour\nfive",
	})
	model = updated.(Model)
	if model.interaction.YOffset() != 0 {
		t.Fatalf("refresh moved viewport to offset %d", model.interaction.YOffset())
	}

	model.interaction.GotoBottom()
	updated, _ = model.Update(interactionMsg{
		id:      "one",
		content: "one\ntwo\nthree\nfour\nfive\nsix",
	})
	model = updated.(Model)
	if !model.interaction.AtBottom() {
		t.Fatalf("viewport stopped following output at offset %d", model.interaction.YOffset())
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

func runeKey(value string) tea.KeyPressMsg {
	return tea.KeyPressMsg{Code: []rune(value)[0], Text: value}
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

func (stubBackend) SetMark(context.Context, string, agent.Mark) error {
	return nil
}

func (stubBackend) RenameWorkspace(context.Context, workspace.Context, string) error {
	return nil
}

func (stubBackend) Delete(context.Context, string) error {
	return nil
}

func (stubBackend) AttachTerminal(context.Context, string, int, int) (pty.Transport, error) {
	return nil, fmt.Errorf("stub backend has no terminals")
}

func (stubBackend) Providers() []provider.Info {
	return nil
}

func (stubBackend) SessionHistory(context.Context) ([]history.Record, error) {
	return nil, nil
}

func (stubBackend) Resume(context.Context, history.Record) (agent.Agent, error) {
	return agent.Agent{}, nil
}

func (stubBackend) RestoreCandidates(
	context.Context,
) ([]resurrect.Candidate, error) {
	return nil, nil
}

func (stubBackend) Restore(
	context.Context,
	...string,
) ([]app.RestoreResult, error) {
	return nil, nil
}

func (stubBackend) Forget(context.Context, ...string) error {
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

func unreadAgentModel(active pane) Model {
	ws := workspace.DirectoryContext("/workspace/project")
	model := NewModel(stubBackend{})
	// Engagement semantics are transcript-view rules; in the terminal
	// view every forwarded keystroke clears attention by its own path
	// (updateTerminalKey).
	model.ptyEnabled = false
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
	model.activePane = active
	model.interactionID = "unread"
	return model
}

func TestSeenClearingMarksSelectedAgentOnEngagement(t *testing.T) {
	cases := []struct {
		label  string
		key    tea.KeyPressMsg
		active pane
	}{
		{"scroll the transcript", runeKey("k"), paneInteraction},
		{"jump to its end", runeKey("G"), paneInteraction},
		{"reply", runeKey("i"), paneAgents},
		{"open its terminal", tea.KeyPressMsg{Code: tea.KeyEnter}, paneAgents},
	}
	for _, testCase := range cases {
		model := unreadAgentModel(testCase.active)
		updated, _ := model.Update(testCase.key)
		model = updated.(Model)
		if model.agents[0].Attention != agent.AttentionNone {
			t.Fatalf("%s: attention = %q, want cleared on engagement",
				testCase.label, model.agents[0].Attention)
		}
	}
}

// Navigating past an unseen result is not reading it: the amber has to
// survive the keys a human presses on the way out of the transcript, or it
// clears before they ever look. See #63.
func TestSeenClearingSurvivesNavigation(t *testing.T) {
	cases := []struct {
		label  string
		key    tea.KeyPressMsg
		active pane
	}{
		{"left, out of the transcript", runeKey("h"), paneInteraction},
		{"right, into the transcript", runeKey("l"), paneAgents},
		{"cycling panes", tea.KeyPressMsg{Code: tea.KeyTab}, paneAgents},
		{"up the agent list", runeKey("k"), paneAgents},
		{"down the workspace list", runeKey("j"), paneWorkspaces},
	}
	for _, testCase := range cases {
		model := unreadAgentModel(testCase.active)
		updated, _ := model.Update(testCase.key)
		model = updated.(Model)
		if model.agents[0].Attention != agent.AttentionWaiting {
			t.Fatalf("%s: attention = %q, want the unseen result kept",
				testCase.label, model.agents[0].Attention)
		}
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

func runeIndex(runes []rune, want rune) int {
	for index, glyph := range runes {
		if glyph == want {
			return index
		}
	}
	return -1
}
