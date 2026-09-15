package zed

import (
	"context"
	"fmt"
	"net/url"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

var (
	operatingSystem = runtime.GOOS
	findExecutable  = exec.LookPath
	runCommand      = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		return exec.CommandContext(ctx, name, args...).CombinedOutput()
	}
	pause = time.Sleep
)

const openProjectDiffScript = `
tell application "Zed" to activate
delay 0.2
tell application "System Events"
  tell process "Zed"
    click menu item "Command Palette..." of menu 1 of menu bar item "Go" of menu bar 1
    delay 0.2
    keystroke "a" using {command down}
    keystroke "git: diff"
    delay 0.2
    key code 36
  end tell
end tell
`

// OpenDiff focuses each requested repository in the local Zed application and
// opens its native project diff. Remote paths use Zed's ssh:// URL, so the
// source stays on the agent's machine.
func OpenDiff(ctx context.Context, host string, paths []string) error {
	if operatingSystem != "darwin" {
		return fmt.Errorf("opening Zed from Stormlight currently requires macOS")
	}
	if len(paths) == 0 {
		return fmt.Errorf("the Zed diff request names no repository")
	}
	zedPath, err := findExecutable("zed")
	if err != nil {
		return fmt.Errorf("find the Zed CLI: %w", err)
	}
	osascriptPath, err := findExecutable("osascript")
	if err != nil {
		return fmt.Errorf("find AppleScript: %w", err)
	}

	for _, path := range paths {
		target, err := targetURI(host, path)
		if err != nil {
			return err
		}
		if output, runErr := runCommand(ctx, zedPath, target); runErr != nil {
			return commandError("open "+target+" in Zed", runErr, output)
		}
		// The CLI hands the request to the running app and exits. Give Zed a
		// beat to focus the file before asking which repository is active.
		pause(600 * time.Millisecond)
		if output, runErr := runCommand(
			ctx,
			osascriptPath,
			"-e",
			openProjectDiffScript,
		); runErr != nil {
			return commandError("open Zed project diff", runErr, output)
		}
		pause(250 * time.Millisecond)
	}
	return nil
}

func targetURI(host, path string) (string, error) {
	path = filepath.Clean(strings.TrimSpace(path))
	if !filepath.IsAbs(path) {
		return "", fmt.Errorf("Zed diff path %q is not absolute", path)
	}
	if strings.TrimSpace(host) == "" {
		return path, nil
	}
	return (&url.URL{
		Scheme: "ssh",
		Host:   strings.TrimSpace(host),
		Path:   filepath.ToSlash(path),
	}).String(), nil
}

func commandError(action string, err error, output []byte) error {
	message := strings.TrimSpace(string(output))
	if message == "" {
		return fmt.Errorf("%s: %w", action, err)
	}
	return fmt.Errorf("%s: %w: %s", action, err, message)
}
