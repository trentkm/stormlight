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
    selected = false,
    focused = false,
    oncommit,
    onenter,
    onopen,
  }: {
    agent: Agent;
    box: Box;
    /** The view's scale, for turning pointer pixels into stage units. */
    zoom: number;
    /** The canvas viewport, root for the visibility observer. */
    clip: HTMLElement | undefined;
    /** The roster's cursor is on this agent. */
    selected?: boolean;
    /** Walked in: this tile holds the keyboard. */
    focused?: boolean;
    oncommit: (box: Box) => void;
    /** A click: someone wants to type here. */
    onenter: () => void;
    /** The label's open button: someone wants the roster's full pane. */
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
   * the grip. A press that never travels is a click, and a click walks
   * in — the tile takes the keyboard and what you type goes to the
   * agent, in place.
   *
   * The tile that already holds the keyboard drags by its label alone.
   * Its screen belongs to the terminal then, the way a window's does:
   * a press there is a selection or a mouse report, and lifting the
   * whole tile on it would make the one terminal you are using the one
   * you cannot select text in.
   *
   * A gesture's press is cancelled (preventDefault) so that it moves no
   * focus. Left alone, a mousedown lands focus on the nearest thing
   * that takes it — this tile (tabindex) or, on a screen, xterm's own
   * textarea, which xterm focuses itself — and either one blurs the
   * terminal that holds the keyboard. That is a walk-out nobody asked
   * for: dragging tile B while typing into A ended the typing. The
   * focused tile's screen is the one press left uncancelled, because
   * xterm needs that mousedown to select text.
   *
   * Deltas accumulate step by step at whatever the zoom is at that
   * step, rather than dividing one grand total by the current zoom —
   * a pinch mid-drag (reflexive on a trackpad) would otherwise rescale
   * the whole journey so far and teleport the tile. Travel accumulates
   * monotonically, so a drag that wanders back over its origin can
   * never turn back into a click.
   */
  let gesture: {
    kind: "move" | "resize";
    pointer: number;
    startX: number;
    startY: number;
    lastX: number;
    lastY: number;
    travel: number;
    engaged: boolean;
  } | null = null;

  const begin = (event: PointerEvent, kind: "move" | "resize") => {
    if (event.button !== 0) return;
    // One gesture at a time. A second pointer landing mid-drag — a
    // palm, a stray finger — must not hijack the tile: its down is
    // ignored, and its later up fails the pointerId check below, so
    // the first hand keeps its grip.
    if (gesture) return;
    event.preventDefault();
    gesture = {
      kind,
      pointer: event.pointerId,
      startX: event.clientX,
      startY: event.clientY,
      lastX: event.clientX,
      lastY: event.clientY,
      travel: 0,
      engaged: false,
    };
    // Capture so a fast hand that leaves the tile keeps its grip.
    // Guarded: jsdom mounts this component without implementing it.
    host.setPointerCapture?.(event.pointerId);
  };

  const onLabel = (target: EventTarget | null) =>
    target instanceof Element && target.closest(".label") !== null;

  const down = (event: PointerEvent) => {
    if (focused && !onLabel(event.target)) return;
    begin(event, "move");
  };

  const grip = (event: PointerEvent) => {
    // The grip is on the tile, but its press is not a drag.
    event.stopPropagation();
    begin(event, "resize");
  };

  const move = (event: PointerEvent) => {
    if (!gesture || event.pointerId !== gesture.pointer) return;
    let px = event.clientX - gesture.lastX;
    let py = event.clientY - gesture.lastY;
    gesture.lastX = event.clientX;
    gesture.lastY = event.clientY;
    // The click threshold is in screen pixels — a hand is steady in
    // pixels, however far the canvas is zoomed out.
    gesture.travel += Math.abs(px) + Math.abs(py);
    if (gesture.travel <= 4) return;
    if (!gesture.engaged) {
      // Crossing the threshold spends what accumulated beneath it:
      // applying only this step would leave every careful drag a few
      // pixels behind the hand, permanently.
      gesture.engaged = true;
      px = event.clientX - gesture.startX;
      py = event.clientY - gesture.startY;
    }
    // Stage units from here on: these pixels at this moment's zoom, so
    // the tile tracks the cursor exactly.
    const dx = px / zoom;
    const dy = py / zoom;
    inFlight =
      gesture.kind === "move"
        ? { ...shown, x: shown.x + dx, y: shown.y + dy }
        : resized(shown, shown.w + dx, shown.h + dy);
  };

  const up = (event: PointerEvent) => {
    if (!gesture || event.pointerId !== gesture.pointer) return;
    const { kind, travel } = gesture;
    gesture = null;
    if (travel <= 4) {
      inFlight = null;
      if (kind === "move") onenter();
      return;
    }
    if (inFlight) oncommit(inFlight);
    inFlight = null;
  };

  const cancel = (event: PointerEvent) => {
    // Only the gesture's own pointer can cancel it: browsers cancel a
    // *secondary* touch when they claim it for a native gesture, and
    // that must not cost the first hand its drag.
    if (!gesture || event.pointerId !== gesture.pointer) return;
    gesture = null;
    inFlight = null;
  };

  const key = (event: KeyboardEvent) => {
    // The tile's own keys, for a tile reached by Tab — never the
    // terminal's. Its keystrokes bubble up through here, and an Enter
    // typed at the agent must not also be an Enter pressed on the tile.
    if (event.target !== host) return;
    if (event.key === "Enter" || event.key === " ") {
      event.preventDefault();
      onenter();
    }
  };

  const open = (event: MouseEvent) => {
    event.stopPropagation();
    onopen();
  };
</script>

<!-- data-walk-target while focused: the walked-in keyboard's anchor is
     the whole tile, so focus moving anywhere inside it — the label, the
     grip, xterm's textarea — is still the walk. A canvas has many
     screens; only the one holding the keyboard carries the hook. -->
<div
  class="tile"
  class:urgent={isUrgent(agent)}
  class:done={!agent.process_live}
  class:lifted={inFlight !== null}
  class:selected
  class:focused
  role="button"
  tabindex="0"
  aria-label="Type to {agent.name || agent.task || agent.id}"
  data-walk-target={focused ? "" : undefined}
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
    <span style:color={isUrgent(agent) ? "var(--attention-ink)" : status.color}
      >{status.glyph}</span
    >
    <span class="name">{agent.name || agent.task || agent.id.slice(0, 8)}</span>
    <!-- Walking in is invisible otherwise: the keyboard changes hands
         and nothing on screen says so. -->
    {#if focused}
      <span class="typing">typing · ctrl-space leaves</span>
    {:else}
      <span class="where">{agent.workspace?.name ?? ""}</span>
    {/if}
    <button
      class="open"
      title="Open in the roster"
      aria-label="Open {agent.name || agent.task || agent.id} in the roster"
      onclick={open}
      onpointerdown={(event) => event.stopPropagation()}
    >
      ↗
    </button>
  </div>
  <!-- The tile holding the keyboard stays attached wherever the camera
       goes: a terminal disposed for scrolling off screen takes the
       focus with it, and the walk with the focus. -->
  <AgentScreen {id} visible={visible || focused} typing {focused} />
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
    background: var(--term-bg);
    border: 1px solid var(--border);
    border-radius: 6px;
    cursor: grab;
    /* The tile owns its gestures; without this the browser answers a
       touch-drag with a page scroll instead. */
    touch-action: none;
  }
  .tile:hover {
    border-color: var(--accent);
  }
  .tile.selected {
    border-color: var(--accent);
  }
  .tile.lifted {
    cursor: grabbing;
    border-color: var(--accent);
    box-shadow: 0 8px 28px var(--shadow-lift);
  }
  /* After :hover and .lifted on purpose: equal specificity, so order
     keeps the urgent band from being repainted by a passing pointer. */
  .tile.urgent {
    border-color: var(--waiting);
    box-shadow: 0 0 14px var(--attention-glow);
  }
  /* The tile you are typing into is lit at its own edge, the way the
     roster's pane is when it has the keyboard — and its screen is the
     terminal's, so the grab cursor retreats to the label. */
  .tile.focused {
    border-color: var(--aim);
    box-shadow: inset 0 0 0 1px var(--aim);
    cursor: default;
  }
  .tile.focused .label {
    cursor: grab;
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
    padding: 0 6px 0 10px;
    background: var(--bg-raised);
    border-bottom: 1px solid var(--border);
    color: var(--text);
    font-size: 12px;
  }
  .tile.urgent .label {
    background: var(--attention-fill);
    color: var(--attention-ink);
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
  .typing {
    color: var(--aim);
    font-size: 11px;
    letter-spacing: 0.02em;
    white-space: nowrap;
  }
  .tile.urgent .where {
    color: var(--attention-ink-dim);
  }
  .open {
    flex: 0 0 auto;
    padding: 0 5px;
    border: none;
    background: transparent;
    color: var(--muted);
    font-size: 12px;
    line-height: 1;
    cursor: pointer;
  }
  .open:hover {
    color: var(--accent);
  }
  .tile.urgent .open {
    color: var(--attention-ink-dim);
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
    background: var(--attention-fill);
    color: var(--attention-ink);
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
