package tmux

import (
	"context"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/trentkm/stormlight/internal/agent"
)

// queuePane is one line of the pane listing, written the way a waiting agent
// comes back from tmux.
type queuePane struct {
	id          string
	window      string
	attention   agent.Attention
	attentionAt int64
	mark        agent.Mark
	dead        bool
}

func queueLine(pane queuePane) string {
	parts := make([]string, basePaneFieldCount+metadataFieldCount)
	parts[0] = "stormlight-agents"
	parts[1] = pane.window
	parts[2] = "0"
	parts[3] = pane.id
	parts[4] = "%" + strings.TrimPrefix(pane.window, "@")
	parts[10] = pane.id
	parts[11] = "codex"
	parts[15] = "1700000000"
	parts[16] = string(agent.ActivityIdle)
	parts[17] = string(pane.attention)
	parts[18] = parts[4]
	parts[29] = string(pane.mark)
	if pane.attentionAt > 0 {
		parts[30] = strconv.FormatInt(pane.attentionAt, 10)
	}
	if pane.dead {
		parts[8] = "1"
		parts[9] = "0"
	}
	return strings.Join(parts, fieldSeparator)
}

type queueRunner struct {
	panes        []queuePane
	returnTarget string
	calls        [][]string
}

func (r *queueRunner) Run(_ context.Context, _ []byte, args ...string) (string, error) {
	r.calls = append(r.calls, slices.Clone(args))
	switch args[0] {
	case "list-panes":
		lines := make([]string, len(r.panes))
		for index, pane := range r.panes {
			lines[index] = queueLine(pane)
		}
		return strings.Join(lines, "\n"), nil
	case "show-options":
		if args[len(args)-1] == returnTargetOption {
			return r.returnTarget, nil
		}
		return "", nil
	}
	return "", nil
}

func (r *queueRunner) call(name string) ([]string, bool) {
	for _, call := range r.calls {
		if call[0] == name {
			return call, true
		}
	}
	return nil, false
}

func newQueueRuntime(runner *queueRunner) *Runtime {
	return &Runtime{runner: runner, sessionName: "stormlight-agents"}
}

func TestNextWaitingAdvancesInArrivalOrder(t *testing.T) {
	runner := &queueRunner{
		panes: []queuePane{
			{id: "late", window: "@3", attention: agent.AttentionWaiting, attentionAt: 300},
			{id: "early", window: "@1", attention: agent.AttentionWaiting, attentionAt: 100},
			{id: "middle", window: "@2", attention: agent.AttentionWaiting, attentionAt: 200},
		},
		returnTarget: "$7",
	}
	runtime := newQueueRuntime(runner)

	// From outside the queue, the head: whoever has been waiting longest.
	if err := runtime.NextWaiting(context.Background(), NextRequest{
		Client: "/dev/ttys004",
	}); err != nil {
		t.Fatal(err)
	}
	switched, ok := runner.call("switch-client")
	if !ok || !slices.Equal(switched,
		[]string{"switch-client", "-c", "/dev/ttys004", "-t", "@1"}) {
		t.Fatalf("switch = %#v", switched)
	}

	// From inside it, one place along.
	runner.calls = nil
	if err := runtime.NextWaiting(context.Background(), NextRequest{
		Client: "/dev/ttys004", Window: "@1", Agent: "early",
	}); err != nil {
		t.Fatal(err)
	}
	switched, _ = runner.call("switch-client")
	if !slices.Equal(switched,
		[]string{"switch-client", "-c", "/dev/ttys004", "-t", "@2"}) {
		t.Fatalf("switch = %#v", switched)
	}
}

// The window the human came from knows the way back to the dashboard; the one
// they arrive in has to learn it, or the return key strands them.
func TestNextWaitingCarriesTheReturnTarget(t *testing.T) {
	runner := &queueRunner{
		panes: []queuePane{
			{id: "here", window: "@1", attention: agent.AttentionWaiting, attentionAt: 100},
			{id: "there", window: "@2", attention: agent.AttentionWaiting, attentionAt: 200},
		},
		returnTarget: "$7",
	}
	runtime := newQueueRuntime(runner)

	if err := runtime.NextWaiting(context.Background(), NextRequest{
		Window: "@1", Agent: "here",
	}); err != nil {
		t.Fatal(err)
	}
	want := []string{"set-option", "-w", "-t", "@2", returnTargetOption, "$7"}
	found := false
	for _, call := range runner.calls {
		if slices.Equal(call, want) {
			found = true
		}
	}
	if !found {
		t.Fatalf("return target was not carried: %#v", runner.calls)
	}
}

// Arriving is engagement, exactly as opening a terminal from the dashboard
// is: the soft amber comes down so the queue drains as it is worked.
func TestNextWaitingClearsSoftAttentionOnArrival(t *testing.T) {
	runner := &queueRunner{panes: []queuePane{
		{id: "unseen", window: "@1", attention: agent.AttentionWaiting, attentionAt: 100},
	}}
	runtime := newQueueRuntime(runner)

	if err := runtime.NextWaiting(context.Background(), NextRequest{}); err != nil {
		t.Fatal(err)
	}
	cleared := false
	for _, call := range runner.calls {
		if len(call) == 6 && call[0] == "set-option" &&
			call[4] == "@stormlight_attention" && call[5] == "" {
			cleared = true
		}
	}
	if !cleared {
		t.Fatalf("attention was not cleared on arrival: %#v", runner.calls)
	}
}

// An urgent prompt is only resolved by answering it, so landing on one must
// not mark it seen — and the cycle has to be able to step past it.
func TestNextWaitingLeavesUrgentAttentionStanding(t *testing.T) {
	runner := &queueRunner{panes: []queuePane{
		{id: "asked", window: "@1", attention: agent.AttentionQuestion, attentionAt: 100},
		{id: "unseen", window: "@2", attention: agent.AttentionWaiting, attentionAt: 200},
	}}
	runtime := newQueueRuntime(runner)

	if err := runtime.NextWaiting(context.Background(), NextRequest{}); err != nil {
		t.Fatal(err)
	}
	for _, call := range runner.calls {
		if len(call) == 6 && call[0] == "set-option" &&
			call[4] == "@stormlight_attention" {
			t.Fatalf("urgent attention was written: %#v", call)
		}
	}
	runner.calls = nil
	if err := runtime.NextWaiting(context.Background(), NextRequest{
		Window: "@1", Agent: "asked",
	}); err != nil {
		t.Fatal(err)
	}
	if switched, _ := runner.call("switch-client"); !slices.Equal(switched,
		[]string{"switch-client", "-t", "@2"}) {
		t.Fatalf("cycle did not step past the urgent agent: %#v", switched)
	}
}

func TestNextWaitingSaysSoWhenTheQueueIsEmpty(t *testing.T) {
	runner := &queueRunner{panes: []queuePane{
		{id: "busy", window: "@1"},
		{id: "gone", window: "@2", attention: agent.AttentionWaiting, dead: true},
	}}
	runtime := newQueueRuntime(runner)

	if err := runtime.NextWaiting(context.Background(), NextRequest{
		Client: "/dev/ttys004",
	}); err != nil {
		t.Fatal(err)
	}
	message, ok := runner.call("display-message")
	if !ok || !slices.Equal(message,
		[]string{"display-message", "-c", "/dev/ttys004", "No agents are waiting"}) {
		t.Fatalf("message = %#v", message)
	}
	if _, switched := runner.call("switch-client"); switched {
		t.Fatal("an empty queue switched the client anyway")
	}
}

func TestNextWaitingDistinguishesTheOnlyWaitingAgent(t *testing.T) {
	runner := &queueRunner{panes: []queuePane{
		{id: "only", window: "@1", attention: agent.AttentionWaiting, attentionAt: 100},
	}}
	runtime := newQueueRuntime(runner)

	if err := runtime.NextWaiting(context.Background(), NextRequest{
		Window: "@1", Agent: "only",
	}); err != nil {
		t.Fatal(err)
	}
	message, _ := runner.call("display-message")
	if !slices.Equal(message,
		[]string{"display-message", "No other agent is waiting"}) {
		t.Fatalf("message = %#v", message)
	}
}

// A mark is the human's own note that a row wants revisiting, so it belongs
// in the queue the same way an unseen result does.
func TestNextWaitingIncludesMarkedAgents(t *testing.T) {
	runner := &queueRunner{panes: []queuePane{
		{id: "flagged", window: "@2", mark: agent.MarkAttention, attentionAt: 50},
	}}
	runtime := newQueueRuntime(runner)

	if err := runtime.NextWaiting(context.Background(), NextRequest{}); err != nil {
		t.Fatal(err)
	}
	if switched, _ := runner.call("switch-client"); !slices.Equal(switched,
		[]string{"switch-client", "-t", "@2"}) {
		t.Fatalf("switch = %#v", switched)
	}
}

// The binding fires with whatever environment the tmux server was started
// with, so the socket and session have to travel in the command line itself.
func TestNextShellCommandCarriesItsOwnServer(t *testing.T) {
	runtime := &Runtime{
		executable:  "/usr/local/bin/stormlight",
		socket:      "stormlight",
		sessionName: "stormlight-agents",
	}
	command := runtime.nextShellCommand()
	for _, want := range []string{
		"'/usr/local/bin/stormlight'",
		"'--tmux-socket' 'stormlight'",
		"'--session' 'stormlight-agents'",
		"'next'",
		"'--client' '#{client_name}'",
		"'--window' '#{window_id}'",
		"'--agent' '#{@stormlight_id}'",
	} {
		if !strings.Contains(command, want) {
			t.Fatalf("command missing %q: %s", want, command)
		}
	}
	if quoted := runtime.nextTmuxCommand(); !strings.HasPrefix(
		quoted, `run-shell -b "`,
	) || !strings.HasSuffix(quoted, `"`) {
		t.Fatalf("tmux command = %s", quoted)
	}
}
