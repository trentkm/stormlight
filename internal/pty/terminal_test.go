package pty

import (
	"bytes"
	"context"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

type fakeTransport struct {
	seed   []byte
	output chan []byte
	writes [][]byte
}

func newFakeTransport(seed string) *fakeTransport {
	return &fakeTransport{seed: []byte(seed), output: make(chan []byte)}
}

func (t *fakeTransport) Seed() []byte                           { return t.seed }
func (t *fakeTransport) Output() <-chan []byte                  { return t.output }
func (t *fakeTransport) Write(data []byte) error                { t.writes = append(t.writes, data); return nil }
func (t *fakeTransport) Resize(context.Context, int, int) error { return nil }
func (t *fakeTransport) Close()                                 { close(t.output) }

func TestTerminalRendersSeedAndScrollback(t *testing.T) {
	transport := newFakeTransport("first\r\nsecond\r\nthird\r\nfourth")
	terminal := New(transport, 12, 2)
	defer terminal.Close()

	if got := terminal.View(); !strings.Contains(got, "third") || !strings.Contains(got, "fourth") {
		t.Fatalf("live terminal missing final rows:\n%s", got)
	}
	terminal.ScrollBy(100)
	if got := terminal.View(); !strings.Contains(got, "first") || !strings.Contains(got, "second") {
		t.Fatalf("scrollback missing seeded rows:\n%s", got)
	}
	if terminal.Scrolled() == 0 {
		t.Fatal("scroll position did not move into history")
	}
}

func TestTerminalCursorAndKeyEncoding(t *testing.T) {
	transport := newFakeTransport("one\r\ntwo")
	terminal := New(transport, 8, 3)
	defer terminal.Close()

	x, y, ok := terminal.Cursor()
	if !ok || x != 3 || y != 1 {
		t.Fatalf("cursor = (%d, %d, %v), want (3, 1, true)", x, y, ok)
	}
	for _, message := range []tea.KeyPressMsg{
		{Text: "é"},
		{Code: tea.KeyEnter},
		{Code: tea.KeyUp},
		{Code: 'c', Mod: tea.ModCtrl},
	} {
		if data := KeyToBytes(message); len(data) == 0 {
			t.Fatalf("KeyToBytes(%q) returned no bytes", message.String())
		}
	}
	if got := KeyToBytes(tea.KeyPressMsg{Code: tea.KeyUp}); !bytes.Equal(got, []byte("\x1b[A")) {
		t.Fatalf("up key = %q, want CSI up", got)
	}
}

func TestViewIsCachedUntilTheTerminalChanges(t *testing.T) {
	transport := newFakeTransport("hello")
	terminal := New(transport, 12, 2)
	defer terminal.Close()

	first := terminal.View()
	if terminal.state.viewDirty {
		t.Fatal("render left the cache dirty")
	}
	if second := terminal.View(); second != first {
		t.Fatalf("idle views differ:\n%q\n%q", first, second)
	}

	// The frame notification is sent after the write lands, so receiving
	// it proves the emulator has the new bytes.
	transport.output <- []byte(" world")
	<-terminal.state.frames
	if !terminal.state.viewDirty {
		t.Fatal("streamed bytes did not invalidate the cache")
	}
	if got := terminal.View(); !strings.Contains(got, "hello world") {
		t.Fatalf("view missing streamed bytes:\n%s", got)
	}
}
