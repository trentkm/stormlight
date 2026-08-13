// Package pty renders a live terminal stream inside Stormlight's dashboard.
package pty

import (
	"context"
	"io"
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

// FrameMsg asks the dashboard to redraw a terminal with new output.
type FrameMsg struct{ ID int64 }
type scrollMsg struct{ ID int64 }

// Model is a terminal box. Copies share its emulator and transport.
type Model struct {
	id    int64
	state *state
}

type state struct {
	transport                      Transport
	mu                             sync.Mutex
	emu                            *vt.Emulator
	cols, rows, termCols, termRows int
	scroll, scrollDelta            int
	scrollPending, closed          bool
	frames                         chan struct{}
	lastNotify                     time.Time
	pending                        *time.Timer
	// view caches the serialized grid between changes, so a dashboard
	// render pass touching every visible terminal only walks the grids
	// that actually received bytes. viewDirty marks it stale; both are
	// guarded by mu.
	view      string
	viewDirty bool
}

// New replays the terminal's seed and starts consuming its output stream.
func New(transport Transport, cols, rows int) Model {
	cols, rows = max(2, cols), max(2, rows)
	s := &state{
		transport: transport, cols: cols, rows: rows, termCols: cols, termRows: rows,
		frames: make(chan struct{}, 1), viewDirty: true,
	}
	s.emu = vt.NewEmulator(cols, rows)
	s.emu.Scrollback().SetMaxLines(DefaultScrollback)
	// vt writes query responses to its input pipe. The real terminal owns
	// those responses, so drain this end to keep a query from blocking paint.
	go func() { _, _ = io.Copy(io.Discard, s.emu) }()
	s.emu.Write(transport.Seed())
	go s.pump()
	return Model{id: lastID.Add(1), state: s}
}

func (s *state) pump() {
	for chunk := range s.transport.Output() {
		s.mu.Lock()
		s.emu.Write(chunk)
		if s.emu.IsAltScreen() {
			s.scroll, s.scrollDelta = 0, 0
		}
		s.viewDirty = true
		s.mu.Unlock()
		s.notify()
	}
	s.notify()
}

func (s *state) notify() {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	if now.Sub(s.lastNotify) >= framePeriod {
		s.lastNotify = now
		select {
		case s.frames <- struct{}{}:
		default:
		}
		return
	}
	if s.pending != nil {
		return
	}
	s.pending = time.AfterFunc(framePeriod-now.Sub(s.lastNotify), func() {
		s.mu.Lock()
		s.pending, s.lastNotify = nil, time.Now()
		s.mu.Unlock()
		select {
		case s.frames <- struct{}{}:
		default:
		}
	})
}

// Init waits for a rendered frame.
func (m Model) Init() tea.Cmd { return m.wait() }
func (m Model) wait() tea.Cmd {
	return func() tea.Msg { <-m.state.frames; return FrameMsg{ID: m.id} }
}

// Write delivers bytes to the hosted terminal.
func (m Model) Write(data []byte) error { return m.state.transport.Write(data) }
func (m Model) ID() int64               { return m.id }

// SetSize resizes both the emulator and the hosted terminal.
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
		lines = strings.Split(s.emu.Render(), "\n")
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
			lines = append(lines, back.Line(i).Render())
		}
		if bottom > back.Len() {
			live := strings.Split(s.emu.Render(), "\n")
			for i := max(0, top-back.Len()); i < min(len(live), bottom-back.Len()); i++ {
				lines = append(lines, live[i])
			}
		}
	}
	s.view = fit(lines, s.cols, s.rows)
	s.viewDirty = false
	return s.view
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
	if s.pending != nil {
		s.pending.Stop()
		s.pending = nil
	}
	s.mu.Unlock()
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
