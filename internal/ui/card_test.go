package ui

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/trentkm/stormlight/internal/agent"
	"github.com/trentkm/stormlight/internal/workspace"
)

func TestACardIsAFrameAtThePanesWidth(t *testing.T) {
	for _, width := range []int{12, 30, 52} {
		card := ansi.Strip(renderCard("title", "detail", width, cardFrame{ink: colorBorder()}))
		lines := strings.Split(card, "\n")
		if len(lines) != cardRows {
			t.Fatalf("width %d: %d lines, want %d:\n%s", width, len(lines), cardRows, card)
		}
		for index, line := range lines {
			if got := lipgloss.Width(line); got != width {
				t.Fatalf("width %d: line %d is %d wide: %q", width, index, got, line)
			}
		}
		if !strings.HasPrefix(lines[0], "╭") || !strings.HasSuffix(lines[0], "╮") ||
			!strings.HasPrefix(lines[3], "╰") || !strings.HasSuffix(lines[3], "╯") ||
			!strings.HasPrefix(lines[1], "│ title") || !strings.HasSuffix(lines[1], "│") {
			t.Fatalf("width %d: not a rounded frame around the lines:\n%s", width, card)
		}
	}
}

func TestACardCutsALineThatOverflows(t *testing.T) {
	card := ansi.Strip(renderCard(strings.Repeat("x", 40), "d", 20, cardFrame{ink: colorBorder()}))
	for index, line := range strings.Split(card, "\n") {
		if got := lipgloss.Width(line); got != 20 {
			t.Fatalf("line %d is %d wide, want 20: %q", index, got, line)
		}
	}
}

func TestTheCardDetailGivesThePathItsRoomFirst(t *testing.T) {
	path := "/Volumes/repos/shared/alpha-service"
	// Wide: every token and the whole path.
	if got := cardDetail([]string{"codex", "AUTO", "working"}, path, 60); got != "codex · AUTO · working · "+path {
		t.Fatalf("wide detail = %q", got)
	}
	// Narrower: the last token yields so the whole path stays.
	got := cardDetail([]string{"codex", "AUTO", "working"}, path, 50)
	if got != "codex · AUTO · "+path {
		t.Fatalf("narrow detail = %q", got)
	}
	// Narrower still: every token yields before the path is cut.
	got = cardDetail([]string{"codex", "AUTO", "working"}, path, 36)
	if got != path {
		t.Fatalf("tight detail = %q", got)
	}
	// Tighter than the path itself: shortened, the name kept whole.
	if got = cardDetail([]string{"codex", "AUTO", "working"}, path, 16); got != "…/alpha-service" {
		t.Fatalf("path-only detail = %q", got)
	}
	// An empty token leaves no stray separator behind.
	if got := cardDetail([]string{"codex", "", ""}, path, 60); got != "codex · "+path {
		t.Fatalf("empty tokens left a mark: %q", got)
	}
	if got := cardDetail([]string{"codex", "idle"}, "", 60); got != "codex · idle" {
		t.Fatalf("no path = %q", got)
	}
}

func TestAnAgentCardSaysWhatTheAgentIsDoing(t *testing.T) {
	value := agent.Agent{ID: "a", Provider: agent.ProviderClaude, Name: "ascii art",
		Task: "fill the dead space with art", Cwd: "/Volumes/repos/stormlight", ProcessLive: true}
	card := ansi.Strip(renderAgentCard(value, false, false, 48, false, -1))
	lines := strings.Split(card, "\n")
	if !strings.Contains(lines[2], "fill the dead space with art") || strings.Contains(card, "/Volumes") {
		t.Fatalf("the detail is not the task:\n%s", card)
	}
	// The latest summary outranks the task.
	value.Summary = "Shipped the night sky in PR #242."
	card = ansi.Strip(renderAgentCard(value, false, false, 48, false, -1))
	if !strings.Contains(card, "Shipped the night sky") || strings.Contains(card, "fill the dead") {
		t.Fatalf("the detail is not the summary:\n%s", card)
	}
	// The mode is the masthead's to say, not the card's.
	value.Mode = agent.ModeAuto
	card = ansi.Strip(renderAgentCard(value, false, false, 48, false, -1))
	if strings.Contains(card, "AUTO") {
		t.Fatalf("the mode is on the card:\n%s", card)
	}
	// A title built from the task is not repeated under itself — even
	// when the title had to be cut to fit, or the task's spacing differs
	// from how the title draws it.
	named := agent.Agent{ID: "b", Provider: agent.ProviderCodex, Name: "cx-fix-parser",
		Task:     "Fix the  parser and the lexer so nested blocks round-trip",
		Activity: agent.ActivityWorking, ProcessLive: true}
	card = ansi.Strip(renderAgentCard(named, false, false, 36, false, -1))
	lines = strings.Split(card, "\n")
	if !strings.Contains(lines[1], "Fix the parser") || strings.Contains(lines[2], "Fix the") ||
		!strings.Contains(lines[2], "codex · working") {
		t.Fatalf("a task-titled card repeats itself:\n%s", card)
	}
}

func TestAnIdleAgentCardHasNoStrayState(t *testing.T) {
	// Nothing said yet and no task: the provider stands in, and the
	// idle state, being empty, leaves no stray separator behind.
	card := ansi.Strip(renderAgentCard(agent.Agent{
		ID: "a", Provider: agent.ProviderCodex, Name: "hello", ProcessLive: true,
	}, false, false, 40, false, -1))
	lines := strings.Split(card, "\n")
	if strings.Contains(lines[2], "·") || !strings.Contains(lines[2], "codex") {
		t.Fatalf("idle card detail = %q", lines[2])
	}
}

func TestAWorkspaceCardNamesItsMachineAndPath(t *testing.T) {
	model := NewModel(stubBackend{})
	remote := workspace.Context{Host: "mini", ID: "mini:w", Kind: "git", Name: "api",
		Root: "/srv/api", ExecutionRoot: "/srv/api"}
	card := ansi.Strip(model.renderWorkspaceCard(workspaceGroup{context: remote}, false, false, 30, false))
	lines := strings.Split(card, "\n")
	if !strings.Contains(lines[1], "api") || !strings.Contains(lines[2], "mini · /srv/api") {
		t.Fatalf("remote card:\n%s", card)
	}
	if strings.Contains(card, "git") {
		t.Fatalf("the kind is back on the card:\n%s", card)
	}
}

// The cursor's card is the one heavy frame; the path's other card is
// thin and a shade behind it, border and title both; off the path, thin
// and quiet. Nothing is filled.
func TestTheCursorsCardIsHeavyAndNothingIsFilled(t *testing.T) {
	cursorFrame, cursorInk := cardGrade(false, true, false)
	otherFrame, otherInk := cardGrade(true, false, false)
	quietFrame, quietInk := cardGrade(false, false, false)
	dangerFrame, _ := cardGrade(true, true, true)
	if !cursorFrame.heavy || cursorFrame.ink != colorBand() {
		t.Fatal("the cursor's card is not the heavy silver frame")
	}
	if otherFrame.heavy || otherFrame.ink != colorBandSoft() {
		t.Fatal("the path's other card is not thin and a shade behind")
	}
	if quietFrame.heavy || quietFrame.ink != colorBorder() {
		t.Fatal("a card off the path is lit")
	}
	if !dangerFrame.heavy || dangerFrame.ink != colorFailed() {
		t.Fatal("a card awaiting delete confirmation is not heavy and red")
	}
	cursor, other, quiet := cursorInk.Render("t"), otherInk.Render("t"), quietInk.Render("t")
	if cursor == other || other == quiet || cursor == quiet {
		t.Fatal("the three title grades are not all distinct")
	}

	lit := renderAgentCard(agent.Agent{ID: "a", Provider: agent.ProviderCodex, Name: "hello", Cwd: "/x"},
		true, true, 40, false, -1)
	lines := strings.Split(ansi.Strip(lit), "\n")
	if !strings.HasPrefix(lines[0], "┏") || !strings.HasPrefix(lines[1], "┃") || !strings.HasPrefix(lines[3], "┗") {
		t.Fatalf("the cursor's card is not drawn heavy:\n%s", ansi.Strip(lit))
	}
	for _, line := range strings.Split(lit, "\n") {
		if strings.Contains(line, "[48;") {
			t.Fatalf("a background fill inside a card: %q", line)
		}
	}
	thin := ansi.Strip(renderAgentCard(agent.Agent{ID: "a", Provider: agent.ProviderCodex, Name: "hello"},
		true, false, 40, false, -1))
	if !strings.HasPrefix(thin, "╭") {
		t.Fatalf("the path's other card is not drawn thin:\n%s", thin)
	}
}

// The dimming of the pane the cursor has left keeps the whole selected
// card lit, border included.
func TestTheDimmingKeepsTheWholeCard(t *testing.T) {
	model := NewModel(stubBackend{})
	model.rowsExpanded = true
	rows := model.selectedRowRange(3, 1, 20)
	if rows.start != cardRows || rows.count != cardRows {
		t.Fatalf("undimmed rows = %+v, want the second card's four rows", rows)
	}
}

func TestAPathShortensParentsBeforeItsName(t *testing.T) {
	path := "/Volumes/repos/shared/alpha-service"
	cases := []struct {
		width int
		want  string
	}{
		{60, path},
		{24, "/V/r/s/alpha-service"},
		{19, "…/alpha-service"},
		{14, "alpha-service"},
		{10, "…a-service"},
	}
	for _, c := range cases {
		if got := shortenPath(path, c.width); got != c.want {
			t.Fatalf("width %d: %q, want %q", c.width, got, c.want)
		}
	}
	if got := shortenPath("~/notes/daily", 10); got != "~/n/daily" {
		t.Fatalf("home path = %q", got)
	}
}

// A list too short for one card holds compact rows instead of a card
// with its bottom cut off; the connector and the dimming agree.
func TestAListTooShortForACardShowsCompactRows(t *testing.T) {
	workspaceContext := workspace.DirectoryContext("/workspace")
	model := NewModel(stubBackend{})
	model.activePane = paneAgents
	model.agents = []agent.Agent{{ID: "one", Name: "one", Workspace: workspaceContext}}
	model.rebuildGroups(workspaceContext.ID, "one")
	model.rowsExpanded = true

	rendered := ansi.Strip(model.renderAgents(40, 3))
	if strings.ContainsAny(rendered, "╭┏") || strings.Count(rendered, "\n") > 0 {
		t.Fatalf("a three-row list drew a card:\n%s", rendered)
	}
	if rows := model.selectedRowRange(1, 0, 3); rows.count != 1 {
		t.Fatalf("dimming for a compact fallback = %+v", rows)
	}
}

// A pane narrower than the frame gets the frame cut to the pane.
func TestACardNeverSpillsPastATinyPane(t *testing.T) {
	for _, width := range []int{1, 3, 4} {
		for index, line := range strings.Split(ansi.Strip(renderCard("t", "d", width, cardFrame{ink: colorBorder()})), "\n") {
			if got := lipgloss.Width(line); got > width {
				t.Fatalf("width %d: line %d is %d wide: %q", width, index, got, line)
			}
		}
	}
}
