package agent

import (
	"cmp"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/trentkm/stormlight/internal/workspace"
)

type Provider string

const (
	ProviderClaude Provider = "claude"
	ProviderCodex  Provider = "codex"
)

type Activity string

const (
	ActivityStarting  Activity = "starting"
	ActivityWorking   Activity = "working"
	ActivityIdle      Activity = "idle"
	ActivityCompleted Activity = "completed"
	ActivityFailed    Activity = "failed"
	ActivityStopped   Activity = "stopped"
)

// Attention is what an agent needs from a human, in two tiers: the urgent
// kinds block on an explicit decision, while AttentionWaiting only means
// the agent is paused until the next prompt.
type Attention string

const (
	AttentionNone     Attention = ""
	AttentionQuestion Attention = "question"
	AttentionApproval Attention = "approval"
	AttentionAuth     Attention = "auth"
	AttentionWaiting  Attention = "waiting"
)

// Urgent reports whether the attention kind blocks the agent on an explicit
// human decision, as opposed to merely waiting for the next prompt.
func (a Attention) Urgent() bool {
	switch a {
	case AttentionQuestion, AttentionApproval, AttentionAuth:
		return true
	}
	return false
}

// TerminalOwned reports whether the agent's own terminal UI holds the
// input right now (a permission prompt, picker, or login flow). Text sent
// from outside would type into that UI; answers belong in the terminal. A
// plain question (the agent asked in text and idles at its composer) is
// urgent but not terminal-owned — replying with text is the answer.
func (a Attention) TerminalOwned() bool {
	return a == AttentionApproval || a == AttentionAuth
}

// Rank orders attention for triage sorting: urgent, then waiting, then none.
func (a Attention) Rank() int {
	switch {
	case a.Urgent():
		return 0
	case a == AttentionWaiting:
		return 1
	default:
		return 2
	}
}

// Mark is the human's own reading of an agent, set from the dashboard when
// Stormlight's inference is wrong or when a row wants revisiting. Stormlight
// derives activity and attention from provider hooks and pane state, and it
// gets that wrong often enough that the human needs a way to say otherwise —
// a mark is that word, and it outranks everything Stormlight inferred.
//
// The two marks retire differently, because different parties can answer
// them. MarkWorking claims the agent is still going, which the agent itself
// settles the moment it reports anything: its next self-report retires the
// mark. MarkAttention claims the human has something to come back to, which
// no provider event can answer, so only the human takes it down — with M or
// by engaging with the row, exactly like the amber inbox it joins.
type Mark string

const (
	MarkNone Mark = ""
	// MarkWorking says the agent is still going, whatever the dashboard
	// reads: the row glows working and any attention on it stands down.
	MarkWorking Mark = "working"
	// MarkAttention says the human wants this row back, whatever the
	// dashboard reads: it joins the waiting tier of the amber inbox.
	MarkAttention Mark = "attention"
)

func ParseMark(value string) (Mark, error) {
	if value == "none" || value == "clear" {
		return MarkNone, nil
	}
	switch Mark(value) {
	case MarkNone, MarkWorking, MarkAttention:
		return Mark(value), nil
	}
	return "", fmt.Errorf("invalid mark %q (working, attention, or none)", value)
}

// Label is how a mark reads in the dashboard and in status lines.
func (m Mark) Label() string {
	switch m {
	case MarkWorking:
		return "in progress"
	case MarkAttention:
		return "needs attention"
	}
	return ""
}

// PermissionMode controls how much a dispatched agent may do without asking.
// The names are provider-neutral; each provider adapter maps them to its own
// flags.
type PermissionMode string

const (
	// ModeAsk keeps the provider's cautious default: consequential actions
	// request approval (bridged into the dashboard where supported).
	ModeAsk PermissionMode = "ask"
	// ModeEdits applies workspace file edits without asking and still asks
	// for shell, network, and anything outside the workspace.
	ModeEdits PermissionMode = "edits"
	// ModeAuto never asks.
	ModeAuto PermissionMode = "auto"
)

// DefaultMode is used when a dispatch does not specify a permission mode.
// Auto is the recommended way to run Stormlight agents: prompts are
// answered in the agent's own terminal, so a mode that rarely prompts is
// what keeps the dashboard a place you watch rather than babysit.
const DefaultMode = ModeAuto

func ParseMode(value string) (PermissionMode, error) {
	switch PermissionMode(value) {
	case "":
		return DefaultMode, nil
	case ModeAsk, ModeEdits, ModeAuto:
		return PermissionMode(value), nil
	}
	return "", fmt.Errorf("invalid permission mode %q (ask, edits, or auto)", value)
}

type Agent struct {
	// Host names the machine this agent is running on; empty is this one.
	// It is never stored in the agent's document: the daemon that
	// answered for it is the fact, and a name recorded on one dashboard
	// would be a claim about the world made from the wrong place.
	Host      string    `json:"-"`
	ID        string    `json:"id"`
	Provider  Provider  `json:"provider"`
	Name      string    `json:"name"`
	Task      string    `json:"task"`
	Summary   string    `json:"summary,omitempty"`
	Cwd       string    `json:"cwd"`
	CreatedAt time.Time `json:"created_at"`
	Activity  Activity  `json:"activity"`
	Attention Attention `json:"attention,omitempty"`
	// AttentionAt is when the agent joined the amber inbox — the moment it
	// started pending on a human, by either route (a provider signal or the
	// human's own mark). It is the queue's ordering key, so it records
	// entry into the state rather than the latest signal: a question that
	// escalates or a summary that arrives later must not send an agent to
	// the back of a line it has been in the whole time.
	AttentionAt time.Time      `json:"attention_at,omitempty"`
	Mark        Mark           `json:"mark,omitempty"`
	WindowID    string         `json:"window_id"`
	PaneID      string         `json:"pane_id"`
	PaneTitle   string         `json:"pane_title,omitempty"`
	Command     string         `json:"command,omitempty"`
	ProcessLive bool           `json:"process_live"`
	ExitCode    *int           `json:"exit_code,omitempty"`
	Mode        PermissionMode `json:"mode,omitempty"`
	// SessionID is the provider's own id for this conversation — the value
	// `claude --resume` and `codex resume` take — reported by its hooks and
	// notify surface. It is what lets a conversation outlive its window.
	SessionID string `json:"session_id,omitempty"`
	// TranscriptPath is the provider's own transcript file for this
	// conversation (Claude Code session JSONL), reported by its hooks.
	TranscriptPath string            `json:"transcript_path,omitempty"`
	Workspace      workspace.Context `json:"workspace"`
}

// EffectiveMark is the mark the dashboard honors. A dead pane has an exit
// status of its own to report and no live state left to correct, so a mark
// stops applying the moment the process is gone.
func (a Agent) EffectiveMark() Mark {
	if !a.ProcessLive {
		return MarkNone
	}
	return a.Mark
}

func (a Agent) NeedsAttention() bool {
	switch a.EffectiveMark() {
	case MarkAttention:
		return true
	case MarkWorking:
		// The human said this agent is still going, so it is not waiting on
		// anyone — that is the whole point of the mark.
		return false
	}
	return a.Attention != AttentionNone
}

// TriageRank orders agents for attention sorting. A mark speaks with the tier
// it names; everything else defers to the derived attention.
func (a Agent) TriageRank() int {
	switch a.EffectiveMark() {
	case MarkAttention:
		return AttentionWaiting.Rank()
	case MarkWorking:
		return AttentionNone.Rank()
	}
	return a.Attention.Rank()
}

func (a Agent) DisplaySummary() string {
	if strings.TrimSpace(a.Summary) != "" {
		return strings.TrimSpace(a.Summary)
	}
	return strings.TrimSpace(a.Task)
}

func Sort(agents []Agent) {
	slices.SortStableFunc(agents, func(a, b Agent) int {
		if d := cmp.Compare(priority(a), priority(b)); d != 0 {
			return d
		}
		if d := b.CreatedAt.Compare(a.CreatedAt); d != 0 {
			return d
		}
		return cmp.Compare(a.Name, b.Name)
	})
}

func priority(a Agent) int {
	if a.NeedsAttention() {
		return 0
	}
	if a.EffectiveMark() == MarkWorking {
		return 1
	}
	switch a.Activity {
	case ActivityWorking, ActivityStarting:
		return 1
	case ActivityIdle:
		return 2
	case ActivityCompleted:
		return 3
	case ActivityFailed:
		return 4
	case ActivityStopped:
		return 5
	default:
		return 6
	}
}
