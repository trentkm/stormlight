package windrun

import (
	"testing"
	"time"

	"github.com/trentkm/windrunner/client"
	"github.com/trentkm/windrunner/wire"

	"github.com/trentkm/stormlight/internal/pty"
)

// newRelay builds a stream through the real constructor, with a source
// the test controls in place of a daemon attachment. Going through the
// constructor is the point: a test that starts the relay itself would
// pass with construction leaving it unstarted, which is the bug that
// killed the overlays.
func newRelay(t *testing.T, source <-chan client.Message, buffer int) *terminalStream {
	t.Helper()
	stream := newStream(nil, source, buffer)
	t.Cleanup(func() { stream.closeOnce.Do(func() { close(stream.done) }) })
	return stream
}

// A stream is only a stream once its relay is running. Built without one —
// which is how the overlays were built, and why the Yazi picker opened
// blank and never painted — nothing it is handed comes out the other side.
// Asserting the channel is merely non-nil would not have caught that; only
// pushing something through it does.
func TestAStreamCarriesWhatItIsGiven(t *testing.T) {
	source := make(chan client.Message, 1)
	stream := newRelay(t, source, 8)

	source <- client.Message{Bytes: []byte("output")}
	if message := next(t, stream); string(message.Bytes) != "output" {
		t.Fatalf("stream delivered %#v", message)
	}
}

// A resync is the only message a viewer in debt receives, so the size it
// carries is the only way that viewer learns the terminal moved. Dropping
// it leaves a replica painting a wide screen into a narrow emulator.
func TestResyncKeepsTheSizeItWasRenderedAt(t *testing.T) {
	source := make(chan client.Message, 1)
	stream := newRelay(t, source, 8)

	source <- client.Message{Resync: &wire.SnapshotPayload{
		Cols: 132, Rows: 43, ANSI: []byte("wide state"),
	}}
	message := next(t, stream)
	if string(message.Resync) != "wide state" {
		t.Fatalf("resync = %q", message.Resync)
	}
	if message.Resize == nil {
		t.Fatal("the resync arrived without the size it was rendered at")
	}
	if message.Resize.Cols != 132 || message.Resize.Rows != 43 {
		t.Fatalf("resync size = %dx%d, want 132x43",
			message.Resize.Cols, message.Resize.Rows)
	}
}

// A size a terminal cannot be must not travel just because it arrived:
// consumers clamp, and a clamped zero is a real 2x2 reflow of a terminal
// every viewer shares.
func TestUnusableResyncSizeIsNotPassedOn(t *testing.T) {
	source := make(chan client.Message, 1)
	stream := newRelay(t, source, 8)

	source <- client.Message{Resync: &wire.SnapshotPayload{ANSI: []byte("no size")}}
	if message := next(t, stream); message.Resize != nil {
		t.Fatalf("a %dx%d resize was passed on",
			message.Resize.Cols, message.Resize.Rows)
	}
}

// The relay must not park on a send once its consumer is gone — and must
// not abandon its source either. Stopping outright strands the client's
// read loop mid-send on its own full buffer, where it never reaches the
// close that ends it and sits on everything it had buffered, resync
// snapshots included.
func TestReleasedRelayKeepsDrainingItsSource(t *testing.T) {
	source := make(chan client.Message)
	// One slot, so the second message finds the relay blocked on the
	// send rather than on the receive — which is the state this is about.
	stream := newRelay(t, source, 1)

	source <- client.Message{Bytes: []byte("fills the buffer")}
	source <- client.Message{Bytes: []byte("relay now blocked sending this")}
	stream.closeOnce.Do(func() { close(stream.done) })

	// A released relay keeps taking from its source, so this lands rather
	// than parking the way a real client's read loop would.
	select {
	case source <- client.Message{Bytes: []byte("after release")}:
	case <-time.After(5 * time.Second):
		t.Fatal("relay stopped reading; a client's read loop would strand here")
	}

	// And it ends when the source does, rather than draining forever.
	close(source)
	deadline := time.After(5 * time.Second)
	for {
		select {
		case _, open := <-stream.Output():
			if !open {
				return
			}
		case <-deadline:
			t.Fatal("the relay never finished")
		}
	}
}

func next(t *testing.T, stream *terminalStream) pty.Message {
	t.Helper()
	select {
	case message, ok := <-stream.Output():
		if !ok {
			t.Fatal("stream closed early")
		}
		return message
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for a message")
	}
	panic("unreachable")
}
