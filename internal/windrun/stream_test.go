package windrun

import (
	"context"
	"testing"
	"time"

	"github.com/trentkm/windrunner/client"
	"github.com/trentkm/windrunner/wire"

	"github.com/trentkm/stormlight/internal/agent"
	"github.com/trentkm/stormlight/internal/pty"
	"github.com/trentkm/stormlight/internal/session"
)

// newRelay builds a stream through the real constructor, with a source
// the test controls in place of a daemon attachment. Going through the
// constructor is the point: a test that starts the relay itself would
// pass with construction leaving it unstarted, which is the bug that
// killed the overlays.
func newRelay(t *testing.T, source <-chan client.Message, buffer int) *terminalStream {
	t.Helper()
	stream := newStream(nil, source, buffer)
	t.Cleanup(stream.release)
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
	stream.release()

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

// An attach that names no size must leave the terminal where it is. The
// terminal belongs to the session and every viewer shares it, so a viewer
// with no layout yet — a browser tab that has not painted, a pane not on
// screen — would otherwise reflow the agent for everyone and leave it
// that way: nothing re-asserts the size a dashboard pane had, so the
// dashboard renders 80 columns of content into its own wider box until
// something else moves it (#155).
func TestAttachWithoutASizeLeavesTheTerminalAlone(t *testing.T) {
	runtime := remoteRuntime(t)

	dispatched, err := runtime.Dispatch(context.Background(), session.DispatchRequest{
		Provider: agent.Provider("claude"),
		Name:     "sized",
		Task:     "hold a terminal",
		Cwd:      t.TempDir(),
		Launch:   session.Launch{Path: "/bin/sh", Args: []string{"-c", "sleep 60"}},
	})
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}

	// A viewer that knows its geometry sets it, the way a laid-out pane
	// does.
	wide, err := runtime.AttachTerminal(context.Background(), dispatched.ID, 132, 43)
	if err != nil {
		t.Fatalf("AttachTerminal: %v", err)
	}
	defer wide.Close()
	waitForSize(t, runtime, dispatched.ID, 132, 43)

	// A viewer that does not must not take it away from them.
	blind, err := runtime.AttachTerminal(context.Background(), dispatched.ID, 0, 0)
	if err != nil {
		t.Fatalf("AttachTerminal without a size: %v", err)
	}
	defer blind.Close()

	if cols, rows := sessionSize(t, runtime, dispatched.ID); cols != 132 || rows != 43 {
		t.Fatalf("a viewer with no size moved the terminal to %dx%d", cols, rows)
	}
}

// The snapshot's bytes are wrapped for a width, and the seed has to say
// which — otherwise a viewer that asserted no geometry paints them at its
// own assumption, which is how a snapshot arrives pre-mangled.
func TestTheSeedNamesTheSizeItWasRenderedAt(t *testing.T) {
	runtime := remoteRuntime(t)

	dispatched, err := runtime.Dispatch(context.Background(), session.DispatchRequest{
		Provider: agent.Provider("claude"),
		Name:     "seeded",
		Task:     "hold a terminal",
		Cwd:      t.TempDir(),
		Launch:   session.Launch{Path: "/bin/sh", Args: []string{"-c", "sleep 60"}},
	})
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}

	sized, err := runtime.AttachTerminal(context.Background(), dispatched.ID, 132, 43)
	if err != nil {
		t.Fatalf("AttachTerminal: %v", err)
	}
	defer sized.Close()
	waitForSize(t, runtime, dispatched.ID, 132, 43)

	// A second viewer, naming nothing — the case the size exists for.
	blind, err := runtime.AttachTerminal(context.Background(), dispatched.ID, 0, 0)
	if err != nil {
		t.Fatalf("AttachTerminal without a size: %v", err)
	}
	defer blind.Close()

	seed := blind.Seed()
	if seed.Resize == nil {
		t.Fatal("the seed named no size, so a viewer has nothing to paint it at")
	}
	if seed.Resize.Cols != 132 || seed.Resize.Rows != 43 {
		t.Fatalf("seed size = %dx%d, want the terminal's 132x43",
			seed.Resize.Cols, seed.Resize.Rows)
	}
}

func sessionSize(t *testing.T, runtime *Runtime, id string) (int, int) {
	t.Helper()
	sessionID, err := runtime.sessionIDFor(id)
	if err != nil {
		t.Fatalf("sessionIDFor: %v", err)
	}
	info, err := runtime.client.Info(sessionID)
	if err != nil {
		t.Fatalf("Info: %v", err)
	}
	return info.Cols, info.Rows
}

func waitForSize(t *testing.T, runtime *Runtime, id string, cols, rows int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if c, r := sessionSize(t, runtime, id); c == cols && r == rows {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	c, r := sessionSize(t, runtime, id)
	t.Fatalf("terminal is %dx%d, want %dx%d", c, r, cols, rows)
}
