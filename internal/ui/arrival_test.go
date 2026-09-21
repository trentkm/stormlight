package ui

// A launched agent takes the cursor. The backend answers a dispatch or a
// resume before the roster lists the newcomer, so the tests here drive the
// two messages apart: the launch, then the refresh that carries it.

import (
	"context"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/trentkm/stormlight/internal/agent"
	"github.com/trentkm/stormlight/internal/app"
	"github.com/trentkm/stormlight/internal/history"
	"github.com/trentkm/stormlight/internal/workspace"
)

var (
	arrivalAlpha = workspace.DirectoryContext("/repos/alpha")
	arrivalBeta  = workspace.DirectoryContext("/repos/beta")
)

func arrivalAgent(id string, ws workspace.Context) agent.Agent {
	return agent.Agent{
		ID:        id,
		Provider:  agent.Provider("claude"),
		Name:      id,
		Task:      "task " + id,
		Cwd:       ws.ExecutionRoot,
		Workspace: ws,
	}
}

// arrivalFixture is a dashboard looking at a1 in alpha, with a2 in beta
// below it — so a launch into beta has to move both cursors to be seen.
func arrivalFixture(t *testing.T) Model {
	t.Helper()
	model := NewModel(stubBackend{})
	model.catalogWorkspaces = []workspace.Context{arrivalAlpha, arrivalBeta}
	next, _ := model.Update(dashboardMsg{agents: []agent.Agent{
		arrivalAgent("a1", arrivalAlpha),
		arrivalAgent("a2", arrivalBeta),
	}})
	model = next.(Model)
	if got := model.selectedAgentID(); got != "a1" {
		t.Fatalf("fixture selected %q, want a1", got)
	}
	return model
}

func TestLaunchedAgentTakesTheCursorWhenTheRosterHasIt(t *testing.T) {
	model := arrivalFixture(t)
	launched := arrivalAgent("a3", arrivalBeta)

	next, _ := model.Update(launchedMsg{agent: launched})
	model = next.(Model)
	if got := model.selectedAgentID(); got != "a1" {
		t.Fatalf("selection moved before the roster had the agent: %q", got)
	}

	next, _ = model.Update(dashboardMsg{agents: []agent.Agent{
		arrivalAgent("a1", arrivalAlpha),
		arrivalAgent("a2", arrivalBeta),
		launched,
	}})
	model = next.(Model)
	if got := model.selectedAgentID(); got != "a3" {
		t.Fatalf("selected %q after the roster carried the launch, want a3", got)
	}
	if got := model.selectedWorkspaceID(); got != arrivalBeta.ID {
		t.Fatalf("workspace cursor on %q, want the launch's %q", got, arrivalBeta.ID)
	}
	if model.arrival.agentID != "" {
		t.Fatalf("arrival still pending after it was taken: %+v", model.arrival)
	}
}

func TestLaunchedAgentOutlastsARosterWithoutIt(t *testing.T) {
	model := arrivalFixture(t)
	launched := arrivalAgent("a3", arrivalBeta)
	next, _ := model.Update(launchedMsg{agent: launched})
	model = next.(Model)

	// A refresh that was already in flight when the launch answered
	// lists the fleet as it was; the next one has the newcomer.
	stale := []agent.Agent{
		arrivalAgent("a1", arrivalAlpha),
		arrivalAgent("a2", arrivalBeta),
	}
	next, _ = model.Update(dashboardMsg{agents: stale})
	model = next.(Model)
	if got := model.selectedAgentID(); got != "a1" {
		t.Fatalf("a roster without the launch moved the cursor to %q", got)
	}
	next, _ = model.Update(dashboardMsg{agents: append(stale, launched)})
	model = next.(Model)
	if got := model.selectedAgentID(); got != "a3" {
		t.Fatalf("selected %q once the launch appeared, want a3", got)
	}
}

func TestLaunchedAgentDoesNotPullBackACursorThatMoved(t *testing.T) {
	model := arrivalFixture(t)
	launched := arrivalAgent("a3", arrivalBeta)
	next, _ := model.Update(launchedMsg{agent: launched})
	model = next.(Model)

	// The user went looking at a2 while the launch was still landing.
	model.rebuildGroups(arrivalBeta.ID, "a2")
	if got := model.selectedAgentID(); got != "a2" {
		t.Fatalf("could not move the cursor to a2: %q", got)
	}

	next, _ = model.Update(dashboardMsg{agents: []agent.Agent{
		arrivalAgent("a1", arrivalAlpha),
		arrivalAgent("a2", arrivalBeta),
		launched,
	}})
	model = next.(Model)
	if got := model.selectedAgentID(); got != "a2" {
		t.Fatalf("the launch pulled a moved cursor to %q", got)
	}
	if model.arrival.agentID != "" {
		t.Fatalf("a forfeited arrival is still pending: %+v", model.arrival)
	}
}

func TestLaunchedAgentIsForgottenAfterThePatienceRunsOut(t *testing.T) {
	model := arrivalFixture(t)
	launched := arrivalAgent("a3", arrivalBeta)
	next, _ := model.Update(launchedMsg{agent: launched})
	model = next.(Model)
	model.arrival.until = time.Now().Add(-time.Second)

	next, _ = model.Update(dashboardMsg{agents: []agent.Agent{
		arrivalAgent("a1", arrivalAlpha),
		arrivalAgent("a2", arrivalBeta),
		launched,
	}})
	model = next.(Model)
	if got := model.selectedAgentID(); got != "a1" {
		t.Fatalf("an expired arrival still moved the cursor to %q", got)
	}
	if model.arrival.agentID != "" {
		t.Fatalf("an expired arrival is still pending: %+v", model.arrival)
	}
}

func TestLaunchFailureRaisesAndLeavesTheCursor(t *testing.T) {
	model := arrivalFixture(t)
	next, _ := model.Update(launchedMsg{err: context.DeadlineExceeded})
	model = next.(Model)
	if model.alert.err == nil {
		t.Fatal("a failed launch raised nothing")
	}
	if model.arrival.agentID != "" {
		t.Fatalf("a failed launch left an arrival pending: %+v", model.arrival)
	}
}

// launchBackend answers dispatch and resume with a named agent so the
// commands' messages can be checked for carrying it.
type launchBackend struct {
	stubBackend
}

func (launchBackend) Dispatch(_ context.Context, request app.DispatchRequest) (agent.Agent, error) {
	return agent.Agent{ID: "dispatched", Name: request.Name}, nil
}

func (launchBackend) Resume(_ context.Context, record history.Record) (agent.Agent, error) {
	return agent.Agent{ID: "resumed", Name: record.Name}, nil
}

func TestDispatchCommandReportsTheLaunchedAgent(t *testing.T) {
	msg := dispatchCmd(launchBackend{}, app.DispatchRequest{Name: "fresh"})()
	launched, ok := msg.(launchedMsg)
	if !ok {
		t.Fatalf("dispatch answered %T, want launchedMsg", msg)
	}
	if launched.err != nil || launched.agent.ID != "dispatched" {
		t.Fatalf("dispatch answered %+v", launched)
	}
}

func TestResumeReportsTheLaunchedAgent(t *testing.T) {
	model := NewModel(launchBackend{})
	model.historyRecords = []history.Record{{SessionID: "s1", Name: "old"}}
	_, cmd := model.resumeSelectedHistory()
	if cmd == nil {
		t.Fatal("resume returned no command")
	}
	// The batch holds the resume and a refresh; the resume is the one
	// that answers with an agent.
	found := false
	for _, msg := range messagesOf(cmd) {
		if launched, ok := msg.(launchedMsg); ok {
			found = true
			if launched.err != nil || launched.agent.ID != "resumed" {
				t.Fatalf("resume answered %+v", launched)
			}
		}
	}
	if !found {
		t.Fatal("resume never answered with a launchedMsg")
	}
}

// messagesOf runs a command tree and gathers every message it produces,
// following tea.Batch down so a batched answer is not hidden behind the
// batch itself.
func messagesOf(cmd tea.Cmd) []tea.Msg {
	if cmd == nil {
		return nil
	}
	msg := cmd()
	batch, ok := msg.(tea.BatchMsg)
	if !ok {
		return []tea.Msg{msg}
	}
	var out []tea.Msg
	for _, item := range batch {
		out = append(out, messagesOf(item)...)
	}
	return out
}
