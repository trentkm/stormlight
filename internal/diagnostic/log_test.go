package diagnostic

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInitWritesJSONAndTailReturnsLatestLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runstead.log")
	if _, err := Init(path, "debug"); err != nil {
		t.Fatal(err)
	}
	Logger().Debug("first test message", "value", 1)
	Logger().Info("second test message", "value", 2)
	Close()

	tail, err := Tail(path, 2)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(tail, "logging initialized") {
		t.Fatalf("tail returned too many lines:\n%s", tail)
	}
	if !strings.Contains(tail, `"msg":"first test message"`) ||
		!strings.Contains(tail, `"msg":"second test message"`) {
		t.Fatalf("missing log messages:\n%s", tail)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("log permissions = %o", info.Mode().Perm())
	}
}

func TestInitRejectsUnknownLevel(t *testing.T) {
	_, err := Init(filepath.Join(t.TempDir(), "runstead.log"), "verbose")
	if err == nil {
		t.Fatal("expected invalid level error")
	}
}

func TestResolvePathSupportsLegacyEnvironmentFallback(t *testing.T) {
	legacy := filepath.Join(t.TempDir(), "legacy.log")
	t.Setenv("RUNSTEAD_LOG_FILE", "")
	t.Setenv("AGENTMUX_LOG_FILE", legacy)

	got, err := ResolvePath("")
	if err != nil {
		t.Fatal(err)
	}
	if got != legacy {
		t.Fatalf("path = %q, want %q", got, legacy)
	}
}

func TestResolvePathPrefersRunsteadEnvironment(t *testing.T) {
	current := filepath.Join(t.TempDir(), "current.log")
	t.Setenv("RUNSTEAD_LOG_FILE", current)
	t.Setenv("AGENTMUX_LOG_FILE", filepath.Join(t.TempDir(), "legacy.log"))

	got, err := ResolvePath("")
	if err != nil {
		t.Fatal(err)
	}
	if got != current {
		t.Fatalf("path = %q, want %q", got, current)
	}
}
