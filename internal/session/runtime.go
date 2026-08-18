package session

import (
	"context"
	"io"
	"os/exec"

	"github.com/trentkm/stormlight/internal/agent"
	"github.com/trentkm/stormlight/internal/pty"
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

// TerminalStreamer is an optional runtime capability: a runtime whose
// terminals can be attached to directly — an exact state snapshot followed
// by the live byte stream, with input and resize flowing back.
type TerminalStreamer interface {
	AttachTerminal(ctx context.Context, id string, cols, rows int) (TerminalStream, error)
}

// TerminalStream is one live attachment to one agent's terminal: an exact
// seed, then an ordered stream of output, resizes and resyncs.
//
// It is the widget's transport by definition rather than by coincidence.
// The two were separate interfaces of identical shape, which meant every
// change had to be made twice and any drift between them was a compile
// error somewhere unrelated.
type TerminalStream = pty.Transport

// FileReader is an optional runtime capability: read a file on the
// machine an agent is running on.
//
// An agent's transcript is written by its provider, beside its
// repository, on its own host — so the path in an agent's record names a
// file this process may have no way to open. The runtime knows which
// daemon holds the agent, which makes it the only layer that can answer.
type FileReader interface {
	ReadAgentFile(ctx context.Context, id, path string) (io.ReadCloser, error)
}

// OverlayRequest describes a short-lived interactive program the
// dashboard wants floated over itself rather than handed the whole
// screen: the Yazi picker, the Neovim task editor.
type OverlayRequest struct {
	// Host is the machine to run it on; empty is this one. An overlay
	// browses a filesystem, so it belongs on the machine that has it.
	Host string
	Path string
	Args []string
	Dir  string
	Cols int
	Rows int
}

// Overlay is one running overlay program: its terminal stream plus the
// exit the dashboard waits on. Close always destroys the hosting session —
// an overlay must never persist into the roster.
type Overlay interface {
	TerminalStream
	Exited() <-chan int
	// Result is the answer the program left behind, read after it exits
	// and before Close takes the session with it. Empty means it left
	// none: a picker the user quit without choosing.
	//
	// The answer comes back through the session rather than through a
	// file, because the program runs on the machine it is choosing from
	// and a file it wrote there is not one this process can read. The
	// session is the one thing both ends already hold.
	Result(ctx context.Context) (string, error)
}

// OverlayHost is an optional runtime capability: run a short-lived
// interactive program on a runtime-owned PTY and stream its terminal, so
// the dashboard can float it in a popup instead of surrendering the
// screen.
type OverlayHost interface {
	StartOverlay(ctx context.Context, request OverlayRequest) (Overlay, error)
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
