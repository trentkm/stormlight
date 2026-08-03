package main

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/trentkm/stormlight/internal/agent"
)

func TestRootCommandUsesStormlightIdentityAndDefaultSession(t *testing.T) {
	t.Setenv("STORMLIGHT_SESSION", "")

	command := newRootCommand()
	if command.Use != "stormlight [path]" {
		t.Fatalf("command use = %q", command.Use)
	}
	session, err := command.PersistentFlags().GetString("session")
	if err != nil {
		t.Fatal(err)
	}
	if session != "stormlight-agents" {
		t.Fatalf("session = %q", session)
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

func TestStormlightEnvironmentOverridesDefaultSession(t *testing.T) {
	t.Setenv("STORMLIGHT_SESSION", "stormlight-custom")

	command := newRootCommand()
	session, err := command.PersistentFlags().GetString("session")
	if err != nil {
		t.Fatal(err)
	}
	if session != "stormlight-custom" {
		t.Fatalf("session = %q", session)
	}
}

func TestDashboardHostingOnlyWrapsOutsideTmuxLaunches(t *testing.T) {
	t.Setenv("TMUX", "")
	t.Setenv(dashboardHostedEnv, "")
	if !shouldHostDashboard() {
		t.Fatal("outside-tmux dashboard was not wrapped")
	}

	t.Setenv("TMUX", "/tmp/tmux/default,1,0")
	if shouldHostDashboard() {
		t.Fatal("inside-tmux dashboard was wrapped")
	}

	t.Setenv("TMUX", "")
	t.Setenv(dashboardHostedEnv, "1")
	if shouldHostDashboard() {
		t.Fatal("hosted dashboard attempted to wrap itself")
	}
}

func TestDashboardHostArgsPreserveSocketAndDirectory(t *testing.T) {
	got := dashboardHostArgs(
		"isolated",
		"/config/stormlight/tmux.conf",
		"stormlight-ui-42",
		"/workspace/project",
		"STORMLIGHT_UI_HOSTED=1 exec '/bin/stormlight'",
	)
	want := []string{
		"-L", "isolated",
		"-f", "/config/stormlight/tmux.conf",
		"new-session",
		"-s", "stormlight-ui-42",
		"-c", "/workspace/project",
		"-n", "stormlight",
		"STORMLIGHT_UI_HOSTED=1 exec '/bin/stormlight'",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("got %#v, want %#v", got, want)
	}
}

func TestDashboardShellCommandQuotesReentryArguments(t *testing.T) {
	got := dashboardShellCommand(
		"/tmp/run stead",
		[]string{"--session", "quote's", ""},
	)
	want := "STORMLIGHT_UI_HOSTED=1 exec " +
		"'/tmp/run stead' '--session' 'quote'\"'\"'s' ''"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestDashboardSessionNamesAreTemporaryAndDistinct(t *testing.T) {
	first := dashboardSessionName(42, time.Unix(0, 1))
	second := dashboardSessionName(42, time.Unix(0, 2))
	if !strings.HasPrefix(first, "stormlight-ui-42-") || first == second {
		t.Fatalf("session names are not unique: %q, %q", first, second)
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
