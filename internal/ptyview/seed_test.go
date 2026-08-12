package ptyview

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/trentkm/stormlight/internal/session"
)

func TestDeepSeedPopulatesScrollback(t *testing.T) {
	var lines []string
	for i := 1; i <= 42; i++ {
		lines = append(lines, fmt.Sprintf("tick %04d", i))
	}
	backend := &fakeBackend{
		screen: strings.Join(lines, "\n") + "\n",
		view:   session.PaneView{CursorX: 0, CursorY: 29},
	}
	s, err := Start(context.Background(), backend, "seed42", t.TempDir(), 72, 30)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(s.Close)

	s.mu.Lock()
	backlog := s.emu.ScrollbackLen()
	s.mu.Unlock()
	if backlog == 0 {
		t.Fatal("deep seed left scrollback empty")
	}
	s.ScrollBy(1 << 30)
	frame := s.Frame()
	if frame.Scrolled == 0 {
		t.Fatal("scroll did not move")
	}
	if !strings.Contains(ansi.Strip(frame.Grid), "tick 0001") {
		t.Fatalf("history missing from scrolled view:\n%s", ansi.Strip(frame.Grid))
	}
}
