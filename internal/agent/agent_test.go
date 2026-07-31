package agent

import (
	"testing"
	"time"
)

func TestSortPrioritizesAttentionThenActivity(t *testing.T) {
	now := time.Now()
	agents := []Agent{
		{ID: "completed", Activity: ActivityCompleted, CreatedAt: now},
		{ID: "working-old", Activity: ActivityWorking, CreatedAt: now.Add(-time.Hour)},
		{ID: "waiting", Activity: ActivityIdle, Attention: AttentionApproval, CreatedAt: now.Add(-2 * time.Hour)},
		{ID: "working-new", Activity: ActivityWorking, CreatedAt: now},
		{ID: "failed", Activity: ActivityFailed, CreatedAt: now},
	}

	Sort(agents)

	got := make([]string, len(agents))
	for i := range agents {
		got[i] = agents[i].ID
	}
	want := []string{"waiting", "working-new", "working-old", "completed", "failed"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("position %d: got %q, want %q (all: %v)", i, got[i], want[i], got)
		}
	}
}

func TestDisplaySummaryFallsBackToTask(t *testing.T) {
	managedAgent := Agent{Task: "  inspect the parser  "}
	if got := managedAgent.DisplaySummary(); got != "inspect the parser" {
		t.Fatalf("got %q", got)
	}
	managedAgent.Summary = "  running tests  "
	if got := managedAgent.DisplaySummary(); got != "running tests" {
		t.Fatalf("got %q", got)
	}
}
