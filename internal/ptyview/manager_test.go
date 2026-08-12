package ptyview

import (
	"context"
	"image/color"
	"strings"
	"testing"
	"time"

	"github.com/trentkm/oathgate"

	"github.com/trentkm/stormlight/internal/session"
)

// fakeStream is a native TerminalStream a test can feed by hand.
type fakeStream struct {
	seed   []byte
	output chan []byte
}

func (f *fakeStream) Seed() []byte                           { return f.seed }
func (f *fakeStream) Output() <-chan []byte                  { return f.output }
func (f *fakeStream) Write([]byte) error                     { return nil }
func (f *fakeStream) Resize(context.Context, int, int) error { return nil }
func (f *fakeStream) Close()                                 { close(f.output) }

type fakeStreamBackend struct{}

func (fakeStreamBackend) OpenTerminalStream(_ context.Context, _ string, _, _ int) (session.TerminalStream, error) {
	return &fakeStream{seed: []byte("seeded\r\n"), output: make(chan []byte, 1)}, nil
}

// TestManagerThreadsWidgetOptions proves the option provider reaches every
// widget the Manager builds: a default-background option must come out of
// Widget().View() as painted cells.
func TestManagerThreadsWidgetOptions(t *testing.T) {
	manager := NewManager(fakeStreamBackend{}, t.TempDir(), func() []oathgate.Option {
		return []oathgate.Option{
			oathgate.WithDefaultBackground(color.RGBA{R: 22, G: 24, B: 27, A: 255}),
		}
	})
	defer manager.CloseAll()
	manager.Ensure(context.Background(), []string{"agent-1"}, 40, 8)

	widget, ok := manager.Widget("agent-1")
	if !ok {
		t.Fatal("Ensure did not build a widget")
	}
	deadline := time.Now().Add(3 * time.Second)
	for !strings.Contains(widget.View(), "seeded") {
		if time.Now().After(deadline) {
			t.Fatalf("widget never showed the seed:\n%q", widget.View())
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !strings.Contains(widget.View(), "\x1b[48;2;22;24;27m") {
		t.Errorf("widget renders without the configured ground:\n%q", widget.View())
	}
}
