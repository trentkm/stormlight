package surface

import (
	"fmt"
	"os/exec"
	"slices"
)

type Capabilities struct {
	Popups       bool
	ClientSwitch bool
}

type PresentationMode int

const (
	PresentationSuspend PresentationMode = iota
	PresentationOverlay
)

type Command struct {
	Path string
	Args []string
	Dir  string
}

type Popup struct {
	Width  string
	Height string
	Title  string
	// BorderStyle colors the frame; BorderLines picks which glyphs draw it
	// (tmux's single, rounded, heavy, double, …). Both are passed through
	// verbatim — the surface decides how to render a popup, not what one
	// should look like.
	BorderStyle string
	BorderLines string
}

type Request struct {
	Command Command
	Popup   *Popup
}

type Presentation struct {
	Command *exec.Cmd
	Mode    PresentationMode
}

type Surface interface {
	Capabilities() Capabilities
	Present(Request) (Presentation, error)
}

type Direct struct{}

func NewDirect() *Direct {
	return &Direct{}
}

func (d *Direct) Capabilities() Capabilities {
	return Capabilities{}
}

func (d *Direct) Present(request Request) (Presentation, error) {
	if request.Command.Path == "" {
		return Presentation{}, fmt.Errorf("interactive command path is empty")
	}
	command := exec.Command(
		request.Command.Path,
		slices.Clone(request.Command.Args)...,
	)
	command.Dir = request.Command.Dir
	return Presentation{
		Command: command,
		Mode:    PresentationSuspend,
	}, nil
}
