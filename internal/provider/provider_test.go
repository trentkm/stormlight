package provider

import (
	"encoding/json"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/trentkm/stormlight/internal/agent"
)

func TestRegistryRejectsUnknownProvider(t *testing.T) {
	registry := NewRegistry()
	_, err := registry.Resolve(agent.Provider("unknown"), "do work", agent.DefaultMode)
	if err == nil {
		t.Fatal("expected an error")
	}
}

func TestRegistryRejectsEmptyTask(t *testing.T) {
	registry := NewRegistry()
	_, err := registry.Resolve(agent.ProviderShell, "   ", agent.DefaultMode)
	if err == nil {
		t.Fatal("expected an error")
	}
}

func TestShellProviderBuildsLoginShellCommand(t *testing.T) {
	registry := NewRegistry()
	launch, err := registry.Resolve(agent.ProviderShell, "printf test", agent.ModeAuto)
	if err != nil {
		t.Fatal(err)
	}
	if len(launch.Args) != 2 || launch.Args[0] != "-lc" || launch.Args[1] != "printf test" {
		t.Fatalf("unexpected args: %#v", launch.Args)
	}
}

func TestCodexArgsConfigureCompletionNotification(t *testing.T) {
	args, err := codexArgs("do work", agent.ModeEdits)
	if err != nil {
		t.Fatal(err)
	}
	if len(args) != 7 || args[0] != "-c" || args[len(args)-1] != "do work" {
		t.Fatalf("unexpected args: %#v", args)
	}
	var command []string
	if err := json.Unmarshal([]byte(strings.TrimPrefix(args[1], "notify=")), &command); err != nil {
		t.Fatalf("decode notify override: %v", err)
	}
	if len(command) != 3 ||
		!strings.Contains(command[2], "_provider-event codex") {
		t.Fatalf("unexpected notification command: %#v", command)
	}
	if !strings.Contains(command[2], "STORMLIGHT_BIN") {
		t.Fatalf("notification command lacks Stormlight executable: %q", command[2])
	}
}

func TestPermissionModeMapsToProviderFlags(t *testing.T) {
	cases := []struct {
		mode   agent.PermissionMode
		claude []string
		codex  []string
	}{
		{
			mode:   agent.ModeAsk,
			claude: nil,
			codex: []string{
				"--ask-for-approval", "untrusted",
				"--sandbox", "workspace-write",
			},
		},
		{
			mode:   agent.ModeEdits,
			claude: []string{"--permission-mode", "acceptEdits"},
			codex: []string{
				"--ask-for-approval", "on-request",
				"--sandbox", "workspace-write",
			},
		},
		{
			mode:   agent.ModeAuto,
			claude: []string{"--permission-mode", "bypassPermissions"},
			codex: []string{
				"--ask-for-approval", "never",
				"--sandbox", "danger-full-access",
			},
		},
	}
	for _, c := range cases {
		claude, err := claudeArgs("do work", c.mode)
		if err != nil {
			t.Fatal(err)
		}
		wantClaude := append([]string{claude[0], claude[1]}, c.claude...)
		wantClaude = append(wantClaude, "do work")
		if !slices.Equal(claude, wantClaude) {
			t.Fatalf("claude %s args = %#v, want %#v", c.mode, claude, wantClaude)
		}

		codex, err := codexArgs("do work", c.mode)
		if err != nil {
			t.Fatal(err)
		}
		wantCodex := append([]string{codex[0], codex[1]}, c.codex...)
		wantCodex = append(wantCodex, "do work")
		if !slices.Equal(codex, wantCodex) {
			t.Fatalf("codex %s args = %#v, want %#v", c.mode, codex, wantCodex)
		}
	}
}

func TestCustomProviderSpecBuildsLaunches(t *testing.T) {
	registry := NewRegistryWithSpecs([]Spec{{
		ID:     agent.Provider("echoer"),
		Label:  "Echoer",
		Binary: "echo",
		Args:   []string{"--message", "{task}"},
		ModeArgs: map[agent.PermissionMode][]string{
			agent.ModeAuto: {"--yes-always"},
		},
	}})

	if !slices.Contains(registry.IDs(), agent.Provider("echoer")) {
		t.Fatalf("custom provider missing from registry: %#v", registry.IDs())
	}

	launch, err := registry.Resolve(agent.Provider("echoer"), "do work", agent.ModeAuto)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"--yes-always", "--message", "do work"}
	if !slices.Equal(launch.Args, want) {
		t.Fatalf("args = %#v, want %#v", launch.Args, want)
	}

	launch, err = registry.Resolve(agent.Provider("echoer"), "do work", agent.ModeEdits)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(launch.Args, []string{"--message", "do work"}) {
		t.Fatalf("edits args = %#v", launch.Args)
	}
}

func TestCustomProviderAppendsTaskWithoutPlaceholder(t *testing.T) {
	registry := NewRegistryWithSpecs([]Spec{{
		ID:     agent.Provider("plain"),
		Binary: "echo",
		Args:   []string{"run"},
	}})
	launch, err := registry.Resolve(agent.Provider("plain"), "the task", agent.ModeAsk)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(launch.Args, []string{"run", "the task"}) {
		t.Fatalf("args = %#v", launch.Args)
	}
}

func TestBuiltinSpecOverridesBinaryAndAppendsExtraArgs(t *testing.T) {
	registry := NewRegistryWithSpecs([]Spec{{
		ID:        agent.ProviderClaude,
		Binary:    "echo",
		ExtraArgs: []string{"--model", "opus"},
	}})
	launch, err := registry.Resolve(agent.ProviderClaude, "do work", agent.ModeAsk)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(launch.Path) != "echo" {
		t.Fatalf("binary override ignored: %q", launch.Path)
	}
	count := len(launch.Args)
	if count < 4 || launch.Args[count-1] != "do work" ||
		launch.Args[count-2] != "opus" || launch.Args[count-3] != "--model" {
		t.Fatalf("extra args misplaced: %#v", launch.Args)
	}
	if launch.Args[0] != "--settings" {
		t.Fatalf("lifecycle hooks lost: %#v", launch.Args)
	}
	if len(registry.IDs()) != 3 {
		t.Fatalf("builtin override changed provider order: %#v", registry.IDs())
	}
}

func TestClaudeArgsConfigureLifecycleHooks(t *testing.T) {
	args, err := claudeArgs("do work", agent.ModeAsk)
	if err != nil {
		t.Fatal(err)
	}
	if len(args) != 3 || args[0] != "--settings" || args[2] != "do work" {
		t.Fatalf("unexpected args: %#v", args)
	}
	var settings claudeSettings
	if err := json.Unmarshal([]byte(args[1]), &settings); err != nil {
		t.Fatalf("decode settings: %v", err)
	}
	for _, name := range []string{
		"UserPromptSubmit",
		"Notification",
		"Stop",
	} {
		for _, group := range settings.Hooks[name] {
			if len(group.Hooks) != 1 {
				t.Fatalf("%s hooks = %#v", name, group)
			}
			if !strings.Contains(group.Hooks[0].Command, "_provider-event claude") {
				t.Fatalf("%s command = %q", name, group.Hooks[0].Command)
			}
			if !strings.Contains(group.Hooks[0].Command, "STORMLIGHT_BIN") {
				t.Fatalf("%s command lacks Stormlight executable: %q",
					name,
					group.Hooks[0].Command,
				)
			}
		}
	}
	matchers := []string{}
	for _, group := range settings.Hooks["Notification"] {
		matchers = append(matchers, group.Matcher)
	}
	// Only permission prompts: turn-end events carry the unseen-result
	// signal, so the delayed idle notification is deliberately absent.
	if !slices.Equal(matchers, []string{"permission_prompt"}) {
		t.Fatalf("notification matchers = %#v", matchers)
	}
	permissionGroups := settings.Hooks["PermissionRequest"]
	if len(permissionGroups) != 1 || len(permissionGroups[0].Hooks) != 1 {
		t.Fatalf("PermissionRequest hooks = %#v", permissionGroups)
	}
	permissionHook := permissionGroups[0].Hooks[0]
	if !strings.Contains(permissionHook.Command, "_provider-permission claude") ||
		permissionHook.Timeout != 86400 ||
		permissionHook.StatusMessage == "" {
		t.Fatalf("PermissionRequest hook = %#v", permissionHook)
	}
}
