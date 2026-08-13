package agent

import "testing"

func live(id string, activity Activity, attention Attention) Agent {
	return Agent{
		ID:          id,
		Activity:    activity,
		Attention:   attention,
		ProcessLive: true,
	}
}

func TestCountPartitionsAgentsByDisplayedState(t *testing.T) {
	agents := []Agent{
		live("working", ActivityWorking, AttentionNone),
		live("starting", ActivityStarting, AttentionNone),
		live("asked", ActivityIdle, AttentionQuestion),
		live("unseen", ActivityIdle, AttentionWaiting),
		live("quiet", ActivityIdle, AttentionNone),
		{ID: "gone", Activity: ActivityCompleted},
	}
	stats := Count(agents)
	want := Stats{Working: 2, Urgent: 1, Waiting: 1, Idle: 1, Exited: 1}
	if stats != want {
		t.Fatalf("stats = %+v, want %+v", stats, want)
	}
	if stats.Total() != len(agents) {
		t.Fatalf("total = %d, want %d", stats.Total(), len(agents))
	}
	if stats.Attention() != 2 {
		t.Fatalf("attention = %d, want 2", stats.Attention())
	}
}

// A mark is the human's own reading and outranks everything derived, in the
// counters exactly as in the rows — otherwise the tally would keep reporting
// the state they overruled.
func TestCountHonorsMarksOverDerivedState(t *testing.T) {
	stalled := live("stalled", ActivityIdle, AttentionWaiting)
	stalled.Mark = MarkWorking
	flagged := live("flagged", ActivityWorking, AttentionNone)
	flagged.Mark = MarkAttention

	stats := Count([]Agent{stalled, flagged})
	want := Stats{Working: 1, Waiting: 1}
	if stats != want {
		t.Fatalf("stats = %+v, want %+v", stats, want)
	}
}

// A dead pane has an exit status of its own to report; nothing is pending on
// a human because of it, and it is certainly not still working.
func TestCountLeavesDeadPanesOutOfLiveTiers(t *testing.T) {
	dead := Agent{ID: "dead", Activity: ActivityWorking, Attention: AttentionQuestion}
	dead.Mark = MarkWorking

	stats := Count([]Agent{dead})
	if stats != (Stats{Exited: 1}) {
		t.Fatalf("stats = %+v", stats)
	}
}
