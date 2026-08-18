package windrun

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/trentkm/windrunner/client"
	"github.com/trentkm/windrunner/wire"

	"github.com/trentkm/stormlight/internal/session"
)

// overlayMetadataKey marks sessions hosting dashboard overlays. They carry
// no agent document, so the roster already ignores them; the marker makes
// them legible in `windrunner ls` and debugging.
const overlayMetadataKey = "stormlight_overlay"

// OverlayResultKey is where an overlay program leaves its answer: its own
// session's metadata, which the daemon stores without reading and the
// dashboard can read from anywhere it can reach that daemon. The program
// writes it through the daemon on its own machine; nothing crosses a
// filesystem boundary that was never crossable.
const OverlayResultKey = "stormlight_overlay_result"

// StartOverlay runs a short-lived interactive program in its own session
// and hands back its terminal. The session dies with the overlay — Close
// removes it — because an overlay resurrected from daemon persistence
// would be a ghost with nothing waiting on its exit.
func (r *Runtime) StartOverlay(ctx context.Context, request session.OverlayRequest) (session.Overlay, error) {
	// An empty path means this host's Stormlight — the way an overlay
	// asks for a program that exists on whichever machine it lands on,
	// without the dashboard having to know where that machine keeps it.
	command := request.Path
	if strings.TrimSpace(command) == "" {
		binPath, _, err := r.dispatchTarget()
		if err != nil {
			return nil, err
		}
		command = binPath
	}
	spawn := wire.Request{
		Command:  command,
		Args:     request.Args,
		Dir:      request.Dir,
		Cols:     max(2, request.Cols),
		Rows:     max(2, request.Rows),
		Metadata: map[string]string{overlayMetadataKey: request.Path},
	}
	// On a remote host the environment is that host's: an overlay browses
	// the filesystem the agents are on, so it wants that machine's HOME
	// and PATH, not this one's.
	socketDir := SocketDir()
	if r.transport == nil {
		spawn.Env = os.Environ()
	} else {
		socketDir = r.transport.Hello().SocketDir
	}
	// The program inside needs to reach the daemon holding it, to leave
	// its answer there. WINDRUNNER_SESSION names the session; this says
	// which daemon to say it to.
	spawn.EnvOverride = []string{"WINDRUNNER_DIR=" + socketDir}
	info, err := r.client.Spawn(spawn)
	if err != nil {
		return nil, fmt.Errorf("spawn overlay %s: %w", command, err)
	}
	attachment, err := r.client.Attach(info.ID, 256)
	if err != nil {
		_ = r.client.Remove(info.ID)
		return nil, fmt.Errorf("attach overlay %s: %w", request.Path, err)
	}
	return &overlay{
		terminalStream: newTerminalStream(attachment),
		client:         r.client,
		id:             info.ID,
	}, nil
}

type overlay struct {
	*terminalStream
	client *client.Client
	id     string
}

func (o *overlay) Exited() <-chan int { return o.attachment.Exited() }

// Result reads what the program left in its session. The session is still
// there because Close has not run yet — which is the whole reason the
// dashboard reads before it closes.
func (o *overlay) Result(_ context.Context) (string, error) {
	info, err := o.client.Info(o.id)
	if err != nil {
		return "", err
	}
	return info.Metadata[OverlayResultKey], nil
}

// Close detaches and removes the session in one motion; Remove also ends
// the process when it is still running (the cancel path).
func (o *overlay) Close() {
	o.terminalStream.Close()
	_ = o.client.Remove(o.id)
}
