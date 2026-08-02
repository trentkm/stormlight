package tmux

import (
	"fmt"
	"os"
	"path/filepath"
)

// DefaultSocket is the private tmux server Stormlight owns. Keeping the
// dashboard and agent sessions off the user's default server means user
// configuration, plugins, and session-restore tools never touch Stormlight,
// and Stormlight behaves identically on every machine.
const DefaultSocket = "stormlight"

// serverConfig is the entire tmux configuration for the Stormlight server.
// User dotfiles are intentionally never loaded; anything Stormlight needs
// from tmux must be declared here or set programmatically.
const serverConfig = `# Managed by Stormlight. Rewritten at startup; do not edit.
# Applies when the Stormlight tmux server starts, not to running servers.
set -s default-terminal "tmux-256color"
set -sa terminal-overrides ",*:RGB"
set -s escape-time 10
set -s focus-events on
set -s extended-keys on
set -as terminal-features 'xterm*:extkeys'
set -g history-limit 50000
set -g mouse on
# Yazi and other overlays probe the real terminal through tmux passthrough
# and time out ("Terminal response timeout") when the sequences are
# swallowed. Yazi does run "tmux set -p allow-passthrough on" itself, but
# that lands mid-probe on whichever pane the popup sits over; enabling it
# server-wide from boot removes the race and also lets image protocols
# through.
set -g allow-passthrough all
`

// EnsureServerConfig writes Stormlight's tmux configuration into the given
// directory and returns its path. The file is rewritten every time so
// upgrades take effect and manual edits cannot introduce per-machine drift.
func EnsureServerConfig(directory string) (string, error) {
	if directory == "" {
		return "", fmt.Errorf("resolve Stormlight config directory")
	}
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return "", fmt.Errorf("create Stormlight config directory: %w", err)
	}
	path := filepath.Join(directory, "tmux.conf")
	current, err := os.ReadFile(path)
	if err == nil && string(current) == serverConfig {
		return path, nil
	}
	if err := os.WriteFile(path, []byte(serverConfig), 0o644); err != nil {
		return "", fmt.Errorf("write Stormlight tmux config: %w", err)
	}
	return path, nil
}
