// Package ptyview makes the Spanreed a terminal multiplexer: every agent
// keeps a live VT emulator fed by a pipe-pane byte stream for its whole
// life (Manager), seeded from a deep capture of the pane so history
// survives dashboard restarts. tmux persists the panes themselves; this
// package persists the dashboard's view of them.
package ptyview

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/charmbracelet/x/vt"
	"golang.org/x/sys/unix"

	"github.com/trentkm/stormlight/internal/diagnostic"
	"github.com/trentkm/stormlight/internal/session"
)

// Backend is the slice of the app service a session needs. Narrow on
// purpose: the package can be exercised with a fake in tests.
type Backend interface {
	PipePaneOpen(ctx context.Context, id, fifoPath string) error
	PipePaneClose(ctx context.Context, id string) error
	CaptureRaw(ctx context.Context, id string, history int) (string, error)
	PaneView(ctx context.Context, id string) (session.PaneView, error)
	ResizeAgentWindow(ctx context.Context, id string, width, height int) error
}

// framePeriod caps how often the reader wakes the UI: a stream writing
// byte-by-byte must not schedule a render per byte.
const framePeriod = 33 * time.Millisecond

// scrollbackLines bounds the emulator's main-screen history. The pipe only
// carries bytes from the moment it opened, so this is a ceiling, not a
// promise of depth.
const scrollbackLines = 2000

// Session is one agent's live terminal. The emulator is guarded by mu
// rather than by vt.SafeEmulator, because scrollback reads bypass
// SafeEmulator's lock and the reader goroutine writes concurrently with the
// UI's Frame calls.
type Session struct {
	AgentID string

	backend  Backend
	fifoPath string
	fifo     *os.File

	mu     sync.Mutex
	emu    *vt.Emulator
	scroll int // lines above the live tail the viewer has scrolled
	width  int
	height int

	frames     chan struct{}
	lastNotify time.Time
	pending    *time.Timer

	readerStarted bool
	readerDone    chan struct{}
	closing       atomic.Bool
	closeOnce     sync.Once
}

// Frame is what the UI draws: the styled grid, where the cursor is, and the
// state that decides how scrolling behaves.
type Frame struct {
	Grid      string
	CursorX   int
	CursorY   int
	AltScreen bool
	// Scrolled reports how many lines above the live tail the view sits;
	// zero means following output.
	Scrolled int
}

// Start brings up the live view for one agent: window sized 1:1, pipe
// opened, emulator seeded from the visible screen, reader running. The
// caller owns the returned session and must Close it.
func Start(ctx context.Context, backend Backend, agentID, stateDir string, width, height int) (*Session, error) {
	if width < 2 || height < 2 {
		return nil, fmt.Errorf("pty view: %dx%d is no place to put a terminal", width, height)
	}
	fifoDir := filepath.Join(stateDir, "pty")
	if err := os.MkdirAll(fifoDir, 0o700); err != nil {
		return nil, fmt.Errorf("create pty fifo dir: %w", err)
	}
	fifoPath := filepath.Join(fifoDir, agentID+".fifo")
	// A stale FIFO from a crashed run is indistinguishable from ours once
	// opened; recreate rather than trust it.
	if err := os.Remove(fifoPath); err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("remove stale fifo: %w", err)
	}
	if err := unix.Mkfifo(fifoPath, 0o600); err != nil {
		return nil, fmt.Errorf("create fifo: %w", err)
	}
	// O_RDWR so the open never blocks waiting for tmux's writer, and so the
	// session's own writer end keeps reads from returning EOF whenever the
	// pipe's `cat` briefly isn't connected. Close() closes the file, which
	// is what unblocks the reader goroutine.
	fifo, err := os.OpenFile(fifoPath, os.O_RDWR, 0)
	if err != nil {
		os.Remove(fifoPath)
		return nil, fmt.Errorf("open fifo: %w", err)
	}

	s := &Session{
		AgentID:    agentID,
		backend:    backend,
		fifoPath:   fifoPath,
		fifo:       fifo,
		width:      width,
		height:     height,
		frames:     make(chan struct{}, 1),
		readerDone: make(chan struct{}),
	}
	// Size before piping: the SIGWINCH repaint the resize provokes then
	// arrives through the pipe at the size the emulator expects.
	if err := backend.ResizeAgentWindow(ctx, agentID, width, height); err != nil {
		s.abort()
		return nil, err
	}
	if err := backend.PipePaneOpen(ctx, agentID, fifoPath); err != nil {
		s.abort()
		return nil, err
	}

	emu := vt.NewEmulator(width, height)
	emu.Scrollback().SetMaxLines(scrollbackLines)
	s.emu = emu
	// The emulator answers terminal queries (DSR, in-band resize reports)
	// by writing into an internal io.Pipe, and an io.Pipe write BLOCKS
	// until read. Nothing here wants those answers — tmux already answered
	// the real program — but they must be drained, or the first query (or
	// the first Resize, which self-reports) wedges Write while it holds
	// mu and freezes every Frame call behind it. Found the hard way, via
	// SIGQUIT. Close releases this goroutine by closing the pipe.
	go func() {
		_, _ = io.Copy(io.Discard, emu)
	}()
	if err := s.seed(ctx); err != nil {
		// Bytes piped between open and seed replay after the seed; for a
		// full-screen program the repaint converges, and for a scroll-mode
		// one the overlap is at worst a duplicated tail line. Spike-grade.
		s.Close()
		return nil, err
	}
	s.readerStarted = true
	go s.readLoop()
	return s, nil
}

// seed replays the pane's screen — and as much scrollback as the emulator
// keeps — into the emulator, then parks the cursor where the pane says it
// is. The pipe is forward-only (verified empirically), so without this the
// view starts black; with history included, the emulator's scrollback
// survives dashboard restarts because tmux held the lines in between.
func (s *Session) seed(ctx context.Context) error {
	screen, err := s.backend.CaptureRaw(ctx, s.AgentID, scrollbackLines)
	if err != nil {
		return err
	}
	view, err := s.backend.PaneView(ctx, s.AgentID)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	// capture-pane emits bare newlines; the emulator needs the carriage
	// returns a real terminal would have seen.
	seeded := strings.ReplaceAll(strings.TrimSuffix(screen, "\n"), "\n", "\r\n")
	if _, err := s.emu.WriteString(seeded); err != nil {
		return err
	}
	_, err = fmt.Fprintf(s.emu, "\x1b[%d;%dH", view.CursorY+1, view.CursorX+1)
	diagnostic.Logger().Info("pty seed",
		"agent_id", s.AgentID,
		"seed_lines", strings.Count(seeded, "\r\n")+1,
		"scrollback", s.emu.ScrollbackLen(),
	)
	return err
}

func (s *Session) readLoop() {
	defer close(s.readerDone)
	buf := make([]byte, 32*1024)
	for {
		n, err := s.fifo.Read(buf)
		if s.closing.Load() {
			// A FIFO is not pollable on macOS, so a blocked Read cannot be
			// interrupted by Close; the wake-up byte Close writes lands
			// here instead, and whatever arrived with it is torn-down
			// state nobody will draw.
			return
		}
		if n > 0 {
			s.mu.Lock()
			s.emu.Write(buf[:n])
			if s.scroll == 0 {
				// Following the tail; scrolled-back readers keep their place.
			} else if s.emu.IsAltScreen() {
				s.scroll = 0
			}
			s.mu.Unlock()
			s.notify()
		}
		if err != nil {
			return
		}
	}
}

// notify wakes the UI at most once per framePeriod, with a trailing edge so
// the final burst of a quiet stream still lands.
func (s *Session) notify() {
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
	if s.pending == nil {
		wait := framePeriod - now.Sub(s.lastNotify)
		s.pending = time.AfterFunc(wait, func() {
			s.mu.Lock()
			s.pending = nil
			s.lastNotify = time.Now()
			s.mu.Unlock()
			select {
			case s.frames <- struct{}{}:
			default:
			}
		})
	}
}

// Frames signals that a new frame is ready to draw. The channel never
// closes; a stopped session simply goes quiet, and the UI drops its
// receiver when it drops the session.
func (s *Session) Frames() <-chan struct{} {
	return s.frames
}

// Frame renders the current view. Scrolled back on the main screen, the
// grid is stitched from scrollback history plus the live rows.
func (s *Session) Frame() Frame {
	s.mu.Lock()
	defer s.mu.Unlock()
	cursor := s.emu.CursorPosition()
	frame := Frame{
		CursorX:   cursor.X,
		CursorY:   cursor.Y,
		AltScreen: s.emu.IsAltScreen(),
		Scrolled:  s.scroll,
	}
	if s.scroll == 0 || frame.AltScreen {
		frame.Grid = s.emu.Render()
		return frame
	}
	live := strings.Split(s.emu.Render(), "\n")
	height := s.emu.Height()
	back := s.emu.Scrollback()
	rows := make([]string, 0, back.Len()+len(live))
	for index := 0; index < back.Len(); index++ {
		rows = append(rows, back.Line(index).Render())
	}
	rows = append(rows, live...)
	top := max(0, len(rows)-height-s.scroll)
	frame.Grid = strings.Join(rows[top:min(len(rows), top+height)], "\n")
	return frame
}

// ScrollBy moves the view within main-screen history; positive is up into
// the past. On the alternate screen there is no past to move through.
func (s *Session) ScrollBy(delta int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.emu.IsAltScreen() {
		return
	}
	s.scroll = clamp(s.scroll+delta, 0, s.emu.ScrollbackLen())
}

func (s *Session) ScrollToBottom() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.scroll = 0
}

func (s *Session) AltScreen() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.emu.IsAltScreen()
}

// Size reports the cell grid the session currently renders at.
func (s *Session) Size() (int, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.width, s.height
}

// Resize moves the tmux window and the emulator together; the two disagree
// only for the moment between the resize and the repaint it provokes.
func (s *Session) Resize(ctx context.Context, width, height int) error {
	if width < 2 || height < 2 {
		return nil
	}
	if err := s.backend.ResizeAgentWindow(ctx, s.AgentID, width, height); err != nil {
		return err
	}
	s.mu.Lock()
	s.emu.Resize(width, height)
	s.width, s.height = width, height
	s.mu.Unlock()
	return nil
}

// Close tears the whole transport down: the pane pipe, the FIFO, the file
// behind it, and the reader. Idempotent, and deliberately independent of
// the caller's context — teardown must run even when the reason is that
// context dying.
func (s *Session) Close() {
	s.closeOnce.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if err := s.backend.PipePaneClose(ctx, s.AgentID); err != nil {
			diagnostic.Logger().Warn("close pane pipe", "agent_id", s.AgentID, "error", err)
		}
		if s.readerStarted {
			// The wake-up byte travels the session's own O_RDWR handle, so
			// the blocked Read always has something to return with.
			s.closing.Store(true)
			s.fifo.Write([]byte{0})
			<-s.readerDone
		}
		s.fifo.Close()
		if s.emu != nil {
			// Close the emulator's response pipe so the drain goroutine
			// sees EOF and exits. Via the pipe writer, not Emulator.Close:
			// the pipe's own methods are synchronized, while Close flips an
			// unsynchronized flag the drain's concurrent Read also touches.
			if pw, ok := s.emu.InputPipe().(*io.PipeWriter); ok {
				pw.CloseWithError(io.EOF)
			}
		}
		s.mu.Lock()
		if s.pending != nil {
			s.pending.Stop()
			s.pending = nil
		}
		s.mu.Unlock()
		os.Remove(s.fifoPath)
	})
}

// abort is the pre-reader teardown for Start's early failures: no pipe, no
// reader, just the FIFO to undo.
func (s *Session) abort() {
	s.fifo.Close()
	os.Remove(s.fifoPath)
}

func clamp(v, low, high int) int {
	if v < low {
		return low
	}
	if v > high {
		return high
	}
	return v
}
