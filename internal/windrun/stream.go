package windrun

import (
	"context"

	"github.com/trentkm/windrunner/client"

	"github.com/trentkm/stormlight/internal/pty"
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
	// Resync rather than be dropped. Everything attaching here is a
	// viewer — the dashboard's terminal box, a browser — and a viewer
	// that falls behind a burst wants the screen as it now stands, not to
	// be cut off mid-stream with no way to know it happened. Without this
	// a build log scrolling past a slow client freezes its terminal for
	// good.
	attachment, err := r.client.AttachWith(sessionID, client.AttachOptions{
		Buffer: 256,
		Resync: true,
	})
	if err != nil {
		return nil, err
	}
	stream := &terminalStream{
		attachment: attachment,
		output:     make(chan pty.Message, 256),
	}
	go stream.relay()
	return stream, nil
}

type terminalStream struct {
	attachment *client.Attachment
	output     chan pty.Message
}

func (t *terminalStream) Seed() []byte {
	return t.attachment.Snapshot().ANSI
}

// relay carries the daemon's stream across into the widget's, preserving
// its order. The two message types line up one for one, which is the
// point: a translation that split them into a channel and a callback
// would be free to reorder them, and the ordering is the contract.
func (t *terminalStream) relay() {
	defer close(t.output)
	for message := range t.attachment.Output() {
		switch {
		case message.Resync != nil:
			t.output <- pty.Message{Resync: message.Resync.ANSI}
		case message.Resize != nil:
			t.output <- pty.Message{Resize: &pty.Size{
				Cols: message.Resize.Cols,
				Rows: message.Resize.Rows,
			}}
		default:
			t.output <- pty.Message{Bytes: message.Bytes}
		}
	}
}

func (t *terminalStream) Output() <-chan pty.Message {
	return t.output
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
