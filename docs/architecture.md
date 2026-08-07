# Architecture

`stormlight` separates agent semantics from terminal and process ownership.

## Layers

### Provider adapters

Provider adapters translate a task into an executable plus arguments. The
built-ins are Claude and Codex; custom provider specs cover other agent CLIs.
Adapters deliberately do not own tmux behavior.

The CLI adapters currently add provider-native lifecycle callbacks:

- Codex: per-launch prompt and stop hooks report state, passed as a `-c`
  config override. The external completion notifier they replaced carried
  only turn ends, so a turn begun in the agent's own pane was invisible.
- Claude: per-launch prompt, notification, and stop hooks report state.
  Permission prompts raise attention through the notification hook; they
  are answered in the agent's own terminal, never intercepted.
- Generic agents: PTY state and optional lifecycle hooks.

Both CLIs accept the same hook schema — an event name mapping to matcher
groups of command handlers, with the payload arriving on the handler's
stdin — so one set of types describes both and only the encoding differs:
Claude takes JSON through `--settings`, Codex takes inline TOML through
`-c`. Codex parses that value as TOML and rejects JSON, so the override has
to encode to a single inline line.

Hooks that resolve rather than observe are deliberately left unregistered
for both providers. Claude's `PreToolUse` and Codex's `PermissionRequest`
answer whether a tool call may proceed, and an approval Stormlight never
answers is an agent stuck waiting on it.

The next Codex revision should use App Server JSON-RPC for threads, turns,
approvals, and streamed items. Claude background-agent discovery or the Agent
SDK can similarly replace its CLI hook bridge. The runtime exposes
`stormlight event` so generic providers can report semantic state.

### Application service

The application service validates requests, resolves a provider and workspace,
and delegates terminal operations to the runtime. The TUI and CLI use the same
service. A persistent workspace catalog supplies workspaces that
do not currently contain an agent.

Workspace resolvers return a stable group ID, a group root, an execution root,
and optional component metadata. External executable resolvers run before the
built-in Git resolver, followed by a canonical-directory fallback. This keeps
environment-specific workspace semantics outside the public runtime.

### tmux runtime

tmux is the process supervisor and terminal transport, run as a private
appliance: agents and the hosted dashboard live on a dedicated Stormlight
server (`tmux -L stormlight`) that boots with Stormlight's own configuration
and never loads user dotfiles. User tmux sessions, plugins, and
session-restore tools cannot observe or disturb Stormlight, and Stormlight
behaves identically on every machine. The managed configuration is rewritten
at startup under the user config directory (`stormlight/tmux.conf`).

When launched from a regular shell, the dashboard re-enters a uniquely named
temporary session on the Stormlight server. That gives overlays and agent
switching the same semantics as an inside-tmux launch. The supervising process
removes the temporary session when its client exits; managed agent sessions
remain independent. When launched from inside the user's own tmux, the
dashboard runs directly in that pane and reaches agents on the Stormlight
server through a nested client with `$TMUX` stripped.

The application layer depends on `session.Runtime`, not the tmux implementation.
That contract owns conversation dispatch, discovery, capture, input, lifecycle,
metadata updates, and terminal attachment. tmux is the first implementation;
other runtimes can provide the same agent model without entering the UI or
provider packages. The stable agent ID is the only runtime handle passed back
through the application layer; tmux session, window, and pane fields remain
transport-specific compatibility metadata.

External interactive presentation is a separate `surface.Surface` contract.
The tmux surface advertises popup and client-switch capabilities and translates
generic commands into `display-popup`; the direct surface suspends Bubble Tea
and runs the command in the current terminal. The UI requests a capability and
does not inspect `$TMUX` or construct multiplexer commands. Yazi directory
selection and Neovim task editing both use this contract and return their
results through permission-restricted temporary handoff files.

Each dispatch creates one window in a managed tmux session. Window options hold
the durable metadata:

| Option | Purpose |
|---|---|
| `@stormlight_id` | Stable agent identifier |
| `@stormlight_provider` | Provider adapter |
| `@stormlight_task` | Original task |
| `@stormlight_summary` | Current one-line summary |
| `@stormlight_cwd` | Working directory |
| `@stormlight_created_at` | Unix creation timestamp |
| `@stormlight_activity` | Normalized activity state |
| `@stormlight_attention` | Pending human-attention type |
| `@stormlight_mark` | Human's manual override of the derived state |
| `@stormlight_pane` | Original agent pane |
| `@stormlight_workspace_id` | Stable workspace group identifier |
| `@stormlight_workspace_kind` | Resolver-defined workspace type |
| `@stormlight_workspace_name` | Human-readable group name |
| `@stormlight_workspace_root` | Canonical group root |
| `@stormlight_execution_root` | Checkout or runnable workspace root |
| `@stormlight_component_name` | Optional package or component name |
| `@stormlight_component_root` | Optional package or component root |
| `@stormlight_workspace_metadata` | Encoded resolver metadata |
| `@stormlight_return_target` | Source session for the current attach, or empty to detach |

The tmux session itself carries `@stormlight_managed=1`. An existing session
without that marker is never reused.

The window uses `remain-on-exit`, allowing completed output and exit status to
remain reviewable. Messages are loaded into tmux buffers and pasted into the
target pane; they are not interpolated into shell command strings.

The Spanreed pane shows the normalized transcript and composer, with the
native terminal as the fallback. Transcript rendering retains only SGR
styling from tmux capture
output; other terminal control sequences are discarded. A session rendered
from a provider's own JSONL transcript arrives unstyled, so the renderer
paints it: prompts, replies, tool calls, and trimmed results take the
palette in `internal/theme`, and the markdown Claude writes is read back as
styling by Glamour, against a stylesheet built from that same palette in
`internal/provider/markdown.go`. Glamour's own wrapping is switched off —
the pane is resizable, so line breaking belongs to the pane, which knows the
current width. Wrapping replays the styling in
effect at the head of every continuation row and closes it at the row's end,
so no color runs into the neighboring pane. Claude and Codex
interactions focus on content from the first populated prompt and remove only
the trailing empty composer/status block. The tmux capture remains the
provider-neutral fallback. Messages
composed in the pane are pasted into the selected tmux pane.

On first attach, the runtime reserves `Q` in tmux's prefix key table and marks
the binding with the `Return from Stormlight` key note. The binding recognizes
Stormlight windows. Stormlight refreshes a binding carrying its note but
refuses to replace a user-owned `Q` binding. The
managed session preserves the global tmux status formats and uses
`client_prefix` to temporarily switch to a high-contrast status style with the
available return and help keys.

## State model

The public agent record keeps process lifetime, activity, and attention
separate, with a fourth axis for the human's own reading:

- Process: live or exited, with an optional exit code.
- Activity: starting, working, idle, completed, failed, or stopped.
- Attention: question, approval, authentication, waiting, or none.
- Mark: working, attention, or none — set by a human, never derived.

Attention is tiered. Question, approval, and authentication are urgent — the
agent is blocked on an explicit human decision. Waiting is soft — the turn
finished with a result the human has not seen. Every turn end is classified
from the final assistant message (providers emit one event for both "done"
and "asked a question", so content is the only instant discriminator): a
closing question is urgent, anything else is an unseen result. The
provider's delayed idle notification is deliberately ignored — it would
re-raise attention the human already cleared. Attention clears on
engagement: a new prompt, opening the terminal, replying, interrupting,
paging through the result while it is on screen, or an explicit mark-seen.
Navigating between panes and rows is deliberately not engagement — those
are the keys a human presses on the way past a result, and counting them
cleared the amber before it was ever read. The runtime refuses to let a
soft signal downgrade an urgent state. Dead panes carry no attention —
their exit status is the story.

This prevents a resumable completed conversation from being conflated with a
currently running process, and keeps "needs me now" distinct from "idle on
me" and from "just idle".

A mark is the one signal nothing derives. Everything above is inference, and
inference is sometimes wrong, so a human can say otherwise (`m` in the
dashboard, `stormlight mark` outside it) and the mark outranks the derived
reading everywhere it is displayed or counted. The two marks retire
differently, because different parties can answer them: a working mark claims
the agent is still running, which the agent settles as soon as it reports
anything, so the next state-bearing update retires it; an attention mark
claims the human has something to return to, which no provider event can
answer, so only an explicit clear or the same engagement that clears amber
takes it down. Like attention, a mark stops applying once the pane is dead.

## Persistence

The current implementation uses tmux options as the source of truth because
managed processes cannot outlive the tmux server. The workspace catalog is an
atomic JSON file independent of tmux, as are the dashboard's column
preferences. A future daemon may add an append-only event journal for agents
that survive tmux restarts or run remotely.

## Workspace boundary

Workspace discovery is intentionally outside provider adapters. Provisioning
and cleanup remain separate concerns: a future worktree manager can prepare a
directory before dispatch and pass it through the same resolver and runtime.
Cleanup must refuse to remove worktrees containing uncommitted changes or
unpushed commits.
