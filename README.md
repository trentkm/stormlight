# Stormlight

A workspace-native control surface for coding agents.

`stormlight` dispatches Claude, Codex, and custom-configured coding agents
onto a small daemon that owns each agent's PTY and terminal. Each agent is
one provider conversation.
The dashboard organizes them as `Workspaces | Agents | Spanreed`, showing
live process state, recent interaction, and agents that need attention,
running directly in your own terminal. The Spanreed pane is your link to the
selected agent — its live terminal, its transcript, your replies, and its
requests — named for the long-distance writing instrument in the books
Stormlight itself is named after.

Workspace-aware grouping keeps agents from the same repository together while
preserving the checkout or worktree each agent is using.

## Design

- A dedicated daemon owns process and terminal lifetime, and nothing else.
  It is built on [windrunner](https://github.com/trentkm/windrunner), hosted
  by the same `stormlight` binary (`stormlight _wrdaemon`), and started on
  demand over a unix socket — nothing else needs installing or configuring.
- Each agent is one daemon session; PTYs the daemon owns, with an
  authoritative terminal emulator per agent, so a snapshot is exact and an
  attach is a byte stream rather than a screen scrape.
- The dashboard can exit without stopping any agent. Agents, their terminals,
  and their scrollback persist in the daemon across dashboard restarts — the
  daemon's sessions *are* the roster.
- Metadata rides in the session itself, as one JSON document the daemon
  stores without reading; liveness and exit status are the daemon's own
  facts and always override what the document claims.
- Provider adapters construct commands; the daemon spawns them directly and
  is provider-neutral.
- Messages are delivered into the agent's PTY as a bracketed paste, never
  interpolated into shell command strings.
- Every agent keeps a live terminal in the dashboard — seeded with an exact
  snapshot, then fed the live byte stream — so selecting an agent switches
  terminals rather than starting one. A transcript reading view sits behind
  `t`.
- Custom provider specs give unsupported agent CLIs a fallback.
- Workspace resolvers are provider-neutral and external resolvers are
  supported.

The Spanreed pane shows the selected agent's real terminal, live. Walking
into it (`Enter` or `Ctrl-space`) hands the keyboard to the agent, byte for
byte; the same chord steps back out. From inside, `option`+`j`/`k` (`alt`
outside macOS) switches agents without stepping out, `option`+`z` zooms the
terminal over the whole body, and `option`+`t` flips to the transcript.
These are modifier chords the hosted TUIs don't bind, so a focused terminal
still receives every letter. When the embedded view is not enough, `F`
attaches the same terminal full-screen in your own terminal — every column
of the display — and `Ctrl-q` returns to the dashboard. Closing the
dashboard stops nothing: agents keep running in the daemon, terminals
intact for the next run.

Conversations outlive their agents. Both providers name every session with
an id their own `resume` command accepts, and Stormlight records it — with
the task, workspace, and transcript path — in an append-only log at
`$XDG_STATE_HOME/stormlight/sessions.jsonl`. Deleting an agent hands its
session to that log rather than erasing it: `H` opens the history browser,
and Enter reopens the selected conversation as a new managed agent in the
workspace it left, with everything it had already said and done.

## Requirements

- `yazi` for the directory picker (installed automatically by the Homebrew
  cask). Optional: `nvim` for task editing.

## Install

```bash
brew install trentkm/stormlight/stormlight
```

## Build from source

```bash
go build -o stormlight .
```

Install it somewhere on `PATH` before dispatching agents. Provider hooks
re-invoke Stormlight through `$STORMLIGHT_BIN` to report lifecycle state,
and the daemon is this same binary.

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

The dashboard refreshes all Stormlight-managed agents automatically, including
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
flags — `auto` (the default and the recommended way to run: never asks),
`edits` (file edits apply immediately, shell and network still ask), or
`ask` (approvals for consequential actions). In the New Agent form, press
`m` to cycle the mode; `auto` agents are marked with an `AUTO` badge in the
agent list. When an agent in a prompting mode does need an answer, the
dashboard raises attention on it and `Enter` walks into its terminal —
prompts are answered in the agent's own terminal, where the provider's real
UI lives.

Inspect and control agents:

```bash
stormlight list
stormlight list --json
stormlight attach <id>
stormlight send <id> "Run the focused test before wrapping up"
stormlight rename <id> "focused test fixer"
stormlight mark <id> attention
stormlight stop <id>
stormlight delete <id>
stormlight logs
```

Agent renames set the stored name, which the dashboard prefers over the
generated task title. Workspace renames (press `R` in the Workspaces pane)
are display-name overrides stored in the workspace catalog; the directory on
disk is untouched.

IDs may be shortened as long as the prefix remains unambiguous.

### Dashboard controls

| Key | Action |
|---|---|
| `h` / `l` | Move between Workspaces, Agents, and Spanreed |
| `j` / `k` | Move in the active pane; scroll the transcript |
| `gg` / `G` | Move to the first or last item |
| `Ctrl-d` / `Ctrl-u` | Move down or up half a page |
| `Ctrl-f` / `Ctrl-b` | Move down or up a full page |
| `<` / `>` | Narrow or widen the focused pane (persists across launches) |
| `z` | Toggle compact and expanded list rows |
| `Enter` | Enter Agents from Workspaces, or walk into the selected agent's terminal |
| `Ctrl-space` | Walk into or out of the terminal — the seam key, symmetric from both sides |
| `Z` | Zoom the terminal: the sidebars collapse and the keyboard walks in |
| `t` | Toggle the live terminal and the transcript reading view |
| `F` | Attach the selected agent full-screen in your terminal; `Ctrl-q` returns |
| `option`+`j` / `k` (in the terminal) | Switch agents without stepping out (`alt` outside macOS) |
| `option`+`z` / `option`+`t` (in the terminal) | Zoom; flip to the transcript |
| `n` | Add a workspace in Workspaces; create an agent in the selected workspace elsewhere |
| `o` | Create an agent with an explicit directory picker |
| `i` / `s` | Reply: the composer in transcript view, the terminal itself in terminal view |
| `Ctrl-v` (while replying) | Paste a clipboard image as a file path |
| `x` | Interrupt the selected agent |
| `Ctrl-x`, then `x` / `y` / `Enter` | Remove a workspace or delete an agent |
| `Ctrl-x`, then `X` | Delete a workspace **and all of its agents** |
| `R` | Rename the selected workspace or agent |
| `,` then `a` / `n` / `c` | Sort by attention, name, or newest (applies to both lists) |
| `m` | Mark the selected agent in progress or needs-attention (your own reading, overriding Stormlight's) |
| `M` | Mark the selected agent — or workspace — seen |
| `K` | Workspace info popup (resolver, roots, metadata) |
| `H` | Session history browser; Enter resumes a conversation |
| `?` | Full keybinding reference |
| `r` / `Ctrl-l` | Refresh |
| `q` | Close the dashboard |

The transcript view (`t`) is where written interaction lives. Press `i` or
`s` to open the reply box — it wraps and grows with your message, and stays
open between messages. Press `Enter` to send, `Ctrl-j` for a newline, and
`Esc` — or `Backspace` once the box is empty — to leave.
Press `/` to search the transcript (`n`/`N` between matches). Drag with the
mouse to highlight transcript lines — releasing copies them to the system
clipboard.
Provider slash commands (`/compact`, `/clear`, custom skills) work from the
reply box too — a single-line message starting with `/` is typed into the
agent as a command instead of pasted as text. In terminal view the agent's
own composer is right there, so `i` / `s` simply walks in.

Images paste too: press `Ctrl-v` while composing and Stormlight saves the
clipboard image to a temp file and inserts its path at the cursor — Claude
Code and Codex read image files referenced by path. On macOS this uses
[`pngpaste`](https://github.com/jcsalterego/pngpaste) when installed
(`brew install pngpaste`), falling back to AppleScript; on Linux it uses
`wl-paste` or `xclip`.

Approval and authentication prompts are answered in the agent's own terminal:
Stormlight raises attention and points at the terminal rather than
reimplementing the provider's prompt. While one is pending, the reply box
stands down — `i` / `s` redirects you to `Enter`, and a prompt arriving
mid-compose closes the composer and parks your draft for when you return. A
plain question is different: the agent is idle at its own composer, so a
Spanreed reply is the answer.

Pressing `n` in Agents or Spanreed inherits the current workspace context.
The centered form contains a vertical `Coding agent` picker, an optional name,
and a wrapping task composer. Use `j` / `k` to choose Codex, Claude, or another
configured coding agent, then press `Enter` to compose and `Enter` again to
launch. Since `Enter` launches, `Ctrl-j` is what breaks a line inside the task
composer — the same key the Spanreed reply box uses. Shell remains available
through `stormlight dispatch --provider shell`, but is not presented as a
conversational coding agent.

`Tab` reaches the name field, which `Enter` on the picker skips past. Leave it
empty and the agent is named after its task; fill it in and that name is what
the agent list carries (the same thing `R` sets afterward, and what
`stormlight dispatch --name` has always taken). The field drops out
of the form in panes too short to hold both it and the task composer.

Press `e` from the coding-agent picker or `Ctrl-o` from the task composer to
edit the task in Neovim. The dashboard suspends, Neovim takes the terminal,
and the saved text returns to the form.

Press `o` when the agent should run somewhere else. This opens the full
directory picker with known workspaces, worktrees, components, Yazi, and
interactive path entry. Use `j` / `k` or `gg` / `G` to select a location.
Pressing `e` on a location edits that path directly. In Yazi, `Enter` chooses
the highlighted directory (or a highlighted file's parent), `q` chooses
Yazi's current directory, and `Q` cancels. Yazi runs full-screen while the
dashboard suspends, and the dashboard returns the moment it closes.

`Enter a path` is an interactive `cd` from the current directory: type to
filter its subdirectories, `Tab` descends into the best match (arrows pick
another, then `Enter` descends), `Backspace` on an empty filter goes up, a
typed absolute or `~` path jumps there, and `Enter` with nothing highlighted
chooses the directory you are in.

The transcript view renders the conversation from the provider's own
transcript file once the agent's hooks have reported one — prompts, replies,
tool calls, and trimmed results in Stormlight's palette — appending the live
terminal screen while a turn is in flight so streaming output stays visible.
An agent without a transcript falls back to its terminal screen as-is.

## Workspaces

Stormlight resolves working directories automatically. Linked Git worktrees share
one workspace group, while each worktree remains a distinct execution root.
Independent clones and non-Git directories remain separate. Because a grouped
workspace can hold several checkouts, the Spanreed heading names the worktree
an agent is working in — `worktree fix-auth` beside its provider and state —
and stays silent for agents in the main checkout.

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

Selecting a row never changes what it reports: the cursor row keeps its glow
and its amber, painted onto the selection background rather than replaced by
it, so moving through the list can't make a working agent read as idle.

Every finished turn is classified from its final message (providers emit
the same event for "done" and "asked you something", so the content is the
discriminator): a closing question goes loud, anything else is an unseen
result. Amber is an inbox — it clears when you engage with the result:
replying, interrupting, or typing into the agent's terminal clears any tier;
paging through the transcript while it is on screen clears unseen; and `M`
marks the selected agent — or every agent in the selected workspace — seen
manually. Moving between panes and rows is not engagement — navigation is
how you leave a result, so it leaves the amber where it is.

### Marking an agent yourself

Stormlight infers all of this from provider hooks and process state, and it is
sometimes wrong: an agent grinding through a long tool call reads as idle, and
a result you want to revisit reads as nothing at all. Press `m` on an agent to
say otherwise. The picker offers two marks and a way out:

| Mark | Says | Look |
|---|---|---|
| `w` in progress | still going; nothing is pending on you | cyan `●` and the working glow, any attention stood down |
| `a` needs attention | come back to this one | amber `◆` in the waiting tier |
| `c` clear | hand the row back to Stormlight's reading | whatever Stormlight infers |

A mark outranks everything Stormlight inferred — in the row, in the workspace
counts, and in the header — and rows say `marked in progress` or
`marked needs attention` outright, so a corrected row never passes for an
inference. The `◆` is deliberately not the inferred tiers' `○` or `!`: at a
glance you can tell your own amber from Stormlight's.

The two marks retire differently, because different parties can answer them.
In-progress claims the agent is still running, which the agent settles the
moment it reports anything — its next hook event retires the mark. Needs-
attention claims *you* have something to come back to, which no provider event
can answer, so only you take it down: `M`, or engaging with the row the same
way engagement clears amber. A mark also stops applying when the process
exits, because an exit status is the whole story of a finished agent.

The same override is available outside the dashboard:

```bash
stormlight mark 0123abcd working
stormlight mark 0123abcd attention
stormlight mark 0123abcd none
```

Custom workspace types can override Git by installing executable resolvers in
`~/.config/stormlight/resolvers`. The protocol is public and does not require
Stormlight-specific code. See [workspace resolvers](docs/workspace-resolvers.md).

## Provider lifecycle

Stormlight injects agent-scoped lifecycle integration for managed providers:

- Codex uses `UserPromptSubmit` and `Stop` hooks to report state, with its
  `agent-turn-complete` notifier kept underneath them. Both providers speak
  the same hook schema, so a Codex agent prompted in its own terminal turns
  blue like a Claude one; the notifier alone only fired at the end of a
  turn, which left a manually prompted agent claiming `idle` for the whole
  time it was working.
- Claude uses `UserPromptSubmit`, `Notification`, and `Stop` hooks to report
  state; the permission notification raises attention on the agent whose
  terminal is holding the prompt.
- Replies sent from the dashboard mark any provider working immediately.

Stormlight registers observers, never resolvers. Neither Claude's `PreToolUse`
nor Codex's `PermissionRequest` hook is installed: their replies decide whether
a tool call proceeds, and approvals belong to the agent's own terminal.

Hooks are wired per launch, so nothing is written to `~/.claude` or
`~/.codex` — an agent Stormlight did not start behaves exactly as it always
did. The hook command reads `$STORMLIGHT_BIN`, which names Stormlight by its
launcher on `PATH` rather than the version-stamped path behind it, so hooks
keep working across an upgrade instead of pointing at a binary the new
release deleted.

### Trusting Codex hooks, once

Codex will not run an injected hook until a human has approved it. The first
Codex agent you dispatch opens a **Hooks need review** prompt in its own
terminal listing two hooks; answer **Trust all and continue** and Codex
records the approval in `~/.codex/config.toml`. Until then the hooks are
installed but inert.

The approval is a hash of the hook commands, and Stormlight's are identical
for every agent — they name Stormlight through `$STORMLIGHT_BIN` rather than
a path — so trusting once covers every future agent on that machine, across
upgrades. It only asks again if these hooks change.

An agent whose hooks are untrusted still reports the end of each turn,
because `agent-turn-complete` carries no such gate. That is why it is kept:
it is the floor that stops an untrusted agent from sitting at `working`
until its process exits. What you lose until you trust the hooks is the
turn-*start* signal — exactly the old behavior.

Stormlight will not pass Codex's `--dangerously-bypass-hook-trust`. That flag
lifts review from every enabled hook, including any a project's own
`.codex/config.toml` installs, which is a much wider grant than Stormlight
needs for its own two.

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

## The windrunner daemon

Agents live in a daemon built on the
[windrunner](https://github.com/trentkm/windrunner) session engine, hosted
by the same binary as `stormlight _wrdaemon` and started automatically the
first time anything needs it. It listens on a unix socket at
`~/.local/state/windrunner/daemon.sock` (honoring `$XDG_STATE_HOME`) — the
windrunner library's own default location, so the `windrunner` CLI's
`windrunner ls` sees Stormlight's agents too. Power users can inspect
sessions there directly.

The daemon owns the agents' processes, so its lifetime is theirs: a reboot
or a killed daemon takes the running processes and their terminals with it.
The conversations survive regardless — the providers keep their transcripts,
Stormlight keeps its session log — and the `H` history browser reopens any
of them as a new agent, idling at its composer.

Tests or parallel Stormlight instances can target an alternate daemon by
pointing `WINDRUNNER_DIR` at a different socket directory:

```bash
WINDRUNNER_DIR=/tmp/stormlight-test stormlight list
```
