package ui

// Column preferences: the < > pane adjustments, persisted in the same
// state directory as the workspace catalog so a chosen layout survives
// relaunches.

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// ColumnPrefs carries the user's pane-width adjustments, in columns,
// relative to the built-in split.
type ColumnPrefs struct {
	WorkspaceAdjust int `json:"workspace_adjust"`
	AgentAdjust     int `json:"agent_adjust"`
}

func columnPrefsPath() string {
	if stateHome := os.Getenv("XDG_STATE_HOME"); stateHome != "" {
		return filepath.Join(stateHome, "stormlight", "ui.json")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".local", "state", "stormlight", "ui.json")
}

// LoadColumnPrefs reads the saved adjustments; a missing or unreadable
// file is simply the default layout.
func LoadColumnPrefs() ColumnPrefs {
	path := columnPrefsPath()
	if path == "" {
		return ColumnPrefs{}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return ColumnPrefs{}
	}
	var prefs ColumnPrefs
	if err := json.Unmarshal(data, &prefs); err != nil {
		return ColumnPrefs{}
	}
	return prefs
}

// saveColumnPrefs is best-effort: a failed write only costs persistence.
func saveColumnPrefs(prefs ColumnPrefs) {
	path := columnPrefsPath()
	if path == "" {
		return
	}
	data, err := json.MarshalIndent(prefs, "", "  ")
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return
	}
	_ = os.WriteFile(path, append(data, '\n'), 0o644)
}
