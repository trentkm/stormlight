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

func TestApplyUpdateSetsAndAcknowledgesTheExactZedRequest(t *testing.T) {
	managedAgent := agent.Agent{}
	request := agent.ZedDiffRequest{
		ID:    "request-one",
		Paths: []string{"/workspace/src/file.go"},
	}
	updated := applyUpdate(
		managedAgent,
		session.Update{ZedDiff: &request},
	)
	if updated.ZedDiff == nil || updated.ZedDiff.ID != request.ID {
		t.Fatalf("Zed request = %#v", updated.ZedDiff)
	}

	// A late acknowledgement for an older request cannot erase this one.
	updated = applyUpdate(updated, session.Update{ClearZedDiff: "older"})
	if updated.ZedDiff == nil {
		t.Fatal("a mismatched acknowledgement cleared the request")
	}
	updated = applyUpdate(updated, session.Update{ClearZedDiff: request.ID})
	if updated.ZedDiff != nil {
		t.Fatalf("request was not cleared: %#v", updated.ZedDiff)
	}
}
