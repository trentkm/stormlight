<script lang="ts">
  import { arrowBetween, arrowToward, type Arrow, type Box } from "../lib/canvas";
  import type { Link } from "../lib/types";

  /**
   * The pipeline, drawn: one arrow per link whose ends are both on this
   * canvas, from the source tile to the target, with the label on it.
   * An arrow's look is its state — solid for one that fires on its own,
   * dashed for one fired by hand, amber and creeping while a hop waits
   * for its target to go idle, and a pulse along its length the moment
   * it fires. The one the hand is drawing follows the pointer until it
   * lands on a tile.
   *
   * Strokes are screen-sized at any zoom, like the drawings; the
   * arrowhead and label are too.
   */
  let {
    links,
    boxOf,
    draft = null,
    chosen = null,
    fired,
    zoom,
    interactive,
    onpick,
  }: {
    links: Link[];
    /** Where a tile is drawn now — drift included — or nothing if the
     *  agent is not on this canvas. */
    boxOf: (id: string) => Box | undefined;
    /** An arrow being dragged out of a tile, to wherever the hand is. */
    draft?: { from: string; to: { x: number; y: number } } | null;
    chosen?: string | null;
    /** Links that fired within the last moment, for the pulse. */
    fired: Set<string>;
    zoom: number;
    interactive: boolean;
    onpick: (id: string, event: PointerEvent) => void;
  } = $props();

  const drawn = $derived(
    links
      .map((link) => {
        const from = boxOf(link.from);
        const to = boxOf(link.to);
        if (!from || !to) return null;
        return { link, arrow: arrowBetween(from, to) };
      })
      .filter((entry): entry is { link: Link; arrow: Arrow } => entry !== null),
  );

  const drafted = $derived.by((): Arrow | null => {
    if (!draft) return null;
    const from = boxOf(draft.from);
    return from ? arrowToward(from, draft.to) : null;
  });

  /** Screen pixels in stage units, so chrome stays the same size. */
  const px = $derived(1 / zoom);
  const head = $derived(12 * px);
  const fontSize = $derived(12 * px);

  /** A label's plate, sized for monospace text. */
  const plate = (text: string) => {
    const chars = Math.max(text.length, 7);
    return { w: (chars * 7.3 + 16) * px, h: 20 * px };
  };
  const labelOf = (link: Link) => link.label || "+ label";
</script>

<svg class="links" class:interactive aria-hidden="true">
  <defs>
    <marker
      id="linkhead"
      viewBox="0 0 10 10"
      refX="9"
      refY="5"
      markerWidth={head}
      markerHeight={head}
      markerUnits="userSpaceOnUse"
      orient="auto"
    >
      <path d="M 0 0 L 10 5 L 0 10 z" />
    </marker>
  </defs>
  {#each drawn as { link, arrow } (link.id)}
    {@const box = plate(labelOf(link))}
    <!-- svelte-ignore a11y_no_static_element_interactions -->
    <g
      class="link"
      class:auto={link.auto}
      class:pending={link.pending !== undefined}
      class:fired={fired.has(link.id)}
      class:chosen={chosen === link.id}
      class:unlabelled={!link.label}
      data-id={link.id}
      onpointerdown={(event) => onpick(link.id, event)}
    >
      <path class="hit" d={arrow.path} />
      <path class="ink" d={arrow.path} marker-end="url(#linkhead)" />
      {#if fired.has(link.id)}
        <path class="pulse" d={arrow.path} />
      {/if}
      <g class="label" transform="translate({arrow.mid.x} {arrow.mid.y})">
        <rect x={-box.w / 2} y={-box.h / 2} width={box.w} height={box.h} rx={4 * px} />
        <text font-size={fontSize} text-anchor="middle" dominant-baseline="central">
          {labelOf(link)}
        </text>
      </g>
    </g>
  {/each}
  {#if drafted}
    <path class="ink draft" d={drafted.path} marker-end="url(#linkhead)" />
  {/if}
</svg>

<style>
  .links {
    position: absolute;
    top: 0;
    left: 0;
    /* One pixel, not zero: see CanvasDrawings — an SVG with no area
       paints nothing. */
    width: 1px;
    height: 1px;
    overflow: visible;
    pointer-events: none;
  }
  .links.interactive .link {
    pointer-events: auto;
    cursor: pointer;
  }
  .ink {
    fill: none;
    stroke: var(--muted);
    stroke-width: 1.5;
    stroke-linecap: round;
    vector-effect: non-scaling-stroke;
    /* By hand: a route, drawn as one. */
    stroke-dasharray: 6 5;
  }
  .link.auto .ink {
    stroke-dasharray: none;
    stroke: var(--text);
  }
  marker path {
    fill: var(--text);
  }
  .hit {
    fill: none;
    stroke: transparent;
    stroke-width: 16;
    vector-effect: non-scaling-stroke;
  }
  .link.pending .ink {
    stroke: var(--waiting);
    stroke-dasharray: 4 6;
    animation: creep 1.2s linear infinite;
  }
  @keyframes creep {
    to {
      stroke-dashoffset: -10;
    }
  }
  .pulse {
    fill: none;
    stroke: var(--working);
    stroke-width: 4;
    stroke-linecap: round;
    vector-effect: non-scaling-stroke;
    stroke-dasharray: 40 1000;
    animation: travel 1.2s ease-out forwards;
    opacity: 0.9;
  }
  @keyframes travel {
    from {
      stroke-dashoffset: 40;
    }
    to {
      stroke-dashoffset: -1000;
      opacity: 0;
    }
  }
  .link.chosen .ink {
    stroke: var(--accent);
    stroke-width: 2.5;
  }
  .label rect {
    fill: var(--bg-raised);
    stroke: var(--border);
    vector-effect: non-scaling-stroke;
  }
  .link.chosen .label rect {
    stroke: var(--accent);
  }
  .label text {
    fill: var(--text-bright);
    font-family: "JetBrains Mono", ui-monospace, "SF Mono", Menlo, monospace;
  }
  .link.unlabelled .label text {
    fill: var(--muted);
  }
  .draft {
    stroke: var(--accent);
    stroke-dasharray: none;
  }
</style>
