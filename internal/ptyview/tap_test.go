package ptyview

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/trentkm/spanreed"

	"github.com/trentkm/stormlight/internal/session"
)

// fakeTapBackend serves a canned pane: a history capture whose tail is a
// stale frame the pane no longer shows, and a screen capture with the
// truth.
type fakeTapBackend struct{}

func (fakeTapBackend) PipePaneOpen(context.Context, string, string) error        { return nil }
func (fakeTapBackend) PipePaneClose(context.Context, string) error               { return nil }
func (fakeTapBackend) ResizeAgentWindow(context.Context, string, int, int) error { return nil }
func (fakeTapBackend) SendRaw(context.Context, string, []byte) error             { return nil }

func (fakeTapBackend) CaptureRaw(_ context.Context, _ string, history int) (string, error) {
	if history > 0 {
		return "scrollback-1\nscrollback-2\nstale-a\nstale-b\nstale-c\nstale-d\nstale-e\nghost prompt text\n", nil
	}
	return "true screen row one\ntrue screen row two\n", nil
}

func (fakeTapBackend) PaneView(context.Context, string) (session.PaneView, error) {
	return session.PaneView{Cols: 40, Rows: 6, CursorX: 3, CursorY: 1}, nil
}

// The seed must rebuild the visible screen from the screen-only capture:
// history replays for scrollback, but its re-wrapped tail is never left on
// screen, where a diff-based TUI would keep it forever.
func TestTapSeedRebuildsScreenFromScreenOnlyCapture(t *testing.T) {
	transport, err := openTap(context.Background(), fakeTapBackend{}, "agent-1", t.TempDir(), 40, 6)
	if err != nil {
		t.Fatal(err)
	}
	widget := spanreed.New(transport, 40, 6)
	defer widget.Close()

	view := widget.View()
	if !strings.Contains(view, "true screen row one") ||
		!strings.Contains(view, "true screen row two") {
		t.Fatalf("screen capture missing from view:\n%s", view)
	}
	if strings.Contains(view, "ghost") || strings.Contains(view, "stale") {
		t.Fatalf("stale history frame left on screen:\n%s", view)
	}
	if x, y, ok := widget.Cursor(); !ok || x != 3 || y != 1 {
		t.Fatalf("cursor at (%d,%d) ok=%v, want (3,1)", x, y, ok)
	}

	// The history that scrolled through during replay stays reachable.
	widget.ScrollBy(100)
	if back := widget.View(); !strings.Contains(back, "scrollback-1") {
		t.Fatalf("history lost from scrollback:\n%s", back)
	}
}

// Once the stream first goes quiet, the tap snaps the screen back to the
// pane's truth — the cure for frames lost in the capture-to-pipe gap while
// the program was painting.
func TestTapResyncsAfterFirstQuiet(t *testing.T) {
	transport, err := openTap(context.Background(), fakeTapBackend{}, "agent-2", t.TempDir(), 40, 6)
	if err != nil {
		t.Fatal(err)
	}
	defer transport.Close()

	deadline := time.After(3 * time.Second)
	for {
		select {
		case chunk := <-transport.Output():
			if strings.Contains(string(chunk), "\x1b[2J") &&
				strings.Contains(string(chunk), "true screen row one") {
				return
			}
		case <-deadline:
			t.Fatal("no resync frame arrived after the stream settled")
		}
	}
}
