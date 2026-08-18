package pty

import (
	"bytes"
	"context"
	"runtime"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
)

type fakeTransport struct {
	seed   []byte
	output chan Message
	writes [][]byte
}

func newFakeTransport(seed string) *fakeTransport {
	return &fakeTransport{seed: []byte(seed), output: make(chan Message)}
}

func (t *fakeTransport) Seed() []byte                           { return t.seed }
func (t *fakeTransport) Output() <-chan Message                 { return t.output }
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
	transport.output <- Message{Bytes: []byte(" world")}
	<-terminal.state.gate.frames
	if !terminal.state.viewDirty {
		t.Fatal("streamed bytes did not invalidate the cache")
	}
	if got := terminal.View(); !strings.Contains(got, "hello world") {
		t.Fatalf("view missing streamed bytes:\n%s", got)
	}
}

// A resync means this viewer fell behind and the daemon sent the screen
// as it now stands instead of the bytes it missed. Writing that into the
// emulator it already has would replay a history the replica is holding —
// the scrollback arrives twice and the screen reads as a stutter. It
// replaces the replica instead.
func TestResyncReplacesTheReplicaRatherThanAppending(t *testing.T) {
	transport := newFakeTransport("first line")
	terminal := New(transport, NewGate(), 40, 6)
	defer terminal.Close()
	terminal.SetVisible(true)

	transport.output <- Message{Bytes: []byte("\r\nsecond line")}
	<-terminal.state.gate.frames

	transport.output <- Message{Resync: []byte("only what is true now")}
	<-terminal.state.gate.frames

	view := terminal.View()
	if !strings.Contains(view, "only what is true now") {
		t.Fatalf("resync did not reach the replica:\n%s", view)
	}
	for _, stale := range []string{"first line", "second line"} {
		if strings.Contains(view, stale) {
			t.Fatalf("resync left %q behind; state replaces, it does not append:\n%s",
				stale, view)
		}
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

	transport.output <- Message{Bytes: []byte("\x1b]8;" + url + ";\arepaint\x1b]8;;\a")}
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
	transport.output <- Message{Bytes: []byte("a")}
	transport.output <- Message{Bytes: []byte("b")}
	select {
	case <-gate.frames:
		t.Fatal("an invisible terminal requested a redraw")
	default:
	}

	terminal.SetVisible(true)
	transport.output <- Message{Bytes: []byte("c")}
	<-gate.frames
}

// Someone else moving the hosted terminal — another dashboard, a browser,
// an F attach — reaches the widget as a resize riding the stream: the
// emulator follows the terminal's true size while the box keeps the
// pane's.
func TestWidgetFollowsATerminalMovedBySomeoneElse(t *testing.T) {
	transport := newFakeTransport("hi")
	terminal := New(transport, NewGate(), 100, 30)
	defer terminal.Close()

	transport.output <- Message{Resize: &Size{Cols: 34, Rows: 40}}
	waitForTerminal(t, terminal, 34, 40)
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

// waitForTerminal waits for the widget's emulator to adopt a size that
// now arrives over the stream rather than through a direct call.
func waitForTerminal(t *testing.T, terminal Model, cols, rows int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if c, r := terminal.TerminalSize(); c == cols && r == rows {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	c, r := terminal.TerminalSize()
	t.Fatalf("terminal size stayed %dx%d, want %dx%d", c, r, cols, rows)
}

func TestMouseReportingShadowsTheHostedProgramsModes(t *testing.T) {
	transport := newFakeTransport("x")
	terminal := New(transport, NewGate(), 20, 4)
	defer terminal.Close()
	if terminal.MouseReporting() {
		t.Fatal("mouse reporting on before the program asked")
	}
	transport.output <- Message{Bytes: []byte("\x1b[?1000h\x1b[?1006h")}
	transport.output <- Message{Bytes: []byte("sync")}
	if !terminal.MouseReporting() {
		t.Fatal("mouse-on did not register")
	}
	transport.output <- Message{Bytes: []byte("\x1b[?1000l")}
	transport.output <- Message{Bytes: []byte("sync")}
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

// A resync builds a whole new emulator, and the old one's drain goroutine
// holds it — scrollback included — until its pipe is closed. Leaving them
// behind costs tens of megabytes each, in exactly the situation resyncs
// happen: a viewer that cannot keep up, resynced as often as the daemon
// paces them.
func TestResyncDoesNotLeakTheReplicaItReplaced(t *testing.T) {
	transport := newFakeTransport("start")
	terminal := New(transport, NewGate(), 80, 24)
	terminal.SetVisible(true)

	before := goroutinesAtRest()
	for i := 0; i < 20; i++ {
		transport.output <- Message{Resync: []byte("replacement")}
		<-terminal.state.gate.frames
	}
	terminal.Close()

	// The drains end asynchronously once their pipes close.
	if after := goroutinesAtRest(); after > before {
		t.Fatalf("%d goroutines survived 20 resyncs and a close (%d before, %d after)",
			after-before, before, after)
	}
}

// The size travels with the state and nowhere else: while a viewer is in
// resync debt the daemon drops every other message, resize notices
// included. A replica that ignores it paints a wide screen into a narrow
// emulator, wrapped and wrong, until someone resizes again.
func TestResyncCarriesTheSizeItWasRenderedAt(t *testing.T) {
	transport := newFakeTransport("start")
	terminal := New(transport, NewGate(), 80, 24)
	defer terminal.Close()
	terminal.SetVisible(true)

	transport.output <- Message{
		Resync: []byte("wide state"),
		Resize: &Size{Cols: 132, Rows: 43},
	}
	<-terminal.state.gate.frames
	waitForTerminal(t, terminal, 132, 43)
}

// A resync already in flight when the widget closes must not revive it:
// installing that state would start a drain nothing is left to stop.
// Parsing a resync happens off the lock, which takes long enough for a
// resize to land in the middle of it. The newer size has to win: the
// daemon terminal is already at it, so installing the size the snapshot
// was rendered at leaves every later byte wrapped for one width and
// painted into an emulator of another — and nothing corrects it, because
// the box size never changed.
func TestResizeDuringAResyncIsNotLost(t *testing.T) {
	transport := newFakeTransport("start")
	terminal := New(transport, NewGate(), 80, 24)
	defer terminal.Close()

	// Big enough that the parse is measurably long, which is the window
	// this is about.
	var seed strings.Builder
	for i := 0; i < 4000; i++ {
		seed.WriteString("a line of terminal output that has to be parsed\r\n")
	}

	parsed := make(chan struct{})
	go func() {
		defer close(parsed)
		terminal.state.replaceReplica([]byte(seed.String()), &Size{Cols: 80, Rows: 24})
	}()
	// Land the resize inside the parse.
	time.Sleep(5 * time.Millisecond)
	terminal.SetSize(200, 50)
	<-parsed

	if cols, rows := terminal.TerminalSize(); cols != 200 || rows != 50 {
		t.Fatalf("emulator is %dx%d after a resize during a resync, want 200x50",
			cols, rows)
	}
}

// A snapshot may carry queries — a cursor-position report, device
// attributes — and the emulator answers them on an unbuffered pipe. With
// nothing draining that pipe, the write replaying the snapshot blocks
// forever and takes the terminal's whole pump with it.
func TestSeedFullOfQueriesDoesNotWedge(t *testing.T) {
	var seed strings.Builder
	for i := 0; i < 64; i++ {
		seed.WriteString("\x1b[6n")
	}
	seeded := make(chan struct{})
	go func() {
		defer close(seeded)
		transport := newFakeTransport(seed.String())
		terminal := New(transport, NewGate(), 40, 6)
		terminal.Close()
	}()
	select {
	case <-seeded:
	case <-time.After(5 * time.Second):
		t.Fatal("a seed full of queries wedged the terminal")
	}
}

func TestResyncAfterCloseIsIgnored(t *testing.T) {
	// Sampled before the widget exists, so winding-down goroutines cannot
	// pad the baseline and hide a leak — which is exactly what sampling
	// it after Close did.
	before := goroutinesAtRest()

	transport := newFakeTransport("start")
	terminal := New(transport, NewGate(), 40, 6)
	terminal.SetVisible(true)
	terminal.Close()

	// Driven directly rather than through the pump: a message already in
	// flight when the widget closes is a race the test would have to win,
	// and what needs proving is the guard, not the timing.
	terminal.state.replaceReplica([]byte("after close"), nil)

	if view := terminal.View(); strings.Contains(view, "after close") {
		t.Fatalf("a closed terminal took a resync:\n%s", view)
	}
	if after := goroutinesAtRest(); after > before {
		t.Fatalf("a resync after close left %d goroutines behind (%d before, %d after)",
			after-before, before, after)
	}
}

// goroutinesAtRest waits for the count to stop falling, so a measurement
// is not taken while something is still winding down.
func goroutinesAtRest() int {
	previous := runtime.NumGoroutine()
	settled := 0
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); {
		time.Sleep(20 * time.Millisecond)
		current := runtime.NumGoroutine()
		if current == previous {
			// Equal, not merely non-decreasing: a count still climbing
			// would otherwise read as settled and inflate the baseline,
			// hiding the very leak this is measuring.
			if settled++; settled >= 3 {
				return current
			}
		} else {
			settled = 0
		}
		previous = current
	}
	return runtime.NumGoroutine()
}
