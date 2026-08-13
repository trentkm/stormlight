# Stormlight configuration — design proposal

Status: implemented (2026-07-31) — `internal/config`, `stormlight config`,
`stormlight config init`. This document remains as the design rationale; the
README's Configuration section is the user-facing reference.
Decisions: TOML; `~/.config/stormlight/` for everything;
workspace-as-directory philosophy (below); custom providers via
`[providers.*]` with first-class adapters kept in-tree.

## What config is — and is not — for

A workspace is a directory. How agents behave inside it belongs to the tree
itself (`CLAUDE.md` / `AGENTS.md`, read natively by the provider CLIs), and
what groups directories into one workspace belongs to resolvers. The config
file therefore holds only *user preferences about Stormlight's own behavior*:
which provider and permission mode to reach for, how the UI looks, where the
logs go. It never defines workspace semantics and never injects agent
context.

## Principles

1. **Zero-config.** The file is optional; every setting has the default the
   binary ships with today. `brew install` → `stormlight` must keep working
   with no file present.
2. **One precedence rule everywhere:** `flags > environment > config file >
   built-in defaults`. This already half-exists (`envFirstOr` in `main.go`);
   the config file slots in as the third layer.
3. **Config never breaks determinism.** Config keys select among behaviors
   Stormlight owns (which mode, which provider, which binary) — they do not
   open the door to arbitrary per-machine drift in how managed agents run.
4. **Config cannot silently escalate permissions.** Nothing a repository
   checkout contains may raise an agent's permission mode (see "Project-local
   config" below).

## File location

`~/.config/stormlight/config.toml`, honoring `$XDG_CONFIG_HOME`. Everything
Stormlight owns lives under one directory each for config and state —
dev-tool convention (git, gh, nvim do the same on macOS), and one directory
to document:

```
~/.config/stormlight/
  config.toml     # user configuration (this proposal)
  resolvers/      # executable workspace resolvers (existing)
~/.local/state/stormlight/
  workspaces.json # workspace catalog (existing)
  sessions.jsonl  # session history log (existing)
```

## Format

TOML, parsed with `pelletier/go-toml/v2`. Rationale: comments (a config file
that can't explain itself is a bug), obvious sectioning, no significant
whitespace, the standard choice in the Go/Homebrew tooling ecosystem. JSON
has no comments; YAML has too many footguns for a file this small.

## Schema (v1)

```toml
# ~/.config/stormlight/config.toml — all keys optional.

[defaults]
provider = "claude"            # codex | claude | shell
mode     = "edits"             # ask | edits | auto

[ui]
rows = "compact"               # compact | expanded

[log]
level = "info"                 # debug | info | warn | error
# file = "…"                   # overrides the default log location

[tools]
# yazi = "/opt/homebrew/bin/yazi"   # override PATH lookup
# nvim = "/opt/homebrew/bin/nvim"

# Per-workspace overrides, keyed by workspace root.
# Applied when the dispatch modal opens and by `stormlight dispatch`
# when the corresponding flag is not passed.
[workspaces."/Volumes/repos/stormlight"]
mode     = "auto"
provider = "claude"

# Provider adapter tweaks. extra_args append after Stormlight's own flags,
# so they can refine but not remove lifecycle hooks.
[providers.codex]
# binary     = "codex"
# extra_args = ["--model", "o4"]

[providers.claude]
# binary     = "claude"
# extra_args = []

# User-defined providers (task #6). args is an exec-style array with a
# {task} placeholder — never a shell string. mode_args are optional; a mode
# without args is still recorded and badged, but only the CLI's own flags
# enforce anything. Lifecycle integration is the public contract: the
# process gets STORMLIGHT_ID and can report via `stormlight event`.
[providers.aider]
# label  = "Aider"
# binary = "aider"
# args   = ["--message", "{task}"]
# [providers.aider.mode_args]
# auto = ["--yes-always"]
```

## Wiring

- New `internal/config` package: `config.Load()` → `Config` struct with the
  defaults filled in; called once in `main.go` before command construction.
- Cobra flag defaults are *initialized from* the loaded config
  (`envFirstOr(cfg.Log.File, "STORMLIGHT_LOG_FILE")` pattern), so the
  precedence rule falls out of the existing machinery instead of being
  reimplemented per flag.
- Validation errors name the key and file
  (`config.toml: defaults.mode: invalid permission mode "always"`), and a
  broken config falls back to defaults with a visible warning rather than
  refusing to start — the dashboard is also how you'd notice the problem.
- `stormlight config` subcommand: prints the effective merged config and the
  file path; `stormlight config init` writes a fully commented template.

## Project-local config (deliberately deferred)

A `.stormlight.toml` in a repo root ("this project defaults to codex") is
attractive for teams but dangerous: a cloned repository must never be able to
grant itself `mode = "auto"`. If we add it later, it needs direnv-style trust
("stormlight noticed project config, run `stormlight trust` to apply") and a
hard rule that permission mode can only be *lowered* by untrusted project
files. v1 keeps overrides in the user's own config file, keyed by path.

## Open questions for Trent

1. TOML OK? (Recommendation: yes.)
2. Per-workspace overrides in v1, or defer with project-local config?
   (Recommendation: include — it's cheap and feeds the "this repo is trusted,
   default to auto" workflow you already want.)
