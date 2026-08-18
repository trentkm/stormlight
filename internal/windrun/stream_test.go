package windrun

import (
	"testing"
	"time"

	"github.com/trentkm/windrunner/client"
	"github.com/trentkm/windrunner/wire"

	"github.com/trentkm/stormlight/internal/pty"
	"github.com/trentkm/stormlight/internal/session"
)

// Every terminal stream must arrive with its relay running. Built by
// struct literal instead, its output channel is nil and every consumer
// waits on it forever — which is exactly what happened to the overlays,
// where a picker that opened blank and never painted was the only
// symptom. Constructing one is a function for that reason, and this is
// the test that says so.
func TestEveryStreamCarriesARunningRelay(t *testing.T) {
	agentTerminal := newTerminalStream(nil)
	defer agentTerminal.Close()
	overlayStream := &overlay{terminalStream: newTerminalStream(nil)}
	// The stream, not the overlay: overlay.Close also removes the
	// daemon session, which this one does not have.
	defer overlayStream.terminalStream.Close()

	for _, testCase := range []struct {
		name   string
		stream session.TerminalStream
	}{
		{"agent terminal", agentTerminal},
		{"overlay", overlayStream},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			if testCase.stream.Output() == nil {
				t.Fatal("stream has no output channel; its relay never started")
			}
		})
	}
}

// A resync is the only message a viewer in debt receives, so the size it
// carries is the only way that viewer learns the terminal moved. Dropping
// it leaves a replica painting a wide screen into a narrow emulator.
func TestResyncKeepsTheSizeItWasRenderedAt(t *testing.T) {
	source := make(chan client.Message, 1)
	stream := newRelayForTest(t, source)

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

// The relay stops when the consumer does. Left running it parks on a full
// channel nobody drains, holding the client's read loop behind it — so a
// browser tab closing mid-burst would leak a goroutine and its attachment
// both.
func TestRelayStopsWhenTheConsumerDoes(t *testing.T) {
	source := make(chan client.Message)
	stream := &terminalStream{
		output: make(chan pty.Message, 1),
		done:   make(chan struct{}),
	}
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		stream.relay(source)
	}()

	// Fill the one slot, then leave: without a release the next send
	// parks forever on a channel with no reader.
	source <- client.Message{Bytes: []byte("one")}
	stream.Close()

	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("the relay outlived its consumer")
	}
}

func newRelayForTest(t *testing.T, source chan client.Message) *terminalStream {
	t.Helper()
	stream := &terminalStream{
		output: make(chan pty.Message, 8),
		done:   make(chan struct{}),
	}
	go stream.relay(source)
	t.Cleanup(stream.Close)
	return stream
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
