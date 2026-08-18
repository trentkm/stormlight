// Package pty renders a live terminal stream inside Stormlight's dashboard.
package pty

import (
	"context"
	"io"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/vt"
)

const (
	framePeriod       = 33 * time.Millisecond
	scrollBurstPeriod = framePeriod
	DefaultScrollback = 2000
)

var lastID atomic.Int64

// FrameMsg asks the dashboard to redraw: some visible terminal received
// output since the last frame.
type FrameMsg struct{}
type scrollMsg struct{ ID int64 }

// ScrollOwner names the widget a deferred wheel tick belongs to, so the
// dashboard can route it to that terminal even after selection moved —
// an unrouted tick would latch scrollPending and wedge the wheel.
func ScrollOwner(msg tea.Msg) (int64, bool) {
	wake, ok := msg.(scrollMsg)
	return wake.ID, ok
}

// Gate coalesces every visible terminal's output into at most one redraw
// per frame period. One Gate serves a whole herd of terminals: however
// many are streaming, the event loop sees ~30 messages a second, and one
// render pass repaints everything that changed.
type Gate struct {
	mu         sync.Mutex
	frames     chan struct{}
	lastNotify time.Time
	pending    *time.Timer
}

func NewGate() *Gate {
	return &Gate{frames: make(chan struct{}, 1)}
}

// Notify requests a redraw, immediately if a frame period has passed and
// on a trailing timer otherwise, so bursts coalesce without the final
// chunk going unpainted.
func (g *Gate) Notify() {
	g.mu.Lock()
	defer g.mu.Unlock()
	now := time.Now()
	if now.Sub(g.lastNotify) >= framePeriod {
		g.lastNotify = now
		select {
		case g.frames <- struct{}{}:
		default:
		}
		return
	}
	if g.pending != nil {
		return
	}
	g.pending = time.AfterFunc(framePeriod-now.Sub(g.lastNotify), func() {
		g.mu.Lock()
		g.pending, g.lastNotify = nil, time.Now()
		g.mu.Unlock()
		select {
		case g.frames <- struct{}{}:
		default:
		}
	})
}

// Wait is the dashboard's single listener: one outstanding Wait per Gate,
// re-armed on every FrameMsg.
func (g *Gate) Wait() tea.Cmd {
	return func() tea.Msg { <-g.frames; return FrameMsg{} }
}

// Model is a terminal box. Copies share its emulator and transport.
type Model struct {
	id    int64
	state *state
}

type state struct {
	transport                      Transport
	gate                           *Gate
	mu                             sync.Mutex
	emu                            *vt.Emulator
	cols, rows, termCols, termRows int
	scroll, scrollDelta            int
	scrollPending, closed          bool
	// visible marks the terminal as on screen: only visible terminals
	// knock on the gate, so a busy agent nobody is looking at costs no
	// render passes.
	visible atomic.Bool
	// mouseReporting shadows the hosted program's mouse modes
	// (1000/1002/1003): while set, the program wants the mouse, and the
	// dashboard forwards wheel and clicks instead of interpreting them.
	mouseReporting atomic.Bool
	// view caches the serialized grid between changes, so a dashboard
	// render pass touching every visible terminal only walks the grids
	// that actually received bytes. viewDirty marks it stale; both are
	// guarded by mu.
	view      string
	viewDirty bool
}

// New replays the terminal's seed and starts consuming its output stream.
// Frames knock on the shared gate; visibility starts false and the owner
// flips it for whichever terminals are on screen.
func New(transport Transport, gate *Gate, cols, rows int) Model {
	cols, rows = max(2, cols), max(2, rows)
	s := &state{
		transport: transport, gate: gate,
		cols: cols, rows: rows, termCols: cols, termRows: rows,
		viewDirty: true,
	}
	s.replaceReplica(transport.Seed())
	model := Model{id: lastID.Add(1), state: s}
	go s.pump(model)
	return model
}

// replaceReplica seeds the emulator with exact state, discarding whatever
// it held. Used for the attach snapshot and for a resync, which are the
// same thing arriving at different moments — writing either into the
// existing emulator would replay a scrollback it already has.
func (s *state) replaceReplica(seed []byte) {
	// A windrunner snapshot has already passed through x/vt once. Normalize
	// its reversed OSC 8 fields before replaying it through this second x/vt.
	seed = []byte(repairVTHyperlinks(string(seed)))
	s.observeModes(seed)
	s.mu.Lock()
	s.emu = vt.NewEmulator(s.termCols, s.termRows)
	s.emu.Scrollback().SetMaxLines(DefaultScrollback)
	emu := s.emu
	s.scroll, s.scrollDelta = 0, 0
	s.emu.Write(seed)
	s.viewDirty = true
	s.mu.Unlock()
	// vt writes query responses to its input pipe. The real terminal owns
	// those responses, so drain this end to keep a query from blocking paint.
	go func() { _, _ = io.Copy(io.Discard, emu) }()
}

// setTerminalSize follows the hosted terminal when someone else moved it:
// the emulator adopts the terminal's true size while the box keeps the
// pane's, and View clips or pads the difference.
func (m Model) setTerminalSize(cols, rows int) {
	cols, rows = max(2, cols), max(2, rows)
	s := m.state
	s.mu.Lock()
	if cols != s.termCols || rows != s.termRows {
		s.emu.Resize(cols, rows)
		s.termCols, s.termRows = cols, rows
		s.viewDirty = true
	}
	s.mu.Unlock()
	if s.visible.Load() {
		s.gate.Notify()
	}
}

func (s *state) pump(model Model) {
	for message := range s.transport.Output() {
		// The size travels just ahead of the repaint that belongs to it,
		// so the replica adopts the terminal's true size and then paints
		// at it.
		if message.Resize != nil {
			model.setTerminalSize(message.Resize.Cols, message.Resize.Rows)
			continue
		}
		// State, not output: this viewer fell behind and the daemon sent
		// what the terminal looks like instead of what it missed.
		if message.Resync != nil {
			s.replaceReplica(message.Resync)
			if s.visible.Load() {
				s.gate.Notify()
			}
			continue
		}
		// Most output is raw PTY data and needs no change. Resize repaints
		// come from windrunner's x/vt and carry its reversed OSC 8 fields.
		chunk := []byte(repairVTHyperlinks(string(message.Bytes)))
		s.observeModes(chunk)
		s.mu.Lock()
		s.emu.Write(chunk)
		if s.emu.IsAltScreen() {
			s.scroll, s.scrollDelta = 0, 0
		}
		s.viewDirty = true
		s.mu.Unlock()
		if s.visible.Load() {
			s.gate.Notify()
		}
	}
	if s.visible.Load() {
		s.gate.Notify()
	}
}

// SetVisible marks the terminal on or off screen; only visible terminals
// request redraws. Flipping to visible needs no catch-up knock — the
// render pass that follows the flip paints the emulator's current state.
func (m Model) SetVisible(visible bool) { m.state.visible.Store(visible) }

// MouseReporting reports whether the hosted program asked for the mouse.
func (m Model) MouseReporting() bool { return m.state.mouseReporting.Load() }

// observeModes shadows the mouse-tracking modes from the byte stream. The
// stream arrives on whole-sequence boundaries (the daemon guarantees it),
// so a plain scan cannot tear a sequence.
func (s *state) observeModes(chunk []byte) {
	for index := 0; index+5 < len(chunk); index++ {
		if chunk[index] != 0x1b || chunk[index+1] != '[' || chunk[index+2] != '?' {
			continue
		}
		rest := chunk[index+3:]
		end := 0
		for end < len(rest) && (rest[end] >= '0' && rest[end] <= '9' || rest[end] == ';') {
			end++
		}
		if end == 0 || end >= len(rest) || (rest[end] != 'h' && rest[end] != 'l') {
			continue
		}
		set := rest[end] == 'h'
		for _, mode := range strings.Split(string(rest[:end]), ";") {
			switch mode {
			case "1000", "1002", "1003":
				s.mouseReporting.Store(set)
			}
		}
	}
}

// Text is the visible screen as plain lines, for selection copies.
func (m Model) Text() []string {
	lines := strings.Split(ansi.Strip(m.View()), "\n")
	for index, line := range lines {
		lines[index] = strings.TrimRight(line, " ")
	}
	return lines
}

// Write delivers bytes to the hosted terminal.
func (m Model) Write(data []byte) error { return m.state.transport.Write(data) }
func (m Model) ID() int64               { return m.id }

// SetSize is the deliberate resize: the dashboard's own window moved, a
// zoom toggled, an attach returned. It asserts the new size on both the
// replica and the hosted terminal — as opposed to setTerminalSize, which
// follows a move someone else made.
func (m Model) SetSize(cols, rows int) (Model, tea.Cmd) {
	cols, rows = max(2, cols), max(2, rows)
	s := m.state
	s.mu.Lock()
	if cols != s.cols || rows != s.rows {
		s.emu.Resize(cols, rows)
		s.cols, s.rows, s.termCols, s.termRows = cols, rows, cols, rows
		s.viewDirty = true
	}
	s.mu.Unlock()
	return m, func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = s.transport.Resize(ctx, cols, rows)
		return nil
	}
}

func (m Model) TerminalSize() (int, int) {
	s := m.state
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.termCols, s.termRows
}
func (m Model) Size() (int, int) {
	s := m.state
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cols, s.rows
}

// QueueScroll coalesces high-resolution wheel events into one redraw.
func (m Model) QueueScroll(delta int) tea.Cmd {
	s := m.state
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.emu.IsAltScreen() {
		return nil
	}
	s.scrollDelta += delta
	if s.scrollPending {
		return nil
	}
	s.scrollPending = true
	return tea.Tick(scrollBurstPeriod, func(time.Time) tea.Msg { return scrollMsg{ID: m.id} })
}

// FlushScroll applies the wheel movement accumulated by QueueScroll.
func (m Model) FlushScroll(msg tea.Msg) bool {
	wake, ok := msg.(scrollMsg)
	if !ok || wake.ID != m.id {
		return false
	}
	s := m.state
	s.mu.Lock()
	defer s.mu.Unlock()
	delta := s.scrollDelta
	s.scrollDelta, s.scrollPending = 0, false
	if !s.closed && !s.emu.IsAltScreen() {
		s.setScroll(clamp(s.scroll+delta, 0, s.emu.ScrollbackLen()))
	}
	return true
}

// Handle consumes deferred terminal messages. The dashboard owns frame
// routing because it only listens to the selected terminal; the terminal
// owns only its coalesced wheel wake-up.
func (m Model) Handle(msg tea.Msg) bool { return m.FlushScroll(msg) }

func (m Model) ScrollBy(delta int) {
	s := m.state
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.emu.IsAltScreen() {
		s.setScroll(clamp(s.scroll+delta, 0, s.emu.ScrollbackLen()))
	}
}
func (m Model) ScrollToBottom() {
	s := m.state
	s.mu.Lock()
	defer s.mu.Unlock()
	s.setScroll(0)
	s.scrollDelta = 0
}

// setScroll moves the scroll position and invalidates the cached view when
// it actually moved. Callers hold mu.
func (s *state) setScroll(scroll int) {
	if scroll != s.scroll {
		s.scroll = scroll
		s.viewDirty = true
	}
}
func (m Model) Scrolled() int {
	s := m.state
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.scroll
}

// View renders exactly the terminal box's dimensions. The serialization
// is cached between changes: an idle terminal answers with the previous
// string instead of walking its grid again.
func (m Model) View() string {
	s := m.state
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.viewDirty {
		return s.view
	}
	var lines []string
	if s.scroll == 0 || s.emu.IsAltScreen() {
		lines = strings.Split(repairVTHyperlinks(s.emu.Render()), "\n")
		if len(lines) > s.rows {
			bottom := len(lines)
			if cursor := s.emu.CursorPosition().Y; cursor < bottom-s.rows {
				bottom = max(cursor+1, s.rows)
			}
			lines = lines[bottom-s.rows : bottom]
		}
	} else {
		back := s.emu.Scrollback()
		top := max(0, back.Len()+s.termRows-s.rows-s.scroll)
		bottom := min(back.Len()+s.termRows, top+s.rows)
		for i := top; i < min(bottom, back.Len()); i++ {
			lines = append(lines, repairVTHyperlinks(back.Line(i).Render()))
		}
		if bottom > back.Len() {
			live := strings.Split(repairVTHyperlinks(s.emu.Render()), "\n")
			for i := max(0, top-back.Len()); i < min(len(live), bottom-back.Len()); i++ {
				lines = append(lines, live[i])
			}
		}
	}
	s.view = fit(lines, s.cols, s.rows)
	s.viewDirty = false
	return s.view
}

// x/vt stores OSC 8's params and URI in each other's fields
// (https://github.com/charmbracelet/x/pull/868), so its renderer emits
// OSC 8 ; URI ; params instead of OSC 8 ; params ; URI. Raw PTY output is
// already correct and starts with an empty params field; serialized snapshots
// and repaints have a URI in that field. Repair only the latter form at each
// emulator boundary until the upstream fix lands.
func repairVTHyperlinks(value string) string {
	const prefix = "\x1b]8;"
	if !strings.Contains(value, prefix) {
		return value
	}

	var repaired strings.Builder
	repaired.Grow(len(value))
	for {
		start := strings.Index(value, prefix)
		if start < 0 {
			repaired.WriteString(value)
			return repaired.String()
		}
		repaired.WriteString(value[:start])
		value = value[start:]

		end := strings.IndexByte(value, '\a')
		if end < 0 {
			repaired.WriteString(value)
			return repaired.String()
		}
		sequence := value[:end+1]
		uri, params, found := strings.Cut(value[len(prefix):end], ";")
		parsed, parseErr := url.Parse(uri)
		if !found || parseErr != nil || parsed.Scheme == "" {
			repaired.WriteString(sequence)
		} else {
			repaired.WriteString(prefix)
			repaired.WriteString(params)
			repaired.WriteByte(';')
			repaired.WriteString(uri)
			repaired.WriteByte('\a')
		}
		value = value[end+1:]
	}
}

// Cursor reports a visible cursor relative to this terminal's box.
func (m Model) Cursor() (int, int, bool) {
	s := m.state
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.scroll != 0 {
		return 0, 0, false
	}
	cursor := s.emu.CursorPosition()
	clipTop := 0
	if s.termRows > s.rows {
		bottom := s.termRows
		if cursor.Y < bottom-s.rows {
			bottom = max(cursor.Y+1, s.rows)
		}
		clipTop = bottom - s.rows
	}
	row := cursor.Y - clipTop
	if cursor.X < 0 || cursor.X >= s.cols || row < 0 || row >= s.rows {
		return 0, 0, false
	}
	return cursor.X, row, true
}

// Close stops the renderer and detaches from the terminal transport.
func (m Model) Close() {
	s := m.state
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed, s.scrollDelta = true, 0
	s.mu.Unlock()
	s.visible.Store(false)
	s.transport.Close()
	if pipe, ok := s.emu.InputPipe().(io.Closer); ok {
		_ = pipe.Close()
	}
}

func fit(lines []string, cols, rows int) string {
	if len(lines) > rows {
		lines = lines[len(lines)-rows:]
	}
	for i, line := range lines {
		if ansi.StringWidth(line) > cols {
			line = ansi.Truncate(line, cols, "")
		}
		lines[i] = line + ansi.ResetStyle
	}
	for len(lines) < rows {
		lines = append(lines, "")
	}
	return strings.Join(lines, "\n")
}
func clamp(value, low, high int) int {
	if value < low {
		return low
	}
	if value > high {
		return high
	}
	return value
}
