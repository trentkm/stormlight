package ui

// External overlays: the Neovim task editor and Yazi picker.
// Split from model.go; see #34.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/trentkm/stormlight/internal/app"
	"github.com/trentkm/stormlight/internal/pty"
)

// overlaySpec is one floating program the dashboard can host: what to run,
// how to read its answer back, and how to clean up if it never answers.
type overlaySpec struct {
	title string
	host  string
	path  string
	args  []string
	dir   string
	// result turns the program's exit into a message. answer is what it
	// left in its session — empty when it left none, which is what
	// quitting without choosing looks like.
	result  func(answer string, err error) tea.Msg
	cleanup func()
}

// overlayView is the running overlay: a windrunner session rendered
// through the same widget as the Spanreed, floated over the dashboard.
type overlayView struct {
	spec       overlaySpec
	session    app.Overlay
	widget     pty.Model
	generation int
}

type overlayOpenedMsg struct {
	generation int
	spec       overlaySpec
	session    app.Overlay
	err        error
}

type overlayExitedMsg struct {
	generation int
	code       int
}

// overlayDimensions sizes the popup frame: most of the body, in the
// proportions the tmux popups used, bounded so small terminals still get
// a frame.
func (m Model) overlayDimensions() (int, int) {
	width, height := m.bodyDimensions()
	return clamp(width*78/100, min(width, 24), width),
		clamp(height*82/100, min(height, 8), height)
}

// openOverlay starts the spec's program in its own runtime session, sized
// to the popup's inside. The opened message carries the session back; the
// generation ties every later message to this particular opening.
func (m *Model) openOverlay(spec overlaySpec) tea.Cmd {
	m.overlayGeneration++
	generation := m.overlayGeneration
	outerWidth, outerHeight := m.overlayDimensions()
	request := app.OverlayRequest{
		Host: spec.host,
		Path: spec.path,
		Args: spec.args,
		Dir:  spec.dir,
		Cols: max(2, outerWidth-2),
		Rows: max(2, outerHeight-2),
	}
	backend := m.backend
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		session, err := backend.StartOverlay(ctx, request)
		return overlayOpenedMsg{
			generation: generation,
			spec:       spec,
			session:    session,
			err:        err,
		}
	}
}

// handleOverlayOpened builds the widget over the session's terminal and
// starts the exit watch. A stale generation means the user already
// cancelled a slow open: the session is destroyed, not adopted.
func (m Model) handleOverlayOpened(msg overlayOpenedMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		msg.spec.cleanup()
		m.raise(msg.err)
		return m, nil
	}
	if msg.generation != m.overlayGeneration || m.overlay != nil {
		session := msg.session
		msg.spec.cleanup()
		return m, func() tea.Msg { session.Close(); return nil }
	}
	outerWidth, outerHeight := m.overlayDimensions()
	widget := pty.New(msg.session, m.ptyManager.Gate(),
		max(2, outerWidth-2), max(2, outerHeight-2))
	widget.SetVisible(true)
	m.overlay = &overlayView{
		spec:       msg.spec,
		session:    msg.session,
		widget:     widget,
		generation: msg.generation,
	}
	session := msg.session
	generation := msg.generation
	exitWatch := func() tea.Msg {
		return overlayExitedMsg{generation: generation, code: <-session.Exited()}
	}
	return m, tea.Batch(exitWatch, m.armPTYWait())
}

// handleOverlayExited closes the popup and reads the program's answer
// back. Cancel bumps the generation, so a late exit from a killed session
// falls through the guard.
func (m Model) handleOverlayExited(msg overlayExitedMsg) (tea.Model, tea.Cmd) {
	if m.overlay == nil || msg.generation != m.overlay.generation {
		return m, nil
	}
	view := m.overlay
	m.overlay = nil
	return m, func() tea.Msg {
		// Read the answer before closing: Close destroys the session, and
		// the session is where the answer is.
		answer, answerErr := view.session.Result(context.Background())
		view.widget.Close()
		// The program's own account of what went wrong outranks its exit
		// status, and it is written precisely when the status is not zero
		// — checking the status first threw away the only message worth
		// reading, leaving "exited with status 1" to explain a missing
		// yazi.
		if answerErr != nil {
			return view.spec.result("", answerErr)
		}
		if msg.code != 0 {
			return view.spec.result("", fmt.Errorf(
				"%s exited with status %d", strings.TrimSpace(view.spec.title), msg.code))
		}
		return view.spec.result(answer, nil)
	}
}

// updateOverlayKey forwards the keyboard to the floating program, byte for
// byte; ctrl+q is the one key that stays ours, and it cancels.
func (m Model) updateOverlayKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if msg.String() == "ctrl+q" {
		return m.cancelOverlay()
	}
	data := pty.KeyToBytes(msg)
	if len(data) == 0 {
		return m, nil
	}
	return m, writeTerminalCmd(m.overlay.widget, data)
}

// cancelOverlay tears the popup down without an answer: the session dies
// with it, and the handoff files are removed unread.
func (m Model) cancelOverlay() (tea.Model, tea.Cmd) {
	view := m.overlay
	m.overlay = nil
	m.overlayGeneration++
	return m, func() tea.Msg {
		view.widget.Close()
		view.spec.cleanup()
		return nil
	}
}

// renderOverlayPopup is the floating frame: the program's live terminal
// inside the modal border every other overlay wears.
func (m Model) renderOverlayPopup() string {
	outerWidth, outerHeight := m.overlayDimensions()
	return renderModal(m.overlay.widget.View(), outerWidth, outerHeight)
}

// overlayCursor places the floating program's cursor on the screen: the
// popup's centered origin, plus its border, plus the widget's own
// box-relative answer.
func (m Model) overlayCursor() *tea.Cursor {
	x, y, visible := m.overlay.widget.Cursor()
	if !visible {
		return nil
	}
	width, height := m.bodyDimensions()
	outerWidth, outerHeight := m.overlayDimensions()
	left := max(0, (width-outerWidth)/2)
	top := max(0, (height-outerHeight)/2)
	return tea.NewCursor(left+1+x, bodyTop+top+1+y)
}

func (m Model) openTaskEditor() (tea.Model, tea.Cmd) {
	if m.nvimPath == "" {
		m.raise(fmt.Errorf("Neovim is not installed or not on PATH"))
		return m, nil
	}
	cwd := strings.TrimSpace(m.cwdInput.Value())
	if !isDirectory(cwd) {
		cwd = m.initialCwd
	}
	spec, err := taskEditorSpec(m.nvimPath, cwd, m.taskInput.Value())
	if err != nil {
		m.raise(err)
		return m, nil
	}
	return m, m.openOverlay(spec)
}

// taskEditorSpec builds the editor overlay: the invocation, and the
// completion handler that reads the edited task back from the handoff
// file.
func taskEditorSpec(binary, cwd, task string) (overlaySpec, error) {
	handoff, err := os.CreateTemp("", "stormlight-task-*.md")
	if err != nil {
		return overlaySpec{}, fmt.Errorf("create task editor file: %w", err)
	}
	handoffPath := handoff.Name()
	cleanup := func() {
		_ = os.Remove(handoffPath)
	}
	if _, err := handoff.WriteString(task); err != nil {
		_ = handoff.Close()
		cleanup()
		return overlaySpec{}, fmt.Errorf("write task editor file: %w", err)
	}
	if err := handoff.Close(); err != nil {
		cleanup()
		return overlaySpec{}, fmt.Errorf("close task editor file: %w", err)
	}

	// The editor stays file-based: it is seeded with the task as it
	// stands, so it needs a file written before it opens, and it only
	// ever edits text this dashboard already holds.
	result := func(_ string, runErr error) tea.Msg {
		defer cleanup()
		if runErr != nil {
			return taskEditedMsg{err: fmt.Errorf("run Neovim: %w", runErr)}
		}
		content, err := os.ReadFile(handoffPath)
		if err != nil {
			return taskEditedMsg{err: fmt.Errorf(
				"read task editor file: %w",
				err,
			)}
		}
		return taskEditedMsg{
			task: strings.TrimSuffix(string(content), "\n"),
		}
	}

	return overlaySpec{
		title:   "Neovim",
		path:    binary,
		args:    []string{handoffPath},
		dir:     cwd,
		result:  result,
		cleanup: cleanup,
	}, nil
}

func (m Model) openYazi() (tea.Model, tea.Cmd) {
	// A missing Yazi here says nothing about another machine, which has
	// its own PATH and answers for itself.
	if m.yaziPath == "" && m.addWorkspaceHostName() == "" && m.dispatchHost == "" {
		m.raise(fmt.Errorf("yazi is not installed or not on PATH"))
		return m, nil
	}
	// The picker browses the machine the form is already aimed at.
	// Adding a workspace, that is the machine chosen in the Remote tab;
	// starting an agent, it is the machine the highlighted directory is
	// on. Browsing this one while the form means another is how you end
	// up hunting for a path that was never here.
	host := m.addWorkspaceHostName()
	start := strings.TrimSpace(m.pickerStart)
	if m.mode != modeAddWorkspace {
		host = m.dispatchHost
		start = strings.TrimSpace(m.cwdInput.Value())
	}
	if host != "" {
		// A path from this filesystem means nothing there, so the picker
		// starts wherever that machine's Stormlight lands.
		start = ""
	}
	// Only a directory on this machine can be checked, and only this
	// machine's has a sensible fallback. Over there, an empty start means
	// "wherever that Stormlight lands", which is the home directory of
	// the account it runs as.
	if host == "" && !isDirectory(start) {
		start = m.initialCwd
	}
	return m, m.openOverlay(yaziPickerSpec(host, start, m.yaziPath))
}

// openSetup runs the host preparation in a popup. It runs *here* rather
// than there — the whole point is a machine that may not have Stormlight
// yet — and it gets a real terminal so a package manager asking for a
// password has somewhere to ask.
func (m Model) openSetup(host string) (tea.Model, tea.Cmd) {
	if host == "" {
		return m, nil
	}
	return m, m.openOverlay(overlaySpec{
		title: "Set up " + host,
		// Empty host: this machine, which is the one holding the ssh
		// configuration and the binary to copy.
		host: "",
		path: "",
		args: []string{"remote", "setup", host, "--install", "--yazi", "--wait"},
		dir:  m.initialCwd,
		result: func(_ string, runErr error) tea.Msg {
			return machinePreparedMsg{host: host, err: runErr}
		},
		cleanup: func() {},
	})
}

// yaziPickerSpec builds the picker overlay.
//
// The program is Stormlight itself on the machine being browsed, because
// the answer has to be recorded there: Yazi writes its choice into files
// beside itself, and on another machine those are paths this process
// cannot read. `_pick` runs Yazi, reads them where they are, and leaves
// the answer in the session both ends already hold.
//
// The configured Yazi path is only passed for this machine. On another
// one it names a binary that may not be there, and that host's own PATH
// is the right place to look.
func yaziPickerSpec(host, start, localYazi string) overlaySpec {
	args := []string{"_pick"}
	if host == "" && strings.TrimSpace(localYazi) != "" {
		args = append(args, "--yazi", localYazi)
	}
	if strings.TrimSpace(start) != "" {
		args = append(args, start)
	}

	return overlaySpec{
		title: "Yazi",
		host:  host,
		// Empty path means this host's Stormlight, whichever machine
		// that turns out to be.
		path: "",
		args: args,
		dir:  start,
		result: func(answer string, runErr error) tea.Msg {
			if runErr != nil {
				return directoryPickedMsg{err: runErr}
			}
			// No answer is a picker someone quit, not a failure.
			return directoryPickedMsg{host: host, path: answer}
		},
		cleanup: func() {},
	}
}

// choiceKey identifies a directory choice. The host is part of it: the
// same path on two machines is two different places to start an agent,
// and folding them together would offer one row that runs on whichever
// happened to be listed first.
func choiceKey(host, path string) string {
	return host + "\x00" + directoryKey(path)
}

func directoryKey(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	absolute, err := filepath.Abs(path)
	if err == nil {
		path = absolute
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err == nil {
		path = resolved
	}
	return filepath.Clean(path)
}

func isDirectory(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}
