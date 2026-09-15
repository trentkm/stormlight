package ui

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/trentkm/stormlight/internal/agent"
)

type dashboardActionBackend struct {
	stubBackend
	agentID   string
	requestID string
}

func TestDashboardActionFailureIsAcknowledged(t *testing.T) {
	backend := &dashboardActionBackend{}
	model := NewModelWithOptions(backend, Options{
		RunDashboardAction: func(
			context.Context,
			agent.Agent,
			agent.DashboardActionRequest,
		) error {
			return errors.New("plugin failed")
		},
	})
	managedAgent := agent.Agent{
		ID:   "agent-one",
		Name: "review fixer",
		DashboardAction: &agent.DashboardActionRequest{
			ID:      "request-one",
			Name:    "review-diff",
			Payload: json.RawMessage(`{"path":"/repository"}`),
		},
	}

	message := model.nextDashboardActionCmd([]agent.Agent{managedAgent})().(dashboardActionMsg)
	if message.err == nil ||
		!strings.Contains(message.err.Error(), "plugin failed") {
		t.Fatalf("error = %v", message.err)
	}
	if backend.agentID != managedAgent.ID ||
		backend.requestID != managedAgent.DashboardAction.ID {
		t.Fatalf(
			"acknowledged agent=%q request=%q",
			backend.agentID,
			backend.requestID,
		)
	}
}

func TestDashboardActionIdentityIncludesTheAgent(t *testing.T) {
	model := NewModelWithOptions(&dashboardActionBackend{}, Options{
		RunDashboardAction: func(
			context.Context,
			agent.Agent,
			agent.DashboardActionRequest,
		) error {
			return nil
		},
	})
	request := &agent.DashboardActionRequest{
		ID:      "same-request",
		Name:    "review-diff",
		Payload: json.RawMessage(`{}`),
	}
	first := agent.Agent{
		ID:              "agent-one",
		DashboardAction: request,
	}
	second := agent.Agent{
		ID:              "agent-two",
		DashboardAction: request,
	}

	if command := model.nextDashboardActionCmd([]agent.Agent{first}); command == nil {
		t.Fatal("the first agent's request was not scheduled")
	}
	model.dashboardActionRunning = false
	if command := model.nextDashboardActionCmd(
		[]agent.Agent{first, second},
	); command == nil {
		t.Fatal("the second agent's request was mistaken for the first")
	}
}

func (b *dashboardActionBackend) AcknowledgeDashboardAction(
	_ context.Context,
	agentID, requestID string,
) error {
	b.agentID = agentID
	b.requestID = requestID
	return nil
}

func TestDashboardActionRunsAndIsAcknowledged(t *testing.T) {
	backend := &dashboardActionBackend{}
	var handledAgent agent.Agent
	var handledRequest agent.DashboardActionRequest
	model := NewModelWithOptions(backend, Options{
		RunDashboardAction: func(
			_ context.Context,
			managedAgent agent.Agent,
			request agent.DashboardActionRequest,
		) error {
			handledAgent = managedAgent
			handledRequest = request
			handledRequest.Payload = append([]byte(nil), request.Payload...)
			return nil
		},
	})
	request := &agent.DashboardActionRequest{
		ID:      "request-one",
		Name:    "review-diff",
		Payload: json.RawMessage(`{"paths":["/remote/repository"]}`),
	}
	managedAgent := agent.Agent{
		ID:              "agent-one",
		Name:            "review fixer",
		Host:            "cloud",
		DashboardAction: request,
	}

	command := model.nextDashboardActionCmd([]agent.Agent{managedAgent})
	if command == nil || !model.dashboardActionRunning {
		t.Fatal("the request was not scheduled")
	}
	result := command()
	message, ok := result.(dashboardActionMsg)
	if !ok {
		t.Fatalf("command returned %T", result)
	}
	if message.err != nil {
		t.Fatal(message.err)
	}
	if handledAgent.ID != managedAgent.ID ||
		handledAgent.Host != managedAgent.Host ||
		handledRequest.Name != request.Name ||
		string(handledRequest.Payload) != string(request.Payload) {
		t.Fatalf(
			"handled agent=%#v request=%#v",
			handledAgent,
			handledRequest,
		)
	}
	if backend.agentID != "agent-one" || backend.requestID != "request-one" {
		t.Fatalf(
			"acknowledged agent=%q request=%q",
			backend.agentID,
			backend.requestID,
		)
	}

	// The daemon listing may still contain the request for one poll after
	// acknowledgement. It must not run twice.
	model.dashboardActionRunning = false
	if repeated := model.nextDashboardActionCmd(
		[]agent.Agent{managedAgent},
	); repeated != nil {
		t.Fatal("the same request was scheduled twice")
	}
}
