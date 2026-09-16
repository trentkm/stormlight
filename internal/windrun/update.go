package windrun

import (
	"slices"
	"strings"
	"time"

	"github.com/trentkm/stormlight/internal/agent"
	"github.com/trentkm/stormlight/internal/session"
)

// applyUpdate merges a session.Update into an agent, mirroring the
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
//
// The dashboard-action parts can refuse: a full queue, a claim on a
// request that is gone or held. A refusal leaves the agent untouched and
// is the error the caller sees; the runtime writes nothing for it.
func applyUpdate(managedAgent agent.Agent, update session.Update) (agent.Agent, error) {
	if update.Activity != "" {
		managedAgent.Activity = update.Activity
	}
	if update.DashboardAction != nil {
		if len(managedAgent.DashboardActions) >= agent.DashboardActionQueueLimit {
			return managedAgent, agent.ErrDashboardActionsFull
		}
		request := *update.DashboardAction
		request.Payload = append([]byte(nil), update.DashboardAction.Payload...)
		request.ClaimedBy = ""
		request.ClaimedAt = time.Time{}
		managedAgent.DashboardActions = append(
			slices.Clone(managedAgent.DashboardActions),
			request,
		)
	}
	if claim := update.ClaimDashboardAction; claim != nil {
		index := slices.IndexFunc(managedAgent.DashboardActions,
			func(request agent.DashboardActionRequest) bool {
				return request.ID == claim.RequestID
			})
		if index < 0 {
			return managedAgent, agent.ErrDashboardActionGone
		}
		queued := slices.Clone(managedAgent.DashboardActions)
		// Held is held, by whoever holds it — this claimer included. A
		// dashboard claims once per run; a second claim from the same
		// one is a stale roster proposing a request it already ran, and
		// letting it through is running the request twice.
		if queued[index].Held(claim.At) {
			return managedAgent, agent.ErrDashboardActionHeld
		}
		queued[index].ClaimedBy = claim.By
		queued[index].ClaimedAt = claim.At
		managedAgent.DashboardActions = queued
	}
	if update.ClearDashboardAction != "" {
		managedAgent.DashboardActions = slices.DeleteFunc(
			slices.Clone(managedAgent.DashboardActions),
			func(request agent.DashboardActionRequest) bool {
				return request.ID == update.ClearDashboardAction
			})
		if len(managedAgent.DashboardActions) == 0 {
			managedAgent.DashboardActions = nil
		}
	}
	if update.SessionID != "" {
		managedAgent.SessionID = update.SessionID
	}
	if update.SessionName != "" {
		managedAgent.SessionName = update.SessionName
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
	return managedAgent, nil
}
