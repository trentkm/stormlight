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
	title   string
	path    string
	args    []string
	dir     string
	result  func(error) tea.Msg
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
		m.err = msg.err
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
		view.widget.Close()
		if msg.code != 0 {
			return view.spec.result(fmt.Errorf(
				"%s exited with status %d", strings.TrimSpace(view.spec.title), msg.code))
		}
		return view.spec.result(nil)
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
		m.err = fmt.Errorf("Neovim is not installed or not on PATH")
		return m, nil
	}
	cwd := strings.TrimSpace(m.cwdInput.Value())
	if !isDirectory(cwd) {
		cwd = m.initialCwd
	}
	spec, err := taskEditorSpec(m.nvimPath, cwd, m.taskInput.Value())
	if err != nil {
		m.err = err
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

	result := func(runErr error) tea.Msg {
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
	if m.yaziPath == "" {
		m.err = fmt.Errorf("yazi is not installed or not on PATH")
		return m, nil
	}
	start := strings.TrimSpace(m.pickerStart)
	if m.mode != modeAddWorkspace {
		start = strings.TrimSpace(m.cwdInput.Value())
	}
	if !isDirectory(start) {
		start = m.initialCwd
	}
	spec, err := yaziPickerSpec(m.yaziPath, start)
	if err != nil {
		m.err = err
		return m, nil
	}
	return m, m.openOverlay(spec)
}

// yaziPickerSpec builds the picker overlay: the invocation, and the
// completion handler that resolves the chosen directory from the handoff
// files.
func yaziPickerSpec(binary, start string) (overlaySpec, error) {
	choiceHandoff, err := createYaziHandoff("choice")
	if err != nil {
		return overlaySpec{}, err
	}
	cwdHandoff, err := createYaziHandoff("cwd")
	if err != nil {
		_ = os.Remove(choiceHandoff)
		return overlaySpec{}, err
	}

	pickerArgs := []string{
		"--chooser-file", choiceHandoff,
		"--cwd-file", cwdHandoff,
		start,
	}
	result := func(runErr error) tea.Msg {
		defer os.Remove(choiceHandoff)
		defer os.Remove(cwdHandoff)
		if runErr != nil {
			return directoryPickedMsg{err: fmt.Errorf("run Yazi: %w", runErr)}
		}
		choice, err := os.ReadFile(choiceHandoff)
		if err != nil {
			return directoryPickedMsg{err: fmt.Errorf("read Yazi choice: %w", err)}
		}
		cwd, err := os.ReadFile(cwdHandoff)
		if err != nil {
			return directoryPickedMsg{err: fmt.Errorf("read Yazi directory: %w", err)}
		}
		selected, err := resolveYaziDirectory(choice, cwd)
		if err != nil {
			return directoryPickedMsg{err: err}
		}
		if selected == "" {
			return directoryPickedMsg{}
		}
		return directoryPickedMsg{path: selected}
	}

	return overlaySpec{
		title:  "Yazi",
		path:   binary,
		args:   pickerArgs,
		dir:    start,
		result: result,
		cleanup: func() {
			_ = os.Remove(choiceHandoff)
			_ = os.Remove(cwdHandoff)
		},
	}, nil
}

func createYaziHandoff(kind string) (string, error) {
	file, err := os.CreateTemp("", "stormlight-yazi-"+kind+"-*")
	if err != nil {
		return "", fmt.Errorf("create Yazi handoff file: %w", err)
	}
	path := file.Name()
	if err := file.Close(); err != nil {
		_ = os.Remove(path)
		return "", fmt.Errorf("close Yazi handoff file: %w", err)
	}
	return path, nil
}

func resolveYaziDirectory(choice, cwd []byte) (string, error) {
	selected := firstYaziPath(choice)
	if selected == "" {
		selected = firstYaziPath(cwd)
	}
	if selected == "" {
		return "", nil
	}

	selected = filepath.Clean(selected)
	info, err := os.Stat(selected)
	if err != nil {
		return "", fmt.Errorf("Yazi selected path is unavailable: %s", selected)
	}
	if !info.IsDir() {
		selected = filepath.Dir(selected)
	}
	if !isDirectory(selected) {
		return "", fmt.Errorf("Yazi selected directory is unavailable: %s", selected)
	}
	return selected, nil
}

func firstYaziPath(data []byte) string {
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSuffix(line, "\r")
		if line != "" {
			return line
		}
	}
	return ""
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
