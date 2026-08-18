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

func TestTerminalPreservesHyperlinks(t *testing.T) {
	const url = "https://example.com/docs"
	correct := []string{
		"\x1b]8;;" + url + "\aempty params\x1b]8;;\a",
		"\x1b]8;id=docs;" + url + "\apopulated params\x1b]8;;\a",
	}
	for _, testCase := range []struct {
		name, seed string
	}{
		{"raw PTY output", strings.Join(correct, " ")},
		{"windrunner snapshot",
			"\x1b]8;" + url + ";\aempty params\x1b]8;;\a " +
				"\x1b]8;" + url + ";id=docs\apopulated params\x1b]8;;\a"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			transport := newFakeTransport(testCase.seed)
			terminal := New(transport, NewGate(), 50, 2)
			defer terminal.Close()

			view := terminal.View()
			for _, want := range correct {
				if !strings.Contains(view, want) {
					t.Errorf("terminal dropped hyperlink %q from %q", want, view)
				}
			}
		})
	}
}

func TestTerminalRepairsHyperlinksInWindrunnerRepaints(t *testing.T) {
	const url = "https://example.com/docs"
	transport := newFakeTransport("")
	terminal := New(transport, NewGate(), 50, 2)
	defer terminal.Close()
	terminal.SetVisible(true)

	transport.output <- []byte("\x1b]8;" + url + ";\arepaint\x1b]8;;\a")
	<-terminal.state.gate.frames
	if view := terminal.View(); !strings.Contains(
		view, "\x1b]8;;"+url+"\arepaint\x1b]8;;\a",
	) {
		t.Errorf("terminal dropped repaint hyperlink from %q", view)
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

func TestKeyEncoderCoversNavigationAndFunctionKeys(t *testing.T) {
	for _, test := range []struct {
		key  string
		want string
	}{
		{"f1", "\x1bOP"},
		{"f5", "\x1b[15~"},
		{"f12", "\x1b[24~"},
		{"ctrl+up", "\x1b[1;5A"},
		{"shift+right", "\x1b[1;2C"},
		{"ctrl+shift+left", "\x1b[1;6D"},
		{"alt+f1", "\x1b[1;3P"},
		{"insert", "\x1b[2~"},
		{"ctrl+delete", "\x1b[3;5~"},
		{"ctrl+home", "\x1b[1;5H"},
		{"alt+backspace", "\x1b\x7f"},
		{"shift+pgup", "\x1b[5;2~"},
	} {
		got := KeyToBytes(keyNamed(test.key))
		if string(got) != test.want {
			t.Errorf("KeyToBytes(%s) = %q, want %q", test.key, got, test.want)
		}
	}
}

// keyNamed builds a KeyPressMsg whose String() is the given chord — the
// encoder only reads the name for these keys.
func keyNamed(name string) tea.KeyPressMsg {
	return namedKeys[name]
}

var namedKeys = map[string]tea.KeyPressMsg{
	"f1":              {Code: tea.KeyF1},
	"f5":              {Code: tea.KeyF5},
	"f12":             {Code: tea.KeyF12},
	"ctrl+up":         {Code: tea.KeyUp, Mod: tea.ModCtrl},
	"shift+right":     {Code: tea.KeyRight, Mod: tea.ModShift},
	"ctrl+shift+left": {Code: tea.KeyLeft, Mod: tea.ModCtrl | tea.ModShift},
	"alt+f1":          {Code: tea.KeyF1, Mod: tea.ModAlt},
	"insert":          {Code: tea.KeyInsert},
	"ctrl+delete":     {Code: tea.KeyDelete, Mod: tea.ModCtrl},
	"ctrl+home":       {Code: tea.KeyHome, Mod: tea.ModCtrl},
	"alt+backspace":   {Code: tea.KeyBackspace, Mod: tea.ModAlt},
	"shift+pgup":      {Code: tea.KeyPgUp, Mod: tea.ModShift},
}
