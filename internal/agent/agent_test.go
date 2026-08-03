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

func TestAttentionTiersDriveUrgencyOwnershipAndRank(t *testing.T) {
	for _, testCase := range []struct {
		name          string
		attention     Attention
		urgent        bool
		terminalOwned bool
		rank          int
	}{
		{"approval", AttentionApproval, true, true, 0},
		{"auth", AttentionAuth, true, true, 0},
		// A plain question is urgent but answerable with text, so the
		// agent's own terminal does not own the input.
		{"question", AttentionQuestion, true, false, 0},
		{"waiting", AttentionWaiting, false, false, 1},
		{"none", AttentionNone, false, false, 2},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			if got := testCase.attention.Urgent(); got != testCase.urgent {
				t.Fatalf("Urgent() = %v, want %v", got, testCase.urgent)
			}
			if got := testCase.attention.TerminalOwned(); got != testCase.terminalOwned {
				t.Fatalf("TerminalOwned() = %v, want %v", got, testCase.terminalOwned)
			}
			if got := testCase.attention.Rank(); got != testCase.rank {
				t.Fatalf("Rank() = %d, want %d", got, testCase.rank)
			}
		})
	}
}

func TestParseModeDefaultsWhenUnsetAndRejectsUnknown(t *testing.T) {
	mode, err := ParseMode("")
	if err != nil || mode != DefaultMode {
		t.Fatalf("empty mode = %q, err = %v", mode, err)
	}

	for _, value := range []string{"ask", "edits", "auto"} {
		mode, err := ParseMode(value)
		if err != nil {
			t.Fatalf("ParseMode(%q) = %v", value, err)
		}
		if mode != PermissionMode(value) {
			t.Fatalf("ParseMode(%q) = %q", value, mode)
		}
	}

	for _, value := range []string{"yolo", "Auto", "edit"} {
		if _, err := ParseMode(value); err == nil {
			t.Fatalf("ParseMode(%q) was accepted", value)
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
