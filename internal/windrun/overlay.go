package windrun

import (
	"context"
	"fmt"
	"os"

	"github.com/trentkm/windrunner/client"
	"github.com/trentkm/windrunner/wire"

	"github.com/trentkm/stormlight/internal/session"
)

// overlayMetadataKey marks sessions hosting dashboard overlays. They carry
// no agent document, so the roster already ignores them; the marker makes
// them legible in `windrunner ls` and debugging.
const overlayMetadataKey = "stormlight_overlay"

// StartOverlay runs a short-lived interactive program in its own session
// and hands back its terminal. The session dies with the overlay — Close
// removes it — because an overlay resurrected from daemon persistence
// would be a ghost with nothing waiting on its exit.
func (r *Runtime) StartOverlay(ctx context.Context, request session.OverlayRequest) (session.Overlay, error) {
	info, err := r.client.Spawn(wire.Request{
		Command:  request.Path,
		Args:     request.Args,
		Dir:      request.Dir,
		Env:      os.Environ(),
		Cols:     max(2, request.Cols),
		Rows:     max(2, request.Rows),
		Metadata: map[string]string{overlayMetadataKey: request.Path},
	})
	if err != nil {
		return nil, fmt.Errorf("spawn overlay %s: %w", request.Path, err)
	}
	attachment, err := r.client.Attach(info.ID, 256)
	if err != nil {
		_ = r.client.Remove(info.ID)
		return nil, fmt.Errorf("attach overlay %s: %w", request.Path, err)
	}
	return &overlay{
		terminalStream: terminalStream{attachment: attachment},
		client:         r.client,
		id:             info.ID,
	}, nil
}

type overlay struct {
	terminalStream
	client *client.Client
	id     string
}

func (o *overlay) Exited() <-chan int { return o.attachment.Exited() }

// Close detaches and removes the session in one motion; Remove also ends
// the process when it is still running (the cancel path).
func (o *overlay) Close() {
	o.terminalStream.Close()
	_ = o.client.Remove(o.id)
}
