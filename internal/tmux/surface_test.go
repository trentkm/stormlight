package tmux

import (
	"slices"
	"testing"

	"github.com/trentkm/stormlight/internal/surface"
)

func TestSurfacePresentsCommandsInTmuxPopup(t *testing.T) {
	current := newSurfaceWithVersion("/opt/homebrew/bin/tmux", "tmux 3.7b\n")
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
			Title:       " Stormlight · Choose directory ",
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
		"-T", " Stormlight · Choose directory ",
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
	presentation, err := newSurfaceWithVersion("tmux", "tmux 3.7").
		Present(surface.Request{
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

func TestSurfaceRefusesPopupsOnCrashProneTmux(t *testing.T) {
	current := newSurfaceWithVersion("tmux", "tmux 3.6a\n")
	if current.Capabilities().Popups {
		t.Fatal("tmux 3.6a must not advertise popups (tmux/tmux#4942)")
	}

	presentation, err := current.Present(surface.Request{
		Command: surface.Command{Path: "yazi", Dir: "/workspace"},
		Popup:   &surface.Popup{Width: "78%"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if presentation.Mode != surface.PresentationSuspend {
		t.Fatalf("mode = %d, want suspend fallback", presentation.Mode)
	}
	if len(presentation.Command.Args) != 1 ||
		presentation.Command.Args[0] != "yazi" {
		t.Fatalf("args = %#v, want direct yazi", presentation.Command.Args)
	}
}

func TestPopupsSupported(t *testing.T) {
	cases := []struct {
		version string
		want    bool
	}{
		{"tmux 3.7\n", true},
		{"tmux 3.7a\n", true},
		{"tmux 3.7b\n", true},
		{"tmux 3.8\n", true},
		{"tmux 3.10\n", true},
		{"tmux 4.0\n", true},
		{"tmux next-3.8\n", true},
		// Bare #{version} format, as reported by the popup-hosting server.
		{"3.7b\n", true},
		{"3.8\n", true},
		{"next-3.8\n", true},
		{"3.6a\n", false},
		{"3.4\n", false},
		{"tmux 3.6\n", false},
		{"tmux 3.6a\n", false},
		{"tmux 3.5a\n", false},
		{"tmux 2.9\n", false},
		{"tmux master\n", false},
		{"", false},
		{"garbage", false},
	}
	for _, c := range cases {
		if got := popupsSupported(c.version); got != c.want {
			t.Errorf("popupsSupported(%q) = %v, want %v", c.version, got, c.want)
		}
	}
}
