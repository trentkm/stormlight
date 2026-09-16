package ui

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/trentkm/stormlight/internal/agent"
)

// dashboardActionBackend records the dispatcher's writes to the agent
// document: the claim before the run and the acknowledgement after.
type dashboardActionBackend struct {
	stubBackend
	claims []string
	acks   []string
}

func (b *dashboardActionBackend) ClaimDashboardAction(
	_ context.Context,
	agentID, requestID, claimer string,
) error {
	if claimer == "" {
		return errors.New("a claim needs a claimer")
	}
	b.claims = append(b.claims, agentID+"/"+requestID)
	return nil
}

func (b *dashboardActionBackend) AcknowledgeDashboardAction(
	_ context.Context,
	agentID, requestID string,
) error {
	b.acks = append(b.acks, agentID+"/"+requestID)
	return nil
}

func TestDashboardActionRunsThroughTheDispatcher(t *testing.T) {
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
			return nil
		},
	})
	request := agent.DashboardActionRequest{
		ID:      "request-one",
		Name:    "review-diff",
		Payload: json.RawMessage(`{"paths":["/remote/repository"]}`),
	}
	managedAgent := agent.Agent{
		ID:               "agent-one",
		Name:             "review fixer",
		Host:             "cloud",
		DashboardActions: []agent.DashboardActionRequest{request},
	}

	command := model.nextDashboardActionCmd([]agent.Agent{managedAgent})
	if command == nil {
		t.Fatal("the request was not scheduled")
	}
	// One at a time: the same roster proposes nothing more while this
	// one is in flight.
	if model.nextDashboardActionCmd([]agent.Agent{managedAgent}) != nil {
		t.Fatal("a second command was scheduled while the first was running")
	}
	message, ok := command().(dashboardActionMsg)
	if !ok || message.err != nil {
		t.Fatalf("command returned %#v", message)
	}
	if message.agentID != "agent-one" || message.requestID != "request-one" || message.action != "review-diff" {
		t.Fatalf("message = %#v", message)
	}
	if handledAgent.ID != managedAgent.ID ||
		handledAgent.Host != managedAgent.Host ||
		handledRequest.Name != request.Name ||
		string(handledRequest.Payload) != string(request.Payload) {
		t.Fatalf("handled agent=%#v request=%#v", handledAgent, handledRequest)
	}
	if len(backend.claims) != 1 || backend.claims[0] != "agent-one/request-one" ||
		len(backend.acks) != 1 || backend.acks[0] != "agent-one/request-one" {
		t.Fatalf("claims=%q acks=%q", backend.claims, backend.acks)
	}
}

func TestDashboardActionFailureIsReportedAndRetired(t *testing.T) {
	backend := &dashboardActionBackend{}
	model := NewModelWithOptions(backend, Options{
		RunDashboardAction: func(context.Context, agent.Agent, agent.DashboardActionRequest) error {
			return errors.New("plugin failed")
		},
	})
	managedAgent := agent.Agent{
		ID:   "agent-one",
		Name: "review fixer",
		DashboardActions: []agent.DashboardActionRequest{{
			ID: "request-one", Name: "review-diff", Payload: json.RawMessage(`{}`),
		}},
	}
	message := model.nextDashboardActionCmd([]agent.Agent{managedAgent})().(dashboardActionMsg)
	if message.err == nil || !strings.Contains(message.err.Error(), "plugin failed") {
		t.Fatalf("error = %v", message.err)
	}
	if len(backend.acks) != 1 || backend.acks[0] != "agent-one/request-one" {
		t.Fatalf("a failed run was not retired: acks=%q", backend.acks)
	}
}

func TestDashboardActionsAreOffWithoutARunner(t *testing.T) {
	model := NewModelWithOptions(&dashboardActionBackend{}, Options{})
	managedAgent := agent.Agent{
		ID:               "agent-one",
		DashboardActions: []agent.DashboardActionRequest{{ID: "request-one", Name: "review-diff"}},
	}
	if model.nextDashboardActionCmd([]agent.Agent{managedAgent}) != nil {
		t.Fatal("a client with no runner scheduled a dashboard action")
	}
}
