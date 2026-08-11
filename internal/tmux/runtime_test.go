package tmux

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

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
		"attention",
		"1700000600",
		"019b9a7e-9846-7da2-9041-d4cec65d4d1d",
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
	if managedAgent.SessionID != "019b9a7e-9846-7da2-9041-d4cec65d4d1d" {
		t.Fatalf("session id = %q", managedAgent.SessionID)
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
	if managedAgent.Mark != agent.MarkAttention {
		t.Fatalf("mark = %q", managedAgent.Mark)
	}
	if managedAgent.AttentionAt.Unix() != 1700000600 {
		t.Fatalf("attention stamp = %v", managedAgent.AttentionAt)
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

// A user-supplied name becomes the tmux window name; only the derived slug
// falls back to the task.
func TestDispatchNamesTheWindow(t *testing.T) {
	for _, testCase := range []struct {
		name string
		want string
	}{
		{name: "", want: "cl-fix-the-oauth-callback"},
		{name: "  oauth   review\n", want: "oauth review"},
	} {
		runner := &dispatchRunner{}
		runtime := &Runtime{runner: runner, sessionName: "stormlight"}
		if _, err := runtime.Dispatch(context.Background(), session.DispatchRequest{
			Provider: agent.ProviderClaude,
			Name:     testCase.name,
			Task:     "Fix the OAuth callback",
			Cwd:      t.TempDir(),
			Launch:   session.Launch{Path: "/usr/bin/claude"},
		}); err != nil {
			t.Fatal(err)
		}
		if runner.windowName != testCase.want {
			t.Fatalf("window name for %q = %q, want %q",
				testCase.name, runner.windowName, testCase.want)
		}
	}
}

// The path resolved when the dashboard started names a file that can be
// deleted while it is still running — tearing down a finished worktree does
// exactly that to a development build. Writing it into the window command
// anyway leaves the pane holding a shell error and no agent (#90).
func TestDispatchReresolvesADeletedExecutable(t *testing.T) {
	deleted := filepath.Join(t.TempDir(), "worktree", "stormlight")
	runner := &dispatchRunner{}
	runtime := &Runtime{
		runner:      runner,
		sessionName: "stormlight",
		executable:  deleted,
	}

	if _, err := runtime.Dispatch(context.Background(), session.DispatchRequest{
		Provider: agent.ProviderClaude,
		Task:     "Fix the OAuth callback",
		Cwd:      t.TempDir(),
		Launch:   session.Launch{Path: "/usr/bin/claude"},
	}); err != nil {
		t.Fatal(err)
	}

	if runner.paneCommand == "" {
		t.Fatal("no command was given to the pane")
	}
	if strings.Contains(runner.paneCommand, deleted) {
		t.Fatalf("pane command still names the deleted binary: %q", runner.paneCommand)
	}
	// The re-resolution is cached, so a second dispatch does not pay for it
	// and the runtime no longer holds a path nothing can start.
	if runtime.executable == deleted {
		t.Fatal("runtime kept the deleted path")
	}
}

// dispatchRunner answers just enough tmux for Dispatch and records the name
// the new window was created with and the command its pane was given.
type dispatchRunner struct {
	windowName  string
	paneCommand string
}

func (r *dispatchRunner) Run(_ context.Context, _ []byte, args ...string) (string, error) {
	if len(args) == 0 {
		return "", nil
	}
	switch args[0] {
	case "show-options":
		return "1", nil
	case "new-window", "new-session":
		if index := slices.Index(args, "-n"); index >= 0 && index+1 < len(args) {
			r.windowName = args[index+1]
		}
		return "@7" + fieldSeparator + "%7", nil
	case "respawn-pane":
		r.paneCommand = args[len(args)-1]
	}
	return "", nil
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
	wantCalls = append(wantCalls, nextPrefixCalls(runtime)...)
	wantCalls = append(wantCalls, rootReturnCalls(runtime)...)
	wantCalls = append(wantCalls, statusBarCalls("stormlight-agents")...)
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
			wantCalls = append(wantCalls, nextPrefixCalls(runtime)...)
			wantCalls = append(wantCalls, rootReturnCalls(runtime)...)
			wantCalls = append(wantCalls, statusBarCalls("stormlight-agents")...)
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
	wantCalls = append(wantCalls, nextPrefixCalls(runtime)...)
	wantCalls = append(wantCalls, rootReturnCalls(runtime)...)
	wantCalls = append(wantCalls, statusBarCalls("stormlight-agents")...)
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
	want := []string{"C-^", returnMouseKey, "C-]", `C-\`, queueMouseKey}
	if !slices.Equal(boundKeys, want) {
		t.Fatalf("root keys bound = %#v, want %#v", boundKeys, want)
	}
}

// tmux ends a #{?...} branch at the first unescaped comma, and a comma inside
// a #[...] style tag counts: leave one raw and the branch stops mid-style,
// taking the rest of the status bar with it. The failure is silent — tmux
// renders a truncated bar rather than complaining — so the rule is enforced
// here instead of being discovered on someone's screen.
func TestStatusFormatsEscapeStyleCommas(t *testing.T) {
	for name, format := range map[string]string{
		// The summary is a conditional now, so an unescaped comma in a
		// style tag inside it ends the branch and takes the rest of the bar
		// with it — the loud tier's #,bold is exactly that shape.
		"statusSummary": (&Runtime{}).statusSummary(agent.Stats{
			Working: 1, Waiting: 1, Urgent: 1, Idle: 1,
		}),
		"statusRight":            (&Runtime{}).statusRight(),
		"agentStatusLineage":     agentStatusLineage,
		"statusWorkspaceSegment": statusWorkspaceSegment,
		"statusTailSegment":      statusTailSegment,
		"baseStatusLeft":         baseStatusLeft,
		"dynamicStatusLeft":      dynamicStatusLeft,
		"dynamicStatusStyle":     dynamicStatusStyle,
		"statusFormat":           statusFormat,
	} {
		for _, tag := range styleTags(format) {
			for index, char := range tag {
				if char == ',' && (index == 0 || tag[index-1] != '#') {
					t.Fatalf("%s: unescaped comma in style tag %q", name, tag)
				}
			}
		}
	}
}

// styleTags returns every #[...] span in a tmux format.
func styleTags(format string) []string {
	var tags []string
	for index := 0; index+1 < len(format); index++ {
		if format[index] != '#' || format[index+1] != '[' {
			continue
		}
		end := strings.IndexByte(format[index:], ']')
		if end < 0 {
			continue
		}
		tags = append(tags, format[index:index+end+1])
	}
	return tags
}

func TestConfigureStatusBarSkipsCurrentVersion(t *testing.T) {
	runner := &captureRunner{feedbackVersion: statusVersion}
	runtime := &Runtime{runner: runner}

	if err := runtime.configureStatusBar(context.Background(), "custom-agents"); err != nil {
		t.Fatal(err)
	}
	assertCalls(t, runner.calls, [][]string{
		{"show-options", "-qv", "-t", "custom-agents", statusVersionOption},
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
	// Every field the pane listing reads back has to be restored; the record
	// is only whole if the window carries all of them. Options written for
	// display alone — the status bar's lineage tail — may ride along.
	for _, field := range agentMetadataFields {
		if _, ok := written["@stormlight_"+field]; !ok {
			t.Fatalf("metadata option %q was not restored: %#v", field, written)
		}
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

// The queue's order is when each agent started waiting, so the stamp is
// written on the way into the amber inbox and cleared on the way out — and
// left alone by everything in between, or a summary arriving mid-wait would
// send the agent to the back of a line it never left.
func TestUpdateStampsEntryAndExitFromTheAttentionQueue(t *testing.T) {
	stamped := func(pane string, update session.Update) (string, bool) {
		runner := &captureRunner{
			agentLine:    pane,
			stormlightID: "capture-id",
		}
		runtime := &Runtime{runner: runner}
		if err := runtime.Update(context.Background(), "capture-id", update); err != nil {
			t.Fatal(err)
		}
		for _, call := range runner.calls {
			if len(call) == 6 && call[0] == "set-option" &&
				call[4] == "@stormlight_attention_at" {
				return call[5], true
			}
		}
		return "", false
	}

	value, written := stamped(captureAgentLine(false), session.Update{
		Attention: agent.AttentionWaiting,
	})
	if !written || value == "" {
		t.Fatalf("entry was not stamped: %q (%v)", value, written)
	}

	waiting := waitingAgentLine("1700000600")
	if value, written := stamped(waiting, session.Update{
		Summary: "still going",
	}); written {
		t.Fatalf("a summary restamped a waiting agent: %q", value)
	}
	if value, written := stamped(waiting, session.Update{
		Attention: agent.AttentionQuestion,
	}); written {
		t.Fatalf("an escalation restamped a waiting agent: %q", value)
	}
	if value, written := stamped(waiting, session.Update{
		ClearAttention: true,
	}); !written || value != "" {
		t.Fatalf("exit stamp = %q (%v)", value, written)
	}
}

// waitingAgentLine is the capture agent already in the amber inbox.
func waitingAgentLine(attentionAt string) string {
	parts := strings.Split(captureAgentLine(false), fieldSeparator)
	parts[17] = string(agent.AttentionWaiting)
	parts[30] = attentionAt
	return strings.Join(parts, fieldSeparator)
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
	// "on" and not "all": under "all" a pane the client is not showing may
	// write straight to the terminal.
	want := []string{"set-option", "-g", "allow-passthrough", "on"}
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

func TestApplyServerOptionsAppendsSyncOnLiveServer(t *testing.T) {
	runner := &captureRunner{
		terminalFeatures: "terminal-features[0] xterm*:clipboard:cstyle\n" +
			"terminal-features[1] *:RGB\n",
	}
	runtime, err := NewRuntime(runner, "stormlight-agents")
	if err != nil {
		t.Fatal(err)
	}
	if err := runtime.ApplyServerOptions(context.Background()); err != nil {
		t.Fatal(err)
	}
	want := []string{"set-option", "-sa", "terminal-features", ",*:sync"}
	found := false
	for _, call := range runner.calls {
		if slices.Equal(call, want) {
			found = true
		}
	}
	if !found {
		t.Fatalf("sync feature was not appended: %#v", runner.calls)
	}
}

// A server that already carries the feature is left alone: the option
// appends, so re-asserting it on every dashboard launch would grow the array
// for as long as the server lives.
func TestApplyServerOptionsLeavesDeclaredFeaturesAlone(t *testing.T) {
	runner := &captureRunner{
		terminalFeatures: "terminal-features[0] xterm*:clipboard\n" +
			"terminal-features[1] *:sync\n",
	}
	runtime, err := NewRuntime(runner, "stormlight-agents")
	if err != nil {
		t.Fatal(err)
	}
	if err := runtime.ApplyServerOptions(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, call := range runner.calls {
		if slices.Contains(call, "terminal-features") &&
			slices.Contains(call, "set-option") {
			t.Fatalf("terminal-features was appended twice: %#v", call)
		}
	}
}

// A pattern that ends in the feature is not the feature: "xterm*:sync"
// promises synchronized output to xterm and nothing else, so a client on
// tmux-256color still needs the entry Stormlight declares.
func TestApplyServerOptionsMatchesWholeFeatureEntries(t *testing.T) {
	runner := &captureRunner{
		terminalFeatures: "terminal-features[0] xterm*:sync\n",
	}
	runtime, err := NewRuntime(runner, "stormlight-agents")
	if err != nil {
		t.Fatal(err)
	}
	if err := runtime.ApplyServerOptions(context.Background()); err != nil {
		t.Fatal(err)
	}
	want := []string{"set-option", "-sa", "terminal-features", ",*:sync"}
	found := false
	for _, call := range runner.calls {
		if slices.Equal(call, want) {
			found = true
		}
	}
	if !found {
		t.Fatalf("xterm*:sync was read as *:sync: %#v", runner.calls)
	}
}

func TestSyncWindowSizesResizesDetachedWindows(t *testing.T) {
	runner := &captureRunner{}
	runtime := &Runtime{runner: runner, sessionName: "stormlight-agents"}

	if err := runtime.SyncWindowSizes(context.Background(), 76, 120, ""); err != nil {
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

	if err := runtime.SyncWindowSizes(context.Background(), 76, 120, ""); err != nil {
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
	agentLine string
	// stormlightID is what show-options reports for @stormlight_id; empty
	// means the window has lost its metadata and Update restores the record.
	stormlightID    string
	sourceSessionID string
	binding         string
	rootBinding     string
	feedbackVersion string
	clientLine      string
	// terminalFeatures is what show-options reports for the server's
	// terminal-features array, one `terminal-features[N] value` per line.
	terminalFeatures string
	calls            [][]string
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
		switch args[len(args)-1] {
		case statusVersionOption:
			return r.feedbackVersion, nil
		case "@stormlight_id":
			return r.stormlightID, nil
		case "terminal-features":
			return r.terminalFeatures, nil
		}
		return "", nil
	}
	return "pane output", nil
}

func statusBarCalls(sessionName string) [][]string {
	return [][]string{
		{"show-options", "-qv", "-t", sessionName, statusVersionOption},
		{"set-option", "-t", sessionName, "@stormlight_status_style_base",
			baseStatusStyle},
		{"set-option", "-t", sessionName, "@stormlight_status_left_base",
			baseStatusLeft},
		{"set-option", "-t", sessionName, "@stormlight_status_style_prefix",
			"bg=#e5c07b,fg=#1f2328,bold"},
		{"set-option", "-t", sessionName, "@stormlight_status_left_prefix",
			prefixStatusLeft},
		{"set-option", "-t", sessionName, statusVersionOption, statusVersion},
		{"set-option", "-t", sessionName, "status-style", dynamicStatusStyle},
		{"set-option", "-t", sessionName, "status-left", dynamicStatusLeft},
		{"set-option", "-t", sessionName, "status-left-length",
			strconv.Itoa(baseStatusLeftLength)},
		{"set-option", "-t", sessionName, statusWidthOption,
			strconv.Itoa((&Runtime{}).statusRightWidth(""))},
		{"set-option", "-t", sessionName, "status-right", (&Runtime{}).statusRight()},
		{"set-option", "-t", sessionName, "status-right-length",
			strconv.Itoa((&Runtime{}).statusRightWidth(""))},
		{"set-option", "-t", sessionName, "status-format[0]", statusFormat},
	}
}

func rootReturnCalls(runtime *Runtime) [][]string {
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
	calls = append(calls, rootNextCalls(runtime)...)
	return append(calls, []string{
		"bind-key", "-T", "root", "-N", nextBindingNote,
		queueMouseKey, "run-shell", "-C", runtime.queueMouseFormat(),
	})
}

// rootNextCalls is the single-press queue key, installed alongside the
// return keys off the same root-table listing.
func rootNextCalls(runtime *Runtime) [][]string {
	calls := make([][]string, 0, 2)
	for _, direction := range runtime.queueDirections() {
		key := direction.rootKeys[0]
		calls = append(calls, []string{
			"bind-key", "-T", "root", "-N", direction.note, key,
			"if-shell", "-F", `#{==:#{@stormlight_id},}`,
			"send-keys " + key, runtime.nextTmuxCommand(direction.step),
		})
	}
	return calls
}

// nextPrefixCall is the prefix-table queue key, installed off the same
// prefix listing the return key is checked against.
func nextPrefixCalls(runtime *Runtime) [][]string {
	calls := make([][]string, 0, 2)
	for _, direction := range runtime.queueDirections() {
		calls = append(calls, []string{
			"bind-key", "-T", "prefix", "-N", direction.note,
			direction.prefixKey, "run-shell", "-b",
			runtime.nextShellCommand(direction.step),
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
		"bind-key    -T root   C-\\\\    send-keys C-\\\\\n" +
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
	// tmux doubles a backslash in the listing so the line can be pasted back
	// in; a raw comparison misses it and reads as nobody owning the key.
	if _, bound := tableBinding(listing, "root", `C-\`); !bound {
		t.Fatal("escaped backslash key was not recognised")
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

// markWrites collects the @stormlight_mark values an Update wrote.
func markWrites(calls [][]string) []string {
	var values []string
	for _, call := range calls {
		if len(call) >= 2 && call[0] == "set-option" &&
			call[len(call)-2] == "@stormlight_mark" {
			values = append(values, call[len(call)-1])
		}
	}
	return values
}

// markedAgentRunner answers for a window that still has its metadata, so an
// Update writes only what its policy decided rather than restoring the record.
func markedAgentRunner(mark agent.Mark) *captureRunner {
	parts := strings.Split(captureAgentLine(false), fieldSeparator)
	parts[basePaneFieldCount+19] = string(mark)
	return &captureRunner{
		agentLine:    strings.Join(parts, fieldSeparator),
		stormlightID: "capture-id",
	}
}

func TestUpdateStoresAndClearsMarks(t *testing.T) {
	runner := markedAgentRunner(agent.MarkNone)
	runtime := &Runtime{runner: runner}

	if err := runtime.Update(context.Background(), "capture-id", session.Update{
		Mark: agent.MarkAttention,
	}); err != nil {
		t.Fatal(err)
	}
	if got := markWrites(runner.calls); len(got) != 1 || got[0] != "attention" {
		t.Fatalf("mark writes = %v", got)
	}

	runner = markedAgentRunner(agent.MarkAttention)
	runtime = &Runtime{runner: runner}
	if err := runtime.Update(context.Background(), "capture-id", session.Update{
		ClearMark: true,
	}); err != nil {
		t.Fatal(err)
	}
	if got := markWrites(runner.calls); len(got) != 1 || got[0] != "" {
		t.Fatalf("clear writes = %v", got)
	}
}

func TestUpdateRetiresWorkingMarkOnProviderSignal(t *testing.T) {
	runner := markedAgentRunner(agent.MarkWorking)
	runtime := &Runtime{runner: runner}

	err := runtime.Update(context.Background(), "capture-id", session.Update{
		Activity:  agent.ActivityIdle,
		Attention: agent.AttentionWaiting,
		TurnEnded: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := markWrites(runner.calls); len(got) != 1 || got[0] != "" {
		t.Fatalf("mark writes = %v, want the in-progress mark retired", got)
	}
}

func TestUpdateKeepsAttentionMarkThroughProviderSignals(t *testing.T) {
	runner := markedAgentRunner(agent.MarkAttention)
	runtime := &Runtime{runner: runner}

	err := runtime.Update(context.Background(), "capture-id", session.Update{
		Activity:  agent.ActivityWorking,
		Attention: agent.AttentionNone,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := markWrites(runner.calls); len(got) != 0 {
		t.Fatalf("mark writes = %v, want the human's mark untouched", got)
	}
}

func TestClearAttentionTakesDownTheAttentionMarkOnly(t *testing.T) {
	runner := markedAgentRunner(agent.MarkAttention)
	runtime := &Runtime{runner: runner}
	if err := runtime.Update(context.Background(), "capture-id", session.Update{
		ClearAttention: true,
	}); err != nil {
		t.Fatal(err)
	}
	if got := markWrites(runner.calls); len(got) != 1 || got[0] != "" {
		t.Fatalf("mark writes = %v, want the attention mark cleared", got)
	}

	runner = markedAgentRunner(agent.MarkWorking)
	runtime = &Runtime{runner: runner}
	if err := runtime.Update(context.Background(), "capture-id", session.Update{
		ClearAttention: true,
	}); err != nil {
		t.Fatal(err)
	}
	if got := markWrites(runner.calls); len(got) != 0 {
		t.Fatalf("mark writes = %v, want the in-progress mark kept", got)
	}
}

// restoreRunner records the tmux commands a restore issues, and can be told
// whether the managed session exists.
type restoreRunner struct {
	sessionExists bool
	options       map[string]string
	respawned     string
	calls         [][]string
}

func (r *restoreRunner) Run(_ context.Context, _ []byte, args ...string) (string, error) {
	r.calls = append(r.calls, slices.Clone(args))
	if len(args) == 0 {
		return "", nil
	}
	switch args[0] {
	case "has-session":
		if r.sessionExists {
			return "", nil
		}
		return "", fmt.Errorf("can't find session: stormlight-agents")
	case "new-window", "new-session":
		return "@7" + fieldSeparator + "%7", nil
	case "show-options":
		return "1", nil
	case "set-option":
		if len(args) >= 6 && args[1] == "-w" {
			if r.options == nil {
				r.options = map[string]string{}
			}
			r.options[args[4]] = args[5]
		}
	case "respawn-pane":
		r.respawned = args[len(args)-1]
	}
	return "", nil
}

func TestRosterLiveDistinguishesALostSessionFromAnEmptyOne(t *testing.T) {
	runner := &restoreRunner{}
	runtime, err := NewRuntime(runner, "stormlight-agents")
	if err != nil {
		t.Fatal(err)
	}
	// A session tmux has never heard of is not an empty roster — it is a
	// roster that was lost, and nothing may overwrite the record from it.
	live, err := runtime.RosterLive(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if live {
		t.Fatal("a missing session reported a live roster")
	}
	runner.sessionExists = true
	live, err = runtime.RosterLive(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !live {
		t.Fatal("an existing session reported a dead roster")
	}
}

func TestRestoreWritesTheRememberedMetadataOntoTheNewWindow(t *testing.T) {
	runner := &restoreRunner{sessionExists: true}
	runtime, err := NewRuntime(runner, "stormlight-agents")
	if err != nil {
		t.Fatal(err)
	}
	createdAt := time.Unix(1700000000, 0).UTC()
	attentionAt := time.Unix(1700000100, 0).UTC()
	restored, err := runtime.Restore(context.Background(), session.RestoreRequest{
		Agent: agent.Agent{
			ID:             "a1b2c3d4",
			Provider:       agent.ProviderClaude,
			Name:           "cl-old-name",
			Task:           "the original task",
			Summary:        "where it left off",
			Cwd:            t.TempDir(),
			CreatedAt:      createdAt,
			Attention:      agent.AttentionQuestion,
			AttentionAt:    attentionAt,
			Mode:           agent.ModeAuto,
			TranscriptPath: "/transcripts/a1.jsonl",
		},
		Launch: session.Launch{Path: "/usr/bin/claude", Args: []string{"--resume=a1"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if restored.ID != "a1b2c3d4" {
		t.Fatalf("restore invented an id: %q", restored.ID)
	}
	// Continuity is the whole promise: the same agent, not a new one wearing
	// its name. Everything the dashboard keys off has to come from the record.
	want := map[string]string{
		"@stormlight_id":              "a1b2c3d4",
		"@stormlight_created_at":      "1700000000",
		"@stormlight_attention":       "question",
		"@stormlight_attention_at":    "1700000100",
		"@stormlight_summary":         "where it left off",
		"@stormlight_transcript_path": "/transcripts/a1.jsonl",
		"@stormlight_mode":            "auto",
		// A restored agent is starting, whatever it was doing when the
		// server died.
		"@stormlight_activity": string(agent.ActivityStarting),
	}
	for key, value := range want {
		if runner.options[key] != value {
			t.Errorf("%s = %q, want %q", key, runner.options[key], value)
		}
	}
	if !strings.Contains(runner.respawned, "'--id' 'a1b2c3d4'") {
		t.Fatalf("the supervisor was not told the restored id: %q", runner.respawned)
	}
}

func TestRestoreRefusesARecordWithNoIdentity(t *testing.T) {
	runtime, err := NewRuntime(&restoreRunner{sessionExists: true}, "stormlight-agents")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.Restore(context.Background(), session.RestoreRequest{
		Agent:  agent.Agent{Cwd: t.TempDir()},
		Launch: session.Launch{Path: "/usr/bin/claude"},
	}); err == nil {
		t.Fatal("an idless record restored")
	}
}
