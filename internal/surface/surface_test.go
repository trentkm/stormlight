package surface

import (
	"slices"
	"testing"
)

func TestDirectSurfaceSuspendsForInteractiveCommands(t *testing.T) {
	current := NewDirect()
	if capabilities := current.Capabilities(); capabilities.Popups ||
		capabilities.ClientSwitch {
		t.Fatalf("direct capabilities = %#v", capabilities)
	}

	presentation, err := current.Present(Request{
		Command: Command{
			Path: "/usr/bin/tool",
			Args: []string{"--choose", "project"},
			Dir:  "/workspace",
		},
		Popup: &Popup{Width: "80%"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if presentation.Mode != PresentationSuspend {
		t.Fatalf("mode = %d, want suspend", presentation.Mode)
	}
	if presentation.Command.Path != "/usr/bin/tool" ||
		!slices.Equal(
			presentation.Command.Args,
			[]string{"/usr/bin/tool", "--choose", "project"},
		) ||
		presentation.Command.Dir != "/workspace" {
		t.Fatalf("command = %#v", presentation.Command)
	}
}

func TestDirectSurfaceRejectsMissingCommand(t *testing.T) {
	if _, err := NewDirect().Present(Request{}); err == nil {
		t.Fatal("missing command was accepted")
	}
}
