package ui

import (
	"bytes"
	"errors"
	"fmt"
	"reflect"
	"testing"

	tea "charm.land/bubbletea/v2"
)

const (
	clipboardTestText = "first\ncaf\u00e9"
	clipboardOSC52    = "\x1b]52;c;Zmlyc3QKY2Fmw6k=\x07"
)

type clipboardCopyModel struct {
	err error
}

func (m clipboardCopyModel) Init() tea.Cmd {
	return copyToClipboardCmd(clipboardTestText)
}

func (m clipboardCopyModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if action, ok := msg.(actionMsg); ok {
		m.err = action.err
		return m, tea.Quit
	}
	return m, nil
}

func (m clipboardCopyModel) View() tea.View {
	return tea.NewView("")
}

type clipboardInvocation struct {
	path string
	args []string
	text string
}

func resetClipboardEnvironment(t *testing.T) {
	t.Helper()
	for _, name := range []string{
		"TMUX",
		"SSH_CONNECTION", "SSH_CLIENT", "SSH_TTY",
		"MOSH_CONNECTION", "MOSH_IP",
	} {
		t.Setenv(name, "")
	}
}

func stubClipboardCommands(
	t *testing.T,
	results map[string]error,
) *[]clipboardInvocation {
	t.Helper()
	previousFind := findClipboardCommand
	previousRun := runClipboardCommand
	var invocations []clipboardInvocation
	findClipboardCommand = func(name string) (string, error) {
		if _, ok := results[name]; ok {
			return name, nil
		}
		return "", fmt.Errorf("%s not found", name)
	}
	runClipboardCommand = func(path string, args []string, text string) error {
		invocations = append(invocations, clipboardInvocation{
			path: path,
			args: append([]string(nil), args...),
			text: text,
		})
		err, ok := results[path]
		if !ok {
			return fmt.Errorf("unexpected clipboard command %s", path)
		}
		return err
	}
	t.Cleanup(func() {
		findClipboardCommand = previousFind
		runClipboardCommand = previousRun
	})
	return &invocations
}

func runClipboardCopy(t *testing.T) (string, clipboardCopyModel) {
	t.Helper()
	var output bytes.Buffer
	result, err := tea.NewProgram(
		clipboardCopyModel{},
		tea.WithInput(nil),
		tea.WithOutput(&output),
		tea.WithoutRenderer(),
	).Run()
	if err != nil {
		t.Fatalf("run clipboard command: %v", err)
	}
	return output.String(), result.(clipboardCopyModel)
}

func assertClipboardSuccess(t *testing.T, model clipboardCopyModel) {
	t.Helper()
	if model.err != nil {
		t.Fatalf("copy failed: %v", model.err)
	}
}

func firstNativeClipboardCommand(t *testing.T) []string {
	t.Helper()
	candidates := nativeClipboardCommands()
	if len(candidates) == 0 {
		t.Skip("platform has no native clipboard command")
	}
	return candidates[0]
}

func TestCopyToClipboardUsesTmuxWhenNested(t *testing.T) {
	resetClipboardEnvironment(t)
	t.Setenv("TMUX", "/tmp/tmux/default,1,0")
	t.Setenv("SSH_CONNECTION", "client 1 server 22")
	invocations := stubClipboardCommands(t, map[string]error{"tmux": nil})

	output, model := runClipboardCopy(t)

	if output != "" {
		t.Fatalf("terminal output = %q, want none", output)
	}
	assertClipboardSuccess(t, model)
	want := []clipboardInvocation{{
		path: "tmux",
		args: []string{"load-buffer", "-w", "-"},
		text: clipboardTestText,
	}}
	if !reflect.DeepEqual(*invocations, want) {
		t.Fatalf("clipboard invocations = %#v, want %#v", *invocations, want)
	}
}

func TestCopyToClipboardReportsTmuxFailure(t *testing.T) {
	resetClipboardEnvironment(t)
	t.Setenv("TMUX", "/tmp/tmux/default,1,0")
	stubClipboardCommands(t, map[string]error{
		"tmux": errors.New("server unavailable"),
	})

	output, model := runClipboardCopy(t)

	if output != "" {
		t.Fatalf("terminal output = %q, want none", output)
	}
	if model.err == nil ||
		model.err.Error() != "copy with tmux: server unavailable" {
		t.Fatalf("error = %v", model.err)
	}
}

func TestCopyToClipboardUsesOSC52ForRemoteSession(t *testing.T) {
	resetClipboardEnvironment(t)
	t.Setenv("SSH_CONNECTION", "client 1 server 22")
	candidate := firstNativeClipboardCommand(t)[0]
	invocations := stubClipboardCommands(t, map[string]error{candidate: nil})

	output, model := runClipboardCopy(t)

	if output != clipboardOSC52 {
		t.Fatalf("terminal output = %q, want %q", output, clipboardOSC52)
	}
	assertClipboardSuccess(t, model)
	if len(*invocations) != 0 {
		t.Fatalf("native clipboard command used remotely: %#v", *invocations)
	}
}

func TestCopyToClipboardUsesNativeToolLocally(t *testing.T) {
	resetClipboardEnvironment(t)
	candidate := firstNativeClipboardCommand(t)
	invocations := stubClipboardCommands(t, map[string]error{
		candidate[0]: nil,
	})

	output, model := runClipboardCopy(t)

	if output != "" {
		t.Fatalf("terminal output = %q, want none", output)
	}
	assertClipboardSuccess(t, model)
	want := []clipboardInvocation{{
		path: candidate[0],
		args: append([]string(nil), candidate[1:]...),
		text: clipboardTestText,
	}}
	if !reflect.DeepEqual(*invocations, want) {
		t.Fatalf("clipboard invocations = %#v, want %#v", *invocations, want)
	}
}

func TestCopyToClipboardFallsBackToOSC52WhenNativeToolFails(t *testing.T) {
	resetClipboardEnvironment(t)
	candidate := firstNativeClipboardCommand(t)[0]
	stubClipboardCommands(t, map[string]error{
		candidate: errors.New("clipboard unavailable"),
	})

	output, model := runClipboardCopy(t)

	if output != clipboardOSC52 {
		t.Fatalf("terminal output = %q, want %q", output, clipboardOSC52)
	}
	assertClipboardSuccess(t, model)
}

func TestCopyToClipboardFallsBackToOSC52WithoutNativeTool(t *testing.T) {
	resetClipboardEnvironment(t)
	stubClipboardCommands(t, nil)

	output, model := runClipboardCopy(t)

	if output != clipboardOSC52 {
		t.Fatalf("terminal output = %q, want %q", output, clipboardOSC52)
	}
	assertClipboardSuccess(t, model)
}
