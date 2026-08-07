package agent

import (
	"testing"
	"time"
)

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

func waiter(id string, attentionAt, createdAt int64) Agent {
	value := live(id, ActivityIdle, AttentionWaiting)
	value.CreatedAt = time.Unix(createdAt, 0).UTC()
	if attentionAt > 0 {
		value.AttentionAt = time.Unix(attentionAt, 0).UTC()
	}
	return value
}

func TestQueueOrdersByWhenWaitingStarted(t *testing.T) {
	// Dispatched last, waiting first: arrival order is the queue, not age.
	agents := []Agent{
		waiter("second", 200, 10),
		waiter("first", 100, 90),
		live("busy", ActivityWorking, AttentionNone),
		{ID: "dead", Attention: AttentionWaiting},
	}
	queue := Queue(agents)
	if len(queue) != 2 {
		t.Fatalf("queue = %#v", ids(queue))
	}
	if got := ids(queue); got[0] != "first" || got[1] != "second" {
		t.Fatalf("queue order = %#v", got)
	}
}

// An agent that raised attention before Stormlight recorded stamps still has
// a place in line: its creation time keeps a partial record ordered.
func TestQueueFallsBackToCreationTimeWithoutAStamp(t *testing.T) {
	queue := Queue([]Agent{
		waiter("stamped", 500, 900),
		waiter("unstamped", 0, 100),
	})
	if got := ids(queue); got[0] != "unstamped" || got[1] != "stamped" {
		t.Fatalf("queue order = %#v", got)
	}
}

func TestNextInQueueAdvancesAndWraps(t *testing.T) {
	queue := Queue([]Agent{
		waiter("a", 100, 0),
		waiter("b", 200, 0),
		waiter("c", 300, 0),
	})

	for _, test := range []struct{ from, want string }{
		{"", "a"},
		{"elsewhere", "a"},
		{"a", "b"},
		{"b", "c"},
		{"c", "a"},
	} {
		next, ok := NextInQueue(queue, test.from)
		if !ok || next.ID != test.want {
			t.Fatalf("next after %q = %q (%v), want %q", test.from, next.ID, ok, test.want)
		}
	}
}

func TestNextInQueueStopsWhenThereIsNowhereToGo(t *testing.T) {
	if _, ok := NextInQueue(nil, ""); ok {
		t.Fatal("empty queue offered an agent")
	}
	queue := Queue([]Agent{waiter("only", 100, 0)})
	if _, ok := NextInQueue(queue, "only"); ok {
		t.Fatal("the only waiting agent was offered as the next one")
	}
	if next, ok := NextInQueue(queue, "somewhere-else"); !ok || next.ID != "only" {
		t.Fatalf("next = %q (%v)", next.ID, ok)
	}
}

func ids(agents []Agent) []string {
	values := make([]string, len(agents))
	for index, managedAgent := range agents {
		values[index] = managedAgent.ID
	}
	return values
}
