package app

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/trentkm/stormlight/internal/agent"
	"github.com/trentkm/stormlight/internal/history"
	"github.com/trentkm/stormlight/internal/provider"
	"github.com/trentkm/stormlight/internal/session"
	"github.com/trentkm/stormlight/internal/workspace"
)

type recordingRuntime struct {
	session.Runtime
	agents      []agent.Agent
	workspaceID string
}

func (r *recordingRuntime) Update(
	context.Context,
	string,
	session.Update,
) error {
	return nil
}

func (r *recordingRuntime) ListAgents(
	context.Context,
) ([]agent.Agent, error) {
	return r.agents, nil
}

func (r *recordingRuntime) SetWorkspace(
	_ context.Context,
	id string,
	_ workspace.Context,
) error {
	r.workspaceID = id
	return nil
}

func TestWorkspaceBackfillUsesRuntimeNeutralAgentID(t *testing.T) {
	current := &recordingRuntime{
		agents: []agent.Agent{{
			ID:       "agent-one",
			WindowID: "@17",
			Cwd:      t.TempDir(),
		}},
	}
	service := NewService(
		current,
		provider.NewRegistry(),
		workspace.NewRegistry(),
	)

	agents, err := service.ListAgents(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(agents) != 1 || !agents[0].Workspace.IsComplete() {
		t.Fatalf("workspace was not resolved: %#v", agents)
	}
	if current.workspaceID != "agent-one" {
		t.Fatalf(
			"workspace persisted with runtime handle %q, want agent ID",
			current.workspaceID,
		)
	}
}

func TestUpdateRecordsSessionHistory(t *testing.T) {
	current := &recordingRuntime{
		agents: []agent.Agent{{
			ID:        "agent-one",
			Provider:  agent.ProviderClaude,
			Name:      "cl-tests",
			Task:      "Run the tests",
			Cwd:       t.TempDir(),
			SessionID: "3308ff3d-2cbc-47ab-81b1-a8fa28940a14",
			Workspace: workspace.DirectoryContext("/workspace/project"),
		}},
	}
	log := history.NewLogAt(filepath.Join(t.TempDir(), "sessions.jsonl"))
	service := NewServiceWithCatalog(
		current,
		provider.NewRegistry(),
		workspace.NewRegistry(),
		workspace.NewCatalogAt(filepath.Join(t.TempDir(), "workspaces.json")),
		log,
	)

	if err := service.Update(
		context.Background(),
		"agent-one",
		session.Update{Activity: agent.ActivityWorking},
	); err != nil {
		t.Fatal(err)
	}

	records, err := log.Records()
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 ||
		records[0].SessionID != "3308ff3d-2cbc-47ab-81b1-a8fa28940a14" ||
		records[0].Task != "Run the tests" {
		t.Fatalf("records = %#v", records)
	}

	// While the window is alive its session is not history yet.
	past, err := service.SessionHistory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(past) != 0 {
		t.Fatalf("live session listed as history: %#v", past)
	}

	// The window going away is what hands the session to the log.
	current.agents = nil
	past, err = service.SessionHistory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(past) != 1 || past[0].SessionID != records[0].SessionID {
		t.Fatalf("past = %#v", past)
	}
}
