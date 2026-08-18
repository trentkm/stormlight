# Architecture

`stormlight` separates agent semantics from terminal and process ownership.

## Layers

### Provider adapters

Provider adapters translate a task into an executable plus arguments. The
built-ins are Claude and Codex; custom provider specs cover other agent CLIs.
Adapters deliberately do not own process or terminal behavior.

The CLI adapters currently add provider-native lifecycle callbacks:

- Codex: per-launch prompt and stop hooks report state, passed as a `-c`
  config override, over the external completion notifier. The notifier alone
  carried only turn ends, so a turn begun in the agent's own terminal was
  invisible; it is retained because Codex holds injected hooks inert behind
  a one-time trust review, and an agent reporting nothing would sit at
  `working` until its process exited. The notifier has no trust gate, so it
  is the floor and the hooks are the ceiling. Both surfaces report a turn
  end once trusted; the two events carry identical state, so applying both
  is idempotent.
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
and delegates terminal operations to the runtime. The TUI, the CLI, and the
HTTP API all use the same service. A persistent workspace catalog supplies
workspaces that do not currently contain an agent.

Workspace resolvers return a stable group ID, a group root, an execution root,
and optional component metadata. External executable resolvers run before the
built-in Git resolver, followed by a canonical-directory fallback.
Resolvers may also enumerate a group's current execution roots; the application
service exposes that same inventory to the CLI and dashboard directory picker.
Enumeration is routed back to the resolver that claimed the workspace and is
bounded, cached, and best-effort, so a failed external integration cannot block
dashboard refresh. This keeps environment-specific workspace semantics outside
the public runtime.

### HTTP API

`stormlight serve` exposes the application service to clients that are not a
terminal — a browser front end first among them. It is a peer of the TUI, not
a layer above or below it: both drive the same service, so a rule about what a
dispatch means or when an agent needs attention is written once.

Two planes share the listener and never mix. The control plane is JSON over
HTTP — roster, workspaces, dispatch, history — where latency is irrelevant.
The data plane is one WebSocket per attached terminal carrying raw bytes in
both directions: the daemon's exact snapshot arrives first as state, then live
output, and keystrokes travel back unwrapped. Terminal input is never queued
behind a control request and never encoded as JSON, because everything about
how typing feels lives on that path. Sizes and resync notices ride the same
socket as text messages, which are rare and never in the typing path.

The dashboard and a browser can hold the same agent at once, each seeded with
its own exact snapshot, because the daemon owns the terminal rather than any
client. What they cannot have is their own geometry: an agent has one terminal,
so the newest viewer's size becomes everyone's, and the others are told over
their own streams and repaint. Sharing the size is what keeps the replicas
identical — the alternative is each client rendering a different reflow of the
same program's output.

The server binds loopback only and requires a per-run token on every request
and upgrade — these routes dispatch agents and stream terminals in every
workspace the catalog knows. Reaching them from another machine is a tunnel
the operator opens deliberately, never a default. WebSocket upgrades also
check `Origin`, so a page the user merely visited cannot reach the API just
because it is on localhost.

### windrunner runtime

Process and terminal ownership live in a daemon built on the
[windrunner](https://github.com/trentkm/windrunner) session engine. Each
session the daemon holds is one PTY plus an authoritative terminal
emulator, so a snapshot of an agent's screen and scrollback is exact
serialized state, and attaching is a byte stream from that state forward —
never a capture of whatever happened to be visible.

The daemon is Stormlight itself: `internal/windrun.NewRuntime` connects to
a unix socket (`daemon.sock` under `$WINDRUNNER_DIR`, else
`$XDG_STATE_HOME/windrunner`, else `~/.local/state/windrunner` — the
windrunner library's own default, so `windrunner ls` lists Stormlight's
agents too) and, when nothing answers, starts `stormlight _wrdaemon` from
the running binary's resolved path (`internal/selfpath`) and waits for it
to come up. The daemon outlives every dashboard: agents, their terminals,
and their scrollback persist across dashboard restarts, and the daemon's
sessions are the roster — there is no second record to reconcile.

The daemon never learns what an agent is; that is the library's boundary.
Agent identity and state ride in the session's opaque metadata as one JSON
document under the `stormlight_agent` key — the serialized `agent.Agent`:
id, provider, task, name, workspace context, permission mode, activity,
attention, mark, session id, and transcript path. Two rules keep the
document honest:

- Liveness and exit are the daemon's facts. Listing decodes the document
  and then overwrites process state from the session itself — `Alive`,
  `ExitCode` — so stale metadata can never claim a dead agent is working.
  An exited process with no recorded completion is classified from its
  exit code.
- Updates are read-modify-write on the whole document
  (`Runtime.mutateAgent`). Two near-simultaneous writers — a hook event
  racing the dashboard — can lose one update; events are sparse enough
  that the next one repairs it, and a daemon-side compare-and-swap is
  noted for later.

Dispatch spawns the provider directly: the adapter's launch command becomes
the session's process, with `STORMLIGHT_ID` (how hook subprocesses name
their agent), `STORMLIGHT_BIN` (how they find Stormlight across upgrades),
and `WINDRUNNER_DIR` (how a hook's own `stormlight _provider-event`
invocation reaches the same daemon) in its environment. There is no
supervisor process between the daemon and the provider; exit state is read
from the daemon. The daemon names the session; that id is not adopted as
the agent's id — the metadata carries Stormlight's id, and the session id
fills the display fields that expect a pane handle.

Messages are delivered as terminal input: multi-line messages inside a
bracketed paste so they arrive as one message, slash commands typed
verbatim (providers ignore pasted slash commands), then a beat later the
Enter that submits. Nothing is ever interpolated into a shell command
string.

#### Remote hosts

A host that is not this machine runs its own daemon, and the dashboard
reaches it over SSH. That is the whole of the design: the daemon runs
where the agents run, so an agent's PTY, its provider process, its hooks,
its transcript file, and the repository it is working in all stay on one
machine, and what crosses the network is the windrunner wire protocol and
nothing else. Closing the laptop stops nothing.

- `remote.Transport` (`internal/remote`) dials `ssh <host> stormlight
  _wrbridge`, wraps the subprocess's stdio as a `net.Conn`, and hands it
  to windrunner's client, whose transport is injectable. The real `ssh`
  is executed rather than reimplemented, so the user's own config comes
  along: keys, agent, `ProxyJump`, `ControlMaster`, and a tailnet name
  where one is in use. Multiplexing is on by default — a client opens one
  connection for the control plane, one for the event feed, and one per
  attached terminal.
- `_wrbridge` is the far side. It runs `EnsureDaemon` on that host — the
  one thing no tunnel can do for the far end — writes a single greeting
  line naming its protocol version, its own binary path, and its socket
  directory, and then splices its stdio onto the daemon's socket.
- The greeting exists because dispatch has to describe the machine the
  agent runs on. `STORMLIGHT_BIN` and `WINDRUNNER_DIR` are how a hook
  subprocess finds its way back, and this machine's answers are wrong for
  both.
- The environment underneath is the far side's. A dashboard's own environ
  describes a different host — its PATH, its home directory, its
  secrets — so a remote dispatch sends only the variables it means, as
  windrunner's `EnvOverride`, and the daemon over there supplies the rest.
- `F` runs `ssh -t <host> stormlight _wrattach`, carrying the socket
  directory explicitly: a tty session runs a login shell where a bridge
  does not, and the two can disagree about where XDG state lives.
- An agent's transcript is written by its provider, beside its repository,
  on its own host — so `session.FileReader` is a runtime capability for
  reading a file where the agent is (`stormlight _read` over the same
  SSH, size-capped on the far side so a cap never pays for the bytes it
  discards). Deliberately not a channel in the wire protocol, which is
  about terminals and knows nothing of files. Remote renders are cached
  for a beat, because this runs on the refresh path; the live screen
  below the divider comes from the terminal snapshot and is never cached,
  so output in flight still arrives every frame.

The trust boundary comes along unchanged. The wire protocol has no
authentication of its own and needs none: reaching the socket means being
the user who owns it, enforced by directory permissions locally and by the
host's own login remotely, with the bridge running as that user. Nothing
binds a port. This is also why the transport is SSH rather than a
tailnet-native listener — Tailscale is a network the transport runs over,
and `stormlight remote add` works on one without a line of code about it.

One dashboard holds several daemons at once. `internal/fleet` is a
`session.Runtime` over the local daemon and one per configured host: it
merges rosters, stamps each agent with the daemon that answered for it,
and routes every later call back to that one. Two rules shape it. A host
that cannot be reached costs its own absence and nothing else — never a
failed refresh, never a frame spent waiting on SSH, and a failed member is
left alone for a retry window rather than dialled on every tick. And an
agent's host is discovered by asking rather than remembered, so ownership
is rebuilt from every listing and never persisted anywhere.

Dispatch follows the workspace it resolved: the context already names the
machine, because resolution happened there. The name is also what
qualifies workspace IDs.

A host is known because something names it, not because it was
configured: a workspace on it, a dispatch aimed at it, a name picked out
of `~/.ssh/config` in the Add Workspace modal, which offers this machine
first and then whatever that file names. Members exist at start-up for the machines the
catalog says hold a workspace — listing names no host, so without that a
machine's agents would be invisible until something mentioned one — and
any other joins the moment it is first named. `[hosts.<name>]` is where a
machine differs from its name: a different destination, a binary a
non-interactive shell will not find, extra ssh flags.

#### The terminal seam

Live terminals reach the dashboard through one narrow seam, declared as an
optional runtime capability:

- `session.TerminalStreamer` (`internal/session`) is the contract: attach
  to an agent's terminal at a size and get a `TerminalStream` — an exact
  snapshot seed, a channel of everything after it, and input and resize
  flowing back.
- `windrun.Runtime.AttachTerminal` implements it over one dedicated daemon
  connection per attachment, resizing first so the snapshot arrives
  pre-wrapped for the view it is about to fill.
- `app.Service.AttachTerminal` surfaces the capability to the UI as a
  `pty.Transport`, failing cleanly when a runtime cannot stream.
- `ptyview.Manager` keeps one live terminal per agent for the agent's
  whole life, reconciling the set against the roster on every refresh:
  agents without a terminal get one, departed agents lose theirs, and all
  of them follow the pane's dimensions. Selecting an agent switches which
  terminal is rendered; it never starts one.
- `pty.Model` (`internal/pty`) is the widget: a `charmbracelet/x/vt`
  emulator fed by the transport, with scrollback, coalesced frame
  notifications (~30fps) so a chatty agent cannot flood the event loop,
  and wheel-burst batching for high-resolution scrolling.

The Spanreed pane renders the selected agent's widget. While the pane holds
focus the keyboard belongs to the agent's terminal byte for byte, and the
real terminal cursor is placed where the agent's program put it — the pane
is the terminal, not a picture of one. The dashboard's own keys in that
mode are modifier chords the hosted TUIs don't bind: `ctrl+space` steps
out, `alt+j`/`k` moves the roster cursor so the portal swaps terminals
under the keyboard, `alt+z` zooms the grid over the full body, and `alt+t`
flips to the transcript reading view. Typing into an agent's terminal is
the strongest form of having seen its result, so attention clears on the
way through.

`F` is the full-screen escape hatch: the runtime's `Attach` returns a
command (`stormlight _wrattach <session>`) that the dashboard runs through
`tea.ExecProcess`, suspending itself while the attachment owns the whole
terminal; `ctrl+q` detaches and the dashboard returns, reasserting every
widget's size because the attached client resized the daemon's sessions.

The transcript view renders the conversation from the provider's own JSONL
transcript once hooks have reported its path — the terminal screen is all a
snapshot can see of an alternate-screen agent, so the transcript file is
the only complete history. The renderer paints it: prompts, replies, tool
calls, and trimmed results take the palette in `internal/theme`, and the
markdown Claude writes is read back as styling by Glamour, against a
stylesheet built from that same palette in
`internal/provider/markdown.go`. Glamour's own wrapping is switched off —
the pane is resizable, so line breaking belongs to the pane, which knows
the current width. While a turn is in flight the live screen is appended
under a divider so streaming output stays visible; an agent with no
transcript falls back to its terminal snapshot.

External overlays — the Yazi directory picker and the Neovim task editor —
float over the dashboard in a popup: the program runs in its own
runtime-owned session (`session.OverlayHost`, a runtime capability beside
`TerminalStreamer`), and the dashboard renders its terminal through the
same widget as the Spanreed, composited into a modal frame. The keyboard
belongs to the floating program while it is up — `Ctrl-q` cancels, which
destroys the session.

Results come back through the session, not through a file. The directory
picker runs on the machine whose directories are being chosen, and Yazi
answers through files it writes beside itself — paths that are neither
readable nor meaningful anywhere else. So `stormlight _pick` runs Yazi
where it belongs, reads those files where they are, and writes the answer
into its own session's metadata; the dashboard reads it before `Close`
takes the session with it. The session is the one thing both ends already
hold, so the same path works local and remote, and an overlay asking for
Stormlight by an empty program path gets whichever machine's copy it
lands on. The task editor stays file-based: it is seeded with the task as
it stands, so it needs a file written before it opens, and it only ever
edits text this dashboard already holds. Overlay sessions carry no
agent document, so the roster never sees them, and Close always removes
them from the daemon: an overlay resurrected from persistence would be a
ghost with nothing waiting on its exit.

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
engagement: a new prompt, typing into the agent's terminal, replying,
interrupting, paging through the result while it is on screen, or an
explicit mark-seen. Navigating between panes and rows is deliberately not
engagement — those are the keys a human presses on the way past a result,
and counting them cleared the amber before it was ever read. The runtime
refuses to let a soft signal downgrade an urgent state. Exited agents
carry no attention — their exit status is the story.

This prevents a resumable completed conversation from being conflated with a
currently running process, and keeps "needs me now" distinct from "idle on
me" and from "just idle".

Entry into the amber inbox is stamped, by either route, and the stamp is
what the attention sort orders by: first in, first out. It records entry
rather than the latest signal, so a summary or an escalation arriving
mid-wait does not send an agent to the back of a line it never left. An
agent leaves the inbox only through engagement, never through mere
navigation.

A mark is the one signal nothing derives. Everything above is inference, and
inference is sometimes wrong, so a human can say otherwise (`m` in the
dashboard, `stormlight mark` outside it) and the mark outranks the derived
reading everywhere it is displayed or counted. The two marks retire
differently, because different parties can answer them: a working mark claims
the agent is still running, which the agent settles as soon as it reports
anything, so the next state-bearing update retires it; an attention mark
claims the human has something to return to, which no provider event can
answer, so only an explicit clear or the same engagement that clears amber
takes it down. Like attention, a mark stops applying once the process has
exited.

## Persistence

Session metadata in the daemon is the source of truth for the running
roster, and the daemon's persistence is what makes it durable: agents
outlive every dashboard because their PTYs and terminals never belonged to
one. The workspace catalog is an atomic JSON file independent of the
daemon, as are the dashboard's column preferences. Its entries carry the
machine they are on, because a path stopped identifying a workspace once
one could be on another: the same directory on two hosts is two
workspaces, named and removed separately. Entries for a host that is
asleep stay in the catalog and resolve when it comes back; a host that
cannot answer costs its own rows and not the listing.

The daemon's lifetime bounds the roster's. A reboot or a killed daemon
takes the processes and their terminals with it — but not the
conversations. `internal/history` keeps an append-only session log
(`sessions.jsonl`) recording every session id the providers ever report,
with the task, workspace, and transcript path; the log is compacted once
per dashboard launch, off every event path. A provider adapter's `Resume`
maps a session id — the one the agent's hooks reported, or failing that
the one the provider's transcript naming encodes — to a launch that
reopens the conversation, and that launch carries no prompt: a resumed
agent idles at its composer. Nothing resumes work by itself. An agent that
never reported a turn has no session id and no transcript, so there is
nothing to reopen — re-dispatching its original task would be a materially
different act performed under the same name. The dashboard's history
browser (`H`) serves the same records long after their agents are deleted.

## Workspace boundary

Workspace discovery is intentionally outside provider adapters. Provisioning
and cleanup remain separate concerns: a future worktree manager can prepare a
directory before dispatch and pass it through the same resolver and runtime.
Cleanup must refuse to remove worktrees containing uncommitted changes or
unpushed commits.
