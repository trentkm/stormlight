# Workspace resolvers

Stormlight resolves every working directory into three levels:

```text
workspace group
  execution root
    agent
```

The workspace ID controls grouping. The execution root identifies the checkout,
worktree, or other runnable directory used by an agent. An optional component
describes a package inside a larger workspace.

## Resolution order

Resolvers run in this order:

1. Executable resolvers from `~/.config/stormlight/resolvers`, sorted by filename.
2. The built-in Git resolver.
3. A canonical-directory fallback.

Set `STORMLIGHT_RESOLVERS_DIR` to use a different executable directory. External
resolvers run first so a monorepo or proprietary workspace can take precedence
over Git repositories nested inside it.

The Git resolver derives the workspace ID from the canonical
`git rev-parse --git-common-dir` path. Linked worktrees therefore appear in one
workspace group with distinct execution roots. Independent clones remain
separate groups.

## Executable protocol

Each non-hidden executable in the resolver directory is invoked as:

```text
resolver resolve <canonical-directory>
```

The process working directory is also set to the canonical directory.

Exit status `0` means the resolver matched and must emit one JSON object on
standard output. Exit status `2` means the resolver is not applicable. Any
other status is logged as a resolver failure and resolution continues.

Example:

```json
{
  "id": "monorepo:/home/me/src/example",
  "kind": "monorepo",
  "name": "example",
  "root": "/home/me/src/example",
  "execution_root": "/home/me/src/example",
  "component_name": "payments",
  "component_root": "/home/me/src/example/services/payments",
  "metadata": {
    "profile": "development"
  }
}
```

`kind` and `root` are required. `id` defaults to `<kind>:<root>`, `name`
defaults to the root directory name, and `execution_root` defaults to `root`.
All returned paths must exist and be directories. IDs must remain stable for
directories that belong in the same workspace group.

Resolvers may also enumerate every runnable location in a workspace:

```text
resolver roots <canonical-workspace-root>
```

Exit status `0` must emit a JSON array of workspace context objects. Every
object must carry the same workspace ID and root, with its own
`execution_root`. Exit status `2` means enumeration is not applicable. Other
failures are logged and Stormlight falls back to the resolved context.

An execution-root context may set `metadata.execution_root_label` to control
the directory picker's label and the agent's dashboard badge, including for
the primary root. Without it, custom non-primary roots use
`root <directory-name>`.

Resolvers are trusted local executables and run during dispatch and workspace
catalog loading. Stormlight caches successful resolution for the life of the
process. Root enumeration runs only on the resolver that claimed the workspace,
has a one-second deadline, and caches successful, unsupported, and failed
outcomes for five seconds. A failed or timed-out inventory never blocks
dashboard refresh; it logs the failure and exposes the resolved primary root.

## Resolution on another machine

Resolution is filesystem work: `git rev-parse`, a directory that has to
exist, an executable resolver in a host's own configuration directory. So
for a workspace on another machine it runs on that machine, through
`stormlight _resolve <path>` over SSH — the same chain, that host's
resolvers, its answer. `--roots` asks the same question about every
runnable checkout.

Two rules follow from the asymmetry:

- **Paths are not canonicalized here.** `EvalSymlinks` and `Stat` describe
  the wrong filesystem. A path can exist on both machines and mean
  different repositories, so a local answer about a remote path looks
  entirely correct and quietly merges two workspaces into one.
- **An unreachable host is an error, not a directory.** Local resolution
  falls back to a plain directory context, which asserts that the directory
  is there. Nothing on this side can assert that about another machine, so
  a host that cannot answer resolves to a failure rather than to a
  workspace that looks perfectly ordinary and describes nothing.

The host is stamped by the side that asked, because a resolver answers
about its own machine and has no name for it. Stamping also qualifies the
workspace ID — `devbox:git:/srv/api/.git` — so the same path on two
machines stays two workspace groups. Paths themselves are never rewritten;
they belong to the host.
