package tmux

import (
	"fmt"
	"os/exec"

	"github.com/trentkm/stormlight/internal/surface"
)

type Surface struct {
	binary string
}

var _ surface.Surface = (*Surface)(nil)

func NewSurface(binary string) *Surface {
	if binary == "" {
		binary = "tmux"
	}
	return &Surface{binary: binary}
}

func (s *Surface) Capabilities() surface.Capabilities {
	return surface.Capabilities{
		Popups:       true,
		ClientSwitch: true,
	}
}

func (s *Surface) Present(
	request surface.Request,
) (surface.Presentation, error) {
	if request.Command.Path == "" {
		return surface.Presentation{}, fmt.Errorf(
			"interactive command path is empty",
		)
	}
	if request.Popup == nil {
		return surface.NewDirect().Present(request)
	}

	popup := request.Popup
	args := []string{"display-popup", "-E"}
	if popup.Width != "" {
		args = append(args, "-w", popup.Width)
	}
	if popup.Height != "" {
		args = append(args, "-h", popup.Height)
	}
	if request.Command.Dir != "" {
		args = append(args, "-d", request.Command.Dir)
	}
	if popup.Title != "" {
		args = append(args, "-T", popup.Title)
	}
	if popup.BorderStyle != "" {
		args = append(args, "-S", popup.BorderStyle)
	}
	args = append(args, request.Command.Path)
	args = append(args, request.Command.Args...)
	return surface.Presentation{
		Command: exec.Command(s.binary, args...),
		Mode:    surface.PresentationOverlay,
	}, nil
}
