# Working on Stormlight

Guidance for coding agents and contributors. Design rationale lives in
[README.md](README.md) (the Design section) and [docs/](docs/); this file
covers how work gets done.

## Philosophy

Stormlight is pre-release. There are no users to break yet, so build things
the right way the first time:

- No half measures, no backwards-compatibility shims, no quick hacks layered
  on wrong designs. When a fix reveals a design flaw, fix the design, not the
  symptom.
- Breaking refactors are welcome.
- Verify real behavior empirically (run the thing, capture the bytes) rather
  than building on assumptions about how tmux or a provider behaves.

## Tracking work

- Outstanding work is tracked as GitHub issues. File follow-ups with
  `gh issue create`, reference issues in commits, and close them
  (`Fixes #N` or `gh issue close`) when the work lands.
- Commit and push completed work as it lands; don't let it pile up.
- Commits carry no AI attribution — no `Co-Authored-By` trailers.

## Work in a worktree

Every task starts in its own git worktree, cut from the freshly fetched
`origin/main` — never in the primary checkout, and never from local `main`:

```sh
git fetch origin
git worktree add -b <branch> --no-track .claude/worktrees/<branch> origin/main
```

Fetch every time. Branching off local `main` inherits whatever that checkout
last saw, which is stale the moment anyone else's PR merges; `origin/main`
after a fetch is the real tip. `--no-track` is not optional — a branch created
from a remote-tracking ref otherwise adopts `origin/main` as its upstream, and
a later `git push` either refuses or aims at the wrong branch. With it, the
branch starts upstreamless and `git push -u origin <branch>` does the right
thing.

While the work is in flight, keep the branch stacked on current main. Fetch
and rebase before you push:

```sh
git fetch origin && git rebase origin/main
```

That keeps the PR a linear diff against the tip rather than a merge knot, and
surfaces conflicts while the change is still yours to reshape.

`/.claude/` is gitignored, so worktrees parked there stay invisible to the
repo. Remove one once its work has landed:

```sh
git worktree remove .claude/worktrees/<branch> && git branch -d <branch>
```

The reason is concurrency: more than one agent works this repo at a time. Two
agents sharing a checkout produce uncommitted changes neither can attribute,
and a commit from one sweeps up whatever the other had half-finished. A
worktree makes each task's diff its own.

Local `main` is never a place work happens. It is not checked out for edits,
not committed to, not branched from. The primary checkout exists to build the
everyday binary and to read from — nothing else.

A worktree isolates the tree and nothing else. The rest is on you:

- **The installed binary.** `~/.local/bin/stormlight` is a global singleton,
  and managed tmux windows re-invoke `stormlight` from `PATH` — so an install
  from one worktree silently changes what every other worktree's agents run.
  Build into the worktree and prepend it to `PATH` for end-to-end runs;
  install to `~/.local/bin` only when updating the everyday binary is the
  point.
- **The tmux server.** Parallel runs need separate sockets
  (`STORMLIGHT_TMUX_SOCKET=stormlight-<branch>`), or they will list, resize,
  and kill each other's agents.
- **XDG state**, exactly as the headless testing section below requires.

Linked worktrees share one `--git-common-dir`, so Stormlight groups them into
a single workspace while keeping each checkout a distinct execution root —
working this way dogfoods the feature.

## Dev loop

Inside a worktree, building the branch you are working on:

```sh
go build -o stormlight . && install -m 0755 stormlight ~/.local/bin/stormlight
```

Installing matters: managed tmux windows re-invoke `stormlight` from PATH for
lifecycle tracking, so a stale installed binary means the dashboard and the
agents disagree about the world.

Refreshing the everyday binary to current `main` is the one job the primary
checkout still does, and it advances `main` read-only to do it:

```sh
git fetch origin && git merge --ff-only origin/main
go build -o stormlight . && install -m 0755 stormlight ~/.local/bin/stormlight
```

`--ff-only` is the whole safeguard. It fast-forwards when local `main` is
merely behind and fails outright if the two have diverged — which, since
nothing is ever committed there, means something is wrong and wants a look
rather than a merge commit. Building without the fetch is how you install a
binary from whatever main happened to be last week; run them together.

Run tests with `go test ./...`.

## Headless TUI testing

The dashboard and agent lifecycle can be exercised without a real terminal:

- Start an isolated server:
  `tmux -L <scratch-socket> -f /dev/null new-session -d -x 120 -y 34`.
- When an *attached client* is required (tmux popups only render for attached
  clients): run `tail -f /dev/null | script -q <log> tmux -L <sock> attach`
  in the background. The `tail` pipe is required — a closed stdin sends EOF
  into the pane shell, which kills the session and (via exit-empty) the
  server. The `script` log captures the full client byte stream, including
  popup contents that `capture-pane` can't see.
- Drive the UI with `send-keys`; read it with `capture-pane -p`.
- For server post-mortems, start tmux with `-vv` and read the
  `tmux-server-*.log` it drops in the working directory.
- **Isolate all state**: set both `XDG_CONFIG_HOME` and `XDG_STATE_HOME` for
  every end-to-end run. The workspace catalog lives in
  `$XDG_STATE_HOME/stormlight/workspaces.json` and is mutated by
  add/remove/rename flows — an unisolated test run will edit your real
  catalog.

## tmux version notes

tmux >= 3.3 is required; overlays (Yazi, Neovim) open as popups only on
tmux >= 3.7 because `display-popup` on older tmux crashes the whole server
when the hosted program queries cursor state
([tmux/tmux#4942](https://github.com/tmux/tmux/issues/4942)). The gate lives
in `internal/tmux/surface.go`.
