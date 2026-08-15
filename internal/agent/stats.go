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

// Queue is the attention inbox: the live agents waiting on a human,
// ordered by who has waited longest.
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

// QueueStep is which way a cycle moves through the inbox.
type QueueStep int

const (
	QueueForward QueueStep = 1
	QueueBack    QueueStep = -1
)

// StepInQueue picks the agent to hand a human next, given a queue from Queue,
// the agent they are looking at now, and which way they asked to go.
//
// Visiting an agent does not take it out of the queue — nothing about
// arriving answers what the agent is waiting for — so the queue holds still
// and the human's place in it is what moves. That is why a step is relative:
// pressing forward repeatedly walks the whole inbox and wraps, and pressing
// back returns to what was just looked at rather than to the front of a line
// that never shortened.
//
// From outside the queue there is no place to move from, so a step lands on
// the end it came from: forward on the head, which is the plain
// first-in-first-out answer, and back on the tail.
func StepInQueue(queue []Agent, currentID string, step QueueStep) (Agent, bool) {
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
		next := (index + int(step) + len(queue)) % len(queue)
		return queue[next], true
	}
	if step == QueueBack {
		return queue[len(queue)-1], true
	}
	return queue[0], true
}
