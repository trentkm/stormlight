package provider

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/trentkm/runstead/internal/agent"
)

func TestRegistryRejectsUnknownProvider(t *testing.T) {
	registry := NewRegistry()
	_, err := registry.Resolve(agent.Provider("unknown"), "do work")
	if err == nil {
		t.Fatal("expected an error")
	}
}

func TestRegistryRejectsEmptyTask(t *testing.T) {
	registry := NewRegistry()
	_, err := registry.Resolve(agent.ProviderShell, "   ")
	if err == nil {
		t.Fatal("expected an error")
	}
}

func TestShellProviderBuildsLoginShellCommand(t *testing.T) {
	registry := NewRegistry()
	launch, err := registry.Resolve(agent.ProviderShell, "printf test")
	if err != nil {
		t.Fatal(err)
	}
	if len(launch.Args) != 2 || launch.Args[0] != "-lc" || launch.Args[1] != "printf test" {
		t.Fatalf("unexpected args: %#v", launch.Args)
	}
}

func TestCodexArgsConfigureCompletionNotification(t *testing.T) {
	args, err := codexArgs("do work")
	if err != nil {
		t.Fatal(err)
	}
	if len(args) != 3 || args[0] != "-c" || args[2] != "do work" {
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
	if !strings.Contains(command[2], "RUNSTEAD_BIN") ||
		!strings.Contains(command[2], "AGENTMUX_BIN") {
		t.Fatalf("notification command lacks compatibility fallback: %q", command[2])
	}
}

func TestClaudeArgsConfigureLifecycleHooks(t *testing.T) {
	args, err := claudeArgs("do work")
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
		groups := settings.Hooks[name]
		if len(groups) != 1 || len(groups[0].Hooks) != 1 {
			t.Fatalf("%s hooks = %#v", name, groups)
		}
		if !strings.Contains(groups[0].Hooks[0].Command, "_provider-event claude") {
			t.Fatalf("%s command = %q", name, groups[0].Hooks[0].Command)
		}
		if !strings.Contains(groups[0].Hooks[0].Command, "RUNSTEAD_BIN") ||
			!strings.Contains(groups[0].Hooks[0].Command, "AGENTMUX_BIN") {
			t.Fatalf("%s command lacks compatibility fallback: %q",
				name,
				groups[0].Hooks[0].Command,
			)
		}
	}
	if settings.Hooks["Notification"][0].Matcher != "permission_prompt" {
		t.Fatalf("notification matcher = %q", settings.Hooks["Notification"][0].Matcher)
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
