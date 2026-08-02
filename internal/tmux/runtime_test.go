package tmux

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/trentkm/stormlight/internal/agent"
	"github.com/trentkm/stormlight/internal/session"
	"github.com/trentkm/stormlight/internal/workspace"
)

func TestParseAgent(t *testing.T) {
	parts := []string{
		"stormlight-agents",
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
		"auto",
		"/tmp/claude/session.jsonl",
	}
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
	if managedAgent.Mode != agent.ModeAuto {
		t.Fatalf("mode = %q", managedAgent.Mode)
	}
	if managedAgent.TranscriptPath != "/tmp/claude/session.jsonl" {
		t.Fatalf("transcript path = %q", managedAgent.TranscriptPath)
	}
}

func TestParseAgentDerivesCompletedFromDeadPane(t *testing.T) {
	parts := make([]string, basePaneFieldCount+metadataFieldCount)
	parts[0] = "stormlight-agents"
	parts[1] = "@1"
	parts[2] = "0"
	parts[3] = "job"
	parts[4] = "%1"
	parts[8] = "1"
	parts[9] = "0"
	parts[10] = "id"
	parts[11] = "codex"
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
	parts := make([]string, basePaneFieldCount+metadataFieldCount)
	if _, ok := parseAgent(strings.Join(parts, fieldSeparator)); ok {
		t.Fatal("expected untagged pane to be skipped")
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

func TestCaptureIncludesScrollbackForLivePane(t *testing.T) {
	runner := &captureRunner{agentLine: captureAgentLine(false)}
	runtime := &Runtime{runner: runner}

	output, err := runtime.Capture(context.Background(), "capture-id", 120)
	if err != nil {
		t.Fatal(err)
	}
	if output != "pane output" {
		t.Fatalf("output = %q", output)
	}
	want := []string{"capture-pane", "-p", "-e", "-J", "-t", "%1", "-S", "-120"}
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
	want := []string{"capture-pane", "-p", "-e", "-J", "-t", "%1", "-S", "-40"}
	if len(runner.calls) != 2 || !slices.Equal(runner.calls[1], want) {
		t.Fatalf("capture call = %#v, want %#v", runner.calls, want)
	}
}

func TestCaptureNegativeBudgetTakesFullHistory(t *testing.T) {
	runner := &captureRunner{agentLine: captureAgentLine(false)}
	runtime := &Runtime{runner: runner}

	if _, err := runtime.Capture(context.Background(), "capture-id", -1); err != nil {
		t.Fatal(err)
	}
	want := []string{"capture-pane", "-p", "-e", "-J", "-t", "%1", "-S", "-"}
	if len(runner.calls) != 2 || !slices.Equal(runner.calls[1], want) {
		t.Fatalf("capture call = %#v, want %#v", runner.calls, want)
	}
}

func TestCaptureDefaultsLineBudget(t *testing.T) {
	runner := &captureRunner{agentLine: captureAgentLine(false)}
	runtime := &Runtime{runner: runner}

	if _, err := runtime.Capture(context.Background(), "capture-id", 0); err != nil {
		t.Fatal(err)
	}
	want := []string{"capture-pane", "-p", "-e", "-J", "-t", "%1", "-S", "-100"}
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
	want := []string{"tmux", "-L", "isolated", "attach-session", "-t", "stormlight-agents"}
	if !slices.Equal(result.Command.Args, want) {
		t.Fatalf("command = %#v, want %#v", result.Command.Args, want)
	}
	wantCalls := [][]string{
		{"list-panes", "-a", "-F", expectedPaneListFormat()},
		{"list-keys", "-T", "prefix"},
		{"bind-key", "-T", "prefix", "-N", "Return from Stormlight", "Q",
			"run-shell", "-C", returnBindingFormat},
	}
	wantCalls = append(wantCalls, rootReturnCalls()...)
	wantCalls = append(wantCalls, prefixFeedbackCalls("stormlight-agents")...)
	wantCalls = append(wantCalls,
		[]string{"set-option", "-w", "-t", "@1", "@stormlight_return_target", ""},
	)
	wantCalls = append(wantCalls, releaseWindowSizeCalls()...)
	wantCalls = append(wantCalls,
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
				{"list-keys", "-T", "prefix"},
				{"bind-key", "-T", "prefix", "-N", "Return from Stormlight", "Q",
					"run-shell", "-C", returnBindingFormat},
			}
			wantCalls = append(wantCalls, rootReturnCalls()...)
			wantCalls = append(wantCalls, prefixFeedbackCalls("stormlight-agents")...)
			wantCalls = append(wantCalls,
				[]string{"set-option", "-w", "-t", "@1", "@stormlight_return_target", "$7"},
			)
			wantCalls = append(wantCalls, releaseWindowSizeCalls()...)
			wantCalls = append(wantCalls,
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
		binding: "bind-key    -T prefix !       break-pane\n" +
			"bind-key    -T prefix Q       display-panes\n" +
			"bind-key -r -T prefix Up      select-pane -U",
	}
	runtime := &Runtime{runner: runner}

	_, err := runtime.Attach(context.Background(), "capture-id")
	if err == nil || !strings.Contains(err.Error(), "bound outside Stormlight") {
		t.Fatalf("error = %v", err)
	}
	assertCalls(t, runner.calls, [][]string{
		{"list-panes", "-a", "-F", expectedPaneListFormat()},
		{"list-keys", "-T", "prefix"},
	})
}

func TestAttachRefreshesStormlightOwnedReturnBinding(t *testing.T) {
	t.Setenv("TMUX", "")
	t.Setenv("TMUX_PANE", "")
	runner := &captureRunner{
		agentLine: captureAgentLine(false),
		binding: "bind-key    -T prefix Q       run-shell -C \"" +
			returnBindingFormat + "\"",
	}
	runtime := &Runtime{runner: runner}

	if _, err := runtime.Attach(context.Background(), "capture-id"); err != nil {
		t.Fatal(err)
	}
	wantCalls := [][]string{
		{"list-panes", "-a", "-F", expectedPaneListFormat()},
		{"list-keys", "-T", "prefix"},
		{"bind-key", "-T", "prefix", "-N", "Return from Stormlight", "Q",
			"run-shell", "-C", returnBindingFormat},
	}
	wantCalls = append(wantCalls, rootReturnCalls()...)
	wantCalls = append(wantCalls, prefixFeedbackCalls("stormlight-agents")...)
	wantCalls = append(wantCalls,
		[]string{"set-option", "-w", "-t", "@1", "@stormlight_return_target", ""},
	)
	wantCalls = append(wantCalls, releaseWindowSizeCalls()...)
	wantCalls = append(wantCalls,
		[]string{"select-window", "-t", "@1"},
	)
	assertCalls(t, runner.calls, wantCalls)
}

func TestAttachSkipsForeignRootBindingsWithoutFailing(t *testing.T) {
	t.Setenv("TMUX", "")
	t.Setenv("TMUX_PANE", "")
	runner := &captureRunner{
		agentLine:   captureAgentLine(false),
		rootBinding: "bind-key    -T root C-6       select-window -n",
	}
	runtime := &Runtime{runner: runner}

	if _, err := runtime.Attach(context.Background(), "capture-id"); err != nil {
		t.Fatal(err)
	}
	var boundKeys []string
	for _, call := range runner.calls {
		if len(call) > 5 && call[0] == "bind-key" && call[2] == "root" {
			boundKeys = append(boundKeys, call[5])
		}
	}
	want := []string{"C-^", returnMouseKey}
	if !slices.Equal(boundKeys, want) {
		t.Fatalf("root keys bound = %#v, want %#v", boundKeys, want)
	}
}

func TestConfigurePrefixFeedbackSkipsCurrentVersion(t *testing.T) {
	runner := &captureRunner{feedbackVersion: prefixFeedbackVersion}
	runtime := &Runtime{runner: runner}

	if err := runtime.configurePrefixFeedback(context.Background(), "custom-agents"); err != nil {
		t.Fatal(err)
	}
	assertCalls(t, runner.calls, [][]string{
		{"show-options", "-qv", "-t", "custom-agents", "@stormlight_prefix_feedback"},
	})
}

func TestUpdateRestoresMissingStormlightWindowMetadata(t *testing.T) {
	runner := &captureRunner{agentLine: captureAgentLine(false)}
	runtime := &Runtime{runner: runner}

	if err := runtime.Update(context.Background(), "capture-id", session.Update{
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
	if written["@stormlight_id"] != "capture-id" {
		t.Fatalf("stormlight id = %q", written["@stormlight_id"])
	}
	if written["@stormlight_activity"] != string(agent.ActivityIdle) {
		t.Fatalf("activity = %q", written["@stormlight_activity"])
	}
	if written["@stormlight_attention"] != "" {
		t.Fatalf("attention was not cleared: %q", written["@stormlight_attention"])
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

func TestApplyServerOptionsAssertsPassthroughOnLiveServer(t *testing.T) {
	runner := &captureRunner{}
	runtime, err := NewRuntime(runner, "stormlight-agents")
	if err != nil {
		t.Fatal(err)
	}
	if err := runtime.ApplyServerOptions(context.Background()); err != nil {
		t.Fatal(err)
	}
	want := []string{"set-option", "-g", "allow-passthrough", "all"}
	found := false
	for _, call := range runner.calls {
		if slices.Equal(call, want) {
			found = true
		}
	}
	if !found {
		t.Fatalf("allow-passthrough was not asserted: %#v", runner.calls)
	}
}

func TestSyncWindowSizesResizesDetachedWindows(t *testing.T) {
	runner := &captureRunner{}
	runtime := &Runtime{runner: runner, sessionName: "stormlight-agents"}

	if err := runtime.SyncWindowSizes(context.Background(), 76, 120); err != nil {
		t.Fatal(err)
	}
	want := [][]string{
		{"list-clients", "-t", "stormlight-agents", "-F", "#{client_name}"},
		{"list-windows", "-t", "stormlight-agents", "-F", "#{window_id}"},
		{"resize-window", "-t", "@1", "-x", "76", "-y", "120"},
	}
	assertCalls(t, runner.calls, want)
}

func TestSyncWindowSizesLeavesAttachedSessionsAlone(t *testing.T) {
	runner := &captureRunner{clientLine: "/dev/ttys001"}
	runtime := &Runtime{runner: runner, sessionName: "stormlight-agents"}

	if err := runtime.SyncWindowSizes(context.Background(), 76, 120); err != nil {
		t.Fatal(err)
	}
	for _, call := range runner.calls {
		if call[0] == "resize-window" {
			t.Fatalf("resized under an attached client: %#v", runner.calls)
		}
	}
}

// releaseWindowSizeCalls is Attach handing manually-sized windows back to
// client-driven sizing before a client goes to look at them.
func releaseWindowSizeCalls() [][]string {
	return [][]string{
		{"list-windows", "-t", "", "-F", "#{window_id}"},
		{"set-option", "-w", "-t", "@1", "window-size", "latest"},
	}
}

type captureRunner struct {
	agentLine       string
	sourceSessionID string
	binding         string
	rootBinding     string
	feedbackVersion string
	clientLine      string
	calls           [][]string
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
		if slices.Contains(args, "root") {
			return r.rootBinding, nil
		}
		return r.binding, nil
	case "list-windows":
		return "@1", nil
	case "list-clients":
		return r.clientLine, nil
	case "show-options":
		if args[len(args)-1] == prefixFeedbackOption {
			return r.feedbackVersion, nil
		}
		return "", nil
	}
	return "pane output", nil
}

func prefixFeedbackCalls(sessionName string) [][]string {
	return [][]string{
		{"show-options", "-qv", "-t", sessionName, "@stormlight_prefix_feedback"},
		{"set-option", "-t", sessionName, "@stormlight_status_style_base",
			baseStatusStyle},
		{"set-option", "-t", sessionName, "@stormlight_status_left_base",
			baseStatusLeft},
		{"set-option", "-t", sessionName, "@stormlight_status_style_prefix",
			"bg=#e5c07b,fg=#1f2328,bold"},
		{"set-option", "-t", sessionName, "@stormlight_status_left_prefix",
			" PREFIX  [Q] return  [?] all keys "},
		{"set-option", "-t", sessionName, "@stormlight_prefix_feedback",
			prefixFeedbackVersion},
		{"set-option", "-t", sessionName, "status-style", dynamicStatusStyle},
		{"set-option", "-t", sessionName, "status-left", dynamicStatusLeft},
		{"set-option", "-t", sessionName, "status-left-length", "40"},
		{"set-option", "-t", sessionName, "status-right", (&Runtime{}).statusRightHint()},
		{"set-option", "-t", sessionName, "status-right-length",
			strconv.Itoa(len((&Runtime{}).statusRightHint()))},
	}
}

func rootReturnCalls() [][]string {
	calls := [][]string{{"list-keys", "-T", "root"}}
	for _, binding := range []struct{ key, passthrough string }{
		{"C-6", "send-keys C-6"},
		{"C-^", "send-keys C-^"},
		{returnMouseKey, "send-keys -M"},
	} {
		format := `#{?#{==:#{@stormlight_id},},` +
			binding.passthrough + `,` + returnAction + `}`
		calls = append(calls, []string{
			"bind-key", "-T", "root", "-N", returnBindingNote,
			binding.key, "run-shell", "-C", format,
		})
	}
	return calls
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
	for _, name := range agentMetadataFields {
		fields = append(fields, "#{@stormlight_"+name+"}")
	}
	return strings.Join(fields, fieldSeparator)
}

func captureAgentLine(dead bool) string {
	parts := make([]string, basePaneFieldCount+metadataFieldCount)
	parts[0] = "stormlight-agents"
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

func TestTableBindingFindsKeyInTableListing(t *testing.T) {
	listing := "bind-key    -T prefix !       break-pane\n" +
		"bind-key -r -T prefix Up      select-pane -U\n" +
		"bind-key    -T prefix Q       run-shell -C \"#{?…}\"\n" +
		"bind-key    -T root   Q       send-keys Q"

	binding, bound := tableBinding(listing, "prefix", "Q")
	if !bound || !strings.Contains(binding, "run-shell") {
		t.Fatalf("binding = %q, bound = %v", binding, bound)
	}
	if _, bound := tableBinding(listing, "prefix", "Z"); bound {
		t.Fatal("unbound key reported as bound")
	}
	if binding, bound := tableBinding(listing, "prefix", "Up"); !bound ||
		!strings.Contains(binding, "select-pane") {
		t.Fatalf("repeat-flag binding = %q, bound = %v", binding, bound)
	}
	if binding, bound := tableBinding(listing, "root", "Q"); !bound ||
		!strings.Contains(binding, "send-keys") {
		t.Fatalf("root binding = %q, bound = %v", binding, bound)
	}
	if _, bound := tableBinding("", "prefix", "Q"); bound {
		t.Fatal("empty listing reported a binding")
	}
}

func TestIsNoServerError(t *testing.T) {
	cases := []struct {
		message string
		matches bool
	}{
		{"tmux list-panes: no server running on /tmp/tmux-501/default", true},
		{"tmux list-panes: error connecting to /private/tmp/tmux-501/default (No such file or directory)", true},
		{"tmux: failed to connect to server", true},
		{"no sessions", true},
		{"tmux list-panes: unknown option", false},
	}
	for _, tc := range cases {
		if got := isNoServerError(errors.New(tc.message)); got != tc.matches {
			t.Errorf("isNoServerError(%q) = %v, want %v", tc.message, got, tc.matches)
		}
	}
}

func TestSendTypesSlashCommandsAsLiteralKeys(t *testing.T) {
	runner := &captureRunner{agentLine: captureAgentLine(false)}
	runtime := &Runtime{runner: runner}

	if err := runtime.Send(context.Background(), "capture-id", "/compact"); err != nil {
		t.Fatal(err)
	}
	wantCalls := [][]string{
		{"list-panes", "-a", "-F", expectedPaneListFormat()},
		{"send-keys", "-t", "%1", "-l", "--", "/compact"},
		{"send-keys", "-t", "%1", "Enter"},
	}
	assertCalls(t, runner.calls[:len(wantCalls)], wantCalls)
}

func TestSendPastesRegularAndMultilineMessages(t *testing.T) {
	for _, message := range []string{"fix the parser", "/compact\nthen continue"} {
		runner := &captureRunner{agentLine: captureAgentLine(false)}
		runtime := &Runtime{runner: runner}

		if err := runtime.Send(context.Background(), "capture-id", message); err != nil {
			t.Fatal(err)
		}
		wantCalls := [][]string{
			{"list-panes", "-a", "-F", expectedPaneListFormat()},
			{"load-buffer", "-b", "stormlight-capture-id", "-"},
			{"paste-buffer", "-d", "-b", "stormlight-capture-id", "-t", "%1"},
			{"send-keys", "-t", "%1", "Enter"},
		}
		assertCalls(t, runner.calls[:len(wantCalls)], wantCalls)
	}
}

func TestUpdateNeverDowngradesUrgentAttentionToWaiting(t *testing.T) {
	line := captureAgentLine(false)
	parts := strings.Split(line, fieldSeparator)
	parts[17] = "approval"
	runner := &captureRunner{agentLine: strings.Join(parts, fieldSeparator)}
	runtime := &Runtime{runner: runner}

	err := runtime.Update(context.Background(), "capture-id", session.Update{
		Activity:  agent.ActivityIdle,
		Attention: agent.AttentionWaiting,
	})
	if err != nil {
		t.Fatal(err)
	}
	kept, demoted := false, false
	for _, call := range runner.calls {
		if len(call) < 2 || call[0] != "set-option" {
			continue
		}
		key, value := call[len(call)-2], call[len(call)-1]
		if key == "@stormlight_attention" {
			if value == "approval" {
				kept = true
			}
			if value == "waiting" {
				demoted = true
			}
		}
	}
	if !kept || demoted {
		t.Fatalf("attention writes kept=%v demoted=%v:\n%#v", kept, demoted, runner.calls)
	}
}

func TestUpdateTurnEndDowngradesResolvedApproval(t *testing.T) {
	line := captureAgentLine(false)
	parts := strings.Split(line, fieldSeparator)
	parts[17] = "approval"
	runner := &captureRunner{agentLine: strings.Join(parts, fieldSeparator)}
	runtime := &Runtime{runner: runner}

	err := runtime.Update(context.Background(), "capture-id", session.Update{
		Activity:  agent.ActivityIdle,
		Attention: agent.AttentionWaiting,
		TurnEnded: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, call := range runner.calls {
		if len(call) >= 2 && call[0] == "set-option" &&
			call[len(call)-2] == "@stormlight_attention" {
			if value := call[len(call)-1]; value != "waiting" {
				t.Fatalf("turn end kept attention %q", value)
			}
			return
		}
	}
	t.Fatal("attention was never written")
}
