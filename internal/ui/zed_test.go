package ui

import (
	"context"
	"slices"
	"testing"

	"github.com/trentkm/stormlight/internal/agent"
)

type zedRequestBackend struct {
	stubBackend
	agentID   string
	requestID string
}

func (b *zedRequestBackend) AcknowledgeZedDiff(
	_ context.Context,
	agentID, requestID string,
) error {
	b.agentID = agentID
	b.requestID = requestID
	return nil
}

func TestZedDiffRequestOpensOnTheDashboardAndIsAcknowledged(t *testing.T) {
	backend := &zedRequestBackend{}
	var openedHost string
	var openedPaths []string
	model := NewModelWithOptions(backend, Options{
		OpenZedDiff: func(
			_ context.Context,
			host string,
			paths []string,
		) error {
			openedHost = host
			openedPaths = append([]string(nil), paths...)
			return nil
		},
	})
	request := &agent.ZedDiffRequest{
		ID:    "request-one",
		Paths: []string{"/remote/workspace/src/Package/file.go"},
	}
	managedAgent := agent.Agent{
		ID:      "agent-one",
		Name:    "review fixer",
		Host:    "cloud",
		ZedDiff: request,
	}

	command := model.nextZedDiffCmd([]agent.Agent{managedAgent})
	if command == nil || !model.zedDiffRunning {
		t.Fatal("the request was not scheduled")
	}
	result := command()
	message, ok := result.(zedDiffMsg)
	if !ok {
		t.Fatalf("command returned %T", result)
	}
	if message.err != nil {
		t.Fatal(message.err)
	}
	if openedHost != "cloud" || !slices.Equal(openedPaths, request.Paths) {
		t.Fatalf("opened host=%q paths=%v", openedHost, openedPaths)
	}
	if backend.agentID != "agent-one" || backend.requestID != "request-one" {
		t.Fatalf("acknowledged agent=%q request=%q", backend.agentID, backend.requestID)
	}

	// The daemon listing may still contain the request for one poll after
	// acknowledgement. It must not open twice.
	model.zedDiffRunning = false
	if repeated := model.nextZedDiffCmd([]agent.Agent{managedAgent}); repeated != nil {
		t.Fatal("the same request was scheduled twice")
	}
}
