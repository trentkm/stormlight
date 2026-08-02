# Stormlight

A workspace-native control surface for coding agents.

`stormlight` dispatches Claude, Codex, and custom-configured coding agents
into isolated windows on a private, Stormlight-owned tmux server. Each agent is one provider
conversation.
The dashboard organizes them as `Workspaces | Agents | Spanreed`, showing
live process state, recent interaction, and agents that need attention without
nesting another tmux instance or owning your terminal. The Spanreed pane is
your written link to the selected agent — its transcript, your replies, and
its requests — named for the long-distance writing instrument in the books
Stormlight itself is named after.

Workspace-aware grouping keeps agents from the same repository together while
preserving the checkout or worktree each agent is using.

## Design

- tmux owns process and terminal lifetime, and nothing else.
- Stormlight runs tmux as a private appliance: a dedicated server
  (`tmux -L stormlight`) with Stormlight's own configuration. User dotfiles,
  plugins, and session-restore tools never load there, so behavior is
  identical on every machine.
- Each agent is one tagged tmux window in the `stormlight-agents` session.
- The dashboard can exit without stopping any agent.
- Metadata is stored as tmux window options, not inferred from window names.
- Provider adapters construct commands; tmux lifecycle is provider-neutral.
- Agent runtimes and dashboard presentation surfaces are separate interfaces.
- Workspace resolvers are provider-neutral and external resolvers are supported.
- Pane messages use tmux buffers, avoiding command interpolation.
- The Spanreed pane combines structured actions with sampled terminal output.
- Claude permission requests can be resolved without leaving the dashboard.
- Custom provider specs give unsupported agent CLIs a fallback.

An outside-tmux launch transparently hosts the dashboard in a temporary
session on the Stormlight server; a launch from inside your own tmux uses the
current pane directly and reaches agents through a nested client. Selecting an
agent switches to its window. Press the tmux prefix followed by `Q` to return
to the dashboard. Stormlight installs this binding only when `Q` is unbound or
already owned by Stormlight. While the prefix is active, the managed session
highlights the tmux status bar and shows `Q` for return and `?` for the full
tmux key list. Closing the dashboard removes only its temporary session;
agents keep running on the Stormlight server.

## Requirements

- tmux 3.3 or newer. tmux 3.7+ is strongly recommended: Yazi and Neovim
  overlays open as tmux popups only on 3.7+, because `display-popup` in older
  tmux crashes the whole tmux server when the hosted program queries cursor
  state ([tmux/tmux#4942](https://github.com/tmux/tmux/issues/4942), fixed in
  3.7). Stormlight detects the version and falls back to full-screen takeover
  on older tmux instead of risking the server.
- Optional: `yazi` for the directory picker, `nvim` for task editing.

## Install

```bash
brew install trentkm/stormlight/stormlight
```

The formula declares `depends_on "tmux"`, so installs get a popup-safe tmux
(>= 3.7) without thinking about any of this.

## Build from source

```bash
go build -o stormlight .
```

Install it somewhere on `PATH` before dispatching agents. Managed tmux windows
invoke the same binary to track process lifecycle.

```bash
install -m 0755 stormlight ~/.local/bin/stormlight
```

## Use

Open the dashboard:

```bash
stormlight
```

Open it on a directory — the path is added as a workspace (if it isn't one
already) and selected, `code .`-style:

```bash
stormlight .
stormlight ~/src/project
```

The dashboard refreshes all Stormlight-managed windows automatically, including
agents started by `stormlight dispatch` in another shell. It does not adopt
Claude or Codex conversations that were started directly outside Stormlight.

Dispatch directly:

```bash
stormlight dispatch --provider codex --cwd ~/src/project \
  "Investigate and fix the flaky integration test"

stormlight dispatch --provider claude --cwd ~/src/project \
  "Review the current branch for correctness"

stormlight dispatch --provider claude --mode auto --cwd ~/src/project \
  "Fix every lint warning"
```

Each agent launches with a permission mode that maps onto the provider's own
flags — `ask` (approvals for consequential actions), `edits` (the default:
file edits apply immediately, shell and network still ask), or `auto` (never
asks). In the New Agent form, press `m` to cycle the mode; `auto` agents are
marked with an `AUTO` badge in the agent list. Claude approval requests keep
arriving in the Spanreed pane in `ask` and `edits` modes.

Inspect and control agents:

```bash
stormlight list
stormlight list --json
stormlight attach <id>
stormlight send <id> "Run the focused test before wrapping up"
stormlight rename <id> "focused test fixer"
stormlight stop <id>
stormlight delete <id>
stormlight logs
```

Agent renames set the tmux window name, which the dashboard prefers over the
generated task title. Workspace renames (press `R` in the Workspaces pane)
are display-name overrides stored in the workspace catalog; the directory on
disk is untouched.

IDs may be shortened as long as the prefix remains unambiguous.

### Dashboard controls

| Key | Action |
|---|---|
| `h` / `l` | Move between Workspaces, Agents, and Spanreed |
| `j` / `k` | Move in the active pane; scroll Spanreed |
| `gg` / `G` | Move to the first or last item |
| `Ctrl-d` / `Ctrl-u` | Move down or up half a page |
| `Ctrl-f` / `Ctrl-b` | Move down or up a full page |
| `z` | Toggle compact and expanded list rows |
| `Enter` | Enter Agents from Workspaces, or open the selected agent terminal |
| `Ctrl-6` | Return from an agent to the dashboard (vim's alternate-buffer toggle; also shown in the agent status bar, which is clickable) |
| tmux prefix, then `Q` | Return from an agent to the dashboard (tmux-native fallback) |
| `n` | Add a workspace in Workspaces; create an agent in the selected workspace elsewhere |
| `o` | Create an agent with an explicit directory picker |
| `i` / `s` | Write a reply in Spanreed |
| `Ctrl-v` (while replying) | Paste a clipboard image as a file path |
| `x` | Interrupt the selected agent |
| `Ctrl-x`, then `x` / `y` / `Enter` | Remove a workspace or delete an agent |
| `Ctrl-x`, then `X` | Delete a workspace **and all of its agents** |
| `R` | Rename the selected workspace or agent |
| `,` then `a` / `n` / `c` | Sort by attention, name, or newest (applies to both lists) |
| `M` | Mark the selected agent — or workspace — seen |
| `K` | Workspace info popup (resolver, roots, metadata) |
| `?` | Full keybinding reference |
| `r` / `Ctrl-l` | Refresh |
| `q` | Close the dashboard |

In Spanreed, press `i` or `s` to open the reply box — it wraps and grows
with your message, and stays open between messages. Press `Enter` to send,
`Ctrl-j` for a newline, and `Esc` to leave.
Provider slash commands (`/compact`, `/clear`, custom skills) work from the
reply box too — a single-line message starting with `/` is typed into the
agent as a command instead of pasted as text. Normal-mode `Enter` opens the
complete provider terminal for controls that cannot be represented inline.

Images paste too: press `Ctrl-v` while composing and Stormlight saves the
clipboard image to a temp file and inserts its path at the cursor — Claude
Code and Codex read image files referenced by path. On macOS this uses
[`pngpaste`](https://github.com/jcsalterego/pngpaste) when installed
(`brew install pngpaste`), falling back to AppleScript; on Linux it uses
`wl-paste` or `xclip`.

Claude permission requests replace the transcript with an inline action. Use
`j` / `k` and `Enter`, or press `y` to allow once, `a` to accept Claude's
persisted permission suggestion, `n` to deny, or `t` to review the request in
the native terminal. If the dashboard closes while a request is pending,
Stormlight stops handling it and Claude presents its native prompt.

Pressing `n` in Agents or Spanreed inherits the current workspace context.
The centered form contains a vertical `Coding agent` picker and a wrapping task
composer. Use `j` / `k` to choose Codex, Claude, or another configured coding
agent, then press `Enter` to compose and `Enter` again to launch. Shell remains
available through `stormlight dispatch --provider shell`, but is not presented as
a conversational coding agent.

Press `e` from the coding-agent picker or `Ctrl-o` from the task composer to
edit the task in Neovim. The saved text returns to the form. Neovim opens in a
tmux popup when the current surface supports popups and otherwise temporarily
takes over the terminal.

Press `o` when the agent should run somewhere else. This opens the full
directory picker with known workspaces, worktrees, components, Yazi, and
interactive path entry. Use `j` / `k` or `gg` / `G` to select a location.
Pressing `e` on a location edits that path directly. In Yazi, `Enter` chooses
the highlighted directory (or a highlighted file's parent), `q` chooses
Yazi's current directory, and `Q` cancels. Yazi opens as a tmux popup over
the dashboard, including when Stormlight was launched from a regular shell.
Direct terminal takeover remains a fallback when the dashboard cannot be
hosted in tmux.

`Enter a path` is an interactive `cd` from the current directory: type to
filter its subdirectories, `Tab` descends into the best match (arrows pick
another, then `Enter` descends), `Backspace` on an empty filter goes up, a
typed absolute or `~` path jumps there, and `Enter` with nothing highlighted
chooses the directory you are in.

The Spanreed pane retains provider terminal colors while removing startup
chrome and the inactive prompt/status area for Claude and Codex. Shell agent
output remains unfiltered.

## Workspaces

Stormlight resolves working directories automatically. Linked Git worktrees share
one workspace group, while each worktree remains a distinct execution root.
Independent clones and non-Git directories remain separate.

Press `n` in the Workspaces pane to open the Add Workspace modal. Yazi and
manual path entry are the only actions; current workspaces appear below as
read-only context. The catalog is stored at
`~/.local/state/stormlight/workspaces.json` by default. Workspaces containing
active agents remain visible even when they are not in the catalog, so the
ordinary confirmation only removes agent-free workspaces. For a workspace
that still has agents, the confirmation demands a deliberate capital `X`,
which deletes the workspace and every agent in it.

Compact rows are the default and show the primary workspace or agent line.
Press `z` to reveal the path and resolver or provider details. Narrow
terminals show one full-width pane at a time and honor the same row density.

Rows never rearrange on their own: the default order is newest first, and
`,` opens a yazi-style sort chord (`a` attention, `n` name, `c` newest).

Attention has two tiers, and it always outranks the working glow:

| Signal | Meaning | Look |
|---|---|---|
| working | agent is running | cyan glow sweeping the name |
| waiting | finished with a result you haven't seen | soft amber `○` marker and count |
| question / approval / auth | agent is blocked on an explicit decision | loud amber `!` — the whole row |

Every finished turn is classified from its final message (providers emit
the same event for "done" and "asked you something", so the content is the
discriminator): a closing question goes loud, anything else is an unseen
result. Amber is an inbox — it clears when you engage: replying or opening
the terminal clears any tier, viewing the result (a keypress while it's
selected with its transcript on screen) clears unseen, and `M` marks the
selected agent — or every agent in the selected workspace — seen manually.

Custom workspace types can override Git by installing executable resolvers in
`~/.config/stormlight/resolvers`. The protocol is public and does not require
Stormlight-specific code. See [workspace resolvers](docs/workspace-resolvers.md).

## Provider lifecycle

Stormlight injects agent-scoped lifecycle integration for managed providers:

- Codex uses its external `agent-turn-complete` notification to become idle and
  publish the latest response summary.
- Claude uses `UserPromptSubmit`, `PermissionRequest`, permission notification,
  and `Stop` hooks to report state and bridge approval choices into Spanreed.
- Replies sent from the dashboard mark any provider working immediately.

The runtime also exposes an `event` command so other provider hooks can report
semantic state without screen scraping:

```bash
stormlight event --state working --summary "Running focused tests"
stormlight event --state idle
stormlight event --state idle --attention approval \
  --summary "Needs permission to access the network"
```

Managed processes receive `STORMLIGHT_ID`, so `--id` is optional inside a
provider hook.

## Configuration

Stormlight runs with no configuration. An optional
`~/.config/stormlight/config.toml` (honoring `$XDG_CONFIG_HOME`) supplies
defaults; precedence is always flags > environment > config file > built-in
defaults.

```bash
stormlight config init   # write a fully commented template
stormlight config        # print the effective merged configuration
```

```toml
[defaults]
provider = "claude"        # what the New Agent form starts on
mode     = "edits"         # ask | edits | auto

[tmux]
socket      = "stormlight"
return_keys = ["C-6", "C-^"]

[workspaces."~/repos/trusted-project"]
mode = "auto"              # per-directory dispatch defaults (matches subdirs)
```

A workspace is a directory. How agents behave inside it belongs to the tree
itself (`CLAUDE.md` / `AGENTS.md`, read natively by the provider CLIs);
config only holds preferences about Stormlight's own behavior.

### Custom providers

First-class providers (Claude, Codex) keep in-tree adapters that integrate
with their extension surfaces — hooks, notifications, permission bridging.
Any other agent CLI can be declared in config:

```toml
[providers.aider]
label  = "Aider"
binary = "aider"
args   = ["--message", "{task}"]   # exec-style; {task} is replaced verbatim
[providers.aider.mode_args]
auto   = ["--yes-always"]          # prepended per permission mode
```

Declared providers appear in the New Agent picker and dispatch like any
other. Lifecycle integration is the public contract: managed processes
receive `STORMLIGHT_ID` and can report state with `stormlight event` from
any hook mechanism the CLI provides. Stormlight never reimplements an agent.

## Diagnostics

Stormlight writes structured JSON Lines diagnostics without prompt text or pane
contents. Logs rotate at 5 MiB with one backup.

```bash
stormlight logs
stormlight logs --lines 25
stormlight logs --path
stormlight --log-level debug
```

Override the location with `--log-file` or `STORMLIGHT_LOG_FILE`.

## The Stormlight tmux server

Stormlight talks to `tmux -L stormlight` by default and boots that server with
its own managed configuration (rewritten at startup at
`~/.config/stormlight/tmux.conf`). Power users can inspect it directly:

```bash
tmux -L stormlight list-sessions
tmux -L stormlight attach -t stormlight-agents
```

Tests or parallel Stormlight instances can target an alternate server:

```bash
STORMLIGHT_TMUX_SOCKET=stormlight-test stormlight list
```

The value maps to `tmux -L <name>`. Setting `--tmux-socket ""` targets the
default tmux server with your own configuration; Stormlight never applies its
managed config there.
