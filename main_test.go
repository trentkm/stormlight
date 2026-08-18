package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/trentkm/stormlight/internal/agent"
	"github.com/trentkm/stormlight/internal/workspace"
)

func TestRootCommandUsesStormlightIdentity(t *testing.T) {
	command := newRootCommand()
	if command.Use != "stormlight [path]" {
		t.Fatalf("command use = %q", command.Use)
	}
	for _, path := range [][]string{
		{"workspace", "add"},
		{"workspace", "list"},
		{"workspace", "roots"},
	} {
		if _, _, err := command.Find(path); err != nil {
			t.Fatalf("missing command %v: %v", path, err)
		}
	}
}

func TestWriteWorkspacesEmitsStructuredJSON(t *testing.T) {
	value := workspace.DirectoryContext(t.TempDir())
	var output bytes.Buffer
	if err := writeWorkspaces(&output, []workspace.Context{value}, true); err != nil {
		t.Fatal(err)
	}
	var decoded []workspace.Context
	if err := json.Unmarshal(output.Bytes(), &decoded); err != nil {
		t.Fatal(err)
	}
	if len(decoded) != 1 || decoded[0].ID != value.ID {
		t.Fatalf("decoded = %#v", decoded)
	}
}

func TestOpenWorkspacePathRequiresDirectory(t *testing.T) {
	directory := t.TempDir()
	resolved, err := openWorkspacePath(directory)
	if err != nil || resolved != directory {
		t.Fatalf("resolved = %q, err = %v", resolved, err)
	}
	if _, err := openWorkspacePath("/definitely/not/a/dir"); err == nil {
		t.Fatal("missing directory was accepted")
	}
}

func TestParseActivityAcceptsKnownStatesAndRejectsOthers(t *testing.T) {
	for _, value := range []string{
		"", "starting", "working", "idle", "completed", "failed", "stopped",
	} {
		got, err := parseActivity(value)
		if err != nil {
			t.Fatalf("parseActivity(%q) = %v", value, err)
		}
		if got != agent.Activity(value) {
			t.Fatalf("parseActivity(%q) = %q", value, got)
		}
	}

	for _, value := range []string{"running", "Working", "done", "idle "} {
		if _, err := parseActivity(value); err == nil {
			t.Fatalf("parseActivity(%q) was accepted", value)
		}
	}
}

func TestParseAttentionTreatsNoneAndEmptyAsCleared(t *testing.T) {
	for _, value := range []string{"none", ""} {
		got, err := parseAttention(value)
		if err != nil {
			t.Fatalf("parseAttention(%q) = %v", value, err)
		}
		if got != agent.AttentionNone {
			t.Fatalf("parseAttention(%q) = %q, want cleared", value, got)
		}
	}

	for _, value := range []string{"question", "approval", "auth", "waiting"} {
		got, err := parseAttention(value)
		if err != nil {
			t.Fatalf("parseAttention(%q) = %v", value, err)
		}
		if got != agent.Attention(value) {
			t.Fatalf("parseAttention(%q) = %q", value, got)
		}
	}

	for _, value := range []string{"urgent", "Question", "blocked"} {
		if _, err := parseAttention(value); err == nil {
			t.Fatalf("parseAttention(%q) was accepted", value)
		}
	}
}

func TestShortIDTruncatesToEightCharacters(t *testing.T) {
	for _, testCase := range []struct{ id, want string }{
		{"", ""},
		{"abc", "abc"},
		{"12345678", "12345678"},
		{"123456789", "12345678"},
	} {
		if got := shortID(testCase.id); got != testCase.want {
			t.Fatalf("shortID(%q) = %q, want %q", testCase.id, got, testCase.want)
		}
	}
}

func TestTruncatePlainCollapsesWhitespaceAndCountsRunes(t *testing.T) {
	for _, testCase := range []struct {
		name   string
		value  string
		length int
		want   string
	}{
		{"shorter than limit", "hello", 10, "hello"},
		{"exactly the limit", "abcde", 5, "abcde"},
		{"collapses runs of whitespace", "  a \n  b\tc  ", 10, "a b c"},
		{"ellipsis replaces the final rune", "abcdefghij", 5, "abcd…"},
		{"limit of one takes a bare rune", "abcde", 1, "a"},
		{"limit of zero is empty", "abcde", 0, ""},
		{"multibyte counted as runes not bytes", "héllo wörld", 5, "héll…"},
		{"wide runes are not split", "🌊🌊🌊🌊", 3, "🌊🌊…"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			got := truncatePlain(testCase.value, testCase.length)
			if got != testCase.want {
				t.Fatalf("truncatePlain(%q, %d) = %q, want %q",
					testCase.value, testCase.length, got, testCase.want)
			}
		})
	}
}

// TestResolveCommandAnswersAsJSON: a dashboard on another machine runs
// this over SSH and parses what comes back, so the contract is the shape
// of stdout.
func TestResolveCommandAnswersAsJSON(t *testing.T) {
	directory := t.TempDir()
	command := newResolveCommand()
	var out bytes.Buffer
	command.SetOut(&out)
	command.SetArgs([]string{directory})
	if err := command.Execute(); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	var value workspace.Context
	if err := json.Unmarshal(out.Bytes(), &value); err != nil {
		t.Fatalf("stdout must be one JSON object: %v (%q)", err, out.String())
	}
	if value.Root == "" || value.Kind == "" || value.ID == "" {
		t.Fatalf("incomplete context: %+v", value)
	}
	// The answer describes this machine; naming the host is the asking
	// side's job, because a host does not know what a remote dashboard
	// calls it.
	if value.Host != "" {
		t.Fatalf("a resolver must not name its own host: %+v", value)
	}
}

func TestResolveCommandReportsEveryCheckout(t *testing.T) {
	command := newResolveCommand()
	var out bytes.Buffer
	command.SetOut(&out)
	command.SetArgs([]string{"--roots", t.TempDir()})
	if err := command.Execute(); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	var values []workspace.Context
	if err := json.Unmarshal(out.Bytes(), &values); err != nil {
		t.Fatalf("--roots must answer with a JSON array: %v (%q)", err, out.String())
	}
	if len(values) == 0 {
		t.Fatal("a workspace always has at least the checkout it was resolved from")
	}
}

// TestTheInstalledPathIsRecorded: a non-interactive SSH shell often has
// no ~/.local/bin on its PATH, so the bridge would look for stormlight by
// name and miss the one just installed. Naming it outright is what
// [hosts.*] is for.
func TestTheInstalledPathIsRecorded(t *testing.T) {
	directory := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", directory)
	path := filepath.Join(directory, "stormlight", "config.toml")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("[defaults]\nprovider = \"claude\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	recordHostBinary(&out, "devbox", "/home/trent/.local/bin/stormlight")

	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(content), "[hosts.devbox]") ||
		!strings.Contains(string(content), "/home/trent/.local/bin/stormlight") {
		t.Fatalf("config = %s", content)
	}
	// What was already there is untouched: appending costs nothing, and
	// re-emitting the file would cost the comments and ordering someone
	// put there by hand.
	if !strings.Contains(string(content), "[defaults]") {
		t.Fatalf("existing configuration was lost: %s", content)
	}

	// A host already configured is left alone and the change is described
	// instead, because appending a second [hosts.devbox] is not valid TOML.
	out.Reset()
	recordHostBinary(&out, "devbox", "/elsewhere/stormlight")
	if strings.Count(string(mustRead(t, path)), "[hosts.devbox]") != 1 {
		t.Fatalf("config = %s", mustRead(t, path))
	}
	if !strings.Contains(out.String(), "already exists") {
		t.Fatalf("it should say what to change: %q", out.String())
	}
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return content
}
