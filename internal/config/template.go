package config

import (
	"fmt"
	"os"
	"path/filepath"
)

const template = `# Stormlight configuration.
# Every key is optional; delete anything you don't want to override.
# Precedence: command-line flags > environment variables > this file > defaults.

[defaults]
# provider = "codex"            # codex | claude, or a custom provider
# mode     = "edits"            # ask | edits | auto

[keys]
# Seam chords: keys the dashboard keeps while an agent's terminal holds
# the keyboard. Bubble Tea names; several chords may name one action.
# agents_next     = ["ctrl+alt+j", "ctrl+alt+down"]
# agents_previous = ["ctrl+alt+k", "ctrl+alt+up"]
# queue_next      = ["ctrl+alt+n"]
# queue_previous  = ["ctrl+alt+p"]
# zoom            = ["alt+z", "ctrl+alt+z"]

[ui]
# rows = "compact"              # compact | expanded

[log]
# level = "info"                # debug | info | warn | error
# file  = ""                    # empty uses the default log location

[tools]
# yazi = "/opt/homebrew/bin/yazi"
# nvim = "/opt/homebrew/bin/nvim"

# Per-directory dispatch defaults, applied when a flag or form choice
# doesn't say otherwise. Keys may use ~ and match subdirectories.
# [workspaces."~/repos/trusted-project"]
# mode     = "auto"
# provider = "claude"

# Built-in provider tweaks. extra_args append after Stormlight's own flags.
# [providers.claude]
# binary     = "claude"
# extra_args = []

# Custom providers. args is an exec-style array; {task} is replaced with the
# task text (appended as the last argument when no placeholder is present).
# mode_args are prepended per permission mode — only the CLI's own flags
# enforce anything.
# [providers.aider]
# label  = "Aider"
# binary = "aider"
# args   = ["--message", "{task}"]
# [providers.aider.mode_args]
# auto = ["--yes-always"]
`

// WriteTemplate writes the commented starter config. It refuses to
// overwrite an existing file unless force is set.
func WriteTemplate(force bool) (string, error) {
	path := Path()
	if path == "" {
		return "", fmt.Errorf("cannot resolve the configuration directory")
	}
	if !force {
		if _, err := os.Stat(path); err == nil {
			return "", fmt.Errorf("%s already exists (use --force to overwrite)", path)
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", fmt.Errorf("create config directory: %w", err)
	}
	if err := os.WriteFile(path, []byte(template), 0o644); err != nil {
		return "", fmt.Errorf("write config template: %w", err)
	}
	return path, nil
}
