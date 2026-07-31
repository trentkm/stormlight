package app

import (
	"context"
	"testing"

	"github.com/trentkm/stormlight/internal/agent"
	"github.com/trentkm/stormlight/internal/provider"
	"github.com/trentkm/stormlight/internal/session"
	"github.com/trentkm/stormlight/internal/workspace"
)

type recordingRuntime struct {
	session.Runtime
	agents      []agent.Agent
	workspaceID string
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
