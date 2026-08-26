<script lang="ts">
  import type { Shape } from "../lib/canvas";

  /**
   * The drawings: every shape on the stage, in one SVG that pans and
   * zooms with the tiles. Strokes are drawn in screen pixels
   * (non-scaling), so a line is a line at any zoom; the arrowhead is
   * sized against the zoom for the same reason.
   *
   * Each shape is drawn twice: a wide transparent stroke underneath,
   * for the hand to find, and the visible one on top. The layer takes
   * the pointer only under the select tool — under a drawing tool the
   * overlay above it has the pointer, and drawings must not steal a
   * stroke that crosses them.
   */
  let {
    shapes,
    draft = null,
    chosen = null,
    moving = null,
    zoom,
    interactive,
    onpick,
  }: {
    shapes: Record<string, Shape>;
    /** The shape being drawn, not yet committed. */
    draft?: Shape | null;
    /** The shape the select tool holds. */
    chosen?: string | null;
    /** A shape mid-drag, and how far it has come. */
    moving?: { id: string; dx: number; dy: number } | null;
    zoom: number;
    interactive: boolean;
    onpick: (id: string, event: PointerEvent) => void;
  } = $props();

  const shown = (id: string, shape: Shape): Shape =>
    moving && moving.id === id
      ? { ...shape, x: shape.x + moving.dx, y: shape.y + moving.dy }
      : shape;

  const path = (shape: Shape): string =>
    (shape.points ?? [])
      .map(([x, y], i) => `${i === 0 ? "M" : "L"} ${x} ${y}`)
      .join(" ");

  const lines = (shape: Shape): string[] => (shape.text ?? "").split("\n");

  /** The arrowhead, in stage units that come out screen-sized. */
  const head = $derived(12 / zoom);
</script>

<svg class="drawings" class:interactive aria-hidden="true">
  <defs>
    <marker
      id="arrowhead"
      viewBox="0 0 10 10"
      refX="9"
      refY="5"
      markerWidth={head}
      markerHeight={head}
      markerUnits="userSpaceOnUse"
      orient="auto-start-reverse"
    >
      <path d="M 0 0 L 10 5 L 0 10 z" class="head" />
    </marker>
  </defs>
  {#each Object.entries(shapes) as [id, stored] (id)}
    {@const shape = shown(id, stored)}
    <!-- svelte-ignore a11y_no_static_element_interactions -->
    <g
      class="shape {shape.kind}"
      class:chosen={chosen === id}
      data-id={id}
      onpointerdown={(event) => onpick(id, event)}
    >
      {#if shape.kind === "rect"}
        <rect class="hit" x={shape.x} y={shape.y} width={shape.w} height={shape.h} rx="6" />
        <rect class="ink" x={shape.x} y={shape.y} width={shape.w} height={shape.h} rx="6" />
      {:else if shape.kind === "ellipse"}
        <ellipse class="hit" cx={shape.x + shape.w / 2} cy={shape.y + shape.h / 2} rx={shape.w / 2} ry={shape.h / 2} />
        <ellipse class="ink" cx={shape.x + shape.w / 2} cy={shape.y + shape.h / 2} rx={shape.w / 2} ry={shape.h / 2} />
      {:else if shape.kind === "text"}
        <text class="ink" x={shape.x} y={shape.y}>
          {#each lines(shape) as line, i (i)}
            <tspan x={shape.x} dy={i === 0 ? "0" : "1.3em"}>{line}</tspan>
          {/each}
        </text>
      {:else}
        <g transform="translate({shape.x} {shape.y})">
          <path class="hit" d={path(shape)} />
          <path
            class="ink"
            d={path(shape)}
            marker-end={shape.kind === "arrow" ? "url(#arrowhead)" : undefined}
          />
        </g>
      {/if}
      {#if chosen === id}
        <rect
          class="halo"
          x={shape.x - 4 / zoom}
          y={shape.y - 4 / zoom}
          width={shape.w + 8 / zoom}
          height={shape.h + 8 / zoom}
        />
      {/if}
    </g>
  {/each}
  {#if draft}
    <g class="shape draft {draft.kind}">
      {#if draft.kind === "rect"}
        <rect class="ink" x={draft.x} y={draft.y} width={draft.w} height={draft.h} rx="6" />
      {:else if draft.kind === "ellipse"}
        <ellipse class="ink" cx={draft.x + draft.w / 2} cy={draft.y + draft.h / 2} rx={draft.w / 2} ry={draft.h / 2} />
      {:else}
        <g transform="translate({draft.x} {draft.y})">
          <path
            class="ink"
            d={path(draft)}
            marker-end={draft.kind === "arrow" ? "url(#arrowhead)" : undefined}
          />
        </g>
      {/if}
    </g>
  {/if}
</svg>

<style>
  .drawings {
    position: absolute;
    top: 0;
    left: 0;
    /* A point, like the stage: shapes place themselves in stage units
       and the SVG shows whatever is drawn outside its zero-size box. */
    width: 0;
    height: 0;
    overflow: visible;
    pointer-events: none;
  }
  .drawings.interactive .shape {
    pointer-events: auto;
    cursor: move;
  }
  .ink {
    fill: none;
    stroke: var(--text-bright);
    stroke-width: 2;
    stroke-linecap: round;
    stroke-linejoin: round;
    vector-effect: non-scaling-stroke;
  }
  text.ink {
    fill: var(--text-bright);
    stroke: none;
    font: 14px "JetBrains Mono", ui-monospace, "SF Mono", Menlo, monospace;
    dominant-baseline: hanging;
    white-space: pre;
  }
  .head {
    fill: var(--text-bright);
  }
  .hit {
    fill: none;
    stroke: transparent;
    stroke-width: 14;
    vector-effect: non-scaling-stroke;
  }
  .shape.chosen .ink {
    stroke: var(--accent);
  }
  .shape.chosen text.ink {
    fill: var(--accent);
  }
  .halo {
    fill: none;
    stroke: var(--accent);
    stroke-width: 1;
    stroke-dasharray: 4 3;
    vector-effect: non-scaling-stroke;
    pointer-events: none;
  }
  .draft .ink {
    stroke: var(--muted);
  }
</style>
