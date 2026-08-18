package ui

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/trentkm/stormlight/internal/pty"
)

// fakeOverlaySession scripts one overlay program: a seeded terminal, a
// recordable input side, and an exit the test fires by hand.
type fakeOverlaySession struct {
	seed   string
	output chan pty.Message
	exited chan int
	answer string

	mu     sync.Mutex
	writes []byte
	closed bool
}

// Result is what the program left in its session. Reading it after Close
// would be reading a session that no longer exists, so the test records
// whether that happened.
func (f *fakeOverlaySession) Result(context.Context) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return "", errClosedBeforeRead
	}
	return f.answer, nil
}

var errClosedBeforeRead = errors.New("session was closed before its answer was read")

func newFakeOverlaySession(seed string) *fakeOverlaySession {
	return &fakeOverlaySession{
		seed:   seed,
		output: make(chan pty.Message),
		exited: make(chan int, 1),
	}
}

func (f *fakeOverlaySession) Seed() []byte               { return []byte(f.seed) }
func (f *fakeOverlaySession) Output() <-chan pty.Message { return f.output }
func (f *fakeOverlaySession) Exited() <-chan int         { return f.exited }
func (f *fakeOverlaySession) Write(p []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.writes = append(f.writes, p...)
	return nil
}
func (f *fakeOverlaySession) Resize(context.Context, int, int) error { return nil }
func (f *fakeOverlaySession) Close() {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return
	}
	f.closed = true
	close(f.output)
}

func (f *fakeOverlaySession) recorded() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return string(f.writes)
}

func (f *fakeOverlaySession) isClosed() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.closed
}

// openFakeOverlay walks the model through handleOverlayOpened as if
// openOverlay's start command had just answered.
func openFakeOverlay(
	t *testing.T,
	model Model,
	session *fakeOverlaySession,
	spec overlaySpec,
) Model {
	t.Helper()
	model.overlayGeneration++
	updated, _ := model.handleOverlayOpened(overlayOpenedMsg{
		generation: model.overlayGeneration,
		spec:       spec,
		session:    session,
	})
	model = updated.(Model)
	if model.overlay == nil {
		t.Fatal("overlay did not open")
	}
	return model
}

func TestOverlayFloatsOverTheDashboardAndOwnsTheKeyboard(t *testing.T) {
	model := flowModelFixture(t, stubBackend{})
	session := newFakeOverlaySession("YAZI-CONTENT")
	model = openFakeOverlay(t, model, session, overlaySpec{
		title:   "Yazi",
		cleanup: func() {},
	})

	body := model.renderBody()
	if !strings.Contains(body, "YAZI-CONTENT") {
		t.Fatalf("popup does not show the program's terminal:\n%s", body)
	}
	if !strings.Contains(body, "╭") {
		t.Fatalf("popup is missing its frame:\n%s", body)
	}

	// Keys land in the hosted program, byte for byte.
	updated, cmd := model.updateOverlayKey(tea.KeyPressMsg{Text: "j", Code: 'j'})
	model = updated.(Model)
	if cmd == nil {
		t.Fatal("keypress produced no write")
	}
	cmd()
	if got := session.recorded(); got != "j" {
		t.Fatalf("forwarded bytes = %q, want %q", got, "j")
	}
}

func TestOverlayCancelDestroysTheSessionUnread(t *testing.T) {
	model := flowModelFixture(t, stubBackend{})
	session := newFakeOverlaySession("seed")
	cleaned := false
	resultRan := false
	model = openFakeOverlay(t, model, session, overlaySpec{
		title:   "Yazi",
		cleanup: func() { cleaned = true },
		result:  func(string, error) tea.Msg { resultRan = true; return nil },
	})
	generation := model.overlay.generation

	updated, cmd := model.updateOverlayKey(tea.KeyPressMsg{Code: 'q', Mod: tea.ModCtrl})
	model = updated.(Model)
	if model.overlay != nil {
		t.Fatal("cancel left the overlay up")
	}
	cmd()
	if !session.isClosed() || !cleaned || resultRan {
		t.Fatalf("cancel outcome: closed=%v cleaned=%v resultRan=%v",
			session.isClosed(), cleaned, resultRan)
	}

	// The killed session's late exit must fall through the guard.
	updated, _ = model.handleOverlayExited(overlayExitedMsg{
		generation: generation,
		code:       0,
	})
	if updated.(Model).overlay != nil {
		t.Fatal("stale exit resurrected the overlay")
	}
}

func TestOverlayExitReadsTheAnswerBack(t *testing.T) {
	model := flowModelFixture(t, stubBackend{})
	session := newFakeOverlaySession("seed")
	// What the program left behind, in the session it was running in.
	session.answer = "/picked"
	model = openFakeOverlay(t, model, session, overlaySpec{
		title:   "Yazi",
		cleanup: func() {},
		result: func(answer string, err error) tea.Msg {
			return directoryPickedMsg{path: answer, err: err}
		},
	})

	updated, cmd := model.handleOverlayExited(overlayExitedMsg{
		generation: model.overlay.generation,
		code:       0,
	})
	model = updated.(Model)
	if model.overlay != nil {
		t.Fatal("exit left the overlay up")
	}
	// The answer has to be read before Close, because Close destroys the
	// session holding it. The fake returns an error if it is asked after
	// closing, so the order is asserted rather than assumed.
	picked, ok := cmd().(directoryPickedMsg)
	if !ok || picked.err != nil || picked.path != "/picked" {
		t.Fatalf("exit result = %#v", picked)
	}
	if !session.isClosed() {
		t.Fatal("exit did not close the session")
	}
}
