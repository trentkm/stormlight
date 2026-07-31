package tmux

import (
	"slices"
	"testing"
)

func TestWithoutTmuxEnvironment(t *testing.T) {
	got := withoutTmuxEnvironment([]string{
		"PATH=/bin",
		"TMUX=/tmp/tmux/default,1,0",
		"TMUX_PANE=%4",
		"HOME=/tmp/home",
	})
	want := []string{"PATH=/bin", "HOME=/tmp/home"}
	if !slices.Equal(got, want) {
		t.Fatalf("got %#v, want %#v", got, want)
	}
}

func TestNewClientFromEnvSupportsLegacySocket(t *testing.T) {
	t.Setenv("RUNSTEAD_TMUX_SOCKET", "")
	t.Setenv("AGENTMUX_TMUX_SOCKET", "legacy")
	if got := NewClientFromEnv().Socket(); got != "legacy" {
		t.Fatalf("socket = %q", got)
	}
}

func TestNewClientFromEnvPrefersRunsteadSocket(t *testing.T) {
	t.Setenv("RUNSTEAD_TMUX_SOCKET", "current")
	t.Setenv("AGENTMUX_TMUX_SOCKET", "legacy")
	if got := NewClientFromEnv().Socket(); got != "current" {
		t.Fatalf("socket = %q", got)
	}
}
