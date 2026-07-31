package tmux

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/trentkm/runstead/internal/agent"
	"github.com/trentkm/runstead/internal/session"
	"github.com/trentkm/runstead/internal/workspace"
)

func TestParseAgent(t *testing.T) {
	parts := []string{
		"runstead-agents",
		"@7",
		"2",
		"cx-fix-parser",
		"%9",
		"codex",
		"/tmp/project",
		"Codex",
		"0",
		"",
		"0123456789abcdef",
		"codex",
		"Fix the parser",
		"Running parser tests",
		"/tmp/project",
		"1700000000",
		"working",
		"approval",
		"%9",
		"git:/tmp/project/.git",
		"git",
		"project",
		"/tmp/project",
		"/tmp/project",
		"project",
		"/tmp/project",
		"eyJicmFuY2giOiJtYWluIn0",
	}
	parts = append(parts, make([]string, metadataFieldCount)...)
	line := strings.Join(parts, fieldSeparator)

	managedAgent, ok := parseAgent(line)
	if !ok {
		t.Fatal("expected managed agent")
	}
	if managedAgent.ID != "0123456789abcdef" ||
		managedAgent.Provider != agent.ProviderCodex {
		t.Fatalf("unexpected agent identity: %#v", managedAgent)
	}
	if managedAgent.WindowID != "@7" ||
		managedAgent.PaneID != "%9" ||
		!managedAgent.ProcessLive {
		t.Fatalf("unexpected tmux state: %#v", managedAgent)
	}
	if managedAgent.Attention != agent.AttentionApproval {
		t.Fatalf("attention = %q", managedAgent.Attention)
	}
	if managedAgent.Workspace.ID != "git:/tmp/project/.git" ||
		managedAgent.Workspace.Metadata["branch"] != "main" {
		t.Fatalf("workspace = %#v", managedAgent.Workspace)
	}
}

func TestParseAgentDerivesCompletedFromDeadPane(t *testing.T) {
	parts := make([]string, basePaneFieldCount+2*metadataFieldCount)
	parts[0] = "runstead-agents"
	parts[1] = "@1"
	parts[2] = "0"
	parts[3] = "job"
	parts[4] = "%1"
	parts[8] = "1"
	parts[9] = "0"
	parts[10] = "id"
	parts[11] = "shell"
	parts[15] = "1700000000"
	parts[16] = "working"
	parts[18] = "%1"

	managedAgent, ok := parseAgent(strings.Join(parts, fieldSeparator))
	if !ok {
		t.Fatal("expected managed agent")
	}
	if managedAgent.Activity != agent.ActivityCompleted {
		t.Fatalf("activity = %q", managedAgent.Activity)
	}
	if managedAgent.ProcessLive {
		t.Fatal("dead pane reported as live")
	}
	if managedAgent.ExitCode == nil || *managedAgent.ExitCode != 0 {
		t.Fatalf("exit code = %#v", managedAgent.ExitCode)
	}
}

func TestParseAgentSkipsUntaggedPane(t *testing.T) {
	parts := make([]string, basePaneFieldCount+2*metadataFieldCount)
	if _, ok := parseAgent(strings.Join(parts, fieldSeparator)); ok {
		t.Fatal("expected untagged pane to be skipped")
	}
}

func TestParseAgentDiscoversLegacyAgentmuxMetadata(t *testing.T) {
	parts := make([]string, basePaneFieldCount+2*metadataFieldCount)
	legacy := basePaneFieldCount + metadataFieldCount
	parts[0] = "agentmux-agents"
	parts[1] = "@3"
	parts[3] = "cl-legacy"
	parts[4] = "%4"
	parts[legacy] = "legacy-id"
	parts[legacy+1] = "claude"
	parts[legacy+4] = "/tmp/legacy"
	parts[legacy+6] = "idle"
	parts[legacy+8] = "%4"

	managedAgent, ok := parseAgent(strings.Join(parts, fieldSeparator))
	if !ok {
		t.Fatal("expected legacy managed agent")
	}
	if managedAgent.ID != "legacy-id" ||
		managedAgent.Provider != agent.ProviderClaude {
		t.Fatalf("unexpected legacy agent: %#v", managedAgent)
	}
	if managedAgent.TmuxSession != "agentmux-agents" {
		t.Fatalf("tmux session = %q", managedAgent.TmuxSession)
	}
}

func TestWindowName(t *testing.T) {
	got := windowName(agent.ProviderCodex, "Fix OAuth callback validation, please")
	if got != "cx-fix-oauth-callback-valid" {
		t.Fatalf("got %q", got)
	}
}

func TestLaunchRoundTrip(t *testing.T) {
	want := session.Launch{
		Path: "/path/with spaces/codex",
		Args: []string{"quote's", "line\nbreak"},
	}
	encoded, err := encodeLaunch(want)
	if err != nil {
		t.Fatal(err)
	}
	got, err := DecodeLaunch(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if got.Path != want.Path || len(got.Args) != len(want.Args) {
		t.Fatalf("got %#v, want %#v", got, want)
	}
	for i := range want.Args {
		if got.Args[i] != want.Args[i] {
			t.Fatalf("arg %d: got %q, want %q", i, got.Args[i], want.Args[i])
		}
	}
}

func TestShellJoinQuotesEveryArgument(t *testing.T) {
	got := shellJoin([]string{"/tmp/agent mux", "quote's", ""})
	want := "'/tmp/agent mux' 'quote'\"'\"'s' ''"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestMetadataValueCollapsesControlWhitespace(t *testing.T) {
	got := metadataValue("one\n two\tthree")
	if got != "one two three" {
		t.Fatalf("got %q", got)
	}
}

func TestCaptureUsesCurrentScreenForLivePane(t *testing.T) {
	runner := &captureRunner{agentLine: captureAgentLine(false)}
	runtime := &Runtime{runner: runner}

	output, err := runtime.Capture(context.Background(), "capture-id", 120)
	if err != nil {
		t.Fatal(err)
	}
	if output != "pane output" {
		t.Fatalf("output = %q", output)
	}
	want := []string{"capture-pane", "-p", "-e", "-t", "%1"}
	if len(runner.calls) != 2 || !slices.Equal(runner.calls[1], want) {
		t.Fatalf("capture call = %#v, want %#v", runner.calls, want)
	}
}

func TestCaptureIncludesScrollbackForExitedPane(t *testing.T) {
	runner := &captureRunner{agentLine: captureAgentLine(true)}
	runtime := &Runtime{runner: runner}

	if _, err := runtime.Capture(context.Background(), "capture-id", 40); err != nil {
		t.Fatal(err)
	}
	want := []string{"capture-pane", "-p", "-e", "-t", "%1", "-S", "-40"}
	if len(runner.calls) != 2 || !slices.Equal(runner.calls[1], want) {
		t.Fatalf("capture call = %#v, want %#v", runner.calls, want)
	}
}

func TestAttachOutsideTmuxReturnsInteractiveCommand(t *testing.T) {
	t.Setenv("TMUX", "")
	t.Setenv("TMUX_PANE", "")
	runner := &captureRunner{agentLine: captureAgentLine(false)}
	runtime := &Runtime{runner: runner, socket: "isolated"}

	result, err := runtime.Attach(context.Background(), "capture-id")
	if err != nil {
		t.Fatal(err)
	}
	if result.Command == nil {
		t.Fatal("expected an interactive attach command")
	}
	want := []string{"tmux", "-L", "isolated", "attach-session", "-t", "runstead-agents"}
	if !slices.Equal(result.Command.Args, want) {
		t.Fatalf("command = %#v, want %#v", result.Command.Args, want)
	}
	wantCalls := [][]string{
		{"list-panes", "-a", "-F", expectedPaneListFormat()},
		{"list-keys", "-T", "prefix", "Q"},
		{"bind-key", "-T", "prefix", "-N", "Return from Runstead", "Q",
			"run-shell", "-C", returnBindingFormat},
	}
	wantCalls = append(wantCalls, prefixFeedbackCalls("runstead-agents")...)
	wantCalls = append(wantCalls,
		[]string{"set-option", "-w", "-t", "@1", "@runstead_return_target", ""},
		[]string{"select-window", "-t", "@1"},
	)
	assertCalls(t, runner.calls, wantCalls)
}

func TestAttachInsideTmuxSwitchesCurrentClient(t *testing.T) {
	tests := []struct {
		name   string
		tmux   string
		socket string
	}{
		{
			name: "default socket",
			tmux: "/private/tmp/tmux/default,1,0",
		},
		{
			name:   "matching named socket",
			tmux:   "/private/tmp/tmux/isolated,1,0",
			socket: "isolated",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("TMUX", test.tmux)
			t.Setenv("TMUX_PANE", "%9")
			runner := &captureRunner{
				agentLine:       captureAgentLine(false),
				sourceSessionID: "$7",
			}
			runtime := &Runtime{runner: runner, socket: test.socket}

			result, err := runtime.Attach(context.Background(), "capture-id")
			if err != nil {
				t.Fatal(err)
			}
			if result.Command != nil {
				t.Fatalf("unexpected external command: %#v", result.Command.Args)
			}
			wantCalls := [][]string{
				{"list-panes", "-a", "-F", expectedPaneListFormat()},
				{"display-message", "-p", "-t", "%9", "#{session_id}"},
				{"list-keys", "-T", "prefix", "Q"},
				{"bind-key", "-T", "prefix", "-N", "Return from Runstead", "Q",
					"run-shell", "-C", returnBindingFormat},
			}
			wantCalls = append(wantCalls, prefixFeedbackCalls("runstead-agents")...)
			wantCalls = append(wantCalls,
				[]string{"set-option", "-w", "-t", "@1", "@runstead_return_target", "$7"},
				[]string{"switch-client", "-t", "@1"},
			)
			assertCalls(t, runner.calls, wantCalls)
		})
	}
}

func TestInsideTmuxServerRejectsDifferentOrMalformedSockets(t *testing.T) {
	t.Setenv("TMUX", "/private/tmp/tmux/current,1,0")
	if insideTmuxServer("other") {
		t.Fatal("different named socket matched the current tmux server")
	}

	t.Setenv("TMUX", "malformed")
	if insideTmuxServer("malformed") {
		t.Fatal("malformed TMUX value matched a named socket")
	}
}

func TestAttachRefusesToReplaceExistingReturnBinding(t *testing.T) {
	t.Setenv("TMUX", "")
	t.Setenv("TMUX_PANE", "")
	runner := &captureRunner{
		agentLine: captureAgentLine(false),
		binding:   "bind-key -T prefix Q display-panes",
	}
	runtime := &Runtime{runner: runner}

	_, err := runtime.Attach(context.Background(), "capture-id")
	if err == nil || !strings.Contains(err.Error(), "already bound") {
		t.Fatalf("error = %v", err)
	}
	assertCalls(t, runner.calls, [][]string{
		{"list-panes", "-a", "-F", expectedPaneListFormat()},
		{"list-keys", "-T", "prefix", "Q"},
		{"list-keys", "-N", "-T", "prefix", "Q"},
	})
}

func TestAttachRefreshesLegacyAgentmuxOwnedReturnBinding(t *testing.T) {
	t.Setenv("TMUX", "")
	t.Setenv("TMUX_PANE", "")
	runner := &captureRunner{
		agentLine:   captureAgentLine(false),
		binding:     "old agentmux binding",
		bindingNote: "Q Return from agentmux",
	}
	runtime := &Runtime{runner: runner}

	if _, err := runtime.Attach(context.Background(), "capture-id"); err != nil {
		t.Fatal(err)
	}
	wantCalls := [][]string{
		{"list-panes", "-a", "-F", expectedPaneListFormat()},
		{"list-keys", "-T", "prefix", "Q"},
		{"list-keys", "-N", "-T", "prefix", "Q"},
		{"bind-key", "-T", "prefix", "-N", "Return from Runstead", "Q",
			"run-shell", "-C", returnBindingFormat},
	}
	wantCalls = append(wantCalls, prefixFeedbackCalls("runstead-agents")...)
	wantCalls = append(wantCalls,
		[]string{"set-option", "-w", "-t", "@1", "@runstead_return_target", ""},
		[]string{"select-window", "-t", "@1"},
	)
	assertCalls(t, runner.calls, wantCalls)
}

func TestConfigurePrefixFeedbackReusesSavedBaseOptions(t *testing.T) {
	runner := &captureRunner{
		feedbackVersion:      "1",
		savedStatusLeftWidth: "48",
	}
	runtime := &Runtime{runner: runner}

	if err := runtime.configurePrefixFeedback(context.Background(), "custom-agents"); err != nil {
		t.Fatal(err)
	}
	assertCalls(t, runner.calls, [][]string{
		{"show-options", "-qv", "-t", "custom-agents", "@runstead_prefix_feedback"},
		{"show-options", "-qv", "-t", "custom-agents", "@runstead_status_left_length_base"},
		{"set-option", "-t", "custom-agents", "@runstead_status_style_prefix",
			"bg=#e5c07b,fg=#1f2328,bold"},
		{"set-option", "-t", "custom-agents", "@runstead_status_left_prefix",
			" PREFIX  [Q] return  [?] all keys "},
		{"set-option", "-t", "custom-agents", "status-style", dynamicStatusStyle},
		{"set-option", "-t", "custom-agents", "status-left", dynamicStatusLeft},
		{"set-option", "-t", "custom-agents", "status-left-length", "48"},
	})
}

func TestConfigurePrefixFeedbackMigratesLegacySavedBaseOptions(t *testing.T) {
	runner := &captureRunner{
		legacyFeedbackVersion: "1",
		legacyStatusStyle:     "fg=white,bg=black",
		legacyStatusLeft:      " legacy ",
		legacyStatusLeftWidth: "42",
	}
	runtime := &Runtime{runner: runner}

	if err := runtime.configurePrefixFeedback(context.Background(), "legacy-agents"); err != nil {
		t.Fatal(err)
	}
	assertCalls(t, runner.calls, [][]string{
		{"show-options", "-qv", "-t", "legacy-agents", "@runstead_prefix_feedback"},
		{"show-options", "-qv", "-t", "legacy-agents", "@agentmux_prefix_feedback"},
		{"show-options", "-qv", "-t", "legacy-agents", "@agentmux_status_style_base"},
		{"show-options", "-qv", "-t", "legacy-agents", "@agentmux_status_left_base"},
		{"show-options", "-qv", "-t", "legacy-agents", "@agentmux_status_left_length_base"},
		{"set-option", "-t", "legacy-agents", "@runstead_status_style_base",
			"fg=white,bg=black"},
		{"set-option", "-t", "legacy-agents", "@runstead_status_left_base", " legacy "},
		{"set-option", "-t", "legacy-agents", "@runstead_status_left_length_base", "42"},
		{"set-option", "-t", "legacy-agents", "@runstead_prefix_feedback", "1"},
		{"set-option", "-t", "legacy-agents", "@runstead_status_style_prefix",
			"bg=#e5c07b,fg=#1f2328,bold"},
		{"set-option", "-t", "legacy-agents", "@runstead_status_left_prefix",
			" PREFIX  [Q] return  [?] all keys "},
		{"set-option", "-t", "legacy-agents", "status-style", dynamicStatusStyle},
		{"set-option", "-t", "legacy-agents", "status-left", dynamicStatusLeft},
		{"set-option", "-t", "legacy-agents", "status-left-length", "42"},
	})
}

func TestUpdateMigratesLegacyWindowMetadata(t *testing.T) {
	runner := &captureRunner{agentLine: legacyCaptureAgentLine()}
	runtime := &Runtime{runner: runner}

	if err := runtime.Update(context.Background(), "legacy-id", session.Update{
		Activity: agent.ActivityIdle,
	}); err != nil {
		t.Fatal(err)
	}

	written := make(map[string]string)
	for _, call := range runner.calls {
		if len(call) == 6 && call[0] == "set-option" && call[1] == "-w" {
			written[call[4]] = call[5]
		}
	}
	if len(written) != metadataFieldCount {
		t.Fatalf("wrote %d metadata options: %#v", len(written), written)
	}
	if written["@runstead_id"] != "legacy-id" {
		t.Fatalf("runstead id = %q", written["@runstead_id"])
	}
	if written["@runstead_activity"] != string(agent.ActivityIdle) {
		t.Fatalf("activity = %q", written["@runstead_activity"])
	}
	if written["@runstead_attention"] != "" {
		t.Fatalf("attention was not cleared: %q", written["@runstead_attention"])
	}
	for key := range written {
		if strings.HasPrefix(key, "@agentmux_") {
			t.Fatalf("wrote legacy option %q", key)
		}
	}
}

func TestSetWorkspaceResolvesRuntimeHandleFromAgentID(t *testing.T) {
	runner := &captureRunner{agentLine: captureAgentLine(false)}
	runtime := &Runtime{runner: runner}

	if err := runtime.SetWorkspace(
		context.Background(),
		"capture-id",
		workspace.DirectoryContext("/workspace/project"),
	); err != nil {
		t.Fatal(err)
	}

	writes := 0
	for _, call := range runner.calls {
		if len(call) < 4 || call[0] != "set-option" || call[1] != "-w" {
			continue
		}
		writes++
		if call[3] != "@1" {
			t.Fatalf("workspace target = %q, want @1", call[3])
		}
	}
	if writes == 0 {
		t.Fatal("workspace metadata was not persisted")
	}
}

type captureRunner struct {
	agentLine             string
	sourceSessionID       string
	binding               string
	bindingNote           string
	feedbackVersion       string
	legacyFeedbackVersion string
	savedStatusLeftWidth  string
	legacyStatusStyle     string
	legacyStatusLeft      string
	legacyStatusLeftWidth string
	calls                 [][]string
}

func (r *captureRunner) Run(_ context.Context, _ []byte, args ...string) (string, error) {
	r.calls = append(r.calls, slices.Clone(args))
	if len(args) == 0 {
		return "", nil
	}
	switch args[0] {
	case "list-panes":
		return r.agentLine, nil
	case "display-message":
		switch args[len(args)-1] {
		case "|#{status-style}|":
			return "|fg=terminal,bg=terminal|", nil
		case "|#{status-left}|":
			return "| #[bold] #{session_name} |", nil
		case "|#{status-left-length}|":
			return "|30|", nil
		default:
			return r.sourceSessionID, nil
		}
	case "list-keys":
		if len(args) > 1 && args[1] == "-N" {
			if r.bindingNote != "" {
				return r.bindingNote, nil
			}
			return "", errors.New("tmux list-keys: unknown key: Q")
		}
		if r.binding != "" {
			return r.binding, nil
		}
		return "", errors.New("tmux list-keys: unknown key: Q")
	case "show-options":
		switch args[len(args)-1] {
		case prefixFeedbackOption:
			return r.feedbackVersion, nil
		case "@agentmux_prefix_feedback":
			return r.legacyFeedbackVersion, nil
		case "@agentmux_status_style_base":
			return r.legacyStatusStyle, nil
		case "@agentmux_status_left_base":
			return r.legacyStatusLeft, nil
		case "@agentmux_status_left_length_base":
			return r.legacyStatusLeftWidth, nil
		case statusLeftLengthOption:
			return r.savedStatusLeftWidth, nil
		default:
			return "", nil
		}
	}
	return "pane output", nil
}

func prefixFeedbackCalls(sessionName string) [][]string {
	return [][]string{
		{"show-options", "-qv", "-t", sessionName, "@runstead_prefix_feedback"},
		{"show-options", "-qv", "-t", sessionName, "@agentmux_prefix_feedback"},
		{"display-message", "-p", "-t", sessionName, "|#{status-style}|"},
		{"display-message", "-p", "-t", sessionName, "|#{status-left}|"},
		{"display-message", "-p", "-t", sessionName, "|#{status-left-length}|"},
		{"set-option", "-t", sessionName, "@runstead_status_style_base",
			"fg=terminal,bg=terminal"},
		{"set-option", "-t", sessionName, "@runstead_status_left_base",
			" #[bold] #{session_name} "},
		{"set-option", "-t", sessionName, "@runstead_status_left_length_base", "30"},
		{"set-option", "-t", sessionName, "@runstead_prefix_feedback", "1"},
		{"set-option", "-t", sessionName, "@runstead_status_style_prefix",
			"bg=#e5c07b,fg=#1f2328,bold"},
		{"set-option", "-t", sessionName, "@runstead_status_left_prefix",
			" PREFIX  [Q] return  [?] all keys "},
		{"set-option", "-t", sessionName, "status-style", dynamicStatusStyle},
		{"set-option", "-t", sessionName, "status-left", dynamicStatusLeft},
		{"set-option", "-t", sessionName, "status-left-length", "34"},
	}
}

func assertCalls(t *testing.T, got, want [][]string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("runner calls = %#v, want %#v", got, want)
	}
	for i := range want {
		if !slices.Equal(got[i], want[i]) {
			t.Fatalf("runner call %d = %#v, want %#v", i, got[i], want[i])
		}
	}
}

func expectedPaneListFormat() string {
	fields := []string{
		"#{session_name}",
		"#{window_id}",
		"#{window_index}",
		"#{window_name}",
		"#{pane_id}",
		"#{pane_current_command}",
		"#{pane_current_path}",
		"#{pane_title}",
		"#{pane_dead}",
		"#{pane_dead_status}",
	}
	for _, namespace := range []string{"runstead", "agentmux"} {
		for _, name := range agentMetadataFields {
			fields = append(fields, "#{@"+namespace+"_"+name+"}")
		}
	}
	return strings.Join(fields, fieldSeparator)
}

func captureAgentLine(dead bool) string {
	parts := make([]string, basePaneFieldCount+2*metadataFieldCount)
	parts[0] = "runstead-agents"
	parts[1] = "@1"
	parts[2] = "0"
	parts[3] = "cx-capture"
	parts[4] = "%1"
	parts[10] = "capture-id"
	parts[11] = "codex"
	parts[15] = "1700000000"
	parts[16] = "working"
	parts[18] = "%1"
	if dead {
		parts[8] = "1"
		parts[9] = "0"
	}
	return strings.Join(parts, fieldSeparator)
}

func legacyCaptureAgentLine() string {
	parts := make([]string, basePaneFieldCount+2*metadataFieldCount)
	legacy := basePaneFieldCount + metadataFieldCount
	parts[0] = "agentmux-agents"
	parts[1] = "@9"
	parts[2] = "1"
	parts[3] = "cl-legacy"
	parts[4] = "%9"
	parts[legacy] = "legacy-id"
	parts[legacy+1] = "claude"
	parts[legacy+2] = "Review the branch"
	parts[legacy+3] = "Waiting for approval"
	parts[legacy+4] = "/tmp/project"
	parts[legacy+5] = "1700000000"
	parts[legacy+6] = "working"
	parts[legacy+7] = "approval"
	parts[legacy+8] = "%9"
	parts[legacy+9] = "git:/tmp/project/.git"
	parts[legacy+10] = "git"
	parts[legacy+11] = "project"
	parts[legacy+12] = "/tmp/project"
	parts[legacy+13] = "/tmp/project"
	return strings.Join(parts, fieldSeparator)
}
