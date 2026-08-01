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
	ProviderShell  Provider = "shell"
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

type Attention string

const (
	AttentionNone     Attention = ""
	AttentionQuestion Attention = "question"
	AttentionApproval Attention = "approval"
	AttentionAuth     Attention = "auth"
)

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
const DefaultMode = ModeEdits

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
	ID          string            `json:"id"`
	Provider    Provider          `json:"provider"`
	Name        string            `json:"name"`
	Task        string            `json:"task"`
	Summary     string            `json:"summary,omitempty"`
	Cwd         string            `json:"cwd"`
	CreatedAt   time.Time         `json:"created_at"`
	Activity    Activity          `json:"activity"`
	Attention   Attention         `json:"attention,omitempty"`
	TmuxSession string            `json:"tmux_session"`
	WindowID    string            `json:"window_id"`
	WindowIndex int               `json:"window_index"`
	PaneID      string            `json:"pane_id"`
	PaneTitle   string            `json:"pane_title,omitempty"`
	Command     string            `json:"command,omitempty"`
	ProcessLive bool              `json:"process_live"`
	ExitCode    *int              `json:"exit_code,omitempty"`
	Mode        PermissionMode    `json:"mode,omitempty"`
	Workspace   workspace.Context `json:"workspace"`
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
