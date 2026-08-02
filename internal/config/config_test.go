package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/trentkm/stormlight/internal/agent"
)

func writeConfig(t *testing.T, content string) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", home)
	directory := filepath.Join(home, "stormlight")
	if err := os.MkdirAll(directory, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "config.toml")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadMissingFileYieldsDefaults(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	cfg, warnings, err := Load()
	if err != nil || len(warnings) != 0 {
		t.Fatalf("err = %v, warnings = %v", err, warnings)
	}
	if cfg.SocketOr("stormlight") != "stormlight" {
		t.Fatalf("socket = %q", cfg.SocketOr("stormlight"))
	}
}

func TestLoadParsesAndValidates(t *testing.T) {
	writeConfig(t, `
[defaults]
provider = "claude"
mode = "auto"

[tmux]
socket = ""
return_keys = ["F12"]

[ui]
rows = "sideways"

[workspaces."/tmp/trusted"]
mode = "auto"

[workspaces."/tmp"]
mode = "ask"
provider = "codex"
`)
	cfg, warnings, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Defaults.Provider != "claude" || cfg.Defaults.Mode != "auto" {
		t.Fatalf("defaults = %#v", cfg.Defaults)
	}
	if cfg.SocketOr("stormlight") != "" {
		t.Fatal("explicit empty socket was not honored")
	}
	if len(cfg.Tmux.ReturnKeys) != 1 || cfg.Tmux.ReturnKeys[0] != "F12" {
		t.Fatalf("return keys = %#v", cfg.Tmux.ReturnKeys)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "ui.rows") {
		t.Fatalf("warnings = %#v", warnings)
	}
	if cfg.UI.Rows != "" {
		t.Fatalf("invalid rows survived: %q", cfg.UI.Rows)
	}

	mode, ok := cfg.ModeForDir("/tmp/trusted/nested/dir")
	if !ok || mode != agent.ModeAuto {
		t.Fatalf("longest-prefix mode = %q, ok = %v", mode, ok)
	}
	mode, ok = cfg.ModeForDir("/tmp/other")
	if !ok || mode != agent.ModeAsk {
		t.Fatalf("parent mode = %q, ok = %v", mode, ok)
	}
	provider, ok := cfg.ProviderForDir("/tmp/other")
	if !ok || provider != agent.ProviderCodex {
		t.Fatalf("provider override = %q, ok = %v", provider, ok)
	}
	if _, ok := cfg.ModeForDir("/somewhere/else"); ok {
		t.Fatal("unrelated directory matched an override")
	}
}

func TestLoadRejectsUnparsableFile(t *testing.T) {
	writeConfig(t, "defaults = not valid toml [")
	if _, _, err := Load(); err == nil {
		t.Fatal("expected a parse error")
	}
}

func TestInvalidModeValuesAreClearedWithWarnings(t *testing.T) {
	writeConfig(t, `
[defaults]
mode = "always"

[workspaces."/tmp"]
mode = "yolo"
`)
	cfg, warnings, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Defaults.Mode != "" {
		t.Fatalf("invalid default mode survived: %q", cfg.Defaults.Mode)
	}
	if _, ok := cfg.ModeForDir("/tmp/x"); ok {
		t.Fatal("invalid workspace mode survived")
	}
	if len(warnings) != 2 {
		t.Fatalf("warnings = %#v", warnings)
	}
}

func TestWriteTemplateRefusesToOverwrite(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	path, err := WriteTemplate(false)
	if err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(content), "[defaults]") {
		t.Fatalf("template missing defaults section: %q", content)
	}
	if _, err := WriteTemplate(false); err == nil {
		t.Fatal("expected refusal to overwrite")
	}
	if _, err := WriteTemplate(true); err != nil {
		t.Fatalf("force overwrite failed: %v", err)
	}
}
