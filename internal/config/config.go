// Package config loads Stormlight's optional user configuration from
// ~/.config/stormlight/config.toml. Everything has a built-in default; a
// missing file is not an error. Precedence is always
// flags > environment > config file > defaults — this package only supplies
// the third layer.
package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	toml "github.com/pelletier/go-toml/v2"
	"github.com/trentkm/stormlight/internal/agent"
)

type Config struct {
	Defaults   Defaults                     `toml:"defaults"`
	Keys       Keys                         `toml:"keys"`
	UI         UI                           `toml:"ui"`
	Log        Log                          `toml:"log"`
	Tools      Tools                        `toml:"tools"`
	Workspaces map[string]WorkspaceOverride `toml:"workspaces"`
	Providers  map[string]Provider          `toml:"providers"`
}

type Defaults struct {
	Provider string `toml:"provider"`
	Mode     string `toml:"mode"`
}

// Keys rebinds the seam chords — the keys that reach the dashboard while
// an agent's terminal holds the keyboard. Each action takes a list of
// chords in Bubble Tea's names ("alt+j", "ctrl+alt+down"); an empty list
// keeps the default. Defaults are alt chords — what hosted TUIs leave
// alone; a desk whose window manager owns alt rebinds here.
type Keys struct {
	AgentsNext     []string `toml:"agents_next"`
	AgentsPrevious []string `toml:"agents_previous"`
	QueueNext      []string `toml:"queue_next"`
	QueuePrevious  []string `toml:"queue_previous"`
	Zoom           []string `toml:"zoom"`
}

type UI struct {
	Rows string `toml:"rows"`
}

type Log struct {
	Level string `toml:"level"`
	File  string `toml:"file"`
}

type Tools struct {
	Yazi string `toml:"yazi"`
	Nvim string `toml:"nvim"`
}

type WorkspaceOverride struct {
	Mode     string `toml:"mode"`
	Provider string `toml:"provider"`
}

type Provider struct {
	Label     string              `toml:"label"`
	Binary    string              `toml:"binary"`
	Args      []string            `toml:"args"`
	ExtraArgs []string            `toml:"extra_args"`
	ModeArgs  map[string][]string `toml:"mode_args"`
}

// Dir returns Stormlight's configuration directory
// ($XDG_CONFIG_HOME or ~/.config, plus /stormlight).
func Dir() string {
	configHome := os.Getenv("XDG_CONFIG_HOME")
	if configHome == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		configHome = filepath.Join(home, ".config")
	}
	return filepath.Join(configHome, "stormlight")
}

// Path returns the config file location, whether or not it exists.
func Path() string {
	directory := Dir()
	if directory == "" {
		return ""
	}
	return filepath.Join(directory, "config.toml")
}

// Load reads and validates the config file. A missing file yields a zero
// Config with no error. Invalid values are cleared and reported as warnings
// so a typo cannot keep the dashboard from starting; only an unreadable or
// unparsable file is a hard error.
func Load() (Config, []string, error) {
	return loadFrom(Path())
}

func loadFrom(path string) (Config, []string, error) {
	var cfg Config
	if path == "" {
		return cfg, nil, nil
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return cfg, nil, nil
	}
	if err != nil {
		return Config{}, nil, fmt.Errorf("read %s: %w", path, err)
	}
	if err := toml.Unmarshal(data, &cfg); err != nil {
		return Config{}, nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return cfg.normalize(path)
}

func (c Config) normalize(path string) (Config, []string, error) {
	var warnings []string
	warn := func(format string, args ...any) {
		warnings = append(warnings,
			filepath.Base(path)+": "+fmt.Sprintf(format, args...))
	}

	if _, err := agent.ParseMode(c.Defaults.Mode); err != nil {
		warn("defaults.mode: %v", err)
		c.Defaults.Mode = ""
	}
	switch c.UI.Rows {
	case "", "compact", "expanded":
	default:
		warn("ui.rows: invalid value %q (compact or expanded)", c.UI.Rows)
		c.UI.Rows = ""
	}
	for _, tool := range []struct {
		name string
		path *string
	}{{"tools.yazi", &c.Tools.Yazi}, {"tools.nvim", &c.Tools.Nvim}} {
		if *tool.path == "" {
			continue
		}
		if info, err := os.Stat(*tool.path); err != nil || info.IsDir() {
			warn("%s: %q is not an executable file", tool.name, *tool.path)
			*tool.path = ""
		}
	}
	for root, override := range c.Workspaces {
		if _, err := agent.ParseMode(override.Mode); err != nil {
			warn("workspaces.%q.mode: %v", root, err)
			override.Mode = ""
			c.Workspaces[root] = override
		}
	}
	for name, provider := range c.Providers {
		for mode := range provider.ModeArgs {
			if _, err := agent.ParseMode(mode); err != nil {
				warn("providers.%s.mode_args: %v", name, err)
				delete(provider.ModeArgs, mode)
			}
		}
	}
	return c, warnings, nil
}

// ModeForDir returns the permission-mode override whose directory key
// contains dir, preferring the longest (most specific) key.
func (c Config) ModeForDir(dir string) (agent.PermissionMode, bool) {
	override, ok := c.overrideForDir(dir)
	if !ok || override.Mode == "" {
		return "", false
	}
	mode, err := agent.ParseMode(override.Mode)
	if err != nil {
		return "", false
	}
	return mode, true
}

// ProviderForDir returns the provider override for dir, if any.
func (c Config) ProviderForDir(dir string) (agent.Provider, bool) {
	override, ok := c.overrideForDir(dir)
	if !ok || override.Provider == "" {
		return "", false
	}
	return agent.Provider(override.Provider), true
}

func (c Config) overrideForDir(dir string) (WorkspaceOverride, bool) {
	dir = filepath.Clean(dir)
	keys := make([]string, 0, len(c.Workspaces))
	for key := range c.Workspaces {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool { return len(keys[i]) > len(keys[j]) })
	for _, key := range keys {
		root := filepath.Clean(expandHome(key))
		if dir == root || strings.HasPrefix(dir+string(filepath.Separator),
			root+string(filepath.Separator)) {
			return c.Workspaces[key], true
		}
	}
	return WorkspaceOverride{}, false
}

func expandHome(path string) string {
	if path == "~" || strings.HasPrefix(path, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, strings.TrimPrefix(path, "~"))
		}
	}
	return path
}

func valueOr(value, fallback string) string {
	if value != "" {
		return value
	}
	return fallback
}

// EffectiveTOML renders the merged configuration (file values over built-in
// defaults) for `stormlight config`.
func (c Config) EffectiveTOML() (string, error) {
	merged := c
	merged.Defaults.Provider = valueOr(merged.Defaults.Provider, "codex")
	merged.Defaults.Mode = valueOr(merged.Defaults.Mode, string(agent.DefaultMode))
	merged.UI.Rows = valueOr(merged.UI.Rows, "compact")
	if len(merged.Keys.AgentsNext) == 0 {
		merged.Keys.AgentsNext = []string{"alt+j", "alt+down"}
	}
	if len(merged.Keys.AgentsPrevious) == 0 {
		merged.Keys.AgentsPrevious = []string{"alt+k", "alt+up"}
	}
	if len(merged.Keys.QueueNext) == 0 {
		merged.Keys.QueueNext = []string{"alt+n"}
	}
	if len(merged.Keys.QueuePrevious) == 0 {
		merged.Keys.QueuePrevious = []string{"alt+p"}
	}
	if len(merged.Keys.Zoom) == 0 {
		merged.Keys.Zoom = []string{"alt+z"}
	}
	merged.Log.Level = valueOr(merged.Log.Level, "info")
	rendered, err := toml.Marshal(merged)
	if err != nil {
		return "", fmt.Errorf("render config: %w", err)
	}
	return string(rendered), nil
}
