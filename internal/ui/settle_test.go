package ui

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/trentkm/stormlight/internal/agent"
	"github.com/trentkm/stormlight/internal/pty"
	"github.com/trentkm/stormlight/internal/ptyview"
	"github.com/trentkm/stormlight/internal/workspace"
)

// settleTransport is one agent's terminal that remembers every size the
// daemon was told, in order — which is what decides what the agent
// repaints at and how many times it has to.
type settleTransport struct {
	output chan pty.Message
	mu     sync.Mutex
	sizes  []pty.Size
}

func newSettleTransport() *settleTransport {
	return &settleTransport{output: make(chan pty.Message, 8)}
}

func (s *settleTransport) Seed() pty.Message {
	return pty.Message{Resync: []byte("$ ")}
}
func (s *settleTransport) Output() <-chan pty.Message { return s.output }
func (s *settleTransport) Write([]byte) error         { return nil }
func (s *settleTransport) Close()                     { close(s.output) }
func (s *settleTransport) Resize(_ context.Context, cols, rows int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sizes = append(s.sizes, pty.Size{Cols: cols, Rows: rows})
	return nil
}

func (s *settleTransport) asserted() []pty.Size {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]pty.Size(nil), s.sizes...)
}

type settleBackend struct {
	stubBackend
	transport *settleTransport
}

func (b settleBackend) AttachTerminal(
	context.Context, string, int, int,
) (pty.Transport, error) {
	return b.transport, nil
}

// deliver runs a command and feeds what it returns to the model, one
// level deep: the herd's reconcile runs here, but what the model returns
// for the reconcile's own message — the frame wait, which blocks until a
// terminal paints — is dropped, as the wheel fixture drops it.
func deliver(model Model, cmd tea.Cmd) Model {
	if cmd == nil {
		return model
	}
	switch msg := cmd().(type) {
	case nil:
	case tea.BatchMsg:
		for _, inner := range msg {
			model = deliver(model, inner)
		}
	default:
		updated, _ := model.Update(msg)
		model = updated.(Model)
	}
	return model
}

// settleFixture is the dashboard with one live, zoomed terminal, laid
// out once so the herd exists at the first size.
func settleFixture(t *testing.T) (Model, *settleTransport) {
	t.Helper()
	transport := newSettleTransport()
	model := NewModel(settleBackend{transport: transport})
	workspaceContext := workspace.DirectoryContext("/tmp/settle")
	model.agents = []agent.Agent{{
		ID:          "settle-1",
		Name:        "settle-1",
		Provider:    agent.ProviderCodex,
		ProcessLive: true,
		Workspace:   workspaceContext,
	}}
	model.rebuildGroups(workspaceContext.ID, "settle-1")
	model.ptyEnabled = true
	model.ptyZoom = true

	updated, cmd := model.Update(tea.WindowSizeMsg{Width: 180, Height: 55})
	model = deliver(updated.(Model), cmd)
	if _, ok := model.selectedPTY(); !ok {
		t.Fatal("the terminal did not open at the first size")
	}
	return model, transport
}

func waitForSizes(t *testing.T, transport *settleTransport, want int) []pty.Size {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for len(transport.asserted()) < want && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	return transport.asserted()
}

// A window drag is a burst of sizes; the daemon hears one, the last,
// once the pane holds still — and a refresh that lands mid-drag cannot
// leak an intermediate one (#243).
func TestADragLandsOneResize(t *testing.T) {
	model, transport := settleFixture(t)
	before := len(transport.asserted())

	for _, width := range []int{170, 160, 150, 140} {
		updated, cmd := model.Update(tea.WindowSizeMsg{Width: width, Height: 55})
		model = deliver(updated.(Model), cmd)
	}
	// The 700ms poll comes back while the drag is still going.
	updated, cmd := model.Update(dashboardMsg{agents: model.agents})
	model = deliver(updated.(Model), cmd)

	if got := transport.asserted(); len(got) != before {
		t.Fatalf("the daemon heard sizes mid-drag, want none: %v", got[before:])
	}
	widget, _ := model.selectedPTY()
	if !widget.Settling() {
		t.Fatal("mid-drag the terminal is not settling")
	}
	liveCols, liveRows := model.ptyGridDimensions()
	if cols, rows := widget.Size(); cols != liveCols || rows != liveRows {
		t.Fatalf("mid-drag the box is %dx%d, want the live %dx%d", cols, rows, liveCols, liveRows)
	}

	got := waitForSizes(t, transport, before+1)
	if len(got) != before+1 {
		t.Fatalf("the settled drag asserted %d sizes, want 1: %v", len(got)-before, got[before:])
	}
	if last := got[len(got)-1]; last.Cols != liveCols || last.Rows != liveRows {
		t.Fatalf("daemon left at %v, want the final %dx%d", last, liveCols, liveRows)
	}
	time.Sleep(2 * ptyview.ResizeSettle)
	if got := transport.asserted(); len(got) != before+1 {
		t.Fatalf("more sizes followed the settled one: %v", got[before+1:])
	}
}

// A zoom is one event, not a gesture, and reflows at once.
func TestAZoomReflowsAtOnce(t *testing.T) {
	model, transport := settleFixture(t)
	before := len(transport.asserted())
	updated, cmd := model.updateTerminalKey(tea.KeyPressMsg{Code: 'z', Mod: tea.ModAlt})
	model = deliver(updated.(Model), cmd)
	if model.ptyZoom {
		t.Fatal("alt+z did not leave zoom; the chord this test drives is not the zoom chord")
	}
	got := transport.asserted()
	if len(got) != before+1 {
		t.Fatalf("an unzoom asserted %d sizes synchronously, want 1: %v", len(got)-before, got[before:])
	}
	widget, _ := model.selectedPTY()
	if widget.Settling() {
		t.Fatal("a zoom left the terminal settling")
	}
}

// For the beat a drag lasts, the window bar names the size the pane is
// heading for, and only then.
func TestTheBarNamesTheSizeThePaneIsHeadingFor(t *testing.T) {
	model, transport := settleFixture(t)
	selected, _ := model.selectedAgent()
	before := ansi.Strip(model.renderTerminalBar(selected, 120))
	if strings.Contains(before, "×") {
		t.Fatalf("a settled bar shows dimensions nobody asked for: %q", before)
	}

	updated, cmd := model.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	model = deliver(updated.(Model), cmd)
	during := ansi.Strip(model.renderTerminalBar(selected, 120))
	cols, rows := model.ptyGridDimensions()
	if want := fmt.Sprintf("%d×%d", cols, rows); !strings.Contains(during, want) {
		t.Fatalf("mid-drag the bar does not name %q: %q", want, during)
	}

	waitForSizes(t, transport, 1)
	after := ansi.Strip(model.renderTerminalBar(selected, 120))
	if strings.Contains(after, "×") {
		t.Fatalf("settled, the bar still shows the pending size: %q", after)
	}
}

// Mid-drag the pane is the live size and the view fits it: the widget
// clips its old screen to the new box, so nothing wraps.
func TestTheOldScreenFitsTheLivePaneMidDrag(t *testing.T) {
	model, _ := settleFixture(t)
	updated, cmd := model.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	model = deliver(updated.(Model), cmd)
	liveCols, liveRows := model.ptyGridDimensions()
	selected, _ := model.selectedAgent()
	rows := strings.Split(model.renderPTYInteraction(selected, 0, 0), "\n")
	if len(rows) != liveRows {
		t.Fatalf("mid-drag pane is %d rows, want the live %d", len(rows), liveRows)
	}
	for index, row := range rows {
		if width := ansi.StringWidth(row); width > liveCols {
			t.Fatalf("row %d is %d wide, over the live %d", index, width, liveCols)
		}
	}
}
