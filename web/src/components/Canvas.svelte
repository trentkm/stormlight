<script lang="ts">
  import { agentsIn, fleet } from "../lib/state.svelte";
  import { canvasLayout } from "../lib/layout.svelte";
  import {
    fitView,
    homeView,
    panBy,
    zoomAt,
    type Box,
    type View,
  } from "../lib/canvas";
  import CanvasTile from "./CanvasTile.svelte";

  let { onopen }: { onopen: () => void } = $props();

  const agents = $derived(agentsIn(fleet.workspaceID));
  // A fresh store per workspace: switching workspaces in the rail swaps
  // the whole arrangement, each remembered separately.
  const layout = $derived(canvasLayout(fleet.workspaceID));

  let clip = $state<HTMLDivElement>();
  let view = $state<View>(homeView);

  // Forget tiles for agents the roster no longer knows — but never on
  // an empty roster, which is what the first moments of a session look
  // like; wiping every saved layout because the event socket has not
  // spoken yet would make persistence a rumor.
  $effect(() => {
    if (agents.length === 0) return;
    layout.prune(new Set(agents.map((a) => a.id)));
  });

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

  // Open on the whole fleet. Waiting for the roster matters: the canvas
  // usually mounts before the first push, and fitting zero boxes is
  // just the home view.
  let fitted = false;
  $effect(() => {
    if (fitted || agents.length === 0 || !clip) return;
    fitted = true;
    fit();
  });

  /**
   * The wheel is the pan, and pinch is the zoom. Trackpads report a
   * pinch as a wheel event with ctrlKey set — the same convention
   * Figma, Excalidraw, and every other canvas rely on — so both
   * gestures arrive through one handler. The view is written directly;
   * no layout is read, so a fast scroll costs one style recalculation
   * per frame.
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
          oncommit={(box) => layout.put(agent.id, box)}
          onopen={() => {
            fleet.selectedID = agent.id;
            onopen();
          }}
        />
      {/if}
    {/each}
  </div>
  {#if agents.length === 0}
    <p class="empty">No agents to arrange.</p>
  {/if}
  <div class="controls">
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
    color: var(--band);
  }
  .zoom {
    min-width: 4ch;
    color: var(--muted);
    font-size: 11px;
    text-align: right;
  }
</style>
