package ui

import (
	"context"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/trentkm/stormlight/internal/agent"
	"github.com/trentkm/stormlight/internal/workspace"
)

// reachingBackend is a dashboard's worth of machines where one of them
// has not answered yet.
type reachingBackend struct {
	stubBackend
	agents       []agent.Agent
	workspaces   []workspace.Context
	hosts        []string
	agentCalls   int
	catalogCalls int
	rootCalls    int
}

func (b *reachingBackend) ListAgents(context.Context) ([]agent.Agent, error) {
	b.agentCalls++
	return b.agents, nil
}

func (b *reachingBackend) ListWorkspaces(context.Context) ([]workspace.Context, error) {
	b.catalogCalls++
	return b.workspaces, nil
}

func (b *reachingBackend) ListWorkspaceRoots(
	context.Context,
) ([]workspace.Context, error) {
	b.rootCalls++
	return b.workspaces, nil
}

func (b *reachingBackend) Reaching() []string { return b.hosts }

func reachingFixture(t *testing.T, backend Backend) Model {
	t.Helper()
	model := NewModel(backend)
	model.ptyEnabled = false
	updated, _ := model.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	return updated.(Model)
}

// TestBeforeTheFirstAnswerTheDashboardIsLoadingNotEmpty: the panes are
// empty for the length of one refresh, and for that moment "No agents"
// is a statement of fact that happens to be false. Nothing has been
// asked yet, which is a different sentence and the only true one.
func TestBeforeTheFirstAnswerTheDashboardIsLoadingNotEmpty(t *testing.T) {
	model := reachingFixture(t, &reachingBackend{})

	workspaces := ansi.Strip(model.renderWorkspaces(30, 12))
	agents := ansi.Strip(model.renderAgents(40, 12))
	if !strings.Contains(workspaces, "Reaching") {
		t.Fatalf("workspaces before the first refresh = %q", workspaces)
	}
	if !strings.Contains(agents, "Reaching") {
		t.Fatalf("agents before the first refresh = %q", agents)
	}
	if strings.Contains(workspaces, "No workspaces") ||
		strings.Contains(agents, "No agents") {
		t.Fatalf("a dashboard that has not asked must not answer:\n%s\n%s",
			workspaces, agents)
	}
}

// TestAnAnsweredDashboardSaysItIsEmptyPlainly: the loading state is for
// not knowing. Once every machine has answered and there is genuinely
// nothing there, say so — a spinner that never stops is worse than the
// empty word it replaced.
func TestAnAnsweredDashboardSaysItIsEmptyPlainly(t *testing.T) {
	model := reachingFixture(t, &reachingBackend{})
	updated, _ := model.Update(dashboardMsg{})
	model = updated.(Model)
	updated, _ = model.Update(catalogMsg{})
	model = updated.(Model)

	workspaces := ansi.Strip(model.renderWorkspaces(30, 12))
	agents := ansi.Strip(model.renderAgents(40, 12))
	if !strings.Contains(workspaces, "No workspaces") {
		t.Fatalf("workspaces = %q", workspaces)
	}
	if !strings.Contains(agents, "No agents") {
		t.Fatalf("agents = %q", agents)
	}
}

func TestRosterCanLoadBeforeCatalogWithoutClaimingNoWorkspaces(t *testing.T) {
	model := reachingFixture(t, &reachingBackend{})
	updated, _ := model.Update(dashboardMsg{})
	model = updated.(Model)

	rendered := ansi.Strip(model.renderWorkspaces(30, 12))
	if !strings.Contains(rendered, "Reaching") ||
		strings.Contains(rendered, "No workspaces") {
		t.Fatalf("workspace catalog still loading = %q", rendered)
	}
}

// TestTheWorkspacesPaneNamesTheMachineItIsWaitingOn: a catalog entry on
// another machine contributes no row until that machine answers, so the
// pane is a short list that grows on its own a second later. Saying
// which machine is still coming is what makes the gap legible.
func TestTheWorkspacesPaneNamesTheMachineItIsWaitingOn(t *testing.T) {
	here := workspace.DirectoryContext("/tmp/here")
	model := reachingFixture(t, &reachingBackend{
		workspaces: []workspace.Context{here},
		hosts:      []string{"mini"},
	})
	updated, _ := model.Update(dashboardMsg{
		workspaces: []workspace.Context{here},
		reaching:   []string{"mini"},
	})
	model = updated.(Model)

	rendered := ansi.Strip(model.renderWorkspaces(40, 12))
	if !strings.Contains(rendered, "Reaching mini") {
		t.Fatalf("the machine still coming should be named: %q", rendered)
	}
	// And the workspaces that did answer are still drawn. A note about
	// one machine must not cost the rest of the list.
	if !strings.Contains(rendered, "here") {
		t.Fatalf("the answered workspaces went missing: %q", rendered)
	}
}

// TestAWorkspaceWhoseMachineIsStillAnsweringHasNoAgentsYet: the case
// this whole thing is about. The catalog remembers the workspace, so its
// row is there; the daemon on that machine has not been reached, so its
// agents are not. "No agents" is precisely the wrong thing to say.
func TestAWorkspaceWhoseMachineIsStillAnsweringHasNoAgentsYet(t *testing.T) {
	remote := workspace.DirectoryContext("/srv/api").OnHost("mini")
	model := reachingFixture(t, &reachingBackend{})
	updated, _ := model.Update(dashboardMsg{
		workspaces: []workspace.Context{remote},
		reaching:   []string{"mini"},
	})
	model = updated.(Model)

	if selected, ok := model.selectedWorkspace(); !ok || selected.Host != "mini" {
		t.Fatalf("the remote workspace should be selected: %+v", selected)
	}
	agents := ansi.Strip(model.renderAgents(40, 12))
	if !strings.Contains(agents, "Reaching mini") {
		t.Fatalf("agents pane = %q", agents)
	}
	if strings.Contains(agents, "No agents") {
		t.Fatalf("a machine still answering is not a machine with no agents: %q", agents)
	}
}

// TestAnAnsweredMachineDoesNotBorrowAnothersWaiting: the local workspace
// is complete the moment this machine answers, and a laptop elsewhere
// still connecting says nothing about it.
func TestAnAnsweredMachineDoesNotBorrowAnothersWaiting(t *testing.T) {
	here := workspace.DirectoryContext("/tmp/here")
	model := reachingFixture(t, &reachingBackend{})
	updated, _ := model.Update(dashboardMsg{
		workspaces: []workspace.Context{here},
		reaching:   []string{"mini"},
	})
	model = updated.(Model)

	agents := ansi.Strip(model.renderAgents(40, 12))
	if !strings.Contains(agents, "No agents") {
		t.Fatalf("a workspace on this machine has its whole answer: %q", agents)
	}
}

// TestTheDotsKeepMovingWhileAMachineIsBeingReached: the animation runs
// on the tick the working glow uses, which stops when no agent is
// working. A still spinner over a live handshake reads as a hang.
func TestTheDotsKeepMovingWhileAMachineIsBeingReached(t *testing.T) {
	model := reachingFixture(t, &reachingBackend{})
	updated, _ := model.Update(dashboardMsg{reaching: []string{"mini"}})
	model = updated.(Model)

	next, cmd := model.Update(shimmerTickMsg{})
	model = next.(Model)
	if !model.shimmerRunning || cmd == nil {
		t.Fatal("the tick must keep running while a machine is being reached")
	}

	// And stop once nothing is outstanding.
	settled, _ := model.Update(dashboardMsg{})
	model = settled.(Model)
	settled, _ = model.Update(catalogMsg{})
	model = settled.(Model)
	next, cmd = model.Update(shimmerTickMsg{})
	model = next.(Model)
	if model.shimmerRunning || cmd != nil {
		t.Fatal("nothing outstanding and no agent working: the tick should rest")
	}
}

// TestTheRefreshReportsWhatItCouldNotReach: the listing and the waiting
// are one answer. A refresh that returned early without saying so is how
// the dashboard came to draw an empty pane in the first place.
func TestTheRefreshReportsWhatItCouldNotReach(t *testing.T) {
	backend := &reachingBackend{hosts: []string{"mini"}}
	model := reachingFixture(t, backend)

	message := model.refreshCmd()()
	refresh, ok := message.(dashboardMsg)
	if !ok {
		t.Fatalf("refresh returned %T", message)
	}
	if len(refresh.reaching) != 1 || refresh.reaching[0] != "mini" {
		t.Fatalf("reaching = %v", refresh.reaching)
	}
}

func TestTheFastRefreshOnlyAsksForTheRoster(t *testing.T) {
	backend := &reachingBackend{}
	model := reachingFixture(t, backend)

	if _, ok := model.refreshCmd()().(dashboardMsg); !ok {
		t.Fatal("refresh must still return the dashboard roster message")
	}
	if backend.agentCalls != 1 || backend.catalogCalls != 0 || backend.rootCalls != 0 {
		t.Fatalf("refresh calls: agents=%d catalog=%d roots=%d",
			backend.agentCalls, backend.catalogCalls, backend.rootCalls)
	}

	if _, ok := model.catalogCmd()().(catalogMsg); !ok {
		t.Fatal("catalog load must have its own message")
	}
	if backend.catalogCalls != 1 || backend.rootCalls != 0 {
		t.Fatalf("catalog calls: catalog=%d roots=%d",
			backend.catalogCalls, backend.rootCalls)
	}
}

func TestOpeningTheDirectoryPickerLoadsExecutionRoots(t *testing.T) {
	backend := &reachingBackend{}
	model := reachingFixture(t, backend)

	_, cmd := model.beginDispatch(true)
	if cmd == nil {
		t.Fatal("opening the directory picker must request execution roots")
	}
	if _, ok := cmd().(workspaceRootsMsg); !ok {
		t.Fatal("execution-root load must have its own message")
	}
	if backend.rootCalls != 1 {
		t.Fatalf("root calls = %d, want 1", backend.rootCalls)
	}
}
