package tmux

import (
	"os"
	"strings"
	"testing"
)

func TestEnsureServerConfigWritesManagedConfig(t *testing.T) {
	path, err := EnsureServerConfig(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(content), "Managed by Stormlight") {
		t.Fatalf("config missing managed marker: %q", content)
	}
	if !strings.Contains(string(content), "default-terminal") {
		t.Fatalf("config missing terminal setup: %q", content)
	}
}

// The boot config and the live-apply list are two halves of one statement:
// a fresh server reads the file, a running one gets the options asserted. A
// value declared in only one of them reaches only half the servers.
func TestServerConfigAndOptionsAgree(t *testing.T) {
	for _, option := range serverOptions {
		line := "set -g " + option[0] + " " + option[1]
		if !strings.Contains(serverConfig, line) {
			t.Fatalf("serverConfig is missing %q", line)
		}
	}
	for _, feature := range serverFeatures {
		line := "set -as terminal-features '," + feature + "'"
		if !strings.Contains(serverConfig, line) {
			t.Fatalf("serverConfig is missing %q", line)
		}
	}
}

func TestEnsureServerConfigOverwritesDrift(t *testing.T) {
	directory := t.TempDir()
	path, err := EnsureServerConfig(directory)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("set -g mouse off\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	again, err := EnsureServerConfig(directory)
	if err != nil {
		t.Fatal(err)
	}
	if again != path {
		t.Fatalf("path changed: %q vs %q", again, path)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != serverConfig {
		t.Fatalf("manual edit survived: %q", content)
	}
}
