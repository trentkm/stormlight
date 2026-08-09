package tmux

import (
	"context"
	"fmt"
	"strings"

	"github.com/trentkm/stormlight/internal/agent"
	"github.com/trentkm/stormlight/internal/diagnostic"
)

// Cycling the queue is the counterpart to the return key: instead of going
// back to the dashboard to pick the next agent that wants you, the key hands
// you the next one directly, in the order they started waiting. The pair
// walks it both ways, because visiting an agent does not take it out of the
// queue — so the queue holds still and your place in it is what moves, and
// one step too far has to be a step back rather than a lap.
//
// They are root-table bindings, so they reach tmux before the agent's own TUI
// and need no prefix; in windows Stormlight does not own, the key is passed
// straight through to the pane. The prefix bindings are the guaranteed-safe
// path for anyone whose agent needs the single-press keys for itself.
const (
	nextBindingNote     = "Next agent waiting"
	previousBindingNote = "Previous agent waiting"
	nextPrefixKey       = "N"
	previousPrefixKey   = "P"
)

// SetNextKeys overrides the single-press keys that step forward through the
// attention queue (default C-] ).
func (r *Runtime) SetNextKeys(keys []string) {
	r.nextKeys = keys
}

// SetPreviousKeys overrides the single-press keys that step back through the
// attention queue (default C-\ ).
func (r *Runtime) SetPreviousKeys(keys []string) {
	r.previousKeys = keys
}

func (r *Runtime) effectiveNextKeys() []string {
	if len(r.nextKeys) > 0 {
		return r.nextKeys
	}
	return []string{"C-]"}
}

func (r *Runtime) effectivePreviousKeys() []string {
	if len(r.previousKeys) > 0 {
		return r.previousKeys
	}
	// The neighbouring control character, and the neighbouring physical key:
	// \ then ] reads back then forward in the order the hands already know.
	// C-[ is Escape, so the bracket pair itself is not available.
	return []string{"C-\\"}
}

// nextShellCommand re-invokes Stormlight to do the picking. The queue's order
// depends on marks and on when each agent started waiting, which is state
// tmux formats cannot rank, so the binding asks the binary that knows.
//
// The socket and session travel with it: a key binding fires with whatever
// environment the tmux server was started with, which is not necessarily the
// one the dashboard was launched from.
func (r *Runtime) nextShellCommand(step agent.QueueStep) string {
	args := []string{r.bindingPath()}
	if r.socket != "" {
		args = append(args, "--tmux-socket", r.socket)
	}
	args = append(args, "--session", r.sessionName, "next")
	if step == agent.QueueBack {
		args = append(args, "--previous")
	}
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
func (r *Runtime) nextTmuxCommand(step agent.QueueStep) string {
	return "run-shell -b " + tmuxQuote(r.nextShellCommand(step))
}

// queueDirection pairs a step with the names it wears in tmux.
type queueDirection struct {
	step      agent.QueueStep
	note      string
	prefixKey string
	rootKeys  []string
}

func (r *Runtime) queueDirections() []queueDirection {
	return []queueDirection{
		{agent.QueueForward, nextBindingNote, nextPrefixKey,
			r.effectiveNextKeys()},
		{agent.QueueBack, previousBindingNote, previousPrefixKey,
			r.effectivePreviousKeys()},
	}
}

// configureNextPrefix binds the prefix keys that cycle the queue. Unlike the
// return key these are not essential — the single-press keys carry the
// feature — so a foreign binding is left alone with a warning rather than
// failing the attach.
func (r *Runtime) configureNextPrefix(ctx context.Context, listing string) {
	for _, direction := range r.queueDirections() {
		if current, bound := tableBinding(
			listing, "prefix", direction.prefixKey,
		); bound && !strings.Contains(current, "@stormlight_") {
			diagnostic.Logger().Warn("foreign tmux prefix binding left in place",
				"key", direction.prefixKey,
				"binding", current,
			)
			continue
		}
		if _, err := r.runner.Run(ctx, nil,
			"bind-key", "-T", "prefix", "-N", direction.note,
			direction.prefixKey, "run-shell", "-b",
			r.nextShellCommand(direction.step),
		); err != nil {
			diagnostic.Logger().Warn("cannot install tmux queue binding",
				"key", direction.prefixKey,
				"error", err,
			)
		}
	}
}

// configureNextRoot installs the single-press queue keys. Outside Stormlight's
// own windows the key is handed to the pane untouched.
func (r *Runtime) configureNextRoot(ctx context.Context, listing string) {
	for _, direction := range r.queueDirections() {
		for _, key := range direction.rootKeys {
			if current, bound := tableBinding(listing, "root", key); bound &&
				!strings.Contains(current, "@stormlight_") {
				diagnostic.Logger().Warn("foreign tmux root binding left in place",
					"key", key,
					"binding", current,
				)
				continue
			}
			if _, err := r.runner.Run(ctx, nil,
				"bind-key", "-T", "root", "-N", direction.note, key,
				"if-shell", "-F", `#{==:#{@stormlight_id},}`,
				"send-keys "+key, r.nextTmuxCommand(direction.step),
			); err != nil {
				diagnostic.Logger().Warn("cannot install tmux queue binding",
					"key", key,
					"error", err,
				)
			}
		}
	}
	r.configureNextMouse(ctx, listing)
}

// queueMouseKey is the click tmux reports for a user range on the status
// line. It is a different key from MouseDown1StatusRight, which is what the
// rest of the right section still answers to, so the two coexist: the return
// region keeps its meaning and only the hints that claim a range of their own
// are peeled off it.
const queueMouseKey = "MouseDown1Status"

// statusClickPassthrough is what a click means when it is not on one of
// Stormlight's hints: select the window it landed on, which is what a click
// on a window range has always meant.
const statusClickPassthrough = "select-window -t="

// tmuxStatusClickDefaults are tmux's own bindings for this key. Unlike every
// other key Stormlight claims, this one is bound out of the box — so "already
// bound" cannot stand for "somebody owns this", and a guard that reads it
// that way declines to install and leaves the hints inert.
var tmuxStatusClickDefaults = []string{"select-window -t=", "switch-client -t ="}

// queueMouseFormat dispatches a status-line click on the range it landed in.
// Anything that is not one of Stormlight's hints — including every click in a
// window Stormlight does not own — keeps the meaning tmux gives it.
func (r *Runtime) queueMouseFormat() string {
	return `#{?#{==:#{@stormlight_id},},` + statusClickPassthrough +
		`,#{?#{==:#{mouse_status_range},` + queueForwardRange + `},` +
		r.nextTmuxCommand(agent.QueueForward) +
		`,#{?#{==:#{mouse_status_range},` + queueBackRange + `},` +
		r.nextTmuxCommand(agent.QueueBack) +
		`,` + statusClickPassthrough + `}}}`
}

// replaceableStatusClick reports whether the current binding is one
// Stormlight may take over: its own, or the default tmux ships.
func replaceableStatusClick(binding string) bool {
	if strings.Contains(binding, "@stormlight_") {
		return true
	}
	compact := strings.Join(strings.Fields(binding), "")
	for _, standard := range tmuxStatusClickDefaults {
		if strings.HasSuffix(compact, strings.ReplaceAll(standard, " ", "")) {
			return true
		}
	}
	return false
}

// configureNextMouse makes the queue hints clickable.
func (r *Runtime) configureNextMouse(ctx context.Context, listing string) {
	if current, bound := tableBinding(listing, "root", queueMouseKey); bound &&
		!replaceableStatusClick(current) {
		diagnostic.Logger().Warn("foreign tmux root binding left in place",
			"key", queueMouseKey,
			"binding", current,
		)
		return
	}
	if _, err := r.runner.Run(ctx, nil,
		"bind-key", "-T", "root", "-N", nextBindingNote,
		queueMouseKey, "run-shell", "-C", r.queueMouseFormat(),
	); err != nil {
		diagnostic.Logger().Warn("cannot install tmux queue binding",
			"key", queueMouseKey,
			"error", err,
		)
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
	Step   agent.QueueStep
}

// NextWaiting switches a client to the next agent in the attention queue.
//
// Arriving deliberately does not clear the amber. Landing in a window is not
// an answer to what the agent is asking, and a cycle that marked rows seen on
// the way past would empty the inbox with nothing done about it — so the
// queue is left exactly as it was and only the human's place in it moves.
// Attention comes down where it always has: from the dashboard, from `M`, or
// from the agent's own next report.
func (r *Runtime) NextWaiting(ctx context.Context, req NextRequest) error {
	agents, err := r.ListAgents(ctx)
	if err != nil {
		return err
	}
	step := req.Step
	if step == 0 {
		step = agent.QueueForward
	}
	queue := agent.Queue(agents)
	target, ok := agent.StepInQueue(queue, req.Agent, step)
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
		"step", int(step),
	)
	return nil
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
