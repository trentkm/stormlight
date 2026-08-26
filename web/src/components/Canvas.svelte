<script lang="ts">
  import { untrack } from "svelte";
  import { run, select, ui } from "../lib/commands.svelte";
  import { agentsIn, fleet } from "../lib/state.svelte";
  import { canvasLayout } from "../lib/layout.svelte";
  import {
    centeredOn,
    fitView,
    homeView,
    intersects,
    panBy,
    showing,
    spanning,
    stagePoint,
    zoomAt,
    type Box,
    type View,
  } from "../lib/canvas";
  import CanvasTile from "./CanvasTile.svelte";
  import { isUrgent } from "../lib/types";

  let { onopen }: { onopen: () => void } = $props();

  const agents = $derived(agentsIn(fleet.workspaceID));
  // A fresh store per workspace: switching workspaces in the rail swaps
  // the whole arrangement, each remembered separately.
  const layout = $derived(canvasLayout(fleet.workspaceID));

  // The tile with the keyboard: the selected agent's, while walked in.
  // The canvas types in place, so the walk that puts the keyboard in
  // the roster's pane puts it here instead when this is the view.
  const focused = $derived(ui.walkedIn ? fleet.selectedID : "");

  let clip = $state<HTMLDivElement>();
  let view = $state<View>(homeView);

  /**
   * A drag in flight, as the offset it has travelled and the tile that
   * is doing the travelling. Held here rather than in the tile because
   * a drag can carry more than one tile: dragging a selected tile
   * carries the whole selection, and every carried tile shows the same
   * offset until the hand lets go. A tile outside the selection drags
   * alone and leaves the selection as it was — dragging is not
   * selecting, and a drag must never move the cursor, which is where
   * the keyboard is.
   */
  let drift = $state<{ id: string; dx: number; dy: number } | null>(null);

  const carried = (id: string): boolean =>
    drift !== null &&
    (id === drift.id ||
      (ui.selection.has(drift.id) && ui.selection.has(id)));

  /** Where a tile is drawn: its box, plus the drift if it is carried. */
  const shownBox = (id: string): Box => {
    const box = layout.tiles[id];
    if (!drift || !carried(id)) return box;
    return { ...box, x: box.x + drift.dx, y: box.y + drift.dy };
  };

  const drifted = (id: string, dx: number, dy: number) => {
    drift = drift?.id === id
      ? { id, dx: drift.dx + dx, dy: drift.dy + dy }
      : { id, dx, dy };
  };

  /** The hand let go: every carried tile lands where it is shown, or
   *  nowhere if the gesture was cancelled. */
  const landed = (commit: boolean) => {
    if (!drift) return;
    if (commit) {
      for (const agent of agents) {
        if (carried(agent.id)) layout.put(agent.id, shownBox(agent.id));
      }
    }
    drift = null;
  };

  // Placement is minted here, in an effect, never from the template:
  // boxFor writes state for an agent it has not seen, and Svelte
  // (rightly) refuses state mutation during render. The template below
  // renders only tiles that already have a box; this effect makes that
  // true one tick after any new agent appears.
  $effect(() => {
    for (const agent of agents) layout.boxFor(agent.id);
  });

  const boxes = (): Box[] =>
    agents
      .map((a) => layout.tiles[a.id])
      .filter((box): box is Box => box !== undefined);

  const fit = () => {
    // A viewport with no extent — hidden tab, mid-layout mount — has
    // nothing to fit into, and fitting anyway slams the zoom to its
    // floor.
    if (!clip || clip.clientWidth === 0 || clip.clientHeight === 0) return;
    view = fitView(boxes(), {
      w: clip.clientWidth,
      h: clip.clientHeight,
    });
  };

  // Open on the whole fleet — once per workspace, not once per mount:
  // switching workspaces in the rail swaps the whole arrangement, and a
  // camera still aimed at the previous one shows empty space. Waiting
  // for the roster matters too: the canvas usually mounts before the
  // first push, and fitting zero boxes is just the home view.
  let fittedFor: string | null = null;
  $effect(() => {
    const workspace = fleet.workspaceID;
    if (fittedFor === workspace || agents.length === 0 || !clip) return;
    fittedFor = workspace;
    fit();
  });

  const viewport = () => ({ w: clip!.clientWidth, h: clip!.clientHeight });

  /** The camera, centred on a box at a zoom it can be read at. */
  const centerOn = (box: Box) => {
    if (!clip) return;
    view = centeredOn(view, box, viewport());
  };

  // The cursor can move by key — alt+j, alt+n — onto a tile the hand
  // never went near, and on a canvas that may be ten screens away. A
  // selection nobody can see is not one, so a tile wholly off screen is
  // brought to the centre; one that is even partly on screen is left
  // where the hand put the camera.
  //
  // Each selection is revealed once, when it has a tile — which may be
  // a tick after it was made, since a dispatched agent is selected
  // before its box is minted. The box is tracked for that tick and no
  // longer: a drag that rewrites the selected tile's box re-runs this,
  // and revealing it again would have the camera chase the tile the
  // hand just pushed off screen. The camera is never tracked, or a pan
  // that carried the tile off the edge would snap it back.
  let revealed = "";
  $effect(() => {
    const id = fleet.selectedID;
    const box = layout.tiles[id];
    if (!box || !clip || revealed === id) return;
    revealed = id;
    untrack(() => {
      if (!showing(view, box, viewport())) centerOn(box);
    });
  });

  /**
   * The wheel is the pan, and pinch is the zoom. Trackpads report a
   * pinch as a wheel event with ctrlKey set — the same convention
   * Figma, Excalidraw, and every other canvas rely on — so both
   * gestures arrive through one handler.
   */
  const wheel = (event: WheelEvent) => {
    event.preventDefault();
    if (event.ctrlKey || event.metaKey) {
      const bounds = clip!.getBoundingClientRect();
      view = zoomAt(
        view,
        { x: event.clientX - bounds.left, y: event.clientY - bounds.top },
        Math.exp(-event.deltaY * 0.01),
      );
      return;
    }
    view = panBy(view, -event.deltaX, -event.deltaY);
  };

  /**
   * Dragging empty canvas pans; with Shift held it draws a marquee, and
   * letting go selects every tile the marquee touches. Tiles stop
   * propagation of their own gestures by handling them first (their
   * pointerdown captures).
   */
  let panning: { pointer: number; x: number; y: number } | null = null;
  let marquee = $state<{
    pointer: number;
    from: { x: number; y: number };
    to: { x: number; y: number };
  } | null>(null);
  const band = $derived(marquee ? spanning(marquee.from, marquee.to) : null);

  const pointOf = (event: PointerEvent) => {
    const bounds = clip!.getBoundingClientRect();
    return stagePoint(view, {
      x: event.clientX - bounds.left,
      y: event.clientY - bounds.top,
    });
  };

  const down = (event: PointerEvent) => {
    if (event.button !== 0) return;
    // Only the backdrop pans. A pointerdown that began on a tile is the
    // tile's gesture; it captures its pointer, so it never surfaces
    // here with the backdrop as target.
    if (event.target !== event.currentTarget && event.target !== stage) return;
    clip?.setPointerCapture?.(event.pointerId);
    if (event.shiftKey) {
      const at = pointOf(event);
      marquee = { pointer: event.pointerId, from: at, to: at };
      return;
    }
    panning = { pointer: event.pointerId, x: event.clientX, y: event.clientY };
  };
  const moved = (event: PointerEvent) => {
    if (marquee?.pointer === event.pointerId) {
      marquee = { ...marquee, to: pointOf(event) };
      return;
    }
    if (!panning || event.pointerId !== panning.pointer) return;
    view = panBy(view, event.clientX - panning.x, event.clientY - panning.y);
    panning = { ...panning, x: event.clientX, y: event.clientY };
  };
  const up = (event: PointerEvent) => {
    if (panning?.pointer === event.pointerId) panning = null;
    if (marquee?.pointer === event.pointerId && band) {
      // Whatever the band touches, and nothing if it touched nothing:
      // a shift-drag over empty canvas is how a selection is dropped.
      select(
        agents
          .filter((agent) => {
            const box = layout.tiles[agent.id];
            return box !== undefined && intersects(band, box);
          })
          .map((agent) => agent.id),
      );
      marquee = null;
    }
  };
  const cancelled = (event: PointerEvent) => {
    if (panning?.pointer === event.pointerId) panning = null;
    if (marquee?.pointer === event.pointerId) marquee = null;
  };

  let stage = $state<HTMLDivElement>();

  const zoomLabel = $derived(`${Math.round(view.z * 100)}%`);

  // The wall sorts urgent agents to the front; a canvas cannot — the
  // user owns placement, and an urgent tile may sit ten screens away.
  // This is the signal that survives that: a count that is always in
  // the corner, and a jump that centres each urgent tile in turn.
  const urgent = $derived(agents.filter(isUrgent));
  let jumpAt = 0;
  const jump = () => {
    if (urgent.length === 0 || !clip) return;
    // Walk from the cursor to the next urgent agent that has a tile;
    // burning an index on one minted a tick from now would silently
    // skip an agent per press.
    let box;
    for (let step = 0; step < urgent.length; step++) {
      const target = urgent[(jumpAt + step) % urgent.length];
      box = layout.tiles[target.id];
      if (box) {
        jumpAt = (jumpAt + step + 1) % urgent.length;
        break;
      }
    }
    if (!box) return;
    centerOn(box);
  };

  /** A click on a tile: the cursor moves there and the keyboard with
   *  it. Both are the roster's own commands, which on this view stay
   *  on this view. */
  const enter = (id: string) => {
    run("select-agent", id);
    run("walk-in");
  };

  /** A shift-click: the tile joins or leaves the selection, and the
   *  cursor moves to it. Building a selection is arranging, not typing,
   *  so the walk ends — otherwise the keyboard would follow the cursor
   *  onto each tile shift-clicked, and the tile it lands on would drag
   *  by its label alone. */
  const toggle = (id: string) => {
    if (ui.selection.has(id)) ui.selection.delete(id);
    else ui.selection.add(id);
    fleet.selectedID = id;
    ui.walkedIn = false;
  };

  /** The label's ↗: the roster's full pane, to look at. Letting go of
   *  the keyboard is said here rather than left to focus, because
   *  whether a button takes focus on click is the browser's opinion —
   *  and arriving on the roster typing to an agent nobody walked into
   *  is the wrong answer on any of them. */
  const open = (id: string) => {
    ui.walkedIn = false;
    run("select-agent", id);
    onopen();
  };
</script>

<!-- role=application: the surface really is one — every pointer and
     wheel event is a camera or tile gesture, not document scrolling —
     and it tells assistive tech the tiles inside carry the semantics. -->
<div
  class="canvas"
  role="application"
  aria-label="Agent canvas"
  bind:this={clip}
  onwheel={wheel}
  onpointerdown={down}
  onpointermove={moved}
  onpointerup={up}
  onpointercancel={cancelled}
>
  <div
    class="stage"
    bind:this={stage}
    style:transform="translate({view.x}px, {view.y}px) scale({view.z})"
  >
    {#each agents as agent (agent.id)}
      {#if layout.tiles[agent.id]}
        <CanvasTile
          {agent}
          box={shownBox(agent.id)}
          zoom={view.z}
          {clip}
          cursor={fleet.selectedID === agent.id}
          selected={ui.selection.has(agent.id)}
          lifted={carried(agent.id)}
          focused={focused === agent.id}
          oncommit={(box) => layout.put(agent.id, box)}
          ondrift={(dx, dy) => drifted(agent.id, dx, dy)}
          onland={landed}
          onenter={() => enter(agent.id)}
          ontoggle={() => toggle(agent.id)}
          onopen={() => open(agent.id)}
        />
      {/if}
    {/each}
    {#if band}
      <div
        class="marquee"
        style:left="{band.x}px"
        style:top="{band.y}px"
        style:width="{band.w}px"
        style:height="{band.h}px"
      ></div>
    {/if}
  </div>
  {#if agents.length === 0}
    <p class="empty">No agents to arrange.</p>
  {/if}
  <div class="controls">
    {#if urgent.length > 0}
      <button class="urgent" onclick={jump} title="Jump to the next agent that needs input">
        ! {urgent.length} need{urgent.length === 1 ? "s" : ""} input
      </button>
    {/if}
    <button onclick={fit} title="Fit every tile in view">fit</button>
    <span class="zoom">{zoomLabel}</span>
  </div>
</div>

<style>
  .canvas {
    position: relative;
    flex: 1 1 auto;
    overflow: hidden;
    /* The canvas owns its gestures; the page must not scroll or
       pinch-zoom underneath them. */
    touch-action: none;
    background:
      radial-gradient(circle, var(--border) 1px, transparent 1px) 0 0 / 28px
        28px,
      var(--bg);
    cursor: grab;
  }
  .canvas:active {
    cursor: grabbing;
  }
  .stage {
    position: absolute;
    top: 0;
    left: 0;
    /* The stage is a point, not a box: tiles position themselves on it
       in stage units, and the transform above is the whole camera. */
    width: 0;
    height: 0;
    transform-origin: top left;
  }
  .marquee {
    position: absolute;
    /* Drawn in stage units like a tile, so it pans and zooms with the
       tiles it is choosing among. */
    border: 1px dashed var(--accent);
    background: var(--selected-bg);
    pointer-events: none;
  }
  .empty {
    position: absolute;
    inset: 0;
    display: grid;
    place-items: center;
    margin: 0;
    color: var(--muted);
    font-size: 12px;
    pointer-events: none;
  }
  .controls {
    position: absolute;
    right: 12px;
    bottom: 12px;
    display: flex;
    align-items: center;
    gap: 8px;
    padding: 4px 6px;
    background: var(--bg-raised);
    border: 1px solid var(--border);
    border-radius: 6px;
  }
  .controls button {
    padding: 1px 10px;
    border: none;
    background: transparent;
    color: var(--muted);
    font-size: 12px;
    cursor: pointer;
  }
  .controls button:hover {
    color: var(--accent);
  }
  .controls button.urgent {
    color: var(--waiting);
    font-weight: 600;
  }
  .zoom {
    min-width: 4ch;
    color: var(--muted);
    font-size: 11px;
    text-align: right;
  }
</style>
