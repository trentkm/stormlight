package ui

// The wheel's cost. A trackpad flick is thousands of messages, and each
// one of them used to buy a full dashboard build — and, over a
// mouse-aware agent, a goroutine holding a write. These pin the bounds
// that replaced both.

import (
	"context"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/trentkm/stormlight/internal/agent"
	"github.com/trentkm/stormlight/internal/pty"
	"github.com/trentkm/stormlight/internal/workspace"
)

// wheelTransport is one agent's terminal for these tests: a seeded
// replica, an output channel the test feeds by hand, and a write side
// that can be told to stop draining.
type wheelTransport struct {
	seed   string
	output chan pty.Message

	mu      sync.Mutex
	writes  int
	bytes   []byte
	blocked chan struct{}
}

func newWheelTransport(seed string) *wheelTransport {
	return &wheelTransport{seed: seed, output: make(chan pty.Message, 8)}
}

// block makes every write park until the test releases it, which is what
// a daemon that has stopped answering looks like from here.
func (w *wheelTransport) block() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.blocked = make(chan struct{})
}

func (w *wheelTransport) release() {
	w.mu.Lock()
	gate := w.blocked
	w.blocked = nil
	w.mu.Unlock()
	if gate != nil {
		close(gate)
	}
}

func (w *wheelTransport) Seed() pty.Message {
	return pty.Message{Resync: []byte(w.seed)}
}
func (w *wheelTransport) Output() <-chan pty.Message { return w.output }
func (w *wheelTransport) Write(data []byte) error {
	w.mu.Lock()
	w.writes++
	w.bytes = append(w.bytes, data...)
	gate := w.blocked
	w.mu.Unlock()
	if gate != nil {
		<-gate
	}
	return nil
}
func (w *wheelTransport) Resize(context.Context, int, int) error { return nil }
func (w *wheelTransport) Close()                                 { close(w.output) }

func (w *wheelTransport) written() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.writes
}

func (w *wheelTransport) delivered() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return string(w.bytes)
}

// wheelBackend hands the terminal herd the one transport under test.
type wheelBackend struct {
	stubBackend
	transport *wheelTransport
}

func (b wheelBackend) AttachTerminal(
	context.Context, string, int, int,
) (pty.Transport, error) {
	return b.transport, nil
}

// wheelModelFixture is the dashboard as the wheel meets it: one agent,
// its terminal live and zoomed, so every column of the body belongs to
// the portal and a tick anywhere lands on it.
func wheelModelFixture(t *testing.T, transport *wheelTransport) (Model, pty.Model) {
	t.Helper()
	model := NewModel(wheelBackend{transport: transport})
	workspaceContext := workspace.DirectoryContext("/tmp/wheel")
	model.agents = []agent.Agent{{
		ID:          "wheel-1",
		Name:        "wheel-1",
		Provider:    agent.ProviderClaude,
		ProcessLive: true,
		Workspace:   workspaceContext,
	}}
	model.rebuildGroups(workspaceContext.ID, "wheel-1")

	updated, cmd := model.Update(tea.WindowSizeMsg{Width: 180, Height: 55})
	model = updated.(Model)
	if cmd != nil {
		cmd()
	}
	model.activePane = paneInteraction
	model.ptyZoom = true
	manager := model.ptyManager
	t.Cleanup(manager.CloseAll)

	widget, ok := model.selectedPTY()
	if !ok {
		t.Fatal("the agent's terminal never opened")
	}
	return model, widget
}

// scrollbackSeed is more lines than the fixture's grid holds, so there is
// history above the screen for the wheel to reach.
func scrollbackSeed(lines int) string {
	rows := make([]string, lines)
	for index := range rows {
		rows[index] = "seeded line"
	}
	return strings.Join(rows, "\r\n")
}

func wheelUp() tea.MouseWheelMsg {
	return tea.MouseWheelMsg{X: 40, Y: 10, Button: tea.MouseWheelUp}
}

// TestWheelBurstBuildsOneFrame is the bound at the event-loop boundary. A
// wheel tick moves nothing by itself — the replica scrolls on the
// coalesced flush a frame later — so a burst of them must cost no
// dashboard builds at all, and the scroll must still land when the flush
// arrives.
func TestWheelBurstBuildsOneFrame(t *testing.T) {
	transport := newWheelTransport(scrollbackSeed(200))
	model, widget := wheelModelFixture(t, transport)

	// Bubble Tea renders after every Update it runs, so the test does too.
	model.View()
	settled := model.frame.builds
	if settled == 0 {
		t.Fatal("the first render built no frame")
	}

	var flush tea.Cmd
	for range 500 {
		updated, cmd := model.Update(wheelUp())
		model = updated.(Model)
		if cmd != nil {
			flush = cmd
		}
		model.View()
	}
	if built := model.frame.builds - settled; built != 0 {
		t.Fatalf("500 wheel events built %d dashboards, want none", built)
	}
	if flush == nil {
		t.Fatal("the burst scheduled no coalesced flush")
	}

	// The flush is what the held frame was waiting for: the accumulated
	// delta lands there, and that paints.
	updated, _ := model.Update(flush())
	model = updated.(Model)
	model.View()
	if built := model.frame.builds - settled; built != 1 {
		t.Fatalf("the flush built %d dashboards, want exactly 1", built)
	}
	if widget.Scrolled() == 0 {
		t.Fatal("500 wheel events left the replica where it started")
	}
}

// TestRenderingAnAgentTouchesNoSymlinks keeps the filesystem out of the
// render path. A frame is built for every message the event loop takes,
// so an lstat chain in there is paid thousands of times during a flick;
// the roots a frame compares are canonical before they ever arrive.
func TestRenderingAnAgentTouchesNoSymlinks(t *testing.T) {
	transport := newWheelTransport("rendered")
	model, _ := wheelModelFixture(t, transport)
	// The worktree case: an execution root that is not the workspace's
	// own, which is the comparison that used to resolve both of them.
	value := workspace.Context{
		ID:            "git:/tmp/wheel/.git",
		Kind:          workspace.KindGit,
		Name:          "wheel",
		Root:          "/tmp/wheel",
		ExecutionRoot: "/tmp/wheel/.claude/worktrees/fix",
	}
	model.agents[0].Workspace = value
	model.rebuildGroups(value.ID, "wheel-1")

	walks := 0
	original := evalSymlinks
	evalSymlinks = func(path string) (string, error) {
		walks++
		return original(path)
	}
	t.Cleanup(func() { evalSymlinks = original })

	model.View()
	if walks != 0 {
		t.Fatalf("a dashboard frame walked %d paths through EvalSymlinks, want none", walks)
	}
	// And it still says which checkout the agent stands in.
	bar := ansi.Strip(model.renderTerminalBar(model.agents[0], 120))
	if !strings.Contains(bar, "worktree fix") {
		t.Fatalf("the terminal bar stopped naming the worktree: %q", bar)
	}
}

// TestBlockedTerminalBoundsWheelWrites is the other half of the bound. A
// mouse-aware agent takes the wheel itself, so every raw tick forwards as
// an SGR report; against a transport that has stopped draining, those
// must queue behind one writer rather than each taking a goroutine and a
// place in an unbounded pile.
func TestBlockedTerminalBoundsWheelWrites(t *testing.T) {
	transport := newWheelTransport("mouse")
	model, widget := wheelModelFixture(t, transport)

	// The hosted program asks for the mouse the only way it can: by
	// saying so on its own output stream.
	transport.output <- pty.Message{Bytes: []byte("\x1b[?1000h")}
	deadline := time.Now().Add(2 * time.Second)
	for !widget.MouseReporting() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if !widget.MouseReporting() {
		t.Fatal("the terminal never noticed the mouse-tracking mode")
	}

	transport.block()
	t.Cleanup(transport.release)
	// Park the writer on a blocked write before measuring, so what the
	// burst meets is a stalled transport rather than a race with one.
	if err := widget.Write([]byte("wake")); err != nil {
		t.Fatalf("priming write: %v", err)
	}
	for transport.written() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}

	before := runtime.NumGoroutine()
	const burst = 2000
	for range burst {
		updated, cmd := model.Update(wheelUp())
		model = updated.(Model)
		if cmd != nil {
			t.Fatal("a forwarded wheel tick returned a command to run in a goroutine")
		}
		model.View()
	}
	if grew := runtime.NumGoroutine() - before; grew > 4 {
		t.Fatalf("%d forwarded wheel ticks added %d goroutines", burst, grew)
	}
	// The queue filled and started refusing, which is the bound saying so
	// out loud rather than growing.
	if err := widget.Write([]byte("one more")); err != pty.ErrWriteQueueFull {
		t.Fatalf("write past a full queue = %v, want ErrWriteQueueFull", err)
	}
	if got := transport.written(); got > 1 {
		t.Fatalf("a blocked transport took %d writes, want the one it is parked on", got)
	}
}

// TestTerminalInputKeepsItsOrder is the other half of moving writes off
// commands. A command per keystroke meant a goroutine per keystroke, all
// of them racing for the attachment's one mutex, so what reached the
// agent was whatever order the scheduler happened to hand out. One queue
// and one writer is what makes typing arrive as typed.
func TestTerminalInputKeepsItsOrder(t *testing.T) {
	transport := newWheelTransport("prompt")
	model, _ := wheelModelFixture(t, transport)

	const typed = "the quick brown fox"
	for _, letter := range typed {
		updated, _ := model.updateTerminalKey(
			tea.KeyPressMsg{Text: string(letter), Code: letter})
		model = updated.(Model)
	}

	deadline := time.Now().Add(2 * time.Second)
	for transport.delivered() != typed && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if got := transport.delivered(); got != typed {
		t.Fatalf("the agent received %q, want %q", got, typed)
	}
}
