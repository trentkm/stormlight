package ptyview

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/trentkm/stormlight/internal/pty"
)

// fakeTransport records the sizes asserted on the daemon, in the order
// they arrive there — which is the order that decides what size the
// agent's PTY ends up at.
type fakeTransport struct {
	mu      sync.Mutex
	applied []pty.Size
	refuse  bool
	output  chan pty.Message
}

func newFakeTransport() *fakeTransport {
	return &fakeTransport{output: make(chan pty.Message)}
}

func (t *fakeTransport) Seed() pty.Message          { return pty.Message{} }
func (t *fakeTransport) Output() <-chan pty.Message { return t.output }
func (t *fakeTransport) Write([]byte) error         { return nil }
func (t *fakeTransport) Close()                     {}

func (t *fakeTransport) Resize(_ context.Context, cols, rows int) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.refuse {
		return errors.New("daemon said no")
	}
	t.applied = append(t.applied, pty.Size{Cols: cols, Rows: rows})
	return nil
}

// refusing makes the next assertions fail, as a busy or unreachable
// daemon does.
func (t *fakeTransport) refusing(refuse bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.refuse = refuse
}

func (t *fakeTransport) count() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.applied)
}

// last is the size the daemon is left holding.
func (t *fakeTransport) last() pty.Size {
	t.mu.Lock()
	defer t.mu.Unlock()
	if len(t.applied) == 0 {
		return pty.Size{}
	}
	return t.applied[len(t.applied)-1]
}

type fakeBackend struct{ transport *fakeTransport }

func (b *fakeBackend) AttachTerminal(
	_ context.Context, _ string, _, _ int,
) (pty.Transport, error) {
	return b.transport, nil
}

// A drag across the screen is a burst of window sizes, and the dashboard
// answers each one with its own Ensure — Bubble Tea runs every returned
// command on its own goroutine, so the assertions race. The one that
// reaches the daemon last decides the agent's real PTY size, and it is
// not necessarily the last one the dashboard decided on.
//
// When a stale intermediate lands after the final size, the widget is
// left believing its box, the daemon holding something smaller, and
// nothing reconciles the two: Ensure re-asserts only when the box
// changes, and the box is already right. The agent then paints its
// narrow screen into the corner of a wide pane for good (#155, #164).
func TestAStaleResizeCannotLandAfterANewerOne(t *testing.T) {
	transport := newFakeTransport()
	manager := NewManager(&fakeBackend{transport: transport})
	ctx := context.Background()

	manager.Ensure(ctx, []string{"agent"}, 100, 40)

	// Two overlapping reconciles, as two window sizes from one drag.
	// Holding the first's command and running it second is exactly what
	// two command goroutines are free to do.
	stale := manager.Ensure(ctx, []string{"agent"}, 168, 42)
	current := manager.Ensure(ctx, []string{"agent"}, 249, 71)
	for _, resize := range current {
		resize()
	}
	for _, resize := range stale {
		resize()
	}

	widget, ok := manager.Widget("agent")
	if !ok {
		t.Fatal("no widget for the agent")
	}
	cols, rows := widget.Size()
	if cols != 249 || rows != 71 {
		t.Fatalf("box = %dx%d, want 249x71", cols, rows)
	}
	if got := transport.last(); got.Cols != 249 || got.Rows != 71 {
		t.Errorf("daemon left at %dx%d, want 249x71 — the size the "+
			"dashboard last decided on", got.Cols, got.Rows)
	}

	// And the reconcile that follows must repair it, whatever landed:
	// the box has not changed, so this is the only chance left.
	for _, resize := range manager.Ensure(ctx, []string{"agent"}, 249, 71) {
		resize()
	}
	if got := transport.last(); got.Cols != 249 || got.Rows != 71 {
		t.Errorf("after a further reconcile the daemon is still %dx%d, "+
			"want 249x71: nothing reclaims the pane", got.Cols, got.Rows)
	}
}

// A refused assertion is the same failure wearing different clothes: the
// box is the size the dashboard wants, so nothing about it says the
// terminal never moved. The next reconcile is the only thing standing
// between that and a pane stuck at the wrong size for good.
func TestARefusedResizeIsTriedAgain(t *testing.T) {
	transport := newFakeTransport()
	manager := NewManager(&fakeBackend{transport: transport})
	ctx := context.Background()

	manager.Ensure(ctx, []string{"agent"}, 100, 40)
	transport.refusing(true)
	for _, resize := range manager.Ensure(ctx, []string{"agent"}, 249, 71) {
		resize()
	}
	if got := transport.last(); got.Cols == 249 {
		t.Fatal("the refusal did not take; the test proves nothing")
	}

	transport.refusing(false)
	for _, resize := range manager.Ensure(ctx, []string{"agent"}, 249, 71) {
		resize()
	}
	if got := transport.last(); got.Cols != 249 || got.Rows != 71 {
		t.Errorf("daemon left at %dx%d after the refusal cleared, "+
			"want 249x71", got.Cols, got.Rows)
	}
}

// The steady state has to stay quiet. Reconciling runs on every refresh,
// several times a second, and a widget whose assertion landed has
// nothing to say to the daemon.
func TestAnUnchangedTerminalAssertsNothing(t *testing.T) {
	transport := newFakeTransport()
	manager := NewManager(&fakeBackend{transport: transport})
	ctx := context.Background()

	manager.Ensure(ctx, []string{"agent"}, 249, 71)
	for range 5 {
		for _, resize := range manager.Ensure(ctx, []string{"agent"}, 249, 71) {
			resize()
		}
	}
	if count := transport.count(); count != 0 {
		t.Errorf("%d resizes asserted on a terminal that never moved, "+
			"want none: the attach already carried the size", count)
	}
}

// The other half of #155, and the reason this compares what the widget
// asserted rather than the terminal's own size: a terminal is shared,
// and a viewer that moves it is not something the dashboard argues with.
// Two dashboards of different sizes would otherwise resize each other's
// agents forever.
func TestAnotherViewersResizeIsFollowedNotFought(t *testing.T) {
	transport := newFakeTransport()
	manager := NewManager(&fakeBackend{transport: transport})
	ctx := context.Background()

	manager.Ensure(ctx, []string{"agent"}, 249, 71)
	widget, ok := manager.Widget("agent")
	if !ok {
		t.Fatal("no widget for the agent")
	}
	// A browser lays itself out and moves the shared terminal.
	transport.output <- pty.Message{Resize: &pty.Size{Cols: 80, Rows: 24}}
	waitFor(t, func() bool {
		cols, rows := widget.TerminalSize()
		return cols == 80 && rows == 24
	}, "the emulator never followed the other viewer")

	for range 3 {
		for _, resize := range manager.Ensure(ctx, []string{"agent"}, 249, 71) {
			resize()
		}
	}
	if count := transport.count(); count != 0 {
		t.Errorf("%d resizes sent back at the other viewer, want none", count)
	}
}

func waitFor(t *testing.T, condition func() bool, complaint string) {
	t.Helper()
	for range 200 {
		if condition() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal(complaint)
}
