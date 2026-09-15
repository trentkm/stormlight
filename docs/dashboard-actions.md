# Dashboard actions

Dashboard actions let a managed agent ask the user's local dashboard to run a
trusted executable. Stormlight transports an opaque JSON request between two
plugin phases; the plugin owns every domain-specific decision.

This boundary is deliberately narrow. Stormlight does not inspect repository
layouts, classify paths, choose desktop applications, or translate a remote
path into an application-specific URL.

## Installation

An action is an executable named for the action under:

```text
~/.config/stormlight/actions/<name>
```

`$XDG_CONFIG_HOME` replaces `~/.config` when set.
`$STORMLIGHT_ACTIONS_DIR` overrides the complete actions directory.

Names are at most 64 ASCII characters. The first character must be a
lowercase letter or digit; the rest may also contain `.`, `-`, and `_`.
Stormlight resolves exactly that filename and requires it to be executable.
A request cannot provide an arbitrary command path.

For a local agent, one executable handles both phases. For an agent on
another machine, install the action under the same name on both machines:
`prepare` runs where the agent and its files live, while `handle` runs where
the dashboard and the user's desktop live. The executables may differ by
platform as long as they implement the same payload contract.

Actions are trusted user-installed programs. Both phases inherit the
environment of the Stormlight process that invokes them.

## Requesting an action

From a managed agent:

```text
stormlight action <name> [args...]
```

`STORMLIGHT_ID` selects the managed agent. Outside its process environment,
pass `--id <agent-id>`.

Stormlight invokes:

```text
<action> prepare [args...]
```

The process working directory is the caller's current directory. It must
write one JSON value to standard output. That value is opaque to Stormlight
and may be an object, array, string, number, boolean, or `null`. Empty output,
invalid JSON, and payloads larger than 256 KiB fail the request. Diagnostics
belong on standard error.

On success, Stormlight records the action name, payload, and a fresh request
id in the managed agent's metadata. No files or executables cross the daemon
boundary.

## Handling an action

The local dashboard notices the pending request and invokes:

```text
<action> handle
```

The process working directory is the actions directory. Standard input is one
JSON object:

```json
{
  "protocol": 1,
  "action": "review-changes",
  "host": "devbox",
  "agent": {
    "id": "7dcb5b78",
    "name": "review the payment change",
    "cwd": "/srv/work/payment"
  },
  "payload": {
    "paths": ["/srv/work/payment"]
  }
}
```

`host` is omitted for a local agent. `agent.cwd` and every path inside the
payload remain in the agent machine's namespace; Stormlight never rewrites
them. The payload is exactly the JSON value emitted by `prepare`.

Exit status zero reports success. A nonzero exit reports standard error, or
standard output when standard error is empty, in the dashboard. The handler
has 30 seconds to finish. Successful standard output is ignored.

The dashboard handles one action at a time and acknowledges the exact request
id after the handler returns, whether it succeeded or failed. Delivery is not
an exactly-once transaction: a dashboard crash or two dashboards observing
the same request can repeat a handler. Actions with non-idempotent effects
should use the agent id and their own payload identity to suppress duplicates.

## Security model

The managed agent controls the action name, arguments to its local `prepare`
phase, and the resulting payload. The dashboard will run only the matching
executable already installed in its own actions directory. Installing an
action grants managed agents the capability represented by that executable;
do not install actions you do not trust.

Stormlight validates the name and JSON envelope, caps the transported
payload, applies the handler deadline, and acknowledges the request. The
plugin remains responsible for validating its payload and safely invoking
every external tool it controls.
