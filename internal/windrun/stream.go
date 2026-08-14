package windrun

import (
	"context"

	"github.com/trentkm/windrunner/client"

	"github.com/trentkm/stormlight/internal/session"
)

// AttachTerminal is the native attach: exact snapshot as the seed, then
// the live byte stream, over one dedicated daemon connection.
func (r *Runtime) AttachTerminal(ctx context.Context, id string, cols, rows int) (session.TerminalStream, error) {
	sessionID, err := r.sessionIDFor(id)
	if err != nil {
		return nil, err
	}
	// Size first so the snapshot arrives pre-wrapped for the view it is
	// about to fill.
	if err := r.client.Resize(sessionID, cols, rows); err != nil {
		return nil, err
	}
	attachment, err := r.client.Attach(sessionID, 256)
	if err != nil {
		return nil, err
	}
	return &terminalStream{attachment: attachment}, nil
}

type terminalStream struct {
	attachment *client.Attachment
}

func (t *terminalStream) Seed() []byte {
	return t.attachment.Snapshot().ANSI
}

// OnResize relays the daemon's resize notices: the session's terminal
// moved — whoever moved it — and the handler runs ahead of the repaint
// that rides the same stream.
func (t *terminalStream) OnResize(handler func(cols, rows int)) {
	t.attachment.OnResize(handler)
}

func (t *terminalStream) Output() <-chan []byte {
	return t.attachment.Output()
}

func (t *terminalStream) Write(p []byte) error {
	return t.attachment.Write(p)
}

func (t *terminalStream) Resize(ctx context.Context, cols, rows int) error {
	return t.attachment.Resize(cols, rows)
}

func (t *terminalStream) Close() {
	t.attachment.Close()
}
