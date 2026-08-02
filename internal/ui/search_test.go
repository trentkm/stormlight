package ui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"github.com/trentkm/stormlight/internal/agent"
	"github.com/trentkm/stormlight/internal/workspace"
)

func TestSearchMatchLinesIgnoresCaseAndStyling(t *testing.T) {
	content := strings.Join([]string{
		"alpha ERROR one",
		"clean line",
		"\x1b[31merror\x1b[0m two",
	}, "\n")
	matches := searchMatchLines(content, "Error")
	if len(matches) != 2 || matches[0] != 0 || matches[1] != 2 {
		t.Fatalf("matches = %#v", matches)
	}
	if searchMatchLines(content, "  ") != nil {
		t.Fatal("blank query matched")
	}
}

func TestHighlightSearchMatchesKeepsTextAndPaintsMatches(t *testing.T) {
	content := strings.Join([]string{
		"alpha error one",
		"\x1b[31mstyled error\x1b[0m tail",
	}, "\n")
	highlighted := highlightSearchMatches(content, "error", 1)
	if ansi.Strip(highlighted) != ansi.Strip(content) {
		t.Fatalf("highlighting changed the text:\n%q\n%q",
			ansi.Strip(highlighted), ansi.Strip(content))
	}
	lines := strings.Split(highlighted, "\n")
	if !strings.Contains(lines[0], "\x1b[7m") {
		t.Fatalf("match line lacks reverse-video paint: %q", lines[0])
	}
	if lines[1] == "\x1b[31mstyled error\x1b[0m tail" {
		t.Fatal("current line was not repainted")
	}
}

func searchModelFixture(t *testing.T) Model {
	t.Helper()
	model := NewModel(stubBackend{})
	updated, _ := model.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	model = updated.(Model)

	workspaceContext := workspace.DirectoryContext("/tmp/project")
	model.agents = []agent.Agent{{
		ID:        "a1",
		Name:      "agent",
		Provider:  agent.ProviderClaude,
		Workspace: workspaceContext,
	}}
	model.rebuildGroups(workspaceContext.ID, "a1")
	model.activePane = paneInteraction
	model.interactionID = "a1"
	model.interactionContent = strings.Join([]string{
		"alpha error one",
		"clean line",
		"another ERROR two",
	}, "\n")
	model.interaction.SetContent(model.interactionContent)
	return model
}

func TestSlashSearchFlowMatchesJumpsAndClears(t *testing.T) {
	model := searchModelFixture(t)

	updated, _ := model.updateNormal(runeKey("/"))
	model = updated.(Model)
	if model.mode != modeSearch {
		t.Fatalf("mode after / = %d", model.mode)
	}

	next, _ := model.updateSearch(runeKey("error"))
	model = next.(Model)
	if model.search.query != "error" || len(model.search.matches) != 2 {
		t.Fatalf("live search state = %#v", model.search)
	}

	next, _ = model.updateSearch(tea.KeyMsg{Type: tea.KeyEnter})
	model = next.(Model)
	if model.mode != modeNormal || model.status != "Match 1/2" {
		t.Fatalf("confirm state: mode=%d status=%q", model.mode, model.status)
	}
	if !strings.Contains(model.searchDecorated(), "\x1b[7m") {
		t.Fatal("transcript is not highlighted after confirm")
	}

	updated, _ = model.updateNormal(runeKey("n"))
	model = updated.(Model)
	if model.status != "Match 2/2" {
		t.Fatalf("n did not advance: %q", model.status)
	}
	updated, _ = model.updateNormal(runeKey("n"))
	model = updated.(Model)
	if model.status != "Match 1/2" {
		t.Fatalf("n did not wrap: %q", model.status)
	}
	updated, _ = model.updateNormal(runeKey("N"))
	model = updated.(Model)
	if model.status != "Match 2/2" {
		t.Fatalf("N did not go back: %q", model.status)
	}

	updated, _ = model.updateNormal(tea.KeyMsg{Type: tea.KeyEscape})
	model = updated.(Model)
	if model.search.query != "" {
		t.Fatalf("esc kept the query: %q", model.search.query)
	}

	// With the search cleared, n returns to its dispatch meaning.
	updated, _ = model.updateNormal(runeKey("n"))
	model = updated.(Model)
	if model.mode != modeDispatch {
		t.Fatalf("n after clear = mode %d, want dispatch", model.mode)
	}
}

func TestSearchEscRestoresViewportOffset(t *testing.T) {
	model := searchModelFixture(t)
	model.interactionContent = strings.Repeat("filler\n", 80) + "needle at bottom"
	model.interaction.SetContent(model.interactionContent)
	model.interaction.SetYOffset(3)

	updated, _ := model.updateNormal(runeKey("/"))
	model = updated.(Model)
	next, _ := model.updateSearch(runeKey("needle"))
	model = next.(Model)
	if model.interaction.YOffset == 3 {
		t.Fatal("live search did not move the viewport")
	}
	next, _ = model.updateSearch(tea.KeyMsg{Type: tea.KeyEscape})
	model = next.(Model)
	if model.interaction.YOffset != 3 {
		t.Fatalf("esc offset = %d, want 3", model.interaction.YOffset)
	}
	if model.search.query != "" {
		t.Fatalf("esc kept the query: %q", model.search.query)
	}
}

func TestSearchSurvivesTranscriptRefresh(t *testing.T) {
	model := searchModelFixture(t)
	updated, _ := model.updateNormal(runeKey("/"))
	model = updated.(Model)
	next, _ := model.updateSearch(runeKey("error"))
	model = next.(Model)
	next, _ = model.updateSearch(tea.KeyMsg{Type: tea.KeyEnter})
	model = next.(Model)

	refreshed, _ := model.Update(interactionMsg{
		id:      "a1",
		content: "fresh error line\nquiet\nfresh error again\nerror three",
	})
	model = refreshed.(Model)
	if len(model.search.matches) != 3 {
		t.Fatalf("matches after refresh = %#v", model.search.matches)
	}
	if !strings.Contains(model.searchDecorated(), "\x1b[7m") {
		t.Fatal("refresh dropped the highlight")
	}
}
