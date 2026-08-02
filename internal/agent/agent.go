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
	ID          string         `json:"id"`
	Provider    Provider       `json:"provider"`
	Name        string         `json:"name"`
	Task        string         `json:"task"`
	Summary     string         `json:"summary,omitempty"`
	Cwd         string         `json:"cwd"`
	CreatedAt   time.Time      `json:"created_at"`
	Activity    Activity       `json:"activity"`
	Attention   Attention      `json:"attention,omitempty"`
	TmuxSession string         `json:"tmux_session"`
	WindowID    string         `json:"window_id"`
	WindowIndex int            `json:"window_index"`
	PaneID      string         `json:"pane_id"`
	PaneTitle   string         `json:"pane_title,omitempty"`
	Command     string         `json:"command,omitempty"`
	ProcessLive bool           `json:"process_live"`
	ExitCode    *int           `json:"exit_code,omitempty"`
	Mode        PermissionMode `json:"mode,omitempty"`
	// TranscriptPath is the provider's own transcript file for this
	// conversation (Claude Code session JSONL), reported by its hooks.
	TranscriptPath string            `json:"transcript_path,omitempty"`
	Workspace      workspace.Context `json:"workspace"`
}

func (a Agent) NeedsAttention() bool {
	return a.Attention != AttentionNone
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
