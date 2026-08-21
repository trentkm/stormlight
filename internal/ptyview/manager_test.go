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
	// entered signals each Resize as it begins; hold, when set, parks it
	// there until released. Together they let a test stand inside one
	// assertion while more Ensure calls arrive.
	entered chan struct{}
	hold    chan struct{}
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
	entered, hold, refuse := t.entered, t.hold, t.refuse
	t.mu.Unlock()
	if entered != nil {
		entered <- struct{}{}
	}
	if hold != nil {
		<-hold
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if refuse {
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

func (t *fakeTransport) sizes() []pty.Size {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]pty.Size(nil), t.applied...)
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

func (t *fakeTransport) count() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.applied)
}

type fakeBackend struct{ transport *fakeTransport }

func (b *fakeBackend) AttachTerminal(
	_ context.Context, _ string, _, _ int,
) (pty.Transport, error) {
	return b.transport, nil
}

// A drag across the screen is a burst of window sizes, and the dashboard
// answers each one with its own Ensure on its own goroutine. Left to run
// side by side, their assertions would race for the daemon, and the one
// landing last — not the one decided last — would own the agent's PTY
// (#155, #164). Ensure single-flights instead: while one reconcile is in
// the air, later calls only record the newest desired state, and the
// reconciler drains that record before it stops. An intermediate size is
// superseded before it is ever applied.
func TestOverlappingEnsuresCoalesceOntoTheNewest(t *testing.T) {
	transport := newFakeTransport()
	manager := NewManager(&fakeBackend{transport: transport})
	ctx := context.Background()

	manager.Ensure(ctx, []string{"agent"}, 100, 40)

	// Park the next assertion mid-flight.
	entered := make(chan struct{}, 1)
	hold := make(chan struct{})
	transport.mu.Lock()
	transport.entered, transport.hold = entered, hold
	transport.mu.Unlock()

	done := make(chan struct{})
	go func() {
		defer close(done)
		manager.Ensure(ctx, []string{"agent"}, 168, 42)
	}()
	<-entered

	// Two more window sizes land while the first is still in the air.
	// Both return without reconciling; only the newest survives.
	manager.Ensure(ctx, []string{"agent"}, 200, 55)
	manager.Ensure(ctx, []string{"agent"}, 249, 71)

	transport.mu.Lock()
	transport.entered, transport.hold = nil, nil
	transport.mu.Unlock()
	close(hold)
	<-done

	for _, size := range transport.sizes() {
		if size.Cols == 200 {
			t.Error("the superseded 200x55 reached the daemon; " +
				"it should have been coalesced away")
		}
	}
	if got := transport.last(); got.Cols != 249 || got.Rows != 71 {
		t.Errorf("daemon left at %dx%d, want 249x71 — the newest size "+
			"anyone asked for", got.Cols, got.Rows)
	}
	widget, ok := manager.Widget("agent")
	if !ok {
		t.Fatal("no widget for the agent")
	}
	if cols, rows := widget.Size(); cols != 249 || rows != 71 {
		t.Errorf("box = %dx%d, want 249x71", cols, rows)
	}
}

// The single-flight covers Ensure against itself, but SetSize commands
// can still run on whatever goroutine Bubble Tea gives them — ResizeAll
// after an external attach, a zoom toggle. The widget itself must refuse
// to let a stale decision overtake a newer one on the way to the daemon.
func TestAStaleResizeCannotLandAfterANewerOne(t *testing.T) {
	transport := newFakeTransport()
	manager := NewManager(&fakeBackend{transport: transport})
	ctx := context.Background()

	manager.Ensure(ctx, []string{"agent"}, 100, 40)
	widget, ok := manager.Widget("agent")
	if !ok {
		t.Fatal("no widget for the agent")
	}

	// Two decisions, run in the wrong order — exactly what two command
	// goroutines are free to do.
	_, stale := widget.SetSize(168, 42)
	_, current := widget.SetSize(249, 71)
	current()
	stale()

	if got := transport.last(); got.Cols != 249 || got.Rows != 71 {
		t.Errorf("daemon left at %dx%d, want 249x71 — the size decided "+
			"last", got.Cols, got.Rows)
	}
}

// A refused assertion leaves the box the size the dashboard wants, so
// nothing about it says the terminal never moved. The next reconcile is
// the only thing standing between that and a pane stuck at the wrong
// size for good.
func TestARefusedResizeIsTriedAgain(t *testing.T) {
	transport := newFakeTransport()
	manager := NewManager(&fakeBackend{transport: transport})
	ctx := context.Background()

	manager.Ensure(ctx, []string{"agent"}, 100, 40)
	transport.refusing(true)
	manager.Ensure(ctx, []string{"agent"}, 249, 71)
	if got := transport.last(); got.Cols == 249 {
		t.Fatal("the refusal did not take; the test proves nothing")
	}

	transport.refusing(false)
	manager.Ensure(ctx, []string{"agent"}, 249, 71)
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
		manager.Ensure(ctx, []string{"agent"}, 249, 71)
	}
	if count := transport.count(); count != 0 {
		t.Errorf("%d resizes asserted on a terminal that never moved, "+
			"want none: the attach already carried the size", count)
	}
}

// The other half of #155, and the reason needsSize compares what the
// widget asserted rather than the terminal's own size: a terminal is
// shared, and a viewer that moves it is not something the dashboard
// argues with. Two dashboards of different sizes would otherwise resize
// each other's agents forever.
func TestAnotherViewersResizeIsFollowedNotFought(t *testing.T) {
	transport := newFakeTransport()
	manager := NewManager(&fakeBackend{transport: transport})
	ctx := context.Background()

	manager.Ensure(ctx, []string{"agent"}, 249, 71)
	widget, ok := manager.Widget("agent")
	if !ok {
		t.Fatal("no widget for the agent")
	}
	// Another viewer states its own geometry and the daemon follows it.
	transport.output <- pty.Message{Resize: &pty.Size{Cols: 80, Rows: 24}}
	waitFor(t, func() bool {
		cols, rows := widget.TerminalSize()
		return cols == 80 && rows == 24
	}, "the emulator never followed the other viewer")

	for range 3 {
		manager.Ensure(ctx, []string{"agent"}, 249, 71)
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
