package tmux

import (
	"slices"
	"testing"

	"github.com/trentkm/runstead/internal/surface"
)

func TestSurfacePresentsCommandsInTmuxPopup(t *testing.T) {
	current := NewSurface("/opt/homebrew/bin/tmux")
	capabilities := current.Capabilities()
	if !capabilities.Popups || !capabilities.ClientSwitch {
		t.Fatalf("tmux capabilities = %#v", capabilities)
	}

	presentation, err := current.Present(surface.Request{
		Command: surface.Command{
			Path: "/opt/homebrew/bin/yazi",
			Args: []string{
				"--chooser-file", "/tmp/choice",
				"--cwd-file", "/tmp/cwd",
			},
			Dir: "/workspace/project",
		},
		Popup: &surface.Popup{
			Width:       "78%",
			Height:      "76%",
			Title:       " Runstead · Choose directory ",
			BorderStyle: "fg=#e5c07b",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if presentation.Mode != surface.PresentationOverlay {
		t.Fatalf("mode = %d, want overlay", presentation.Mode)
	}
	want := []string{
		"/opt/homebrew/bin/tmux",
		"display-popup",
		"-E",
		"-w", "78%",
		"-h", "76%",
		"-d", "/workspace/project",
		"-T", " Runstead · Choose directory ",
		"-S", "fg=#e5c07b",
		"/opt/homebrew/bin/yazi",
		"--chooser-file", "/tmp/choice",
		"--cwd-file", "/tmp/cwd",
	}
	if !slices.Equal(presentation.Command.Args, want) {
		t.Fatalf("args = %#v, want %#v", presentation.Command.Args, want)
	}
}

func TestSurfaceFallsBackToSuspensionWithoutPopupRequest(t *testing.T) {
	presentation, err := NewSurface("tmux").Present(surface.Request{
		Command: surface.Command{Path: "tool"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if presentation.Mode != surface.PresentationSuspend ||
		presentation.Command.Path != "tool" {
		t.Fatalf("presentation = %#v", presentation)
	}
}
