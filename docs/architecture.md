# Architecture

`runstead` separates agent semantics from terminal and process ownership.

## Layers

### Provider adapters

Provider adapters translate a task into an executable plus arguments. The first
spike includes Claude, Codex, and shell adapters. They deliberately do not own
tmux behavior.

The CLI adapters currently add provider-native lifecycle callbacks:

- Codex: the documented external completion notifier reports idle state and
  the latest response.
- Claude: per-launch prompt, permission, and stop hooks report state. A
  `PermissionRequest` hook publishes provider-neutral approval actions and
  converts the selected option back into Claude's hook response contract.
- Generic agents: PTY state and optional lifecycle hooks.

The next Codex revision should use App Server JSON-RPC for threads, turns,
approvals, and streamed items. Claude background-agent discovery or the Agent
SDK can similarly replace its CLI hook bridge. The runtime exposes
`runstead event` so generic providers can report semantic state.

### Application service

The application service validates requests, resolves a provider and workspace,
and delegates terminal operations to the runtime. The TUI and CLI use the same
service. It also exposes the pending-action queue without coupling the UI to
Claude's JSON schema. A persistent workspace catalog supplies workspaces that
do not currently contain an agent.

Workspace resolvers return a stable group ID, a group root, an execution root,
and optional component metadata. External executable resolvers run before the
built-in Git resolver, followed by a canonical-directory fallback. This keeps
environment-specific workspace semantics outside the public runtime.

### tmux runtime

tmux is the process supervisor and terminal transport. `runstead` never starts
a nested tmux server.

When launched from a regular shell, the dashboard re-enters a uniquely named
temporary session on the selected tmux server. That gives overlays and agent
switching the same semantics as an inside-tmux launch. The supervising process
removes the temporary session when its client exits; managed agent sessions
remain independent.

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
does not inspect `$TMUX` or construct multiplexer commands.

Each dispatch creates one window in a managed tmux session. Window options hold
the durable metadata:

| Option | Purpose |
|---|---|
| `@runstead_id` | Stable agent identifier |
| `@runstead_provider` | Provider adapter |
| `@runstead_task` | Original task |
| `@runstead_summary` | Current one-line summary |
| `@runstead_cwd` | Working directory |
| `@runstead_created_at` | Unix creation timestamp |
| `@runstead_activity` | Normalized activity state |
| `@runstead_attention` | Pending human-attention type |
| `@runstead_pane` | Original agent pane |
| `@runstead_workspace_id` | Stable workspace group identifier |
| `@runstead_workspace_kind` | Resolver-defined workspace type |
| `@runstead_workspace_name` | Human-readable group name |
| `@runstead_workspace_root` | Canonical group root |
| `@runstead_execution_root` | Checkout or runnable workspace root |
| `@runstead_component_name` | Optional package or component name |
| `@runstead_component_root` | Optional package or component root |
| `@runstead_workspace_metadata` | Encoded resolver metadata |
| `@runstead_return_target` | Source session for the current attach, or empty to detach |

The tmux session itself carries `@runstead_managed=1`. A session carrying the
legacy `@agentmux_managed=1` marker is accepted and migrated. An existing
session with neither marker is never reused.

The window uses `remain-on-exit`, allowing completed output and exit status to
remain reviewable. Messages are loaded into tmux buffers and pasted into the
target pane; they are not interpolated into shell command strings.

The Interaction pane is layered. A pending structured action has priority,
followed by the normalized transcript and composer, with the native terminal as
the fallback. Transcript rendering retains only SGR styling from tmux capture
output; other terminal control sequences are discarded. Claude and Codex
interactions focus on content from the first populated prompt and remove only
the trailing empty composer/status block. The tmux capture remains the
provider-neutral fallback, including for arbitrary shell agents. Messages
composed in the pane are pasted into the selected tmux pane.

Pending actions use permission-restricted request and response files. The TUI
refreshes a short-lived controller heartbeat while open. A provider hook waits
only while that heartbeat is fresh; if Runstead exits or crashes, the hook
returns without a decision so the provider's native prompt remains available.

On first attach, the runtime reserves `Q` in tmux's prefix key table and marks
the binding with the `Return from Runstead` key note. The binding recognizes
both Runstead and legacy Agentmux windows. Runstead refreshes bindings carrying
either product's note but refuses to replace a user-owned `Q` binding. The
managed session preserves the global tmux status formats and uses
`client_prefix` to temporarily switch to a high-contrast status style with the
available return and help keys.

### Legacy compatibility

Discovery requests both `@runstead_*` and `@agentmux_*` fields in one tmux
query. New metadata wins when present; otherwise the legacy record is used.
The first state update writes a complete Runstead record, after which only
`@runstead_*` options are changed. This keeps live agents available across the
rename without moving windows or restarting provider processes.

## State model

The public agent record keeps process lifetime, activity, and attention
separate:

- Process: live or exited, with an optional exit code.
- Activity: starting, working, idle, completed, failed, or stopped.
- Attention: question, approval, authentication, or none.

This prevents a resumable completed conversation from being conflated with a
currently running process.

## Persistence

The current implementation uses tmux options as the source of truth because
managed processes cannot outlive the tmux server. The workspace catalog is an
atomic JSON file independent of tmux. Ephemeral pending actions live under
`$XDG_RUNTIME_DIR/runstead/actions` when available, otherwise under the
Runstead state directory. A future daemon may add an append-only event journal
for agents that survive tmux restarts or run remotely.

## Workspace boundary

Workspace discovery is intentionally outside provider adapters. Provisioning
and cleanup remain separate concerns: a future worktree manager can prepare a
directory before dispatch and pass it through the same resolver and runtime.
Cleanup must refuse to remove worktrees containing uncommitted changes or
unpushed commits.
