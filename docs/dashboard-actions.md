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

Closing standard output is answering: a plugin still running a second after
it has closed its output is stopped, and its answer stands. A helper it left
behind holding its output is cut off the same way once the plugin exits.

On success, Stormlight appends the action name, payload, and a fresh request
id to the managed agent's queue, which lives in the agent's metadata document.
No files or executables cross the daemon boundary. An agent may have at most
8 requests waiting; a ninth is refused with an error that says so, because a
queue that only grows means no dashboard is running to drain it.

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
  "request": "3f9c2a7d1b6e4c08",
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

`host` is omitted for a local agent. `request` is the request's id, the key
for a handler that must recognise a request it has already run. `agent.cwd`
and every path inside the payload remain in the agent machine's namespace;
Stormlight never rewrites them. The payload is exactly the JSON value emitted
by `prepare`.

Exit status zero reports success. A nonzero exit reports standard error, or
standard output when standard error is empty, in the dashboard. The handler
has 30 seconds to finish. Successful standard output is ignored.

A handler often starts something that outlives it — an editor, a browser —
and what it starts inherits its output pipes. Stormlight reads those for one
second after the handler exits and then closes them; it does not wait for the
editor to quit. A program that needs its output read should not be left
behind holding the handler's pipes.

Both kinds of dashboard run actions: the terminal dashboard, and
`stormlight serve`, whether or not a browser is connected to it.

Each dashboard handles one action at a time. Before running a request it
claims it — a conditional write on the agent's document, refused if any
other writer has moved the document since it was read, and rebuilt on the
current state until it lands — so of two dashboards reaching for the same
request exactly one runs it. Requests are handled in the order the agent made
them, and a request another dashboard holds blocks the ones behind it rather
than being skipped. After the handler returns, whether it succeeded or
failed, the dashboard retires the exact request id; the ones queued since
stay.

A claim stands for five minutes, longer than the longest a live dashboard
can spend running and retiring one request. A dashboard that dies mid-run
leaves its claim behind, and once it is that old another dashboard takes the
request over and runs it again. The same happens to a dashboard that ran the
handler but could not retire the request — retiring is tried three times, so
this takes a daemon that stays unreachable through all of them. Those are
the paths to a repeated handler: a dashboard lost, not ordinary concurrency.
Actions whose effects must not repeat even then should remember the
`request` id they were handed.

Claims are stamped with the claiming dashboard's clock and compared on
whichever dashboard reads them. Two machines watching one remote host whose
clocks disagree by more than the five-minute claim can both run a request;
keep dashboards on synchronised time.

## Security model

The managed agent controls the action name, arguments to its local `prepare`
phase, and the resulting payload. The dashboard will run only the matching
executable already installed in its own actions directory. Installing an
action grants managed agents the capability represented by that executable;
do not install actions you do not trust.

Stormlight validates the name and JSON envelope, caps the transported
payload, bounds both phases (60 seconds for `prepare`, 30 for `handle`), and
claims and retires the request. The plugin remains responsible for validating
its payload and safely invoking every external tool it controls — and its
payload is written by whatever runs beside the agent, so a plugin must treat
it as untrusted input, including when the agent is on another machine.
