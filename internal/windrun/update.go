package windrun

import (
	"strings"
	"time"

	"github.com/trentkm/stormlight/internal/agent"
	"github.com/trentkm/stormlight/internal/session"
)

// applyUpdate merges a session.Update into an agent, mirroring the tmux
// runtime's rules (runtime.go Update); the two should collapse into one
// shared function once this runtime has proven the shape. The rules that
// matter:
//
//   - Waiting is a floor, not a ceiling: a late soft signal never
//     downgrades an urgent question/approval/auth state — except at turn
//     end, which proves the prompt was resolved.
//   - Clearing attention retires an attention mark with it; a fresh
//     self-report retires an in-progress mark, because the agent is the
//     authority on what it is doing.
//   - AttentionAt records entry into the amber inbox, not the latest
//     signal, so escalations keep their place in line.
func applyUpdate(managedAgent agent.Agent, update session.Update) agent.Agent {
	if update.Activity != "" {
		managedAgent.Activity = update.Activity
	}
	if update.SessionID != "" {
		managedAgent.SessionID = update.SessionID
	}
	if update.TranscriptPath != "" {
		managedAgent.TranscriptPath = update.TranscriptPath
	}

	previousAttention := managedAgent.Attention
	switch {
	case update.ClearAttention:
		managedAgent.Attention = ""
	case update.Attention != "" || update.Activity != "":
		attention := update.Attention
		if attention == agent.AttentionWaiting &&
			previousAttention.Urgent() && !update.TurnEnded {
			attention = previousAttention
		}
		managedAgent.Attention = attention
	}

	switch {
	case update.ClearMark:
		managedAgent.Mark = ""
	case update.Mark != "":
		managedAgent.Mark = update.Mark
	case update.ClearAttention && managedAgent.Mark == agent.MarkAttention:
		managedAgent.Mark = ""
	case (update.Activity != "" || update.Attention != "") &&
		managedAgent.Mark == agent.MarkWorking:
		managedAgent.Mark = ""
	}

	if strings.TrimSpace(update.Summary) != "" {
		managedAgent.Summary = update.Summary
	}

	switch {
	case managedAgent.Attention == "":
		managedAgent.AttentionAt = time.Time{}
	case previousAttention == "" || managedAgent.AttentionAt.IsZero():
		managedAgent.AttentionAt = time.Now()
	}
	return managedAgent
}
