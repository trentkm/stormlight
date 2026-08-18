// Package picker runs the interactive directory chooser.
//
// It runs on the machine whose directories are being chosen, which is not
// always the machine with the dashboard on it. That is the whole reason
// this is a package rather than a few lines in the UI: Yazi answers
// through files it writes beside itself, and those files are only
// readable, and only meaningful, where it ran.
package picker

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Choose runs the chooser over the caller's own terminal and reports the
// directory the user settled on. An empty answer means they quit without
// choosing, which is not an error.
func Choose(binary, start string) (string, error) {
	if strings.TrimSpace(binary) == "" {
		resolved, err := exec.LookPath("yazi")
		if err != nil {
			return "", fmt.Errorf("yazi is not installed or not on PATH")
		}
		binary = resolved
	}
	if !isDirectory(start) {
		working, err := os.Getwd()
		if err != nil {
			return "", err
		}
		start = working
	}

	choiceHandoff, err := createHandoff("choice")
	if err != nil {
		return "", err
	}
	defer os.Remove(choiceHandoff)
	cwdHandoff, err := createHandoff("cwd")
	if err != nil {
		return "", err
	}
	defer os.Remove(cwdHandoff)

	command := exec.Command(binary,
		"--chooser-file", choiceHandoff,
		"--cwd-file", cwdHandoff,
		start,
	)
	command.Dir = start
	// The caller is a terminal — a windrunner session's PTY — and Yazi is
	// meant to own it.
	command.Stdin, command.Stdout, command.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := command.Run(); err != nil {
		return "", fmt.Errorf("run Yazi: %w", err)
	}

	choice, err := os.ReadFile(choiceHandoff)
	if err != nil {
		return "", fmt.Errorf("read Yazi choice: %w", err)
	}
	cwd, err := os.ReadFile(cwdHandoff)
	if err != nil {
		return "", fmt.Errorf("read Yazi directory: %w", err)
	}
	return resolveDirectory(choice, cwd)
}

// resolveDirectory reads the two answers Yazi leaves: the file or
// directory the user chose, and the directory it was sitting in. A chosen
// file means the directory holding it — nobody picks a file when they
// were asked for a workspace.
func resolveDirectory(choice, cwd []byte) (string, error) {
	selected := firstPath(choice)
	if selected == "" {
		selected = firstPath(cwd)
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

func firstPath(data []byte) string {
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSuffix(line, "\r")
		if line != "" {
			return line
		}
	}
	return ""
}

// createHandoff makes a file only this user can read. Yazi writes the
// answer into it, and until it does the file is empty rather than absent,
// so a chooser that never ran is told apart from one that chose nothing.
func createHandoff(kind string) (string, error) {
	file, err := os.CreateTemp("", "stormlight-yazi-"+kind+"-*")
	if err != nil {
		return "", fmt.Errorf("create Yazi handoff file: %w", err)
	}
	path := file.Name()
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		_ = os.Remove(path)
		return "", fmt.Errorf("set Yazi handoff permissions: %w", err)
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(path)
		return "", fmt.Errorf("close Yazi handoff file: %w", err)
	}
	return path, nil
}

func isDirectory(path string) bool {
	if strings.TrimSpace(path) == "" {
		return false
	}
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}
