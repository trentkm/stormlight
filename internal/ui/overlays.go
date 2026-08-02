package ui

// External overlays: the Neovim task editor and Yazi picker.
// Split from model.go; see #34.

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/trentkm/stormlight/internal/surface"
)

func (m Model) openTaskEditor() (tea.Model, tea.Cmd) {
	if m.nvimPath == "" {
		m.err = fmt.Errorf("Neovim is not installed or not on PATH")
		m.status = "Action failed"
		return m, nil
	}
	cwd := strings.TrimSpace(m.cwdInput.Value())
	if !isDirectory(cwd) {
		cwd = m.initialCwd
	}
	command, err := taskEditorCmd(
		m.surface,
		m.nvimPath,
		cwd,
		m.taskInput.Value(),
	)
	if err != nil {
		m.err = err
		m.status = "Action failed"
		return m, nil
	}
	m.status = "Opening Neovim"
	return m, command
}

func taskEditorCmd(
	current surface.Surface,
	binary string,
	cwd string,
	task string,
) (tea.Cmd, error) {
	handoff, err := os.CreateTemp("", "stormlight-task-*.md")
	if err != nil {
		return nil, fmt.Errorf("create task editor file: %w", err)
	}
	handoffPath := handoff.Name()
	cleanup := func() {
		_ = os.Remove(handoffPath)
	}
	if _, err := handoff.WriteString(task); err != nil {
		_ = handoff.Close()
		cleanup()
		return nil, fmt.Errorf("write task editor file: %w", err)
	}
	if err := handoff.Close(); err != nil {
		cleanup()
		return nil, fmt.Errorf("close task editor file: %w", err)
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

	var popup *surface.Popup
	if current.Capabilities().Popups {
		popup = &surface.Popup{
			Width:       "82%",
			Height:      "76%",
			Title:       " Stormlight · Edit task ",
			BorderStyle: "fg=#e5c07b",
		}
	}
	presentation, err := current.Present(surface.Request{
		Command: surface.Command{
			Path: binary,
			Args: []string{handoffPath},
			Dir:  cwd,
		},
		Popup: popup,
	})
	if err != nil {
		cleanup()
		return nil, err
	}
	if presentation.Command == nil {
		cleanup()
		return nil, fmt.Errorf("surface returned an empty Neovim command")
	}
	switch presentation.Mode {
	case surface.PresentationOverlay:
		return func() tea.Msg {
			return result(presentation.Command.Run())
		}, nil
	case surface.PresentationSuspend:
		return tea.ExecProcess(presentation.Command, result), nil
	default:
		cleanup()
		return nil, fmt.Errorf(
			"surface returned unsupported presentation mode %d",
			presentation.Mode,
		)
	}
}

func (m Model) openYazi() (tea.Model, tea.Cmd) {
	if m.yaziPath == "" {
		m.err = fmt.Errorf("yazi is not installed or not on PATH")
		m.status = "Action failed"
		return m, nil
	}
	start := strings.TrimSpace(m.pickerStart)
	if m.mode != modeAddWorkspace {
		start = strings.TrimSpace(m.cwdInput.Value())
	}
	if !isDirectory(start) {
		start = m.initialCwd
	}
	command, err := yaziPickerCmd(m.surface, m.yaziPath, start)
	if err != nil {
		m.err = err
		m.status = "Action failed"
		return m, nil
	}
	m.status = "Opening Yazi"
	return m, command
}

func yaziPickerCmd(
	current surface.Surface,
	binary string,
	start string,
) (tea.Cmd, error) {
	choiceHandoff, err := createYaziHandoff("choice")
	if err != nil {
		return nil, err
	}
	cwdHandoff, err := createYaziHandoff("cwd")
	if err != nil {
		_ = os.Remove(choiceHandoff)
		return nil, err
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

	var popup *surface.Popup
	if current.Capabilities().Popups {
		popup = &surface.Popup{
			Width:       "78%",
			Height:      "76%",
			Title:       " Stormlight · Choose directory ",
			BorderStyle: "fg=#e5c07b",
		}
	}
	presentation, err := current.Present(surface.Request{
		Command: surface.Command{
			Path: binary,
			Args: pickerArgs,
			Dir:  start,
		},
		Popup: popup,
	})
	if err != nil {
		_ = os.Remove(choiceHandoff)
		_ = os.Remove(cwdHandoff)
		return nil, err
	}
	if presentation.Command == nil {
		_ = os.Remove(choiceHandoff)
		_ = os.Remove(cwdHandoff)
		return nil, fmt.Errorf("surface returned an empty Yazi command")
	}
	switch presentation.Mode {
	case surface.PresentationOverlay:
		return func() tea.Msg {
			return result(presentation.Command.Run())
		}, nil
	case surface.PresentationSuspend:
		return tea.ExecProcess(presentation.Command, result), nil
	default:
		_ = os.Remove(choiceHandoff)
		_ = os.Remove(cwdHandoff)
		return nil, fmt.Errorf(
			"surface returned unsupported presentation mode %d",
			presentation.Mode,
		)
	}
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
