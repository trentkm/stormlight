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
	stream := &terminalStream{
		attachment: attachment,
		output:     make(chan pty.Message, 256),
		done:       make(chan struct{}),
	}
	var source <-chan client.Message
	if attachment != nil {
		source = attachment.Output()
	}
	go stream.relay(source)
	return stream
}

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
			translated = pty.Message{
				Resync: message.Resync.ANSI,
				Resize: &pty.Size{
					Cols: message.Resync.Cols,
					Rows: message.Resync.Rows,
				},
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
			// The consumer is gone. Stopping here rather than parking on
			// a channel nobody drains is what keeps the client's read
			// loop free to finish and close.
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
	if t.attachment != nil {
		t.attachment.Close()
	}
}
