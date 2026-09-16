package windrun

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/trentkm/stormlight/internal/agent"
	"github.com/trentkm/stormlight/internal/session"
)

func TestApplyUpdateRecordsProviderSessionName(t *testing.T) {
	managedAgent := agent.Agent{
		Name:        "focused fixer",
		SessionName: "old name",
	}
	updated, err := applyUpdate(
		managedAgent,
		session.Update{SessionName: "focused fixer"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if updated.SessionName != "focused fixer" {
		t.Fatalf("session name = %q", updated.SessionName)
	}
}

func TestApplyUpdateQueuesClaimsAndRetiresDashboardActions(t *testing.T) {
	now := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)
	first := agent.DashboardActionRequest{
		ID:      "request-one",
		Name:    "review-diff",
		Payload: []byte(`{"path":"/workspace/src/file.go"}`),
		// A request arrives unclaimed whatever the caller put here.
		ClaimedBy: "forged",
		ClaimedAt: now,
	}
	managedAgent, err := applyUpdate(agent.Agent{}, session.Update{DashboardAction: &first})
	if err != nil {
		t.Fatal(err)
	}
	second := agent.DashboardActionRequest{ID: "request-two", Name: "review-diff", Payload: []byte(`{}`)}
	managedAgent, err = applyUpdate(managedAgent, session.Update{DashboardAction: &second})
	if err != nil {
		t.Fatal(err)
	}
	if len(managedAgent.DashboardActions) != 2 ||
		managedAgent.DashboardActions[0].ID != "request-one" ||
		managedAgent.DashboardActions[1].ID != "request-two" ||
		string(managedAgent.DashboardActions[0].Payload) != string(first.Payload) ||
		managedAgent.DashboardActions[0].ClaimedBy != "" ||
		!managedAgent.DashboardActions[0].ClaimedAt.IsZero() {
		t.Fatalf("queue = %#v", managedAgent.DashboardActions)
	}

	// A claim names the request; one on a request nobody queued is gone.
	if _, err := applyUpdate(managedAgent, session.Update{
		ClaimDashboardAction: &agent.DashboardActionClaim{RequestID: "older", By: "tui", At: now},
	}); !errors.Is(err, agent.ErrDashboardActionGone) {
		t.Fatalf("claim on a missing request: %v", err)
	}
	claimed, err := applyUpdate(managedAgent, session.Update{
		ClaimDashboardAction: &agent.DashboardActionClaim{RequestID: "request-one", By: "tui", At: now},
	})
	if err != nil {
		t.Fatal(err)
	}
	if head := claimed.DashboardActions[0]; head.ClaimedBy != "tui" || !head.ClaimedAt.Equal(now) {
		t.Fatalf("claim did not land: %#v", head)
	}
	if managedAgent.DashboardActions[0].ClaimedBy != "" {
		t.Fatal("the claim wrote through to the document it was derived from")
	}

	// While the claim is fresh it belongs to its holder; the holder may
	// restate it, nobody else may take it.
	if _, err := applyUpdate(claimed, session.Update{
		ClaimDashboardAction: &agent.DashboardActionClaim{RequestID: "request-one", By: "web", At: now.Add(time.Minute)},
	}); !errors.Is(err, agent.ErrDashboardActionHeld) {
		t.Fatalf("a second dashboard claiming a held request: %v", err)
	}
	if _, err := applyUpdate(claimed, session.Update{
		ClaimDashboardAction: &agent.DashboardActionClaim{RequestID: "request-one", By: "tui", At: now.Add(time.Minute)},
	}); err != nil {
		t.Fatalf("the holder restating its claim: %v", err)
	}
	// A claim past its TTL is a dashboard that died; the request is up
	// for taking again.
	stale := now.Add(agent.DashboardActionClaimTTL)
	taken, err := applyUpdate(claimed, session.Update{
		ClaimDashboardAction: &agent.DashboardActionClaim{RequestID: "request-one", By: "web", At: stale},
	})
	if err != nil || taken.DashboardActions[0].ClaimedBy != "web" {
		t.Fatalf("taking over a stale claim: %v, %#v", err, taken.DashboardActions[0])
	}

	// Retiring names the id: the newer request stays.
	retired, err := applyUpdate(taken, session.Update{ClearDashboardAction: "request-one"})
	if err != nil {
		t.Fatal(err)
	}
	if len(retired.DashboardActions) != 1 || retired.DashboardActions[0].ID != "request-two" {
		t.Fatalf("queue after retiring the head = %#v", retired.DashboardActions)
	}
	retired, err = applyUpdate(retired, session.Update{ClearDashboardAction: "request-two"})
	if err != nil || retired.DashboardActions != nil {
		t.Fatalf("an emptied queue = %#v, %v", retired.DashboardActions, err)
	}
}

func TestApplyUpdateRefusesAFullDashboardActionQueue(t *testing.T) {
	var managedAgent agent.Agent
	var err error
	for index := range agent.DashboardActionQueueLimit {
		managedAgent, err = applyUpdate(managedAgent, session.Update{
			DashboardAction: &agent.DashboardActionRequest{ID: fmt.Sprint("request-", index), Name: "open"},
		})
		if err != nil {
			t.Fatalf("request %d: %v", index, err)
		}
	}
	if _, err := applyUpdate(managedAgent, session.Update{
		DashboardAction: &agent.DashboardActionRequest{ID: "one-too-many", Name: "open"},
	}); !errors.Is(err, agent.ErrDashboardActionsFull) {
		t.Fatalf("the request past the limit: %v", err)
	}
	if len(managedAgent.DashboardActions) != agent.DashboardActionQueueLimit {
		t.Fatalf("the refused request landed: %d queued", len(managedAgent.DashboardActions))
	}
}
