package windrun

import (
	"testing"

	"github.com/trentkm/stormlight/internal/agent"
	"github.com/trentkm/stormlight/internal/session"
)

func TestApplyUpdateRecordsProviderSessionName(t *testing.T) {
	managedAgent := agent.Agent{
		Name:        "focused fixer",
		SessionName: "old name",
	}
	updated := applyUpdate(
		managedAgent,
		session.Update{SessionName: "focused fixer"},
	)
	if updated.SessionName != "focused fixer" {
		t.Fatalf("session name = %q", updated.SessionName)
	}
}

func TestApplyUpdateSetsAndAcknowledgesTheExactDashboardAction(t *testing.T) {
	managedAgent := agent.Agent{}
	request := agent.DashboardActionRequest{
		ID:      "request-one",
		Name:    "review-diff",
		Payload: []byte(`{"path":"/workspace/src/file.go"}`),
	}
	updated := applyUpdate(
		managedAgent,
		session.Update{DashboardAction: &request},
	)
	if updated.DashboardAction == nil ||
		updated.DashboardAction.ID != request.ID ||
		string(updated.DashboardAction.Payload) != string(request.Payload) {
		t.Fatalf("dashboard action = %#v", updated.DashboardAction)
	}

	// A late acknowledgement for an older request cannot erase this one.
	updated = applyUpdate(updated, session.Update{ClearDashboardAction: "older"})
	if updated.DashboardAction == nil {
		t.Fatal("a mismatched acknowledgement cleared the request")
	}
	updated = applyUpdate(
		updated,
		session.Update{ClearDashboardAction: request.ID},
	)
	if updated.DashboardAction != nil {
		t.Fatalf("request was not cleared: %#v", updated.DashboardAction)
	}
}
