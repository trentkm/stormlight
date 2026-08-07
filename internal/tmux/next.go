package tmux

import (
	"context"
	"fmt"
	"strings"

	"github.com/trentkm/stormlight/internal/agent"
	"github.com/trentkm/stormlight/internal/diagnostic"
	"github.com/trentkm/stormlight/internal/session"
)

// Cycling the queue is the counterpart to the return key: instead of going
// back to the dashboard to pick the next agent that wants you, the key hands
// you the next one directly, in the order they started waiting.
//
// It is a root-table binding, so it reaches tmux before the agent's own TUI
// and needs no prefix; in windows Stormlight does not own, the key is passed
// straight through to the pane. The prefix binding is the guaranteed-safe
// path for anyone whose agent needs the single-press key for itself.
const (
	nextBindingNote = "Next agent waiting"
	nextPrefixKey   = "N"
)

// SetNextKeys overrides the single-press keys that cycle the attention queue
// (default C-] ).
func (r *Runtime) SetNextKeys(keys []string) {
	r.nextKeys = keys
}

func (r *Runtime) effectiveNextKeys() []string {
	if len(r.nextKeys) > 0 {
		return r.nextKeys
	}
	return []string{"C-]"}
}

// nextShellCommand re-invokes Stormlight to do the picking. The queue's order
// depends on marks and on when each agent started waiting, which is state
// tmux formats cannot rank, so the binding asks the binary that knows.
//
// The socket and session travel with it: a key binding fires with whatever
// environment the tmux server was started with, which is not necessarily the
// one the dashboard was launched from.
func (r *Runtime) nextShellCommand() string {
	args := []string{r.executable}
	if r.socket != "" {
		args = append(args, "--tmux-socket", r.socket)
	}
	args = append(args, "--session", r.sessionName, "next")
	// Expanded by tmux before the shell sees them: the client that pressed
	// the key, and the window — and so the agent — it was pressed in. The
	// agent option doubles as the marker that makes this binding
	// recognisably Stormlight's own when it comes back from list-keys.
	args = append(args,
		"--client", "#{client_name}",
		"--window", "#{window_id}",
		"--agent", "#{@stormlight_id}",
	)
	return shellJoin(args)
}

// nextTmuxCommand is the same invocation as a tmux command, for the places
// tmux parses a command line rather than taking it as one argument.
func (r *Runtime) nextTmuxCommand() string {
	return "run-shell -b " + tmuxQuote(r.nextShellCommand())
}

// configureNextPrefix binds the prefix key that cycles the queue. Unlike the
// return key this is not essential — the single-press keys carry the
// feature — so a foreign binding is left alone with a warning rather than
// failing the attach.
func (r *Runtime) configureNextPrefix(ctx context.Context, listing string) {
	if current, bound := tableBinding(listing, "prefix", nextPrefixKey); bound &&
		!strings.Contains(current, "@stormlight_") {
		diagnostic.Logger().Warn("foreign tmux prefix binding left in place",
			"key", nextPrefixKey,
			"binding", current,
		)
		return
	}
	if _, err := r.runner.Run(ctx, nil,
		"bind-key", "-T", "prefix", "-N", nextBindingNote,
		nextPrefixKey, "run-shell", "-b", r.nextShellCommand(),
	); err != nil {
		diagnostic.Logger().Warn("cannot install tmux queue binding",
			"key", nextPrefixKey,
			"error", err,
		)
	}
}

// configureNextRoot installs the single-press queue keys. Outside Stormlight's
// own windows the key is handed to the pane untouched.
func (r *Runtime) configureNextRoot(ctx context.Context, listing string) {
	for _, key := range r.effectiveNextKeys() {
		if current, bound := tableBinding(listing, "root", key); bound &&
			!strings.Contains(current, "@stormlight_") {
			diagnostic.Logger().Warn("foreign tmux root binding left in place",
				"key", key,
				"binding", current,
			)
			continue
		}
		if _, err := r.runner.Run(ctx, nil,
			"bind-key", "-T", "root", "-N", nextBindingNote, key,
			"if-shell", "-F", `#{==:#{@stormlight_id},}`,
			"send-keys "+key, r.nextTmuxCommand(),
		); err != nil {
			diagnostic.Logger().Warn("cannot install tmux queue binding",
				"key", key,
				"error", err,
			)
		}
	}
}

// NextRequest names where a cycle was asked for: the tmux client that will
// be switched, the window it was asked from, and the agent living in that
// window. All three are optional — a bare `stormlight next` from a shell
// inside the server still works — but without an agent there is nothing to
// advance past, so the queue's head is the answer.
type NextRequest struct {
	Client string
	Window string
	Agent  string
}

// NextWaiting switches a client to the next agent in the attention queue.
//
// Arriving counts as engagement, exactly as opening a terminal from the
// dashboard does, so the soft amber comes down behind you and the queue
// drains as you work it. An urgent state is left standing: only answering
// the prompt resolves it, and the cycle steps past it instead.
func (r *Runtime) NextWaiting(ctx context.Context, req NextRequest) error {
	agents, err := r.ListAgents(ctx)
	if err != nil {
		return err
	}
	queue := agent.Queue(agents)
	target, ok := agent.NextInQueue(queue, req.Agent)
	if !ok {
		message := "No agents are waiting"
		if len(queue) > 0 {
			message = "No other agent is waiting"
		}
		return r.notify(ctx, req.Client, message)
	}

	// The window the human came from knows the way back to the dashboard;
	// the one they are going to has to learn it, or the return key would
	// strand them wherever the cycle stopped.
	r.carryReturnTarget(ctx, req.Window, target.WindowID)

	args := []string{"switch-client"}
	if req.Client != "" {
		args = append(args, "-c", req.Client)
	}
	args = append(args, "-t", target.WindowID)
	if _, err := r.runner.Run(ctx, nil, args...); err != nil {
		return fmt.Errorf("switch tmux client: %w", err)
	}
	diagnostic.Logger().Info("cycled to waiting agent",
		"agent_id", target.ID,
		"window_id", target.WindowID,
		"queue_depth", len(queue),
	)
	if target.Attention.Urgent() {
		return nil
	}
	return r.Update(ctx, target.ID, session.Update{ClearAttention: true})
}

func (r *Runtime) carryReturnTarget(ctx context.Context, from, to string) {
	if from == "" || from == to {
		return
	}
	target, err := r.runner.Run(ctx, nil,
		"show-options", "-qv", "-w", "-t", from, returnTargetOption,
	)
	if err != nil || target == "" {
		return
	}
	if _, err := r.runner.Run(ctx, nil,
		"set-option", "-w", "-t", to, returnTargetOption, target,
	); err != nil {
		diagnostic.Logger().Warn("cannot carry tmux return target",
			"window_id", to,
			"error", err,
		)
	}
}

func (r *Runtime) notify(ctx context.Context, client, message string) error {
	args := []string{"display-message"}
	if client != "" {
		args = append(args, "-c", client)
	}
	args = append(args, message)
	_, err := r.runner.Run(ctx, nil, args...)
	return err
}

// tmuxQuote wraps a string so tmux's own parser reads it as one argument.
// Double quotes rather than single: tmux expands #{...} formats inside them,
// which is how the client and window reach the command line.
func tmuxQuote(value string) string {
	replacer := strings.NewReplacer(`\`, `\\`, `"`, `\"`)
	return `"` + replacer.Replace(value) + `"`
}
