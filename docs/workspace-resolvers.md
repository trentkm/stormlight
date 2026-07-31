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

Resolvers are trusted local executables and run during dispatch and workspace
catalog loading. Stormlight caches successful results for the life of the
process.
