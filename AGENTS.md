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
  than building on assumptions about how a provider or terminal behaves.

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
repo — which is exactly why they accumulate unnoticed. Tearing one down is
part of finishing the task, not a chore for later. The moment its PR merges:

```sh
git worktree remove .claude/worktrees/<branch> && git branch -d <branch>
```

Both halves matter. `git worktree remove` alone leaves the branch behind;
`git branch -d` alone leaves a checkout pinned to a ref nothing points at.
Use `-d`, never `-D` — the safe form refuses to delete anything not yet
merged, so it doubles as the check that the work really did land. If it
refuses, the branch has commits that never made it to `main`; find out why
before forcing anything.

A leftover worktree is not inert. It is a full checkout that Stormlight lists
as a distinct execution root, so stale ones crowd the dashboard alongside live
work, and the next agent cannot tell a finished task from an active one. Before
removing any worktree you did not create, check it the way you would before
deleting anyone's work:

```sh
git -C .claude/worktrees/<branch> status --porcelain   # uncommitted work?
git -C .claude/worktrees/<branch> log --oneline origin/main..HEAD   # unpushed?
```

Empty output from both means it is safe. Anything else belongs to a task still
in flight — leave it alone.

The reason is concurrency: more than one agent works this repo at a time. Two
agents sharing a checkout produce uncommitted changes neither can attribute,
and a commit from one sweeps up whatever the other had half-finished. A
worktree makes each task's diff its own.

Local `main` is never a place work happens. It is not checked out for edits,
not committed to, not branched from. The primary checkout exists to build the
everyday binary and to read from — nothing else.

A worktree isolates the tree and nothing else. The rest is on you:

- **The installed binary.** `~/.local/bin/stormlight` is a global singleton,
  and provider hooks re-invoke `stormlight` through `$STORMLIGHT_BIN`, which
  names the launcher on `PATH` — so an install from one worktree silently
  changes what every other worktree's agents run. Build into the worktree
  and prepend it to `PATH` for end-to-end runs; install to `~/.local/bin`
  only when updating the everyday binary is the point.
- **The windrunner daemon.** Parallel runs need separate socket directories
  (`WINDRUNNER_DIR=/tmp/stormlight-<branch>`), or they will list, resize,
  and kill each other's agents. The daemon also keeps running the code it
  started with: an already-running daemon serves your new build's dashboard
  with old daemon-side behavior, so daemon-side changes need a scratch
  `WINDRUNNER_DIR` — or a deliberately restarted daemon — to be seen at all.
- **XDG state**, exactly as the headless testing section below requires.

Linked worktrees share one `--git-common-dir`, so Stormlight groups them into
a single workspace while keeping each checkout a distinct execution root —
working this way dogfoods the feature.

## Dev loop

Inside a worktree, building the branch you are working on:

```sh
go build -o stormlight . && install -m 0755 stormlight ~/.local/bin/stormlight
```

Installing matters: provider hooks re-invoke `stormlight` through
`$STORMLIGHT_BIN`, which names the launcher on `PATH`, so a stale installed
binary means the dashboard and the agents disagree about the world.

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

### The web client

`web/` is the browser client: TypeScript and Svelte, built by Vite into
`web/dist`, which is embedded in the binary. `web/dist` is committed so a
checkout without Node still builds — which means **a change under
`web/src` is not finished until you rebuild it**:

```sh
npm --prefix web ci        # first time, or after a dependency change
npm --prefix web run build # after any change under web/src
```

CI rebuilds it and fails if the committed output differs, so a stale
`web/dist` is caught rather than shipped. The failure looks like a diff in
files you never edited; this is why.

Its own checks are `npm --prefix web test` (the terminal client's
protocol) and `npm --prefix web run check` (types). CI runs both, on
Node 22.

Watch the Node version. Newer Node exposes browser globals that CI's does
not — `sessionStorage` among them — so a module that reaches for one at
import time passes here and fails there. Reproduce the runner with:

```sh
NODE_OPTIONS=--no-experimental-webstorage npm --prefix web test
```

### Never read an agent through the roster in an effect

The server pushes the whole roster whenever any part of it changes, and
`fleet.agents` is replaced wholesale each time — so every agent object is
a new proxy several times a second. A prop written `agent.id` therefore
reaches a component as a *getter* whose dependency is that proxy, and an
effect that reads it depends on the roster rather than on the identity it
names. The value never changes. The thing it was read through does, on
every poll.

The cost is not subtle. The terminal pane cost one whole terminal per
poll: disposed and rebuilt, which is a new socket, a new seed, and a
replica rebuilt from nothing — the view landing on the first line of an
empty buffer and snapping back, about once a second. It read as flicker
and it took a long time to find, because nothing about the rendered rows
was ever wrong; only which rows were on screen.

So hold the identity apart from the object it came from, at the top of
the component:

```svelte
const id = $derived(agent.id);   // compared by value; the same string
                                 // disturbs nothing downstream
```

`WallCell` and `CanvasTile` have always done this. `Terminal` did not,
which is exactly why the wall stayed steady while the roster's pane
flickered. `web/src/components/terminal.test.ts` pins it: a push that
renames nothing must leave the terminal alone.

The general rule: anything long-lived that an effect builds — a socket,
an emulator, an observer — must key on a value, never on something read
through a roster object.

### Terminals are rebuilt whole or not at all

Replacing a replica is a reset followed by the screen and everything
scrolled off above it, which is far more than xterm's parser gets through
before it yields to the renderer. Painted halfway it is an empty buffer
with the view on its first line. Any write that replaces the terminal's
state is wrapped in a synchronized update (`ESC[?2026h` … `ESC[?2026l`)
so there is no halfway to paint — the same mechanism the agents use on
their own frames. Codex leans on it hard and Claude Code less so, which
is why codex flickered far worse than anything else.


## Releasing

CI is the only thing that publishes. A release happens when — and only
when — a `v*` tag lands on `origin`:

```sh
git fetch origin && git tag <version> origin/main && git push origin <version>
```

Merging a PR never releases anything; `ci.yml` runs the tests and stops. That
tag push is the sole path to publishing, and there is no `workflow_dispatch`
on the release workflow, so nothing can trigger it from the Actions UI either.

Tag `origin/main` explicitly. Tagging `HEAD` from a worktree ships whatever
that branch happens to be, which is not what anyone reviewed.

The corollary is the quiet failure mode: because the tag names `origin/main`,
work that has not merged yet is simply absent from the release. The tag is
valid, CI goes green, and the release ships without it — nothing looks wrong.
Confirm your PR is merged and `origin/main` contains it before tagging.

Cutting a release is a deliberate decision, not a step in finishing a task.
An agent that has landed a PR is done; it does not then tag. Releases are the
maintainer's call, and the rhythm is one release per batch of merged work
rather than one per merge — a version is a re-download for everyone tracking
the tap, so a release with nothing user-visible in it costs them bandwidth to
receive nothing. The test is whether you can name, in a sentence, what changed
for someone using it. Docs, tests, refactors, and CI changes ride along with
the next release that clears that bar.

Pick the number by reading what is already published — `git tag --sort=-creatordate | head -1` —
and what has landed since it, rather than assuming the next one.

**Never run `goreleaser release` locally.** It is the same pipeline CI runs,
so a local invocation does not preview the release — it *performs* one,
creating the GitHub release, uploading the archives, and pushing the cask to
the tap. Worse, it publishes from whatever your worktree holds rather than
from `origin/main`, which is the one thing the tagging rule above exists to
prevent. Every release from v0.1.0 through v0.2.2 failed this way: CI
arrived at a release the local run had already completed and died on the
first asset collision. `replace_existing_artifacts` in `release:` closes
that particular hole — it exists so a half-finished release can be re-run
rather than burning a version number — so today CI would overwrite the local
run's assets instead of erroring, which is quieter but no better. To check
the config without publishing, use `goreleaser release --snapshot --clean`,
which builds into `dist/` and touches nothing remote.

The workflow needs one secret beyond the automatic `GITHUB_TOKEN`:
`HOMEBREW_TAP_GITHUB_TOKEN`, a fine-grained PAT with `contents: write` on
`trentkm/homebrew-stormlight`. Without it goreleaser builds and publishes the
release, then fails pushing the cask — a half-shipped version where the
binaries exist but `brew upgrade` never sees them.

What lands in the tap is a cask, not a formula: `Casks/stormlight.rb`, whose
`on_macos`/`on_linux` blocks name a prebuilt archive per platform. Casks are
the supported shape for shipping a binary — goreleaser's `brews` is
soft-deprecated as of v2.10 and removed in v2.16 — and Homebrew's cask code
is cross-platform, so Linux keeps its `brew install` path. `.goreleaser.yaml`
carries the reasoning at the `homebrew_casks` block, along with the two
things the cask does beyond naming URLs: it depends on `yazi`, and its
post-install hook strips the quarantine attribute on macOS so an unsigned
binary is not stopped by Gatekeeper on first run.

A published version is immutable. The cask pins a `sha256` for each of the
four archives, so re-tagging a version that users may already have fetched
swaps the bytes under a checksum Homebrew has cached and breaks installs with
a mismatch. A bad release is superseded by the next version, never rewritten.

Versions are semver with a pre-1.0 reading: user-visible features bump the
minor, fixes bump the patch.

The minor number has no ceiling and is not a countdown. `0.x` means exactly
what semver says it means — no stability promise, anything may change at any
time — so v0.9.0 and v0.27.0 are ordinary places to be, and moving from 0.2
to 0.3 costs nothing. Staying in `0.x` is what buys the freedom the
Philosophy section above assumes: breaking refactors stay welcome precisely
because no version has promised otherwise.

1.0.0 is therefore a decision, not a milestone reached by incrementing. It
declares that the interface — the dashboard's keys, the workspace catalog
format, the state files — is stable enough that breaking it costs a major
bump. Cut it when that promise is one worth keeping, not when the minor
number looks large.

## The website

`site/` is the public site, deployed to GitHub Pages by `pages.yml` whenever
a push to `main` touches it. It is plain static HTML — no build step — and
its docs pages are hand-rendered copies of `docs/*.md`, not generated from
them, so nothing keeps the two in sync automatically.

That makes drift the failure mode to watch for. A change that lands in
`docs/architecture.md`, `docs/workspace-resolvers.md`, or the config design
doc without reaching its `site/docs/*.html` counterpart leaves the website
describing an architecture that no longer exists — worse than no docs,
because it reads as authoritative. When a PR materially changes what those
documents say — new layers, renamed contracts, changed state semantics —
update the matching site page in the same PR. Section headings in the site's
pages carry `id` anchors that the sidebar links target; keep them consistent
when adding or removing sections. The landing page (`site/index.html`) tells
the shorter story and only needs touching when the user-facing pitch changes:
a new pane, a renamed concept, a different install path.

Cosmetic drift matters less than semantic drift. Wording differences between
a doc and its site rendering are fine; a site page claiming a contract the
code no longer honors is not.

## Headless TUI testing

The dashboard and agent lifecycle can be exercised without a real terminal.
tmux is not something Stormlight runs on — agents live in the windrunner
daemon — but a scratch tmux server remains the outer harness of choice: it
provides a pane to launch the dashboard in, drive with `send-keys`, and
read with `capture-pane`.

- Start an isolated server:
  `tmux -L <scratch-socket> -f /dev/null new-session -d -x 120 -y 34`.
- When an *attached client* is required (glyph fidelity — see below): run
  `tail -f /dev/null | script -q <log> tmux -L <sock> attach`
  in the background. The `tail` pipe is required — a closed stdin sends EOF
  into the pane shell, which kills the session and (via exit-empty) the
  server. The `script` log captures the full client byte stream.
- Drive the UI with `send-keys`; read it with `capture-pane -p`.
- **Launch the binary by absolute path**, never by name off a `PATH` you
  set on the session:

  ```sh
  tmux -L <sock> send-keys "$PWD/stormlight" Enter   # yes
  tmux -L <sock> ... -e PATH="$PWD:$PATH"            # no
  ```

  `-e PATH=...` reaches the session environment, and then the pane's login
  shell sources its rc and puts `~/.local/bin` back in front — so `stormlight`
  resolves to the *installed* binary and the branch build under test never
  runs. It fails silently and looks exactly like the change not working. When
  a fix passes its tests but the dashboard disagrees, confirm which binary is
  running before believing the screen: point `STORMLIGHT_LOG_FILE` at a
  scratch file and read the `command started` line, which names the version.
- **Isolate all state**: set `XDG_CONFIG_HOME`, `XDG_STATE_HOME`, and
  `WINDRUNNER_DIR` for every end-to-end run. `WINDRUNNER_DIR` gives the run
  its own daemon socket directory: without it the test dashboard connects to
  your real daemon, its dispatches land among your real agents, and your
  real agents crowd every capture. The scratch daemon outlives the run —
  auto-started daemons persist by design — so kill it when the test is done.
  The workspace catalog lives in
  `$XDG_STATE_HOME/stormlight/workspaces.json` and is mutated by
  add/remove/rename flows — an unisolated test run will edit your real
  catalog. Seed it by copying a catalog the app itself wrote, not by
  hand-authoring one: the file is an object, a plausible-looking array is
  rejected at load, and the dashboard then starts with no workspaces and
  every subsequent capture shows a screen you did not mean to test.
- **`capture-pane` on a session with no attached client is not what the user
  sees.** With no client, tmux has no UTF-8 hint and downgrades box drawing,
  so rounded corners come back as `┌` where a real terminal shows `╭`. Bubble
  Tea v2 also declines to assume capabilities it cannot confirm, so a
  clientless run degrades further than v1 did. Assert on glyphs only through
  the attached-client recipe above; a plain detached capture is fine for
  layout and text, not for appearance.

Two habits make the difference between measuring the program and measuring
the harness. Reproduce in a Go test before trusting a capture — the same
render path, driven directly, is deterministic where a live session carries
state from whatever you pressed earlier. And when a branch and `main` seem to
differ, build both and diff their captures rather than reading one and
reasoning about the other; `diff` on two `capture-pane` dumps settles in a
second what a screenshot argues about for an hour.
