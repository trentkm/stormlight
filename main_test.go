package main

import "testing"

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
