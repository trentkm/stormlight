<script lang="ts">
  import AgentScreen from "./AgentScreen.svelte";
  import { resized, type Box } from "../lib/canvas";
  import { statusVisual } from "../lib/theme";
  import { isUrgent, type Agent } from "../lib/types";

  let {
    agent,
    box,
    zoom,
    clip,
    oncommit,
    onopen,
  }: {
    agent: Agent;
    box: Box;
    /** The view's scale, for turning pointer pixels into stage units. */
    zoom: number;
    /** The canvas viewport, root for the visibility observer. */
    clip: HTMLElement | undefined;
    oncommit: (box: Box) => void;
    onopen: () => void;
  } = $props();

  // The id apart from the object: every roster push re-proxies every
  // agent, and a terminal keyed on the object would re-attach at the
  // roster's cadence.
  const id = $derived(agent.id);

  let host: HTMLDivElement;
  // Attached until the observer says otherwise — a callback that never
  // arrives (hidden tabs defer them) must leave a tile showing its
  // terminal rather than showing nothing.
  let visible = $state(true);

  // While a gesture is live the tile follows the hand; between gestures
  // it sits where the layout says. Holding the in-flight box apart from
  // the committed one means a gesture never fights the store, and
  // letting go of it (null) is what hands control back.
  let inFlight = $state<Box | null>(null);
  const shown = $derived(inFlight ?? box);

  const status = $derived(
    statusVisual(agent.activity, agent.attention, agent.process_live),
  );

  $effect(() => {
    if (!clip) return;
    const watcher = new IntersectionObserver(
      // The last entry: deliveries batch in order, and acting on the
      // oldest would park a fast flap on a stale answer.
      (entries) => (visible = entries[entries.length - 1].isIntersecting),
      { root: clip, rootMargin: "200px" },
    );
    watcher.observe(host);
    return () => watcher.disconnect();
  });

  /**
   * One pointer gesture: drag from anywhere on the tile, resize from
   * the grip. A press that never travels is a click, and a click opens
   * the agent in the roster — a watching tile takes no keystrokes, so
   * the click a hand makes on "their" terminal has to lead somewhere.
   */
  let gesture: {
    kind: "move" | "resize";
    pointer: number;
    startX: number;
    startY: number;
    from: Box;
    travelled: boolean;
  } | null = null;

  const begin = (event: PointerEvent, kind: "move" | "resize") => {
    if (event.button !== 0) return;
    gesture = {
      kind,
      pointer: event.pointerId,
      startX: event.clientX,
      startY: event.clientY,
      from: shown,
      travelled: false,
    };
    // Capture so a fast hand that leaves the tile keeps its grip.
    // Guarded: jsdom mounts this component without implementing it.
    host.setPointerCapture?.(event.pointerId);
  };

  const down = (event: PointerEvent) => begin(event, "move");

  const grip = (event: PointerEvent) => {
    // The grip is on the tile, but its press is not a drag.
    event.stopPropagation();
    begin(event, "resize");
  };

  const move = (event: PointerEvent) => {
    if (!gesture || event.pointerId !== gesture.pointer) return;
    const px = event.clientX - gesture.startX;
    const py = event.clientY - gesture.startY;
    // The click threshold is in screen pixels — a hand is steady in
    // pixels, however far the canvas is zoomed out.
    if (Math.abs(px) + Math.abs(py) > 4) gesture.travelled = true;
    if (!gesture.travelled) return;
    // Stage units from here on: a drag of 30 pixels at half zoom moves
    // the tile 60 stage units, so it tracks the cursor exactly.
    const dx = px / zoom;
    const dy = py / zoom;
    inFlight =
      gesture.kind === "move"
        ? { ...gesture.from, x: gesture.from.x + dx, y: gesture.from.y + dy }
        : resized(gesture.from, gesture.from.w + dx, gesture.from.h + dy);
  };

  const up = (event: PointerEvent) => {
    if (!gesture || event.pointerId !== gesture.pointer) return;
    const { kind, travelled } = gesture;
    gesture = null;
    if (!travelled) {
      inFlight = null;
      if (kind === "move") onopen();
      return;
    }
    if (inFlight) oncommit(inFlight);
    inFlight = null;
  };

  const cancel = () => {
    gesture = null;
    inFlight = null;
  };

  const key = (event: KeyboardEvent) => {
    if (event.key === "Enter" || event.key === " ") {
      event.preventDefault();
      onopen();
    }
  };
</script>

<div
  class="tile"
  class:urgent={isUrgent(agent)}
  class:done={!agent.process_live}
  class:lifted={inFlight !== null}
  role="button"
  tabindex="0"
  aria-label="Open {agent.name || agent.task || agent.id}"
  bind:this={host}
  style:left="{shown.x}px"
  style:top="{shown.y}px"
  style:width="{shown.w}px"
  style:height="{shown.h}px"
  onpointerdown={down}
  onpointermove={move}
  onpointerup={up}
  onpointercancel={cancel}
  onkeydown={key}
>
  <div class="label" title={agent.task}>
    <span style:color={isUrgent(agent) ? "#1f2328" : status.color}>{status.glyph}</span>
    <span class="name">{agent.name || agent.task || agent.id.slice(0, 8)}</span>
    <span class="where">{agent.workspace?.name ?? ""}</span>
  </div>
  <AgentScreen {id} {visible} />
  {#if isUrgent(agent)}
    <p class="needs">needs input</p>
  {/if}
  <div class="grip" onpointerdown={grip} aria-hidden="true"></div>
</div>

<style>
  .tile {
    position: absolute;
    display: flex;
    flex-direction: column;
    overflow: hidden;
    background: var(--bg-sunken);
    border: 1px solid var(--border);
    border-radius: 6px;
    cursor: grab;
    /* The tile owns its gestures; without this the browser answers a
       touch-drag with a page scroll instead. */
    touch-action: none;
  }
  .tile:hover {
    border-color: var(--band);
  }
  .tile.lifted {
    cursor: grabbing;
    border-color: var(--band);
    box-shadow: 0 8px 28px rgba(0, 0, 0, 0.45);
  }
  /* After :hover and .lifted on purpose: equal specificity, so order
     keeps the urgent band from being repainted by a passing pointer. */
  .tile.urgent {
    border-color: var(--waiting);
    box-shadow: 0 0 14px rgba(229, 192, 123, 0.18);
  }
  .tile.done {
    opacity: 0.72;
  }
  .label {
    display: flex;
    align-items: center;
    gap: 8px;
    flex: 0 0 auto;
    height: 26px;
    padding: 0 10px;
    background: var(--bg-raised);
    border-bottom: 1px solid var(--border);
    color: var(--text);
    font-size: 12px;
  }
  .tile.urgent .label {
    background: var(--waiting);
    color: #1f2328;
  }
  .name {
    flex: 1 1 auto;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .where {
    color: var(--muted);
    font-size: 11px;
  }
  .tile.urgent .where {
    color: #4a3e1e;
  }
  .needs {
    position: absolute;
    /* Present to the eye, transparent to the pointer: the banner must
       not shield the click it invites. */
    pointer-events: none;
    left: 0;
    right: 0;
    bottom: 0;
    margin: 0;
    padding: 3px 10px;
    background: var(--waiting);
    color: #1f2328;
    font-size: 11px;
    font-weight: 700;
  }
  .grip {
    position: absolute;
    right: 0;
    bottom: 0;
    width: 18px;
    height: 18px;
    cursor: nwse-resize;
    /* Drawn as a corner, not a box: two short strokes. */
    background:
      linear-gradient(
        135deg,
        transparent 0 55%,
        var(--muted) 55% 62%,
        transparent 62% 75%,
        var(--muted) 75% 82%,
        transparent 82%
      );
  }
</style>
