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
}

// PaneStreamer is an optional runtime capability: a runtime that can expose
// an agent's terminal as a raw byte stream and accept raw bytes back, so a
// dashboard can host a live terminal view instead of sampling captures.
//
// It is separate from Runtime because it is about the transport, not agent
// semantics: a runtime without a streamable pane is complete without it, and
// callers treat its absence as "this runtime cannot stream".
type PaneStreamer interface {
	// PipePaneOpen streams every byte the agent's pane emits, from now on,
	// into fifoPath. Any pipe a previous run left behind is replaced.
	PipePaneOpen(ctx context.Context, id, fifoPath string) error
	// PipePaneClose stops the stream. Closing an unpiped pane is a no-op.
	PipePaneClose(ctx context.Context, id string) error
	// CaptureRaw returns the pane's screen with escape sequences intact and
	// lines wrapped exactly as the pane wrapped them — the seed a terminal
	// emulator replays before the stream takes over. history asks for that
	// many lines of pane scrollback above the screen (zero for the screen
	// alone), which is how an emulator's history survives a dashboard
	// restart: tmux held it while nobody was watching.
	CaptureRaw(ctx context.Context, id string, history int) (string, error)
	// PaneView reports where the pane's cursor is and whether the
	// alternate screen is active, completing the seed.
	PaneView(ctx context.Context, id string) (PaneView, error)
	// ResizeAgentWindow sizes one agent's window exactly, with the same
	// attached-client deference SyncWindowSizes applies.
	ResizeAgentWindow(ctx context.Context, id string, width, height int) error
	// SendRaw delivers bytes to the agent's terminal input verbatim — no
	// bracketed paste, no synthesized Enter.
	SendRaw(ctx context.Context, id string, data []byte) error
}

// PaneView is the cursor-and-screen state CaptureRaw cannot carry. Cols
// and Rows are the pane's actual size, which is not always the size that
// was asked for: an attached client owns its windows' sizes, and an
// emulator replaying that pane's rows must match the pane, not the wish.
type PaneView struct {
	CursorX   int
	CursorY   int
	AltScreen bool
	Cols      int
	Rows      int
}

// TerminalStreamer is an optional runtime capability: a runtime whose
// terminals can be attached to directly — an exact state snapshot followed
// by the live byte stream, with input and resize flowing back. It is the
// native shape of a windrunner-hosted agent, where the tap-based
// PaneStreamer is the tmux retrofit of the same idea.
type TerminalStreamer interface {
	OpenTerminalStream(ctx context.Context, id string, cols, rows int) (TerminalStream, error)
}

// TerminalStream is one live attachment to one agent's terminal.
type TerminalStream interface {
	// Seed is the exact serialized state at attach time; everything on
	// Output happened after it.
	Seed() []byte
	Output() <-chan []byte
	Write(p []byte) error
	Resize(ctx context.Context, cols, rows int) error
	Close()
}

// Restorer is an optional runtime capability: a runtime whose agent records
// live and die with the runtime itself, and which can therefore be handed a
// record from before it started and asked to build the agent back.
//
// It is separate from Runtime because it is about the runtime's own
// mortality rather than about agents: a runtime that persists its roster by
// construction has nothing to restore, and callers treat its absence as
// "this roster cannot be lost".
type Restorer interface {
	// SessionName identifies the world a roster belongs to, so a record
	// written by one can say which one it came from.
	SessionName() string
	// RosterLive reports whether the runtime's agent listing is
	// authoritative right now. An empty listing is ambiguous — it reads the
	// same whether the last agent was deleted or the whole session is gone —
	// and the difference decides whether a snapshot may be overwritten with
	// nothing. False means the roster is not there to be believed.
	RosterLive(context.Context) (bool, error)
	// Restore builds an agent back from a record, preserving its identity so
	// continuity holds across the gap.
	Restore(context.Context, RestoreRequest) (agent.Agent, error)
}

// RestoreRequest is a dispatch whose identity and history already exist. The
// runtime is being told what the agent was, not asked to invent one.
type RestoreRequest struct {
	Agent  agent.Agent
	Launch Launch
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
	// SessionID records the provider's own conversation id when an event
	// carries it; empty means "leave as is".
	SessionID string
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
