package agent

import (
	"cmp"
	"slices"
	"time"
)

// Stats buckets a set of agents by the state a human reads them as. The
// buckets partition the set — every agent lands in exactly one, chosen by
// the same precedence the dashboard's row glyph uses — so a summary built
// from them adds up to the agents it describes.
//
// It lives here rather than in the dashboard because it is no longer the
// dashboard's alone: the header counts and the managed session's tmux status
// bar are the same tally shown in two places, and two derivations of it
// would drift.
type Stats struct {
	// Working is running, by Stormlight's reading or the human's.
	Working int
	// Urgent is blocked on an explicit human decision.
	Urgent int
	// Waiting is a finished turn the human has not seen, including rows the
	// human flagged themselves.
	Waiting int
	// Idle is alive with nothing pending on anyone.
	Idle int
	// Exited is a dead pane, whose exit status is its own story.
	Exited int
}

// Attention is the size of the amber inbox: everything pending on a human.
func (s Stats) Attention() int { return s.Urgent + s.Waiting }

// Total is every agent counted.
func (s Stats) Total() int {
	return s.Working + s.Urgent + s.Waiting + s.Idle + s.Exited
}

// Count buckets agents for display, marks included: a row the human
// corrected is corrected in the counters too, or the summary would keep
// reporting the reading they overruled.
func Count(agents []Agent) Stats {
	var stats Stats
	for _, managedAgent := range agents {
		switch {
		case managedAgent.EffectiveMark() == MarkWorking:
			stats.Working++
		case managedAgent.EffectiveMark() == MarkAttention:
			stats.Waiting++
		case !managedAgent.ProcessLive:
			stats.Exited++
		case managedAgent.Attention.Urgent():
			stats.Urgent++
		case managedAgent.Attention == AttentionWaiting:
			stats.Waiting++
		case managedAgent.Activity == ActivityWorking,
			managedAgent.Activity == ActivityStarting:
			stats.Working++
		case managedAgent.Activity == ActivityIdle:
			stats.Idle++
		default:
			stats.Exited++
		}
	}
	return stats
}

// Queue is the agents pending on a human, oldest first.
//
// The order is arrival order — when the agent started waiting, not when it
// was dispatched — so working the queue front to back answers whoever has
// been held up longest. AttentionAt is the stamp for that; an agent that
// raised attention before Stormlight recorded stamps falls back to its
// creation time, which keeps a partial record ordered rather than random.
func Queue(agents []Agent) []Agent {
	queue := make([]Agent, 0, len(agents))
	for _, managedAgent := range agents {
		if managedAgent.ProcessLive && managedAgent.NeedsAttention() {
			queue = append(queue, managedAgent)
		}
	}
	slices.SortStableFunc(queue, func(a, b Agent) int {
		if d := queuedAt(a).Compare(queuedAt(b)); d != 0 {
			return d
		}
		return cmp.Compare(a.ID, b.ID)
	})
	return queue
}

func queuedAt(a Agent) time.Time {
	if a.AttentionAt.IsZero() {
		return a.CreatedAt
	}
	return a.AttentionAt
}

// NextInQueue picks the agent to hand a human next, given a queue from Queue
// and the agent they are looking at now.
//
// From inside the queue it advances one place and wraps, so a key pressed
// repeatedly walks the whole inbox even where arriving does not clear the
// amber — an urgent prompt stays urgent until it is answered, and a cycle
// that kept landing on it would never reach anything else. From anywhere
// else it hands back the head, which is the plain first-in-first-out
// answer.
func NextInQueue(queue []Agent, currentID string) (Agent, bool) {
	if len(queue) == 0 {
		return Agent{}, false
	}
	for index, managedAgent := range queue {
		if managedAgent.ID != currentID {
			continue
		}
		if len(queue) == 1 {
			return Agent{}, false
		}
		return queue[(index+1)%len(queue)], true
	}
	return queue[0], true
}
