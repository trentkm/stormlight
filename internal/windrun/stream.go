package windrun

import (
	"context"
	"sync"

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
	return newTerminalStream(attachment), nil
}

// newTerminalStream is the only way to build one. The relay behind it is
// not optional — a stream without it hands back a nil channel that every
// consumer waits on forever — so construction and starting it are one
// step rather than two a caller can get half right.
func newTerminalStream(attachment *client.Attachment) *terminalStream {
	return newStream(attachment, attachment.Output(), streamBuffer)
}

// streamBuffer is how far a consumer may fall behind this relay before
// the daemon's own backpressure — and then its resync — takes over.
const streamBuffer = 256

// newStream is the construction itself, taking the source separately so a
// test can drive one without a daemon — which is the only way to check
// that building a stream really does start its relay.
func newStream(
	attachment *client.Attachment,
	source <-chan client.Message,
	buffer int,
) *terminalStream {
	stream := &terminalStream{
		attachment: attachment,
		output:     make(chan pty.Message, buffer),
		done:       make(chan struct{}),
	}
	go stream.relay(source)
	return stream
}

// usableSize reports whether a geometry is one a terminal can be. The
// daemon rejects anything smaller than 2x2, so a size below it is a
// malformed claim rather than a small terminal.
func usableSize(cols, rows int) bool { return cols >= 2 && rows >= 2 }

type terminalStream struct {
	attachment *client.Attachment
	output     chan pty.Message
	// done releases the relay when the consumer has stopped reading, so
	// a closed viewer does not leave it parked on a full channel.
	done      chan struct{}
	closeOnce sync.Once
}

func (t *terminalStream) Seed() []byte {
	return t.attachment.Snapshot().ANSI
}

// relay carries the daemon's stream across into the widget's, preserving
// its order. The two message types line up one for one, which is the
// point: a translation that split them into a channel and a callback
// would be free to reorder them, and the ordering is the contract.
func (t *terminalStream) relay(source <-chan client.Message) {
	defer close(t.output)
	for {
		var message client.Message
		var ok bool
		select {
		case <-t.done:
			return
		case message, ok = <-source:
			if !ok {
				return
			}
		}
		var translated pty.Message
		switch {
		case message.Resync != nil:
			// The size travels with the state and nowhere else: while a
			// viewer is in debt the daemon drops every message, resize
			// notices included, so the snapshot is the only thing that
			// can tell it the terminal moved. Dropping these two fields
			// leaves a replica repainting a wide screen into a narrow
			// emulator, wrapped and wrong, until someone resizes again.
			translated = pty.Message{Resync: message.Resync.ANSI}
			// The snapshot names the size it was rendered at, and the
			// daemon fills it — but a size a terminal cannot be must not
			// travel just because it arrived. Passing a zero on would
			// have consumers clamp it to a legal 2x2 and reflow to it.
			if usableSize(message.Resync.Cols, message.Resync.Rows) {
				translated.Resize = &pty.Size{
					Cols: message.Resync.Cols,
					Rows: message.Resync.Rows,
				}
			}
		case message.Resize != nil:
			translated = pty.Message{Resize: &pty.Size{
				Cols: message.Resize.Cols,
				Rows: message.Resize.Rows,
			}}
		default:
			translated = pty.Message{Bytes: message.Bytes}
		}
		select {
		case t.output <- translated:
		case <-t.done:
			// The consumer is gone, but simply stopping would stall the
			// client's read loop mid-send on its own full buffer — it
			// would never reach the close that ends it, and would sit on
			// sixty-four buffered messages, resync snapshots among them.
			// Keep taking them until the attachment closes, which Close
			// is doing as this runs.
			for range source {
			}
			return
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
	// Release the relay first: closing only the attachment leaves it
	// parked on a channel nobody drains, holding the client's read loop
	// behind it.
	t.closeOnce.Do(func() { close(t.done) })
	t.attachment.Close()
}
