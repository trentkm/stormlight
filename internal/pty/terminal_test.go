package pty

import (
	"bytes"
	"context"
	"errors"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

type fakeTransport struct {
	seed     []byte
	seedSize *Size
	output   chan Message
	writes   [][]byte
	mu       sync.Mutex
	resizes  []Size
	refuse   bool
}

func newFakeTransport(seed string) *fakeTransport {
	return &fakeTransport{seed: []byte(seed), output: make(chan Message)}
}

func (t *fakeTransport) Seed() Message {
	return Message{Resync: t.seed, Resize: t.seedSize}
}
func (t *fakeTransport) Output() <-chan Message  { return t.output }
func (t *fakeTransport) Write(data []byte) error { t.writes = append(t.writes, data); return nil }
func (t *fakeTransport) Resize(_ context.Context, cols, rows int) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.refuse {
		return errors.New("daemon said no")
	}
	t.resizes = append(t.resizes, Size{Cols: cols, Rows: rows})
	return nil
}

func (t *fakeTransport) resized() []Size {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]Size(nil), t.resizes...)
}
func (t *fakeTransport) Close() { close(t.output) }

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

func TestTerminalClampsScrollMarginsToItsGrid(t *testing.T) {
	// A TUI may repaint once at its old width after SIGWINCH. Real terminals
	// constrain a DECSLRM right edge to the grid; x/vt must do the same or a
	// following reverse-index scroll indexes past every row.
	transport := newFakeTransport("\x1b[?69h\x1b[1;31s\x1b[H\x1bMX")
	terminal := New(transport, NewGate(), 30, 4)
	defer terminal.Close()

	if cols, rows := terminal.TerminalSize(); cols != 30 || rows != 4 {
		t.Fatalf("terminal size = %dx%d, want 30x4", cols, rows)
	}
	if lines := terminal.Text(); len(lines) == 0 || !strings.HasPrefix(lines[0], "X") {
		t.Fatalf("reverse index after clamped margin rendered %q", lines)
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

	// With a size, the way every real one arrives — the daemon sends
	// nothing else to a viewer in debt, so state and size travel
	// together. A consumer that checks the size first swallows the state
	// and never notices.
	transport.output <- Message{
		Resync: []byte("only what is true now"),
		Resize: &Size{Cols: 40, Rows: 6},
	}
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

// The seed names the size its bytes were wrapped for, and the replica has
// to be that size or it renders a snapshot mangled before it arrived.
// This is the case the size exists for: a viewer that asserted no
// geometry of its own, because its pane had not been laid out and the
// terminal is shared with a dashboard that had.
func TestTheSeedsSizeIsAdopted(t *testing.T) {
	transport := newFakeTransport("wrapped for a wide terminal")
	transport.seedSize = &Size{Cols: 132, Rows: 43}

	terminal := New(transport, NewGate(), 80, 24)
	defer terminal.Close()

	if cols, rows := terminal.TerminalSize(); cols != 132 || rows != 43 {
		t.Fatalf("emulator is %dx%d, want the seed's 132x43", cols, rows)
	}
	// The box belongs to the pane, not to the seed: View clips or pads
	// the difference.
	if cols, rows := terminal.Size(); cols != 80 || rows != 24 {
		t.Fatalf("box became %dx%d; the pane owns the box", cols, rows)
	}
}

// A transport that cannot say — one with no daemon behind it to ask —
// must leave the caller's size alone rather than have one invented.
func TestASeedWithNoSizeKeepsTheCallersOwn(t *testing.T) {
	transport := newFakeTransport("no size to report")

	terminal := New(transport, NewGate(), 100, 30)
	defer terminal.Close()

	if cols, rows := terminal.TerminalSize(); cols != 100 || rows != 30 {
		t.Fatalf("emulator is %dx%d, want the caller's 100x30", cols, rows)
	}
}

// A size no terminal can be is refused, not corrected. Clamping a zero
// into a legal 2x2 is how a pane collapses to a ribbon: the rest of this
// codebase refuses such sizes rather than rounding them up, and the seed
// is the last place a bad one can enter.
func TestADegenerateSeedSizeIsIgnored(t *testing.T) {
	transport := newFakeTransport("state from somewhere confused")
	transport.seedSize = &Size{Cols: 0, Rows: 0}

	terminal := New(transport, NewGate(), 120, 40)
	defer terminal.Close()

	if cols, rows := terminal.TerminalSize(); cols != 120 || rows != 40 {
		t.Fatalf("a 0x0 seed size produced a %dx%d replica", cols, rows)
	}
}

// Settle moves the box now and the terminal later: the view fits the
// pane at once, clipped the way it is for another viewer's size, and the
// emulator and daemon follow only once the box has held still (#243).
func TestSettleMovesTheBoxNowAndTheTerminalOnceItHoldsStill(t *testing.T) {
	transport := newFakeTransport(strings.Repeat("line\r\n", 30) + "$ ")
	terminal := New(transport, NewGate(), 80, 40)
	defer terminal.Close()
	const settle = 15 * time.Millisecond

	terminal.Settle(60, 20, settle)
	terminal.Settle(50, 12, settle)
	if cols, rows := terminal.Size(); cols != 50 || rows != 12 {
		t.Fatalf("box = %dx%d, want the live 50x12", cols, rows)
	}
	if cols, rows := terminal.TerminalSize(); cols != 80 || rows != 40 {
		t.Fatalf("terminal = %dx%d mid-settle, want the old 80x40", cols, rows)
	}
	if !terminal.Settling() {
		t.Fatal("mid-settle the widget does not say so")
	}
	view := strings.Split(ansi.Strip(terminal.View()), "\n")
	if len(view) != 12 {
		t.Fatalf("mid-settle view is %d rows, want the box's 12", len(view))
	}
	// The screen's bottom is what shows, so the prompt stays in view
	// and the cursor sits inside the box, as when another viewer shrinks
	// the terminal.
	if !strings.Contains(strings.Join(view, "\n"), "$") {
		t.Fatalf("mid-settle view lost the prompt at the screen's bottom:\n%s", strings.Join(view, "\n"))
	}
	if _, y, visible := terminal.Cursor(); !visible || y >= 12 {
		t.Fatalf("cursor visible=%v at row %d, want visible inside the 12-row box", visible, y)
	}
	if got := transport.resized(); len(got) != 0 {
		t.Fatalf("the daemon heard %v mid-settle, want nothing", got)
	}

	deadline := time.Now().Add(time.Second)
	for len(transport.resized()) == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	got := transport.resized()
	if len(got) != 1 || got[0] != (Size{Cols: 50, Rows: 12}) {
		t.Fatalf("daemon heard %v, want exactly one 50x12", got)
	}
	if cols, rows := terminal.TerminalSize(); cols != 50 || rows != 12 {
		t.Fatalf("terminal = %dx%d after settling, want 50x12", cols, rows)
	}
	if terminal.Settling() {
		t.Fatal("settled, the widget still says settling")
	}
}

// A deliberate size decided while a settle is pending is the newer
// decision: the settle's timer finds itself superseded and says nothing.
func TestSetSizeSupersedesAPendingSettle(t *testing.T) {
	transport := newFakeTransport("$ ")
	terminal := New(transport, NewGate(), 80, 40)
	defer terminal.Close()
	const settle = 15 * time.Millisecond

	terminal.Settle(60, 20, settle)
	_, assert := terminal.SetSize(100, 50)
	assert()
	if terminal.Settling() {
		t.Fatal("a SetSize left the widget settling")
	}
	time.Sleep(3 * settle)
	got := transport.resized()
	if len(got) != 1 || got[0] != (Size{Cols: 100, Rows: 50}) {
		t.Fatalf("daemon heard %v, want only the deliberate 100x50", got)
	}
}

// A Settle for the box the widget already has is not a gesture. With the
// assertion landed there is nothing to do and nothing to report; with it
// never landed it is retried at once, without a wait and without the
// widget calling itself settling — a pane that is not moving must not
// blink its size on every refresh.
func TestSettleForTheSameBoxIsARetryNotAGesture(t *testing.T) {
	transport := newFakeTransport("$ ")
	terminal := New(transport, NewGate(), 80, 40)
	defer terminal.Close()

	terminal.Settle(80, 40, 15*time.Millisecond)
	if terminal.Settling() {
		t.Fatal("an unchanged, landed box reports settling")
	}
	time.Sleep(45 * time.Millisecond)
	if got := transport.resized(); len(got) != 0 {
		t.Fatalf("an unchanged, landed box was asserted: %v", got)
	}

	// Now the assertion has not landed: the attach's size is what the
	// widget believes, and a later deliberate size was refused.
	transport.refuse = true
	_, assert := terminal.SetSize(100, 50)
	assert()
	transport.refuse = false
	terminal.Settle(100, 50, 15*time.Millisecond)
	if terminal.Settling() {
		t.Fatal("a retry reports settling")
	}
	// The refusal recorded nothing; the retry is the one size heard.
	deadline := time.Now().Add(time.Second)
	for len(transport.resized()) < 1 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	got := transport.resized()
	if len(got) != 1 || got[0] != (Size{Cols: 100, Rows: 50}) {
		t.Fatalf("daemon heard %v, want the refused 100x50 retried once", got)
	}
}

// A gesture that ends where it began owes the daemon nothing.
func TestAGestureThatEndsWhereItBeganAssertsNothing(t *testing.T) {
	transport := newFakeTransport("$ ")
	terminal := New(transport, NewGate(), 80, 40)
	defer terminal.Close()
	const settle = 15 * time.Millisecond

	terminal.Settle(100, 50, settle)
	terminal.Settle(80, 40, settle)
	time.Sleep(4 * settle)
	if got := transport.resized(); len(got) != 0 {
		t.Fatalf("daemon heard %v for a gesture that went nowhere", got)
	}
	if terminal.Settling() {
		t.Fatal("still settling after the wait")
	}
}

// fence proves the pump has finished the chunk before it: the output
// channel is unbuffered, so a send returns once the previous one was
// taken and processed.
func fence(transport *fakeTransport) {
	transport.output <- Message{Bytes: nil}
	transport.output <- Message{Bytes: nil}
}

func plain(terminal Model) string {
	return ansi.Strip(terminal.View())
}

// The blink a resize used to show, replayed from a captured Codex
// session (#245): the daemon's clear-and-repaint, then Codex's clear
// outside any synchronized update, then its redraw inside one. The clear
// is never painted; the redraw is painted once it is whole.
func TestAResizeNeverPaintsTheClearBeforeTheRedraw(t *testing.T) {
	transport := newFakeTransport("hello")
	terminal := New(transport, NewGate(), 80, 24)
	defer terminal.Close()
	if !strings.Contains(plain(terminal), "hello") {
		t.Fatal("seed not painted")
	}

	_, assert := terminal.SetSize(60, 20)
	assert()

	// The daemon answers with a clear and a repaint in one message.
	transport.output <- Message{Bytes: []byte("\x1b[H\x1b[2JREPAINT")}
	fence(transport)
	if view := plain(terminal); !strings.Contains(view, "REPAINT") {
		t.Fatalf("the daemon's repaint was not painted:\n%s", view)
	}

	// Codex clears outside any synchronized update.
	transport.output <- Message{Bytes: []byte("\x1b[r\x1b[0m\x1b[H\x1b[2J\x1b[3J\x1b[H")}
	fence(transport)
	if view := plain(terminal); !strings.Contains(view, "REPAINT") {
		t.Fatalf("the clear was painted as a blank frame:\n%s", view)
	}
	if _, _, visible := terminal.Cursor(); visible {
		t.Fatal("a cursor was shown on a held frame")
	}

	// Then redraws inside one, across several messages.
	transport.output <- Message{Bytes: []byte("\x1b[?2026h\x1b[HRE")}
	transport.output <- Message{Bytes: []byte("DRAW")}
	fence(transport)
	if view := plain(terminal); strings.Contains(view, "DRAW") || !strings.Contains(view, "REPAINT") {
		t.Fatalf("a half-drawn screen was painted:\n%s", view)
	}
	transport.output <- Message{Bytes: []byte("\x1b[?2026l")}
	fence(transport)
	if view := plain(terminal); !strings.Contains(view, "REDRAW") || strings.Contains(view, "REPAINT") {
		t.Fatalf("the finished redraw was not painted:\n%s", view)
	}
	if _, _, visible := terminal.Cursor(); !visible {
		t.Fatal("the cursor stayed hidden on a painted screen")
	}
}

// Synchronized updates are honored whether or not a resize is involved:
// the screen inside one is painted when it closes, not as it arrives.
func TestASynchronizedUpdateIsPaintedWholeOrNotAtAll(t *testing.T) {
	transport := newFakeTransport("hi")
	terminal := New(transport, NewGate(), 80, 24)
	defer terminal.Close()
	plain(terminal)

	transport.output <- Message{Bytes: []byte("\x1b[?2026h\x1b[2J\x1b[HPART")}
	fence(transport)
	if view := plain(terminal); !strings.Contains(view, "hi") {
		t.Fatalf("an open synchronized update was painted:\n%s", view)
	}
	transport.output <- Message{Bytes: []byte("IAL\x1b[?2026l")}
	fence(transport)
	if view := plain(terminal); !strings.Contains(view, "PARTIAL") {
		t.Fatalf("the closed update was not painted:\n%s", view)
	}
}

// A program that opens a synchronized update and never closes it cannot
// freeze the pane: the watchdog paints what is there.
func TestAStalledSynchronizedUpdateIsReleased(t *testing.T) {
	previous := syncWatchdog
	syncWatchdog = 20 * time.Millisecond
	t.Cleanup(func() { syncWatchdog = previous })

	transport := newFakeTransport("hi")
	terminal := New(transport, NewGate(), 80, 24)
	defer terminal.Close()
	plain(terminal)

	transport.output <- Message{Bytes: []byte("\x1b[?2026h\x1b[2J\x1b[HSTUCK")}
	fence(transport)
	deadline := time.Now().Add(time.Second)
	for !strings.Contains(plain(terminal), "STUCK") && time.Now().Before(deadline) {
		time.Sleep(2 * time.Millisecond)
	}
	if view := plain(terminal); !strings.Contains(view, "STUCK") {
		t.Fatalf("the stalled update was never released:\n%s", view)
	}
}

// Outside the window after a resize, a blank screen is what the program
// meant, and it is painted.
func TestABlankScreenIsPaintedOnceTheResizeWindowPasses(t *testing.T) {
	previous := resizeHold
	resizeHold = 150 * time.Millisecond
	t.Cleanup(func() { resizeHold = previous })

	// The seed sits on the last row: a held frame is fitted to the box
	// the way the screen is, bottom first, and the pane is shrinking.
	transport := newFakeTransport(strings.Repeat("\r\n", 23) + "hi")
	terminal := New(transport, NewGate(), 80, 24)
	defer terminal.Close()
	plain(terminal)

	_, assert := terminal.SetSize(60, 20)
	assert()
	transport.output <- Message{Bytes: []byte("\x1b[H\x1b[2J")}
	fence(transport)
	if view := plain(terminal); !strings.Contains(view, "hi") {
		t.Fatalf("a clear inside the window was painted:\n%s", view)
	}
	deadline := time.Now().Add(time.Second)
	for strings.Contains(plain(terminal), "hi") && time.Now().Before(deadline) {
		time.Sleep(2 * time.Millisecond)
	}
	if view := plain(terminal); strings.Contains(view, "hi") {
		t.Fatalf("the window never closed; the stale frame is still up:\n%s", view)
	}
}
