package tmux

import (
	"fmt"
	"os/exec"
	"regexp"
	"strconv"

	"github.com/trentkm/stormlight/internal/surface"
)

type Surface struct {
	binary string
	popups bool
}

var _ surface.Surface = (*Surface)(nil)

// NewSurface gates popups on the version of the server that will host them:
// display-popup runs on the server behind $TMUX, which can be older than the
// binary in PATH (a distro tmux under an SSH session, or a server that
// predates a brew upgrade). Popups on a pre-3.7 server kill that server
// (tmux/tmux#4942), so asking the wrong version is not a cosmetic mistake.
func NewSurface(binary string) *Surface {
	if binary == "" {
		binary = "tmux"
	}
	version := ""
	if out, err := exec.Command(
		binary, "display-message", "-p", "#{version}",
	).Output(); err == nil {
		version = string(out)
	} else if out, err := exec.Command(binary, "-V").Output(); err == nil {
		version = string(out)
	}
	return newSurfaceWithVersion(binary, version)
}

func newSurfaceWithVersion(binary, version string) *Surface {
	return &Surface{
		binary: binary,
		popups: popupsSupported(version),
	}
}

// popupsSupported reports whether tmux popups are safe to use. Before tmux
// 3.7, a program that queries cursor state with DECRQM (as Yazi and Neovim
// do) makes the popup's option lookup hit "fatal: missing option
// cursor-style", killing the whole tmux server (tmux/tmux#4942).
func popupsSupported(version string) bool {
	matches := tmuxVersionPattern.FindStringSubmatch(version)
	if matches == nil {
		return false
	}
	major, err := strconv.Atoi(matches[1])
	if err != nil {
		return false
	}
	minor, err := strconv.Atoi(matches[2])
	if err != nil {
		return false
	}
	return major > 3 || (major == 3 && minor >= 7)
}

// The pattern accepts both `tmux -V` output ("tmux 3.7b") and the bare
// #{version} format ("3.7b", "next-3.8").
var tmuxVersionPattern = regexp.MustCompile(
	`^(?:tmux )?(?:next-)?(\d+)\.(\d+)`,
)

func (s *Surface) Capabilities() surface.Capabilities {
	return surface.Capabilities{
		Popups:       s.popups,
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
	if request.Popup == nil || !s.popups {
		direct := request
		direct.Popup = nil
		return surface.NewDirect().Present(direct)
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
	if popup.BorderLines != "" {
		args = append(args, "-b", popup.BorderLines)
	}
	args = append(args, request.Command.Path)
	args = append(args, request.Command.Args...)
	return surface.Presentation{
		Command: exec.Command(s.binary, args...),
		Mode:    surface.PresentationOverlay,
	}, nil
}
