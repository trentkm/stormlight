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
	if cfg.Defaults.Provider != "" || cfg.Defaults.Mode != "" {
		t.Fatalf("defaults = %#v", cfg.Defaults)
	}
}

func TestLoadParsesAndValidates(t *testing.T) {
	writeConfig(t, `
[defaults]
provider = "claude"
mode = "auto"

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

func TestEffectiveTOMLFillsBuiltinDefaults(t *testing.T) {
	rendered, err := Config{}.EffectiveTOML()
	if err != nil {
		t.Fatal(err)
	}

	for _, want := range []string{
		`provider = 'codex'`,
		`mode = '` + string(agent.DefaultMode) + `'`,
		`rows = 'compact'`,
		`level = 'info'`,
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("rendered config missing %q:\n%s", want, rendered)
		}
	}
}

func TestEffectiveTOMLKeepsFileValuesOverDefaults(t *testing.T) {
	cfg := Config{
		Defaults: Defaults{Provider: "claude", Mode: "ask"},
		UI:       UI{Rows: "sideways"},
		Log:      Log{Level: "debug"},
	}

	rendered, err := cfg.EffectiveTOML()
	if err != nil {
		t.Fatal(err)
	}

	for _, want := range []string{
		`provider = 'claude'`,
		`mode = 'ask'`,
		`rows = 'sideways'`,
		`level = 'debug'`,
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("rendered config missing %q:\n%s", want, rendered)
		}
	}
}

func TestEffectiveTOMLRoundTripsThroughLoad(t *testing.T) {
	rendered, err := Config{}.EffectiveTOML()
	if err != nil {
		t.Fatal(err)
	}
	writeConfig(t, rendered)

	cfg, warnings, err := Load()
	if err != nil {
		t.Fatalf("rendered config did not parse: %v\n%s", err, rendered)
	}
	if len(warnings) != 0 {
		t.Fatalf("rendered config produced warnings %#v:\n%s", warnings, rendered)
	}
	if cfg.Defaults.Provider != "codex" {
		t.Fatalf("defaults did not survive the round trip: %#v", cfg.Defaults)
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
