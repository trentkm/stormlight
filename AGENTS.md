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

## Dev loop

```sh
go build -o stormlight . && install -m 0755 stormlight ~/.local/bin/stormlight
```

Installing matters: managed tmux windows re-invoke `stormlight` from PATH for
lifecycle tracking, so a stale installed binary means the dashboard and the
agents disagree about the world.

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
