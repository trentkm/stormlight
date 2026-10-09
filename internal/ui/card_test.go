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
		card := ansi.Strip(renderCard("title", "detail", width, colorBorder()))
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
	card := ansi.Strip(renderCard(strings.Repeat("x", 40), "d", 20, colorBorder()))
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
	// A title built from the task is not repeated under itself.
	named := agent.Agent{ID: "b", Provider: agent.ProviderCodex, Name: "cx-fix-parser",
		Task: "Fix the parser", Activity: agent.ActivityWorking, ProcessLive: true}
	card = ansi.Strip(renderAgentCard(named, false, false, 48, false, -1))
	lines = strings.Split(card, "\n")
	if !strings.Contains(lines[1], "Fix the parser") || strings.Contains(lines[2], "Fix the parser") ||
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

// Both cards on the path are lit the same: the chosen workspace and the
// chosen agent in it, whichever pane the cursor is in. Nothing else is,
// and nothing is filled.
func TestThePathsCardsAreLitAndNothingIsFilled(t *testing.T) {
	if cardBorderFor(false, true, false) != colorBand() || cardBorderFor(true, false, false) != colorBandSoft() {
		t.Fatal("the path's cards are not lit, the cursor's brightest")
	}
	if cardBorderFor(true, false, false) == cardBorderFor(false, true, false) {
		t.Fatal("the cursor's card is not told apart from the other on the path")
	}
	if cardBorderFor(false, false, false) != colorBorder() {
		t.Fatal("a card off the path is lit")
	}
	if cardBorderFor(true, true, true) != colorFailed() {
		t.Fatal("a card awaiting delete confirmation is not red")
	}
	lit := renderAgentCard(agent.Agent{ID: "a", Provider: agent.ProviderCodex, Name: "hello", Cwd: "/x"},
		true, true, 40, false, -1)
	for _, line := range strings.Split(lit, "\n") {
		if strings.Contains(line, "[48;") {
			t.Fatalf("a background fill inside a card: %q", line)
		}
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
