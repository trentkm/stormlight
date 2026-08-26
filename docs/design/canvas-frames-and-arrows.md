# The canvas grows up: frames and arrows

How to build the two things that turn the canvas from an arrangement of
terminals into a surface that *means* something: frames that group and
lock tiles, and arrows between tiles that send. Issues
[#214](https://github.com/trentkm/stormlight/issues/214) and
[#215](https://github.com/trentkm/stormlight/issues/215); both are part
of the canvas epic [#170](https://github.com/trentkm/stormlight/issues/170).

The order is deliberate: multi-select, then frames, then arrows that fire
by hand, then arrows that fire on their own. Each phase is a PR that
ships on its own and is useful on its own, and each is the prerequisite
the next one leans on. Nothing here touches the terminal contract — a
tile is still a watcher that never names a size — and only the last
phase touches Go.

## What exists

The canvas is `web/src/components/Canvas.svelte` and `CanvasTile.svelte`
over a pure-geometry module, `web/src/lib/canvas.ts`, and a per-workspace
layout store, `web/src/lib/layout.svelte.ts`. Three facts about it shape
everything below:

- **The stage is a point, and the camera is one transform.** Tiles are
  absolutely positioned in stage units; `translate(x, y) scale(z)` on the
  stage element is the whole view. Anything drawn on the canvas — a frame,
  an arrow — is another child of the stage in stage units, and pans and
  zooms for free.
- **The layout store is one JSON object per workspace in localStorage**,
  `stormlight.canvas.<workspace-id>`, holding `Record<agentID, Box>`.
  `load()` validates every box against shared limits and discards what it
  does not believe; `put()` clamps into the same limits, so a session can
  never store what its own reload would throw away. Frames and arrows
  are more keys in that object, validated the same way.
- **A gesture is a tile's own affair.** `CanvasTile` captures its pointer
  on `pointerdown`, accumulates deltas at the zoom of each step, treats
  ≤4px of travel as a click, and commits once on `pointerup`. The backdrop
  pans. Frames and arrows are two more kinds of gesture, and they follow
  the same shape: in-flight state held apart from the store, one commit
  on release.

The cursor (`fleet.selectedID`) is a single agent, and the keyboard
(`ui.walkedIn`) follows it. Frames need a *set*, which is the first thing
to add.

## Phase 1: multi-select

A canvas where you can only hold one tile at a time is a single-tile
tool. Frames want "frame these"; moving a batch wants "move these"; and
arrows are nicer to draw between two things you have already indicated.

**State.** `ui.selection: Set<string>` beside `fleet.selectedID`. The
cursor stays a single agent — the roster, the pane, and the keyboard all
key on it — and the selection is a set that *contains* the cursor when
anything is selected. Clicking a tile sets both to that one agent, which
is today's behaviour unchanged.

**Gestures.**
- Shift-click a tile: toggle it in the selection; the cursor moves to it.
- Marquee: drag on the backdrop with Shift held draws a selection box in
  stage units; on release, every tile whose box intersects it is selected.
  Plain backdrop drag still pans — the modifier is what distinguishes
  them, and Shift is the one every canvas tool uses.
- Drag any selected tile: every selected tile moves by the same delta.
  The dragged tile's `inFlight` box drives the others by offset; commit is
  one `put` per tile.
- Escape clears the selection (and, walked in, is the agent's — so only
  when the keyboard is the page's).

**Rendering.** A selected tile carries `.selected`, which already exists
for the cursor; the cursor's tile additionally carries `.cursor` so the
two remain distinguishable. Multi-selection reads as a lit ring on each.

**Geometry.** `lib/canvas.ts` gains `intersects(a: Box, b: Box)` (the
overlap test `place()` already does inline, without the placement gap)
and `union(boxes: Box[]): Box`. Both are pure and unit-tested; the
marquee and "frame these" are the callers.

**What it does not do.** Multi-select does not reach the roster or the
wall. Actions on the cursor (interrupt, mark, delete) stay on the cursor.
The one batch action a selection gets in this phase is moving.

## Phase 2: frames

A frame is a rectangle on the stage with a name and a lock. Tiles inside
it move with it; a locked frame refuses to move and refuses to let its
tiles be moved.

### Membership is geometric

A frame does **not** store which tiles it holds. A tile is in a frame when
its centre is inside the frame's box. That one decision removes a class
of bugs before they exist: there is no list to reparent when a tile is
dragged into or out of a frame, nothing to prune when an agent is deleted,
nothing that can disagree with what the screen shows. Dropping a tile
inside a frame *is* adding it; dragging it out *is* removing it.

Centre rather than full containment because containment is unforgiving
during a drag — a tile half over the edge would flicker in and out — and
because a person's intent when they drop a tile mostly-inside a frame is
"in". Excalidraw's full-containment rule fits shapes that are small
relative to their frames; terminals are not.

A tile whose centre is in two overlapping frames belongs to the smaller
one (the innermost). Overlapping frames are allowed but not encouraged;
nested frames are not a feature.

### Storage

The per-workspace layout object grows a `frames` key:

```json
{
  "<agent-id>": { "x": 0, "y": 0, "w": 440, "h": 300 },
  "frames": {
    "<frame-id>": { "x": -24, "y": -60, "w": 950, "h": 400,
                    "name": "migration", "locked": false }
  }
}
```

`frames` is a reserved key; an agent id can never collide with it (ids are
hex). `load()` validates each frame the way it validates a box — finite,
inside the position and extent limits, `name` a string capped at a sane
length, `locked` a boolean — and discards what it does not believe.
Frame ids are minted client-side (`crypto.randomUUID()`); they are local
to one browser's arrangement and never leave it.

The store's interface grows `frames`, `putFrame(id, frame)`,
`dropFrame(id)`, and `frameOf(box): Frame | undefined` (the innermost
frame containing the box's centre). `boxFor` and `put` are unchanged.

### Gestures

- **Draw.** A `frame` button joins the corner controls. While it is armed
  (a toggle; Escape disarms), a drag on the backdrop draws a rectangle in
  stage units, and release creates the frame around it — named
  "frame N" until renamed — and disarms. With a multi-selection live,
  the same button skips the drag and frames the selection's `union()`
  plus padding: "frame these". Drawing behind existing tiles captures
  them; that is the point.
- **Move.** The frame's title bar is its drag handle. Moving a frame moves
  every tile whose centre it contains by the same delta, in flight and on
  commit — one `putFrame` and one `put` per tile. The frame's body is
  *not* a handle: a press there is either a tile's (tiles are above
  frames) or the backdrop's, and pans.
- **Resize.** A grip in the frame's corner, like a tile's. Resizing does
  not move tiles; it changes which tiles are inside.
- **Rename.** Double-click the title, or `R` with the frame under the
  cursor.
- **Lock.** A padlock in the title bar toggles `locked`. Locked: the frame
  refuses its own move and resize, its tiles refuse drags (the tile's
  `pointerdown` asks `frameOf(box)?.locked` and returns), and the frame
  cannot be deleted until unlocked. Locked reads as the padlock filled
  and the title dimmed.
- **Delete.** Backspace/Delete with the frame under the cursor, or a ✕ in
  the title bar. Deleting a frame never deletes tiles; they simply stop
  being grouped.

A frame under the cursor is one whose title bar was last clicked; a
`ui.frameID` beside `fleet.selectedID`. Clicking anything else clears it.

### Rendering

Frames are children of the stage *before* the tiles in DOM order, so
tiles paint over them without z-index. A frame is a rounded rectangle
with a translucent fill (`--band` at low alpha), a dashed border in
`--border`, and a title bar along its top edge outside the fill so it
reads as a label rather than a header. `fit` includes frames in the
bounding box it fits.

### Frame by workspace

On the "All agents" canvas, a control action that draws one frame per
workspace around the `union()` of that workspace's tiles, named by the
workspace. Zero-config grouping, the wall's structure made spatial, and
the first real use of frames anyone will have — it should ship in the
same PR as frames. It creates ordinary frames: renameable, lockable,
deletable, and no longer tied to the workspace once drawn.

### What it deliberately does not do

- A frame has no behaviour beyond geometry. It does not filter dispatch,
  set a working directory, or scope the keyboard. Each of those is a real
  idea and a separate decision; frames that are only geometry are the
  foundation any of them would stand on.
- Nested frames. Overlap is tolerated by the innermost rule; nesting as a
  concept is not built.
- Server-side persistence. Layouts remain one browser's arrangement.
  Frames make an arrangement worth more, which is the argument for moving
  layouts to the server one day — but that is a change to where *all* of
  it lives, not a frames feature.

## Phase 3: arrows that send, by hand

An arrow from tile A to tile B is a route: when it fires, B receives a
message built from A's last turn, framed by the arrow's label. In this
phase it fires when you click it. That is enough to find out whether the
idea is any good before building the machinery that fires it unasked.

### The label is the prompt

This is the decision that makes arrows useful rather than merely visual.
An arrow labelled *"review this diff, be adversarial"* from `implementer`
to `reviewer` sends `reviewer`:

```
review this diff, be adversarial

From implementer:
<implementer's last assistant message>
```

Unlabelled, an arrow is a pipe, and a pipe between two chat agents is a
novelty. Labelled, it is a pipeline you can read off the surface:
implement → review → fix, each hop's instruction written on the hop.

### Storage

The layout object grows a `links` key:

```json
{
  "links": {
    "<link-id>": { "from": "<agent-id>", "to": "<agent-id>",
                   "label": "review this diff, be adversarial" }
  }
}
```

Validated like everything else: `from` and `to` strings, `label` a
string capped in length, and — enforced at creation and again at load —
no link from an agent to itself, and no cycle: adding `A → B` is refused
if `B` already reaches `A`. `load()` drops any link that would close a
cycle rather than repairing it, so a store hand-edited into a loop comes
back as no loop.

### The message

`Service.Transcript` already parses each provider's transcript into
entries for the transcript pane; the last assistant entry is the message.
The client asks the API for it at fire time (`GET
/api/agents/{id}/transcript` with the tail) rather than caching it, so
the arrow always sends what A *last* said. If A has no transcript entry
yet, the arrow refuses to fire and says so — sending a label with
nothing under it is a prompt that reads as a mistake.

Delivery is `api.send(to, message)`, which is `Service.Send` → bracketed
paste plus Enter, the path `stormlight send` and the composer already
use. Nothing new on the server for this phase.

### Gestures and rendering

- **Draw.** Each tile's label grows a port: a small ● at its right edge,
  visible on hover. Drag from a port to another tile; release creates
  the link and opens its label for typing. Release on the backdrop
  cancels. With exactly two tiles selected and the cursor on one, `a`
  draws from the cursor to the other.
- **Render.** One `<svg>` child of the stage, sized to nothing and
  overflow visible, with a `<path>` per link: a cubic bezier from the
  midpoint of A's nearest edge to the midpoint of B's nearest edge, an
  arrowhead marker at B, and the label as `<text>` at the path's
  midpoint on a small opaque plate. Paths are recomputed from the tiles'
  *shown* boxes, so an arrow follows a tile in flight.
- **Fire.** Click the arrow (its path has a wide transparent stroke
  under the visible one, so it is clickable) — or, with the arrow under
  the cursor, Enter. The arrow flashes along its length while the
  message is in flight and the target tile's label shows "sent from A"
  for a moment.
- **Edit.** Double-click the label. Delete with Backspace/Delete on the
  arrow under the cursor, or ✕ on hover.

An arrow under the cursor is `ui.linkID`, cleared by clicking anything
else, in the same way as frames.

### What it deliberately does not do

- Fire on its own. That is phase 4, and it should not be built until
  hand-fired arrows have earned it.
- Carry anything but the last assistant message. Diff contents, file
  lists, tool output — every one of those is a real idea, and every one
  is a different arrow kind. Phase 3 has one kind.

## Phase 4: arrows that fire on their own

A rule that only fires while a browser tab is open is a demo. The real
version lives where turns end.

### Where turns end

`stormlight event` is a provider hook subprocess (`main.go`, the `event`
command): Claude Code's `Stop` hook and Codex's `agent-turn-complete`
notification invoke it inside the agent's own environment, it parses the
payload with `provider.ParseEvent`, and it applies the result through
`Service.Update`, marking the agent idle and recording `TurnEnded`. It
already runs at exactly the moment an arrow should fire, on every turn,
whether or not any dashboard is open. That is where auto-fire goes.

### Links move to the server

For the hook to see a link, the link has to live where the hook can read
it: in Stormlight's own state, beside the workspace catalog
(`$XDG_STATE_HOME/stormlight/links.json`), and through the API:

```
GET    /api/links                 → [{id, from, to, label, auto}]
POST   /api/links   {from, to, label, auto}
PATCH  /api/links/{id}  {label?, auto?}
DELETE /api/links/{id}
```

The server enforces what `load()` enforced on the client — no self-link,
no cycle — and refuses with a 409 rather than storing a loop. Links are
fleet state, not layout: the same link shows on every browser, and the
canvas draws it wherever those two tiles happen to be arranged. The
client's `links` key in localStorage goes away in this phase; a
migration reads it once, posts each link, and deletes the key.

Links reference agent ids. When an agent is deleted, its links are
deleted with it (`Service.Delete`). An agent absent from a roster push
— an unreachable host — keeps its links, the way it keeps its box.

### Firing

In the `event` command, after `Service.Update` succeeds and only when
`event.TurnEnded` is true: load the links whose `from` is this agent and
whose `auto` is set, build each message the way phase 3 does (the label,
then "From A:" and the last assistant message — which this payload
carries directly for Codex, and which the transcript at
`event.TranscriptPath` yields for Claude Code), and `Service.Send` it to
each target. Failures are logged, never retried, and never fail the hook:
a hook that returns non-zero can stall the provider that called it.

Two guards, both in the server, because a loop here runs unattended:

- **Hop cap.** A message that arrived through a link carries a hop count
  in its framing (`[via stormlight, hop 2]` as a trailing line the
  provider ignores and the next hook can read from the transcript). A
  link does not fire for a turn whose triggering message was already at
  the cap (3). Cycle refusal at creation catches the static loops; the
  cap catches the ones that route through a human.
- **Target must be idle.** A message delivered while the target is
  mid-turn lands in its input box and submits the moment the turn ends —
  which is what the providers do with pasted input, and is also a
  surprise. The hook checks the target's activity and, if it is working,
  records the send as pending on the link (`pending: {message, at}`) for
  the target's own next `TurnEnded` to deliver. One pending message per
  link; a newer one replaces it.

### The canvas draws the rule

Phase 3's rendering is unchanged; `auto` is a toggle on the arrow (a ⟳
glyph at the arrowhead when set), and the click-to-fire stays for arrows
that are not auto, and works on auto arrows too. The dashboard also
reflects a fire when it happens: the roster push that follows a send
already carries the target's activity going working, so nothing new is
needed for the canvas to show it — but the arrow's "sent from A" flash
should also fire from the push, keyed on a `last_fired` timestamp the
link carries, so a fire from the hook path is as visible as one from a
click.

The TUI can list links (`stormlight link list`) and delete them; drawing
them is the canvas's job.

## Tests, per phase

- `lib/canvas.test.ts`: `intersects`, `union`, frame containment and
  the innermost rule, arrow endpoints (nearest-edge midpoints), and the
  cycle check — all pure.
- `lib/layout.test.ts` (new): `load()` discards a malformed frame, a
  frame past the limits, a self-link, and a cycle; `put*` clamps.
- `components/canvas.test.ts`: marquee selects what it intersects; a
  batch drag moves every selected tile by the same delta; a frame drag
  carries its tiles and leaves outsiders; a locked frame refuses a tile
  drag and its own; frame-by-workspace draws one per workspace; a port
  drag creates a link and a backdrop release does not; firing an arrow
  calls `send` with the label first and the last message under it, and
  refuses with no message; `fit` includes frames.
- Go, phase 4: the `event` command fires auto links on `TurnEnded` only,
  never on a turn start; a working target defers to pending; a hop at
  the cap does not fire; a cycle is refused with 409; deleting an agent
  deletes its links.

Each of the gesture tests should be validated the way the walk-in tests
were: revert the line under test and watch the test fail.

## Open questions, to settle before their phase

1. **Shift for marquee, or a tool?** Shift-drag on the backdrop is the
   convention, but on a trackpad two-finger drag already pans and a
   modifier-drag is awkward. A `select` tool button beside `frame` may
   be the honest answer for trackpads. Decide by using it.
2. **Arrow kinds.** Phase 3 has one payload: the last message. "Send the
   diff" is the obvious second, and it is a different arrow, not an
   option on this one. Wait for someone to want it.
3. **Layouts server-side.** Frames make arrangements worth keeping across
   browsers. If that happens, it is the whole layout that moves — tiles,
   frames, and a per-user rather than per-browser key — not a frames
   feature.
4. **Where link state goes for remote hosts.** A link between an agent on
   this machine and one on a remote host fires from a hook running *over
   there*, which reads state *over there*. Either links are replicated
   to each host that has an endpoint, or the hook on the remote host
   reports the turn end to this machine's service and the send happens
   from here. The second is simpler and matches how the remote daemon
   already relays. Settle it before phase 4, not during.
