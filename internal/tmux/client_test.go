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

func TestNewClientFromEnvUsesStormlightSocket(t *testing.T) {
	t.Setenv("STORMLIGHT_TMUX_SOCKET", "isolated")
	if got := NewClientFromEnv().Socket(); got != "isolated" {
		t.Fatalf("socket = %q", got)
	}
}

func TestNewClientFromEnvDefaultsToPrivateServer(t *testing.T) {
	t.Setenv("STORMLIGHT_TMUX_SOCKET", "")
	if got := NewClientFromEnv().Socket(); got != DefaultSocket {
		t.Fatalf("socket = %q, want %q", got, DefaultSocket)
	}
}

func TestClientAppliesSocketAndConfigToEveryCommand(t *testing.T) {
	client := NewClientWithConfig("stormlight", "/config/tmux.conf")
	got := client.commandArgs([]string{"list-panes", "-a"})
	want := []string{
		"-L", "stormlight",
		"-f", "/config/tmux.conf",
		"list-panes", "-a",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("got %#v, want %#v", got, want)
	}

	bare := NewClient("").commandArgs([]string{"list-panes"})
	if !slices.Equal(bare, []string{"list-panes"}) {
		t.Fatalf("bare args = %#v", bare)
	}
}
