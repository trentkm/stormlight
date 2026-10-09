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
		card := ansi.Strip(renderCard("title", "detail", width, colorBorder(), nil))
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
	card := ansi.Strip(renderCard(strings.Repeat("x", 40), "d", 20, colorBorder(), nil))
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
	// Tighter than the path itself: cut from the left, tail kept.
	got = cardDetail([]string{"codex", "AUTO", "working"}, path, 14)
	if strings.Contains(got, "codex") || !strings.HasPrefix(got, "…") ||
		!strings.HasSuffix(got, "alpha-service") || lipgloss.Width(got) > 14 {
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

func TestAnAgentsPathIsWhereItRuns(t *testing.T) {
	value := agent.Agent{Cwd: "/tmp/cwd"}
	if got := agentPath(value); got != "/tmp/cwd" {
		t.Fatalf("cwd only = %q", got)
	}
	value.Workspace = workspace.Context{ID: "w", Kind: "git", Name: "repo", Root: "/repo",
		ExecutionRoot: "/repo-worktrees/fix"}
	if got := agentPath(value); got != "/repo-worktrees/fix" {
		t.Fatalf("worktree = %q", got)
	}
	value.Workspace.ComponentRoot = "/repo-worktrees/fix/src/parser"
	if got := agentPath(value); got != "/repo-worktrees/fix/src/parser" {
		t.Fatalf("component = %q", got)
	}
}

func TestAnIdleAgentCardHasNoStrayState(t *testing.T) {
	card := ansi.Strip(renderAgentCard(agent.Agent{
		ID: "a", Provider: agent.ProviderCodex, Name: "hello", Cwd: "/Users/me/src/app",
		ProcessLive: true,
	}, false, false, 40, false, -1))
	lines := strings.Split(card, "\n")
	if strings.Contains(lines[2], "· ·") || !strings.Contains(lines[2], "codex · ") ||
		!strings.Contains(lines[2], "src/app") {
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
