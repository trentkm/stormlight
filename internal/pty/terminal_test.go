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
	terminal := New(transport, NewGate(), 12, 2)
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
	terminal := New(transport, NewGate(), 8, 3)
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
	terminal := New(transport, NewGate(), 12, 2)
	defer terminal.Close()

	first := terminal.View()
	if terminal.state.viewDirty {
		t.Fatal("render left the cache dirty")
	}
	if second := terminal.View(); second != first {
		t.Fatalf("idle views differ:\n%q\n%q", first, second)
	}

	// The gate knock is sent after the write lands, so receiving it
	// proves the emulator has the new bytes.
	terminal.SetVisible(true)
	transport.output <- []byte(" world")
	<-terminal.state.gate.frames
	if !terminal.state.viewDirty {
		t.Fatal("streamed bytes did not invalidate the cache")
	}
	if got := terminal.View(); !strings.Contains(got, "hello world") {
		t.Fatalf("view missing streamed bytes:\n%s", got)
	}
}

func TestOnlyVisibleTerminalsKnockOnTheGate(t *testing.T) {
	gate := NewGate()
	transport := newFakeTransport("quiet")
	terminal := New(transport, gate, 12, 2)
	defer terminal.Close()

	// The output channel is unbuffered, so each send proves the pump
	// finished the previous chunk — gate decision included.
	transport.output <- []byte("a")
	transport.output <- []byte("b")
	select {
	case <-gate.frames:
		t.Fatal("an invisible terminal requested a redraw")
	default:
	}

	terminal.SetVisible(true)
	transport.output <- []byte("c")
	<-gate.frames
}

type notifyingTransport struct {
	*fakeTransport
	handler func(cols, rows int)
}

func (t *notifyingTransport) OnResize(handler func(cols, rows int)) {
	t.handler = handler
}

// Someone else moving the hosted terminal — another dashboard, an F
// attach — reaches the widget as a resize notice: the emulator follows
// the terminal's true size while the box keeps the pane's.
func TestWidgetFollowsATerminalMovedBySomeoneElse(t *testing.T) {
	transport := &notifyingTransport{fakeTransport: newFakeTransport("hi")}
	terminal := New(transport, NewGate(), 100, 30)
	defer terminal.Close()
	if transport.handler == nil {
		t.Fatal("widget did not register for resize notices")
	}

	transport.handler(34, 40)
	if cols, rows := terminal.TerminalSize(); cols != 34 || rows != 40 {
		t.Fatalf("terminal size = %dx%d, want 34x40", cols, rows)
	}
	if cols, rows := terminal.Size(); cols != 100 || rows != 30 {
		t.Fatalf("box size changed to %dx%d; the pane owns the box", cols, rows)
	}

	// A deliberate SetSize re-asserts both.
	terminal.SetSize(80, 24)
	if cols, rows := terminal.TerminalSize(); cols != 80 || rows != 24 {
		t.Fatalf("terminal size after SetSize = %dx%d", cols, rows)
	}
}

func TestMouseReportingShadowsTheHostedProgramsModes(t *testing.T) {
	transport := newFakeTransport("x")
	terminal := New(transport, NewGate(), 20, 4)
	defer terminal.Close()
	if terminal.MouseReporting() {
		t.Fatal("mouse reporting on before the program asked")
	}
	transport.output <- []byte("\x1b[?1000h\x1b[?1006h")
	transport.output <- []byte("sync")
	if !terminal.MouseReporting() {
		t.Fatal("mouse-on did not register")
	}
	transport.output <- []byte("\x1b[?1000l")
	transport.output <- []byte("sync")
	if terminal.MouseReporting() {
		t.Fatal("mouse-off did not register")
	}
}

func TestTextReturnsThePlainScreen(t *testing.T) {
	transport := newFakeTransport("\x1b[31mred\x1b[0m line")
	terminal := New(transport, NewGate(), 20, 3)
	defer terminal.Close()
	lines := terminal.Text()
	if len(lines) != 3 || lines[0] != "red line" {
		t.Fatalf("text = %q", lines)
	}
}
