package windrun

import (
	"context"

	"github.com/trentkm/windrunner/client"

	"github.com/trentkm/stormlight/internal/session"
)

// OpenTerminalStream is the native attach: exact snapshot as the seed,
// then the live byte stream, over one dedicated daemon connection.
func (r *Runtime) OpenTerminalStream(ctx context.Context, id string, cols, rows int) (session.TerminalStream, error) {
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

// The tap-shaped capability, satisfied so every existing Service
// passthrough works unchanged for windrunner agents. Pipe management is
// meaningless here — the stream path never calls it — and says so rather
// than pretending.

func (r *Runtime) SendRaw(ctx context.Context, id string, data []byte) error {
	sessionID, err := r.sessionIDFor(id)
	if err != nil {
		return err
	}
	return r.client.Input(sessionID, data)
}

func (r *Runtime) ResizeAgentWindow(ctx context.Context, id string, width, height int) error {
	sessionID, err := r.sessionIDFor(id)
	if err != nil {
		return err
	}
	return r.client.Resize(sessionID, width, height)
}

func (r *Runtime) CaptureRaw(ctx context.Context, id string, history int) (string, error) {
	return r.Capture(ctx, id, history)
}

func (r *Runtime) PaneView(ctx context.Context, id string) (session.PaneView, error) {
	// The snapshot embeds the cursor; nothing consumes this on the stream
	// path.
	return session.PaneView{}, nil
}

func (r *Runtime) PipePaneOpen(ctx context.Context, id, fifoPath string) error {
	return errNotTapped
}

func (r *Runtime) PipePaneClose(ctx context.Context, id string) error {
	return errNotTapped
}

var errNotTapped = notTappedError{}

type notTappedError struct{}

func (notTappedError) Error() string {
	return "windrunner agents stream natively; there is no pane pipe"
}
