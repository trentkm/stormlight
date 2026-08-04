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

func TestMarkOverridesDerivedTriage(t *testing.T) {
	for _, testCase := range []struct {
		name   string
		agent  Agent
		needs  bool
		rank   int
		effect Mark
	}{
		{
			name: "in progress stands down a stale question",
			agent: Agent{
				ProcessLive: true,
				Attention:   AttentionQuestion,
				Mark:        MarkWorking,
			},
			needs:  false,
			rank:   AttentionNone.Rank(),
			effect: MarkWorking,
		},
		{
			name: "needs attention flags a quiet agent",
			agent: Agent{
				ProcessLive: true,
				Activity:    ActivityIdle,
				Mark:        MarkAttention,
			},
			needs:  true,
			rank:   AttentionWaiting.Rank(),
			effect: MarkAttention,
		},
		{
			name: "a dead pane reports its exit, not a mark",
			agent: Agent{
				Activity: ActivityCompleted,
				Mark:     MarkWorking,
			},
			needs:  false,
			rank:   AttentionNone.Rank(),
			effect: MarkNone,
		},
		{
			name:   "unmarked agents defer to attention",
			agent:  Agent{ProcessLive: true, Attention: AttentionWaiting},
			needs:  true,
			rank:   AttentionWaiting.Rank(),
			effect: MarkNone,
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			if got := testCase.agent.EffectiveMark(); got != testCase.effect {
				t.Fatalf("EffectiveMark() = %q, want %q", got, testCase.effect)
			}
			if got := testCase.agent.NeedsAttention(); got != testCase.needs {
				t.Fatalf("NeedsAttention() = %v, want %v", got, testCase.needs)
			}
			if got := testCase.agent.TriageRank(); got != testCase.rank {
				t.Fatalf("TriageRank() = %d, want %d", got, testCase.rank)
			}
		})
	}
}

func TestSortLiftsMarkedAgentsOverDerivedState(t *testing.T) {
	now := time.Now()
	agents := []Agent{
		{ID: "completed", Activity: ActivityCompleted, CreatedAt: now},
		{
			ID:          "marked-working",
			Activity:    ActivityIdle,
			ProcessLive: true,
			Mark:        MarkWorking,
			CreatedAt:   now,
		},
		{
			ID:          "marked-attention",
			Activity:    ActivityIdle,
			ProcessLive: true,
			Mark:        MarkAttention,
			CreatedAt:   now,
		},
		{ID: "idle", Activity: ActivityIdle, CreatedAt: now},
	}

	Sort(agents)

	want := []string{"marked-attention", "marked-working", "idle", "completed"}
	for index := range want {
		if agents[index].ID != want[index] {
			t.Fatalf("position %d = %q, want %q", index, agents[index].ID, want[index])
		}
	}
}

func TestParseMarkAcceptsNamesAndRejectsUnknown(t *testing.T) {
	for value, want := range map[string]Mark{
		"":          MarkNone,
		"none":      MarkNone,
		"clear":     MarkNone,
		"working":   MarkWorking,
		"attention": MarkAttention,
	} {
		mark, err := ParseMark(value)
		if err != nil {
			t.Fatalf("ParseMark(%q) = %v", value, err)
		}
		if mark != want {
			t.Fatalf("ParseMark(%q) = %q, want %q", value, mark, want)
		}
	}
	for _, value := range []string{"Working", "in-progress", "attn"} {
		if _, err := ParseMark(value); err == nil {
			t.Fatalf("ParseMark(%q) was accepted", value)
		}
	}
	if MarkWorking.Label() != "in progress" ||
		MarkAttention.Label() != "needs attention" ||
		MarkNone.Label() != "" {
		t.Fatal("mark labels changed")
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
