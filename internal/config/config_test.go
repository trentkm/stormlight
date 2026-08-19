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

func TestHostsAreRead(t *testing.T) {
	path := writeConfig(t, `
[hosts.devbox]
destination = "trent@10.0.0.4"
bin = "/opt/stormlight/bin/stormlight"
options = ["-p", "2222"]

[hosts."trent@laptop"]

[hosts.plain]
`)
	cfg, warnings, err := loadFrom(path)
	if err != nil {
		t.Fatalf("loadFrom: %v", err)
	}
	if len(warnings) != 0 {
		t.Fatalf("unexpected warnings: %v", warnings)
	}
	if got := cfg.Hosts["devbox"]; got.Destination != "trent@10.0.0.4" ||
		got.Bin != "/opt/stormlight/bin/stormlight" ||
		len(got.Options) != 2 {
		t.Fatalf("host not read: %+v", got)
	}
	// A key that is already a destination is taken as one, so nobody has
	// to write trent@laptop twice.
	if got := cfg.Hosts["trent@laptop"]; got.Destination != "trent@laptop" {
		t.Fatalf("a user@host key should double as the destination: %+v", got)
	}
	// A plain name is left alone: ssh_config may well define it.
	if got := cfg.Hosts["plain"]; got.Destination != "" {
		t.Fatalf("a plain name is resolved by ssh, not by us: %+v", got)
	}
}

// TestAnUnusableHostNameIsRefused: the name qualifies workspace IDs and is
// spliced into an ssh command line, so it has to stay a plain word.
func TestAnUnusableHostNameIsRefused(t *testing.T) {
	path := writeConfig(t, "[hosts.\"dev box\"]\n")
	cfg, warnings, err := loadFrom(path)
	if err != nil {
		t.Fatalf("loadFrom: %v", err)
	}
	if len(warnings) == 0 {
		t.Fatal("a host name with a space should warn")
	}
	if _, kept := cfg.Hosts["dev box"]; kept {
		t.Fatalf("an unusable host must not be configured: %+v", cfg.Hosts)
	}
}

// TestAHostsShellMustBeAbsolute: the setting exists because the PATH a
// non-interactive SSH session gets does not find the shell by name, so a
// bare name is the one thing it cannot be.
func TestAHostsShellMustBeAbsolute(t *testing.T) {
	path := writeConfig(t, `
[hosts.good]
shell = "/home/linuxbrew/.linuxbrew/bin/fish"

[hosts.bare]
shell = "fish"
`)
	cfg, warnings, err := loadFrom(path)
	if err != nil {
		t.Fatalf("loadFrom: %v", err)
	}
	if got := cfg.Hosts["good"].Shell; got != "/home/linuxbrew/.linuxbrew/bin/fish" {
		t.Fatalf("an absolute shell should be kept: %q", got)
	}
	if got := cfg.Hosts["bare"].Shell; got != "" {
		t.Fatalf("a bare shell name should be dropped: %q", got)
	}
	// Dropped silently would leave the host quietly using the wrong
	// shell, which is the failure this setting was added to end.
	said := false
	for _, warning := range warnings {
		if strings.Contains(warning, "hosts.bare.shell") {
			said = true
		}
	}
	if !said {
		t.Fatalf("dropping it should say so: %q", warnings)
	}
	// And the host itself survives: only the unusable setting goes.
	if _, ok := cfg.Hosts["bare"]; !ok {
		t.Fatal("a bad shell should not remove the host")
	}
}
