package agent

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
