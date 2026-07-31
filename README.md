# Runstead

A workspace-native control surface for coding agents.

`runstead` dispatches Claude, Codex, and arbitrary shell tasks into isolated
windows on your existing tmux server. Each agent is one provider conversation.
The dashboard organizes them as `Workspaces | Agents | Interaction`, showing
live process state, recent interaction, and agents that need attention without
nesting another tmux instance or owning your terminal.

Workspace-aware grouping keeps agents from the same repository together while
preserving the checkout or worktree each agent is using.

## Design

- tmux owns process and terminal lifetime.
- Each agent is one tagged tmux window in the `runstead-agents` session.
- The dashboard can exit without stopping any agent.
- Metadata is stored as tmux window options, not inferred from window names.
- Provider adapters construct commands; tmux lifecycle is provider-neutral.
- Workspace resolvers are provider-neutral and external resolvers are supported.
- Pane messages use tmux buffers, avoiding command interpolation.
- The Interaction pane combines structured actions with sampled terminal output.
- Claude permission requests can be resolved without leaving the dashboard.
- The shell adapter gives unsupported CLIs a generic fallback.

No nested tmux server is created. An outside-tmux launch transparently hosts
the dashboard in a temporary session on the existing tmux server; an
inside-tmux launch uses the current client directly. Selecting an agent
switches that client to its window. Press your tmux prefix followed by `Q` to
return to the dashboard. Runstead installs this binding only when `Q` is
unbound or already owned by Runstead. While the prefix is active, the managed
session highlights the tmux status bar and shows `Q` for return and `?` for the
full tmux key list. Closing the dashboard removes only its temporary session.

## Build

```bash
go build -o runstead .
```

Install it somewhere on `PATH` before dispatching agents. Managed tmux windows
invoke the same binary to track process lifecycle.

```bash
install -m 0755 runstead ~/.local/bin/runstead
```

## Upgrading from Agentmux

Runstead discovers existing `agentmux-agents` windows and reads their legacy
tmux metadata in place. Opening or updating one of those agents migrates its
metadata to the Runstead schema without restarting it. `AGENTMUX_*` environment
variables remain fallback aliases, and `~/.config/agentmux/resolvers` is used
when the new resolver directory does not exist.

Keep an `agentmux` command alias during the transition so hooks already running
inside existing agents can still invoke their configured executable path. New
tmux state uses only the Runstead names; managed processes also receive legacy
environment aliases for existing custom hooks.

## Use

Open the dashboard:

```bash
runstead
```

The dashboard refreshes all runstead-managed windows automatically, including
agents started by `runstead dispatch` in another shell. It does not adopt
Claude or Codex conversations that were started directly outside Runstead.

Dispatch directly:

```bash
runstead dispatch --provider codex --cwd ~/src/project \
  "Investigate and fix the flaky integration test"

runstead dispatch --provider claude --cwd ~/src/project \
  "Review the current branch for correctness"

runstead dispatch --provider shell --cwd ~/src/project \
  "go test ./..."
```

Inspect and control agents:

```bash
runstead list
runstead list --json
runstead attach <id>
runstead send <id> "Run the focused test before wrapping up"
runstead stop <id>
runstead delete <id>
runstead logs
```

IDs may be shortened as long as the prefix remains unambiguous.

### Dashboard controls

| Key | Action |
|---|---|
| `h` / `l` | Move between Workspaces, Agents, and Interaction |
| `j` / `k` | Move in the active pane; scroll Interaction |
| `gg` / `G` | Move to the first or last item |
| `Ctrl-d` / `Ctrl-u` | Move down or up half a page |
| `Ctrl-f` / `Ctrl-b` | Move down or up a full page |
| `z` | Toggle compact and expanded list rows |
| `Enter` | Enter Agents from Workspaces, or open the selected agent terminal |
| tmux prefix, then `Q` | Return from an agent to the dashboard |
| `n` | Add a workspace in Workspaces; create an agent in the selected workspace elsewhere |
| `o` | Create an agent with an explicit directory picker |
| `i` / `s` | Compose a message in Interaction |
| `x` | Interrupt the selected agent |
| `d`, then `d` / `y` / `Enter` | Remove a workspace or delete an agent |
| `r` / `Ctrl-l` | Refresh |
| `q` | Close the dashboard |

In Interaction, press `i` or `s`, type a message, and press `Enter` to send it.
Press `Esc` to cancel composition. Normal-mode `Enter` opens the complete
provider terminal for controls that cannot be represented inline.

Claude permission requests replace the transcript with an inline action. Use
`j` / `k` and `Enter`, or press `y` to allow once, `a` to accept Claude's
persisted permission suggestion, `n` to deny, or `t` to review the request in
the native terminal. If the dashboard closes while a request is pending,
Runstead stops handling it and Claude presents its native prompt.

Pressing `n` in Agents or Interaction inherits the current workspace context.
The centered form contains only `Coding agent` and `Input`; use `h` / `l` to
choose a provider, then `Enter` to move to the input and launch.

Press `o` when the agent should run somewhere else. This opens the full
directory picker with known workspaces, worktrees, components, Yazi, and manual
path entry. Use `j` / `k` or `gg` / `G` to select a location. Pressing `e` on a
location edits that path directly. In Yazi, `Enter` chooses the highlighted
directory (or a highlighted file's parent), `q` chooses Yazi's current
directory, and `Q` cancels. Yazi opens as a tmux popup over the dashboard,
including when Runstead was launched from a regular shell. Direct terminal
takeover remains a fallback when the dashboard cannot be hosted in tmux.

The Interaction pane retains provider terminal colors while removing startup
chrome and the inactive prompt/status area for Claude and Codex. Shell agent
output remains unfiltered.

## Workspaces

Runstead resolves working directories automatically. Linked Git worktrees share
one workspace group, while each worktree remains a distinct execution root.
Independent clones and non-Git directories remain separate.

Press `n` in the Workspaces pane to open the Add Workspace modal. Yazi and
manual path entry are the only actions; current workspaces appear below as
read-only context. The catalog is stored at
`~/.local/state/runstead/workspaces.json` by default. Workspaces containing
active agents remain visible even when they are not in the catalog.

Compact rows are the default and show the primary workspace or agent line.
Press `z` to reveal the path and resolver or provider details. Narrow terminals
always use expanded rows and show one full-width pane at a time.

Custom workspace types can override Git by installing executable resolvers in
`~/.config/runstead/resolvers`. The protocol is public and does not require
runstead-specific code. See [workspace resolvers](docs/workspace-resolvers.md).

## Provider lifecycle

Runstead injects agent-scoped lifecycle integration for managed providers:

- Codex uses its external `agent-turn-complete` notification to become idle and
  publish the latest response summary.
- Claude uses `UserPromptSubmit`, `PermissionRequest`, permission notification,
  and `Stop` hooks to report state and bridge approval choices into Interaction.
- Replies sent from the dashboard mark any provider working immediately.

The runtime also exposes an `event` command so other provider hooks can report
semantic state without screen scraping:

```bash
runstead event --state working --summary "Running focused tests"
runstead event --state idle
runstead event --state idle --attention approval \
  --summary "Needs permission to access the network"
```

Managed processes receive `RUNSTEAD_ID`, so `--id` is optional inside a
provider hook.

## Diagnostics

Runstead writes structured JSON Lines diagnostics without prompt text or pane
contents. Logs rotate at 5 MiB with one backup.

```bash
runstead logs
runstead logs --lines 25
runstead logs --path
runstead --log-level debug
```

Override the location with `--log-file` or `RUNSTEAD_LOG_FILE`.

## Isolated tmux socket

Tests or alternate tmux servers can be targeted without changing the default
server:

```bash
RUNSTEAD_TMUX_SOCKET=runstead-test runstead list
```

The value maps to `tmux -L <name>`.
