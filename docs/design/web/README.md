# Stormlight Web — design mockups

Static design mockups for the Stormlight Web GUI (the epic is
[#125](https://github.com/trentkm/stormlight/issues/125)). Each `*.dc.html`
file is a self-contained artboard — open it directly in a browser:

- `Main.dc.html` — the stage-2 main view: workspace rail, agent roster,
  and the Spanreed pane with Terminal / Transcript / Diff tabs.
- `Wall.dc.html` — the mission-control wall: every agent's terminal live
  at once; waiting-for-input agents glow amber.
- `Transcript.dc.html` — the Spanreed pane's Transcript tab: the agent's
  transcript as rendered markdown with collapsible tool calls.
- `Diff.dc.html` — the Diff tab: the agent's worktree diff against
  `origin/main`.
- `canvas.json` — layout manifest from the design-canvas tool the
  artboards were authored in; irrelevant for viewing, kept so the set can
  be re-imported for further design passes.

These are mockups, not markup to reuse: layout and copy illustrate the
target, and hex values are hand-inlined. The palette is lifted verbatim
from `internal/theme` (working `#61AFEF`, waiting `#E5C07B`, done
`#72C087`, failed `#E06C75`, band `#C6E9FF`, code `#8FDCCB`), the status
glyphs (`● ◆ ○ ✓`) are the roster's own, and the icy band-blue family is
the selection/focus language, kept distinct from the status colors. The
real frontend should read its colors from one source of truth shared with
`internal/theme` rather than copying these values.

Each artboard's `<script src="./support.js">` line is scaffolding from the
authoring tool; the file intentionally does not exist here, and browsers
render the artboards fine without it.
