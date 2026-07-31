package main

import (
	"slices"
	"strings"
	"testing"
	"time"
)

func TestRootCommandUsesRunsteadIdentityAndLegacyEnvironmentFallback(t *testing.T) {
	t.Setenv("RUNSTEAD_SESSION", "")
	t.Setenv("AGENTMUX_SESSION", "legacy-agents")

	command := newRootCommand()
	if command.Use != "runstead" {
		t.Fatalf("command use = %q", command.Use)
	}
	session, err := command.PersistentFlags().GetString("session")
	if err != nil {
		t.Fatal(err)
	}
	if session != "legacy-agents" {
		t.Fatalf("session = %q", session)
	}
}

func TestRunsteadEnvironmentTakesPrecedence(t *testing.T) {
	t.Setenv("RUNSTEAD_SESSION", "runstead-custom")
	t.Setenv("AGENTMUX_SESSION", "legacy-agents")

	command := newRootCommand()
	session, err := command.PersistentFlags().GetString("session")
	if err != nil {
		t.Fatal(err)
	}
	if session != "runstead-custom" {
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
		"runstead-ui-42",
		"/workspace/project",
		"RUNSTEAD_UI_HOSTED=1 exec '/bin/runstead'",
	)
	want := []string{
		"-L", "isolated",
		"new-session",
		"-s", "runstead-ui-42",
		"-c", "/workspace/project",
		"-n", "runstead",
		"RUNSTEAD_UI_HOSTED=1 exec '/bin/runstead'",
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
	want := "RUNSTEAD_UI_HOSTED=1 exec " +
		"'/tmp/run stead' '--session' 'quote'\"'\"'s' ''"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestDashboardSessionNamesAreTemporaryAndDistinct(t *testing.T) {
	first := dashboardSessionName(42, time.Unix(0, 1))
	second := dashboardSessionName(42, time.Unix(0, 2))
	if !strings.HasPrefix(first, "runstead-ui-42-") || first == second {
		t.Fatalf("session names are not unique: %q, %q", first, second)
	}
}
