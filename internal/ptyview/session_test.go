package ptyview

import (
	"context"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/trentkm/stormlight/internal/session"
)

// fakeBackend records the tmux side of the transport so the FIFO, reader,
// and emulator can be exercised without a tmux server.
type fakeBackend struct {
	mu          sync.Mutex
	screen      string
	view        session.PaneView
	pipeOpened  string
	pipeClosed  int
	resizedW    int
	resizedH    int
	resizeCalls int
}

func (f *fakeBackend) PipePaneOpen(_ context.Context, _, fifoPath string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pipeOpened = fifoPath
	return nil
}

func (f *fakeBackend) PipePaneClose(context.Context, string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pipeClosed++
	return nil
}

func (f *fakeBackend) CaptureRaw(context.Context, string, int) (string, error) {
	return f.screen, nil
}

func (f *fakeBackend) PaneView(context.Context, string) (session.PaneView, error) {
	return f.view, nil
}

func (f *fakeBackend) ResizeAgentWindow(_ context.Context, _ string, w, h int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.resizedW, f.resizedH = w, h
	f.resizeCalls++
	return nil
}

func startTestSession(t *testing.T, backend *fakeBackend) *Session {
	t.Helper()
	s, err := Start(context.Background(), backend, "abc123", t.TempDir(), 20, 5)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(s.Close)
	return s
}

func TestStartSeedsScreenAndCursor(t *testing.T) {
	backend := &fakeBackend{
		screen: "\x1b[31mred\x1b[0m line\nsecond\n",
		view:   session.PaneView{CursorX: 3, CursorY: 1},
	}
	s := startTestSession(t, backend)

	frame := s.Frame()
	plain := ansi.Strip(frame.Grid)
	if !strings.Contains(plain, "red line") || !strings.Contains(plain, "second") {
		t.Fatalf("seed missing from grid:\n%s", plain)
	}
	if !strings.Contains(frame.Grid, "31m") {
		t.Fatalf("seed lost its styling:\n%q", frame.Grid)
	}
	if frame.CursorX != 3 || frame.CursorY != 1 {
		t.Fatalf("cursor at (%d,%d), want (3,1)", frame.CursorX, frame.CursorY)
	}
	if backend.resizedW != 20 || backend.resizedH != 5 {
		t.Fatalf("window sized %dx%d, want 20x5", backend.resizedW, backend.resizedH)
	}
	if backend.pipeOpened == "" {
		t.Fatal("pipe never opened")
	}
}

func TestStreamedBytesReachTheFrame(t *testing.T) {
	backend := &fakeBackend{}
	s := startTestSession(t, backend)

	// Write as tmux's `cat` would: a separate writer on the same FIFO.
	writer, err := os.OpenFile(backend.pipeOpened, os.O_WRONLY, 0)
	if err != nil {
		t.Fatalf("open fifo for writing: %v", err)
	}
	defer writer.Close()
	if _, err := writer.WriteString("\r\nstreamed!"); err != nil {
		t.Fatalf("write fifo: %v", err)
	}

	select {
	case <-s.Frames():
	case <-time.After(2 * time.Second):
		t.Fatal("no frame signal within 2s of stream write")
	}
	if plain := ansi.Strip(s.Frame().Grid); !strings.Contains(plain, "streamed!") {
		t.Fatalf("streamed bytes missing from grid:\n%s", plain)
	}
}

func TestScrollbackStitchesHistoryAboveLiveRows(t *testing.T) {
	backend := &fakeBackend{}
	s := startTestSession(t, backend)

	writer, err := os.OpenFile(backend.pipeOpened, os.O_WRONLY, 0)
	if err != nil {
		t.Fatalf("open fifo for writing: %v", err)
	}
	defer writer.Close()
	var lines []string
	for _, line := range []string{"one", "two", "three", "four", "five", "six", "seven", "eight"} {
		lines = append(lines, line+"\r\n")
	}
	if _, err := writer.WriteString(strings.Join(lines, "")); err != nil {
		t.Fatalf("write fifo: %v", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for !strings.Contains(ansi.Strip(s.Frame().Grid), "eight") {
		if time.Now().After(deadline) {
			t.Fatalf("stream never landed; grid:\n%s", ansi.Strip(s.Frame().Grid))
		}
		time.Sleep(10 * time.Millisecond)
	}

	// The 5-row screen has scrolled; "one" lives in scrollback now.
	s.ScrollBy(1 << 30)
	frame := s.Frame()
	if frame.Scrolled == 0 {
		t.Fatal("scrolling up did not move the view")
	}
	if plain := ansi.Strip(frame.Grid); !strings.Contains(plain, "one") {
		t.Fatalf("scrolled view missing history:\n%s", plain)
	}
	s.ScrollToBottom()
	if s.Frame().Scrolled != 0 {
		t.Fatal("ScrollToBottom did not return to the tail")
	}
}

func TestCloseTearsDownTransport(t *testing.T) {
	backend := &fakeBackend{}
	s := startTestSession(t, backend)
	fifoPath := backend.pipeOpened

	s.Close()
	s.Close() // idempotent

	backend.mu.Lock()
	closed := backend.pipeClosed
	backend.mu.Unlock()
	if closed != 1 {
		t.Fatalf("pipe closed %d times, want exactly 1", closed)
	}
	if _, err := os.Stat(fifoPath); !os.IsNotExist(err) {
		t.Fatalf("fifo still on disk after Close: %v", err)
	}
}

func TestManagerReconcilesHerdAgainstRoster(t *testing.T) {
	backend := &fakeBackend{}
	manager := NewManager(backend, t.TempDir())
	ctx := context.Background()

	manager.Ensure(ctx, []string{"aa11", "bb22"}, 20, 5)
	if manager.Session("aa11") == nil || manager.Session("bb22") == nil {
		t.Fatal("Ensure did not start sessions for the roster")
	}

	// Same roster, same size: nothing to do, sessions stay.
	first := manager.Session("aa11")
	manager.Ensure(ctx, []string{"aa11", "bb22"}, 20, 5)
	if manager.Session("aa11") != first {
		t.Fatal("an unchanged roster restarted a session")
	}

	// bb22 left the roster; its session closes, aa11 survives.
	manager.Ensure(ctx, []string{"aa11"}, 20, 5)
	if manager.Session("bb22") != nil {
		t.Fatal("departed agent kept its session")
	}
	if manager.Session("aa11") != first {
		t.Fatal("surviving agent lost its session")
	}

	manager.CloseAll()
	if manager.Session("aa11") != nil {
		t.Fatal("CloseAll left a session behind")
	}
}

func TestConcurrentEnsureStartsOneSessionPerAgent(t *testing.T) {
	backend := &fakeBackend{}
	manager := NewManager(backend, t.TempDir())
	ctx := context.Background()

	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			manager.Ensure(ctx, []string{"aa11"}, 20, 5)
		}()
	}
	wg.Wait()

	if manager.Session("aa11") == nil {
		t.Fatal("no session started")
	}
	// One pipe opened per pipe closed would mean a duplicate stole and
	// then severed the stream; a lone session never closes its pipe here.
	backend.mu.Lock()
	closed := backend.pipeClosed
	backend.mu.Unlock()
	if closed != 0 {
		t.Fatalf("a duplicate session closed the pane pipe %d time(s)", closed)
	}
}
