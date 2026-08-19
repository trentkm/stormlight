package windrun

import (
	"context"
	"sync"

	"github.com/trentkm/windrunner/client"
	"github.com/trentkm/windrunner/wire"

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
	// Size first, so the snapshot arrives pre-wrapped for the view it is
	// about to fill — but only when the caller has a size to assert. The
	// terminal belongs to the session and every viewer shares it, so a
	// caller that does not know its own geometry must not move it: a
	// viewer with no layout would otherwise reflow the agent for everyone
	// and leave it that way, since nothing re-asserts the size a
	// dashboard pane had (#155).
	if cols >= 2 && rows >= 2 {
		if err := r.client.Resize(sessionID, cols, rows); err != nil {
			return nil, err
		}
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

// snapshotSize is the geometry a snapshot says it was rendered at, or nil
// when it names one no terminal can be. The daemon rejects anything below
// 2x2, so a smaller number is a malformed claim rather than a small
// terminal — and passing it on would have consumers clamp it into a real
// 2x2 reflow of a terminal every viewer shares.
func snapshotSize(snapshot *wire.SnapshotPayload) *pty.Size {
	if snapshot.Cols < 2 || snapshot.Rows < 2 {
		return nil
	}
	return &pty.Size{Cols: snapshot.Cols, Rows: snapshot.Rows}
}

type terminalStream struct {
	attachment *client.Attachment
	output     chan pty.Message
	// done releases the relay when the consumer has stopped reading, so
	// a closed viewer does not leave it parked on a full channel.
	done      chan struct{}
	closeOnce sync.Once
}

func (t *terminalStream) Seed() pty.Message {
	snapshot := t.attachment.Snapshot()
	return pty.Message{
		Resync: snapshot.ANSI,
		Resize: snapshotSize(&snapshot),
	}
}

// relay carries the daemon's stream across into the widget's, preserving
// its order. The two message types line up one for one, which is the
// point: a translation that split them into a channel and a callback
// would be free to reorder them, and the ordering is the contract.
func (t *terminalStream) relay(source <-chan client.Message) {
	defer close(t.output)
	for message := range source {
		var translated pty.Message
		switch {
		case message.Resync != nil:
			// The size travels with the state and nowhere else: while a
			// viewer is in debt the daemon drops every message, resize
			// notices included, so the snapshot is the only thing that
			// can tell it the terminal moved. Dropping these two fields
			// leaves a replica repainting a wide screen into a narrow
			// emulator, wrapped and wrong, until someone resizes again.
			translated = pty.Message{
				Resync: message.Resync.ANSI,
				Resize: snapshotSize(message.Resync),
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
	// Release the relay first, then end the attachment. The order is the
	// contract: released, the relay stops queueing and starts draining,
	// so closing the attachment can finish rather than stalling on a
	// read loop with nowhere to put what it has.
	t.release()
	t.attachment.Close()
}

// release lets the relay stop queueing for a consumer that has gone.
func (t *terminalStream) release() {
	t.closeOnce.Do(func() { close(t.done) })
}
