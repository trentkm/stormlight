package provider

import (
	"encoding/json"
	"maps"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/pelletier/go-toml/v2"
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
	_, err := registry.Resolve(agent.ProviderClaude, "   ", agent.DefaultMode)
	if err == nil {
		t.Fatal("expected an error")
	}
}

func TestCodexArgsConfigureLifecycleHooks(t *testing.T) {
	args, err := codexArgs("do work", agent.ModeEdits)
	if err != nil {
		t.Fatal(err)
	}
	if len(args) != 9 || args[0] != "-c" || args[2] != "-c" ||
		args[len(args)-1] != "do work" {
		t.Fatalf("unexpected args: %#v", args)
	}
	// Codex holds injected hooks at "installed, not active" until a human
	// trusts them, and reports nothing at all in the meantime. `notify` has
	// no such gate, so it stays as the floor: without it an agent whose
	// hooks are untrusted would sit at `working` until its process exited.
	var notify struct {
		Notify []string `toml:"notify"`
	}
	if err := toml.Unmarshal([]byte(args[1]), &notify); err != nil {
		t.Fatalf("decode notify override: %v", err)
	}
	if len(notify.Notify) != 3 ||
		!strings.Contains(notify.Notify[2], "_provider-event codex") ||
		!strings.Contains(notify.Notify[2], "STORMLIGHT_BIN") {
		t.Fatalf("notify fallback lost: %#v", notify.Notify)
	}

	// Codex reads the override from a single argument and parses the value
	// as TOML, rejecting JSON outright, so the encoding has to stay an
	// inline single-line assignment.
	override := args[3]
	if strings.ContainsAny(override, "\r\n") {
		t.Fatalf("override spans lines: %q", override)
	}
	var settings struct {
		Hooks map[string][]hookGroup `toml:"hooks"`
	}
	if err := toml.Unmarshal([]byte(override), &settings); err != nil {
		t.Fatalf("decode hooks override: %v", err)
	}

	names := slices.Sorted(maps.Keys(settings.Hooks))
	// A turn start is the signal `notify` never carried; without
	// UserPromptSubmit a Codex agent prompted in its own pane stays `idle`
	// on the dashboard for the whole turn.
	if !slices.Equal(names, []string{"Stop", "UserPromptSubmit"}) {
		t.Fatalf("hook events = %#v", names)
	}
	for _, name := range names {
		for _, group := range settings.Hooks[name] {
			if len(group.Hooks) != 1 {
				t.Fatalf("%s hooks = %#v", name, group)
			}
			command := group.Hooks[0].Command
			if !strings.Contains(command, "_provider-event codex") {
				t.Fatalf("%s command = %q", name, command)
			}
			if !strings.Contains(command, "STORMLIGHT_BIN") {
				t.Fatalf("%s command lacks Stormlight executable: %q", name, command)
			}
		}
	}
	// Codex's PermissionRequest hook decides whether a tool call proceeds
	// rather than merely observing it. Prompts are answered in the agent's
	// own terminal, so registering it would put Stormlight in the path of
	// an approval it never answers.
	if groups := settings.Hooks["PermissionRequest"]; len(groups) != 0 {
		t.Fatalf("PermissionRequest hook resurfaced: %#v", groups)
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
		// Two -c overrides precede the mode flags: notify, then hooks.
		wantCodex := append(slices.Clone(codex[:4]), c.codex...)
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
	if len(registry.IDs()) != 2 {
		t.Fatalf("builtin override changed provider order: %#v", registry.IDs())
	}
}

// Resume launches must carry the same lifecycle wiring a fresh dispatch
// gets — a resumed agent that reports nothing would sit on the dashboard
// exactly like the broken-hooks failure mode the wiring exists to prevent.
func TestResumeArgsReopenSessionWithLifecycleWiring(t *testing.T) {
	const sessionID = "019b9a7e-9846-7da2-9041-d4cec65d4d1d"

	claude, err := claudeResumeArgs(sessionID, agent.ModeAuto)
	if err != nil {
		t.Fatal(err)
	}
	if claude[len(claude)-1] != "--resume="+sessionID {
		t.Fatalf("claude resume args = %#v", claude)
	}
	if claude[0] != "--settings" ||
		!slices.Contains(claude, "bypassPermissions") {
		t.Fatalf("claude lifecycle wiring lost: %#v", claude)
	}
	if slices.Contains(claude, "") {
		t.Fatalf("claude resume carries an empty prompt: %#v", claude)
	}

	codex, err := codexResumeArgs(sessionID, agent.ModeAuto)
	if err != nil {
		t.Fatal(err)
	}
	if codex[0] != "resume" || codex[len(codex)-1] != sessionID {
		t.Fatalf("codex resume args = %#v", codex)
	}
	if codex[1] != "-c" || codex[3] != "-c" ||
		!slices.Contains(codex, "--ask-for-approval") {
		t.Fatalf("codex lifecycle wiring lost: %#v", codex)
	}
}

func TestRegistryResumeRejectsCustomProvidersAndEmptyIDs(t *testing.T) {
	registry := NewRegistryWithSpecs([]Spec{{
		ID:     agent.Provider("echoer"),
		Binary: "echo",
	}})
	if _, err := registry.Resume(agent.Provider("echoer"), "some-id", agent.ModeAsk); err == nil {
		t.Fatal("custom provider resumed without a resume verb")
	}
	if _, err := registry.Resume(agent.ProviderClaude, "  ", agent.ModeAsk); err == nil {
		t.Fatal("blank session id accepted")
	}
}

// Codex derives the session id from its rollout naming —
// `rollout-<timestamp>-<uuid>.jsonl` — the fallback handle for records
// written before hook payloads carried session_id.
func TestCodexSessionIDReadsTheRolloutBasename(t *testing.T) {
	cases := map[string]string{
		"/Users/u/.codex/sessions/2026/08/09/rollout-2026-08-09T10-19-00-019fe71b-1a86-7400-b39d-74bf1c5f903d.jsonl": "019fe71b-1a86-7400-b39d-74bf1c5f903d",
		"rollout-2026-08-09T10-19-00-019fe71b-1a86-7400-b39d-74bf1c5f903d.jsonl":                                     "019fe71b-1a86-7400-b39d-74bf1c5f903d",
		"/Users/u/.codex/history.jsonl": "",
		"rollout-short.jsonl":           "",
		"019fe71b-1a86-7400-b39d-74bf1c5f903d.jsonl": "",
		"": "",
	}
	for path, want := range cases {
		if got := codexSessionID(path); got != want {
			t.Errorf("codexSessionID(%q) = %q, want %q", path, got, want)
		}
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
	var settings hookSettings
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
	// Prompts are answered in the agent's own terminal: nothing may
	// intercept the request, or the agent would block on a resolver that
	// no longer exists.
	if groups := settings.Hooks["PermissionRequest"]; len(groups) != 0 {
		t.Fatalf("PermissionRequest hook resurfaced: %#v", groups)
	}
}

func TestClaudeResumeArgsCarryTheHooksAndNoPrompt(t *testing.T) {
	args, err := claudeResumeArgs("5f2b8c14-0000-4000-8000-000000000001", agent.ModeAuto)
	if err != nil {
		t.Fatal(err)
	}
	// Reopening a conversation is not resuming its work: the launch ends at
	// --resume, with nothing after it that Claude would read as a prompt.
	last := args[len(args)-1]
	if last != "--resume=5f2b8c14-0000-4000-8000-000000000001" {
		t.Fatalf("resume args end with %q: %#v", last, args)
	}
	if !slices.Contains(args, "--settings") {
		t.Fatalf("a restored agent lost its lifecycle hooks: %#v", args)
	}
	// The permission mode has to survive too, or an auto agent comes back
	// asking about every edit it was cleared to make before the reboot.
	if !slices.Contains(args, "bypassPermissions") {
		t.Fatalf("permission mode lost on resume: %#v", args)
	}
}

// The session id has to stay one argument, because that is what lets a
// user's extra_args slot in ahead of it the way they do ahead of a prompt.
func TestResumeKeepsExtraArgsOffTheSessionID(t *testing.T) {
	registry := NewRegistryWithSpecs([]Spec{{
		ID:        agent.ProviderClaude,
		Binary:    "echo",
		ExtraArgs: []string{"--model", "opus"},
	}})
	launch, err := registry.Resume(
		agent.ProviderClaude,
		"5f2b8c14-0000-4000-8000-000000000001",
		agent.ModeAuto,
	)
	if err != nil {
		t.Fatal(err)
	}
	count := len(launch.Args)
	if launch.Args[count-1] != "--resume=5f2b8c14-0000-4000-8000-000000000001" ||
		launch.Args[count-2] != "opus" || launch.Args[count-3] != "--model" {
		t.Fatalf("extra args misplaced on resume: %#v", launch.Args)
	}
}

func TestClaudeSessionIDReadsTheTranscriptBasename(t *testing.T) {
	cases := map[string]string{
		"/home/u/.claude/projects/slug/abc-123.jsonl": "abc-123",
		"abc-123.jsonl": "abc-123",
		"/home/u/.claude/projects/slug/abc-123.json": "",
		"/home/u/.claude/projects/slug/":             "",
		"":                                           "",
	}
	for path, want := range cases {
		if got := claudeSessionID(path); got != want {
			t.Errorf("claudeSessionID(%q) = %q, want %q", path, got, want)
		}
	}
}

// Both built-in providers can reopen their own conversations — verified
// against `claude --resume` and `codex resume` (codex-cli 0.147.0) — and a
// provider that cannot has to say so: a restore screen that silently
// offered one would spawn a command nobody verified.
func TestProvidersDeclareWhetherTheyCanResume(t *testing.T) {
	registry := NewRegistry()
	if !registry.CanResume(agent.ProviderClaude) {
		t.Error("Claude should be resumable")
	}
	if !registry.CanResume(agent.ProviderCodex) {
		t.Error("Codex should be resumable")
	}
	if registry.CanResume(agent.Provider("nope")) {
		t.Error("an unknown provider claims a resume path")
	}
	if _, err := registry.Resume(agent.ProviderClaude, "  ", agent.ModeAuto); err == nil {
		t.Error("resuming with no session id reported success")
	}
}
