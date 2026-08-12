package ptyview

// The tmux tap as an oathgate transport: pipe-pane into a FIFO for the
// stream, a raw capture for the seed. Everything reconstruction-shaped
// lives here; the widget on top neither knows nor cares.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/sys/unix"

	"github.com/trentkm/stormlight/internal/diagnostic"
)

// scrollbackLines bounds seed depth and emulator history. The pipe only
// carries bytes from the moment it opened; tmux held the rest.
const scrollbackLines = 2000

// tapTransport implements oathgate.Transport over a tmux pane.
type tapTransport struct {
	agentID  string
	backend  Backend
	fifoPath string
	fifo     *os.File
	seed     []byte

	output chan []byte

	readerDone chan struct{}
	closing    atomic.Bool
	closeOnce  sync.Once
}

// openTap builds the transport: window sized (best effort), seed captured
// strictly BEFORE the pipe opens — the gap's bytes must be lost, never
// doubled, or TUIs that repaint with relative cursor moves drift by a row
// forever — then the FIFO stream begins.
func openTap(ctx context.Context, backend Backend, agentID, stateDir string, width, height int) (*tapTransport, error) {
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
	// O_RDWR so the open never blocks waiting for tmux's writer, and so
	// the transport's own writer end keeps reads from returning EOF when
	// the pipe's `cat` briefly isn't connected.
	fifo, err := os.OpenFile(fifoPath, os.O_RDWR, 0)
	if err != nil {
		os.Remove(fifoPath)
		return nil, fmt.Errorf("open fifo: %w", err)
	}
	t := &tapTransport{
		agentID:    agentID,
		backend:    backend,
		fifoPath:   fifoPath,
		fifo:       fifo,
		output:     make(chan []byte, 64),
		readerDone: make(chan struct{}),
	}
	// Size before piping, so the SIGWINCH repaint arrives at the size the
	// widget expects. Best effort: an attached client owns its sizes, and
	// the pane's real dimensions are adopted by the Manager either way.
	if err := backend.ResizeAgentWindow(ctx, agentID, width, height); err != nil {
		t.abort()
		return nil, err
	}
	screen, err := backend.CaptureRaw(ctx, agentID, scrollbackLines)
	if err != nil {
		t.abort()
		return nil, err
	}
	view, err := backend.PaneView(ctx, agentID)
	if err != nil {
		t.abort()
		return nil, err
	}
	// capture-pane emits bare newlines; a terminal needs the carriage
	// returns it would have seen, and the cursor parked where the pane
	// says it is.
	seeded := strings.ReplaceAll(strings.TrimSuffix(screen, "\n"), "\n", "\r\n")
	t.seed = []byte(seeded + fmt.Sprintf("\x1b[%d;%dH", view.CursorY+1, view.CursorX+1))
	if err := backend.PipePaneOpen(ctx, agentID, fifoPath); err != nil {
		t.abort()
		return nil, err
	}
	go t.readLoop()
	return t, nil
}

func (t *tapTransport) Seed() []byte          { return t.seed }
func (t *tapTransport) Output() <-chan []byte { return t.output }

// Write delivers input through the runtime — the FIFO carries output
// only; tmux is the one who can type into the pane.
func (t *tapTransport) Write(p []byte) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return t.backend.SendRaw(ctx, t.agentID, p)
}

func (t *tapTransport) Resize(ctx context.Context, cols, rows int) error {
	return t.backend.ResizeAgentWindow(ctx, t.agentID, cols, rows)
}

func (t *tapTransport) readLoop() {
	defer close(t.readerDone)
	defer close(t.output)
	buf := make([]byte, 32*1024)
	for {
		n, err := t.fifo.Read(buf)
		if t.closing.Load() {
			// A FIFO is not pollable on macOS, so a blocked Read cannot
			// be interrupted by Close; the wake-up byte Close writes lands
			// here instead.
			return
		}
		if n > 0 {
			chunk := make([]byte, n)
			copy(chunk, buf[:n])
			t.output <- chunk
		}
		if err != nil {
			return
		}
	}
}

func (t *tapTransport) Close() {
	t.closeOnce.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if err := t.backend.PipePaneClose(ctx, t.agentID); err != nil {
			diagnostic.Logger().Warn("close pane pipe",
				"agent_id", t.agentID, "error", err)
		}
		// The wake-up byte travels the transport's own O_RDWR handle, so
		// the blocked Read always has something to return with.
		t.closing.Store(true)
		t.fifo.Write([]byte{0})
		<-t.readerDone
		t.fifo.Close()
		os.Remove(t.fifoPath)
	})
}

// abort is the pre-reader teardown for openTap's early failures.
func (t *tapTransport) abort() {
	t.fifo.Close()
	os.Remove(t.fifoPath)
}
