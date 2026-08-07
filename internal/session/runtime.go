package session

import (
	"context"
	"os/exec"

	"github.com/trentkm/stormlight/internal/agent"
	"github.com/trentkm/stormlight/internal/workspace"
)

type Runtime interface {
	ListAgents(context.Context) ([]agent.Agent, error)
	Dispatch(context.Context, DispatchRequest) (agent.Agent, error)
	Capture(context.Context, string, int) (string, error)
	Attach(context.Context, string) (AttachResult, error)
	Send(context.Context, string, string) error
	Interrupt(context.Context, string) error
	Delete(context.Context, string) error
	Rename(context.Context, string, string) error
	Update(context.Context, string, Update) error
	SetWorkspace(context.Context, string, workspace.Context) error
	// SyncWindowSizes resizes detached agent windows to the given cell
	// size, so agents render at the width the dashboard displays.
	SyncWindowSizes(context.Context, int, int) error
}

// StatusPublisher is an optional runtime capability: a runtime whose
// terminal has chrome of its own can carry Stormlight's tally on it, so the
// count of what is working and what is waiting stays readable from inside an
// agent rather than only from the dashboard.
//
// It is separate from Runtime because it is presentation, not agent
// semantics: a runtime with nowhere to put a status line is complete without
// it, and callers treat its absence as "nothing to show it on".
type StatusPublisher interface {
	PublishStatus(context.Context, agent.Stats) error
}

type DispatchRequest struct {
	Provider  agent.Provider
	Name      string
	Task      string
	Cwd       string
	Mode      agent.PermissionMode
	Launch    Launch
	Workspace workspace.Context
}

type Launch struct {
	Path string   `json:"path"`
	Args []string `json:"args"`
}

type Update struct {
	Activity  agent.Activity
	Attention agent.Attention
	Summary   string
	// TranscriptPath records the provider's own transcript file when a
	// hook reports it; empty means "leave as is".
	TranscriptPath string
	// TurnEnded marks an update produced by the provider's end-of-turn
	// event; it may downgrade urgent attention, because a finished turn
	// proves any pending prompt was resolved.
	TurnEnded bool
	// ClearAttention removes any attention state without touching the
	// rest of the record; an empty Attention alone means "leave as is".
	ClearAttention bool
	// Mark records the human's own reading of the agent; empty means
	// "leave as is", so removing one takes ClearMark.
	Mark agent.Mark
	// ClearMark removes any mark without touching the rest of the record.
	ClearMark bool
}

type AttachResult struct {
	Command *exec.Cmd
}
