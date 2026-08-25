<script lang="ts">
  import { untrack } from "svelte";
  import { run, ui } from "../lib/commands.svelte";
  import { agentsIn, fleet } from "../lib/state.svelte";
  import { canvasLayout } from "../lib/layout.svelte";
  import {
    centeredOn,
    fitView,
    homeView,
    panBy,
    showing,
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
  // Only the cursor is tracked. The camera is not, or a pan that
  // carried the tile off the edge would snap it back; and the tile's
  // box is not, or the hand that dragged the selected tile off screen
  // and let go would watch the camera chase it there.
  $effect(() => {
    const id = fleet.selectedID;
    untrack(() => {
      const box = layout.tiles[id];
      if (!box || !clip) return;
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

  /** Dragging empty canvas pans; tiles stop propagation of their own
   *  gestures by handling them first (their pointerdown captures). */
  let panning: { pointer: number; x: number; y: number } | null = null;

  const down = (event: PointerEvent) => {
    if (event.button !== 0) return;
    // Only the backdrop pans. A pointerdown that began on a tile is the
    // tile's gesture; it captures its pointer, so it never surfaces
    // here with the backdrop as target.
    if (event.target !== event.currentTarget && event.target !== stage) return;
    panning = { pointer: event.pointerId, x: event.clientX, y: event.clientY };
    clip?.setPointerCapture?.(event.pointerId);
  };
  const moved = (event: PointerEvent) => {
    if (!panning || event.pointerId !== panning.pointer) return;
    view = panBy(view, event.clientX - panning.x, event.clientY - panning.y);
    panning = { ...panning, x: event.clientX, y: event.clientY };
  };
  const up = (event: PointerEvent) => {
    if (panning?.pointer === event.pointerId) panning = null;
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
  onpointercancel={up}
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
          box={layout.tiles[agent.id]}
          zoom={view.z}
          {clip}
          selected={fleet.selectedID === agent.id}
          focused={focused === agent.id}
          oncommit={(box) => layout.put(agent.id, box)}
          onenter={() => enter(agent.id)}
          onopen={() => open(agent.id)}
        />
      {/if}
    {/each}
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
