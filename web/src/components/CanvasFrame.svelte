<script lang="ts">
  import { frameMin, frameTitle, type Box, type Frame } from "../lib/canvas";

  /**
   * A frame on the stage: a tinted box behind the tiles it holds, with
   * a title bar above it that is its handle. The body is not a handle —
   * it is behind the tiles and lets the pointer through to the backdrop
   * — so a press on a frame is always a press on its title or its grip.
   *
   * Membership is the canvas's business (a tile is in a frame when its
   * centre is); this component only draws the box, moves it, and offers
   * its name, lock and delete controls.
   */
  let {
    frame,
    box,
    zoom,
    lifted = false,
    oncommit,
    ondrift,
    onland,
    onrename,
    onlock,
    ondelete,
  }: {
    frame: Frame;
    /** Where it is drawn: the frame's box, plus any drift in flight. */
    box: Box;
    zoom: number;
    lifted?: boolean;
    /** A resize, committed. */
    oncommit: (box: Box) => void;
    /** A move, one step in stage units; the canvas carries the tiles. */
    ondrift: (dx: number, dy: number) => void;
    onland: (commit: boolean) => void;
    onrename: (name: string) => void;
    onlock: (locked: boolean) => void;
    ondelete: () => void;
  } = $props();

  let host: HTMLDivElement;
  let inFlight = $state<Box | null>(null);
  const shown = $derived(inFlight ?? box);

  let gesture: {
    kind: "move" | "resize";
    pointer: number;
    lastX: number;
    lastY: number;
    engaged: boolean;
  } | null = null;

  const begin = (event: PointerEvent, kind: "move" | "resize") => {
    if (event.button !== 0 || gesture || frame.locked) return;
    // Cancelled so the press moves no focus: a frame's title is a
    // handle, and taking focus from a terminal to grab it would end
    // the typing there.
    event.preventDefault();
    event.stopPropagation();
    gesture = {
      kind,
      pointer: event.pointerId,
      lastX: event.clientX,
      lastY: event.clientY,
      engaged: false,
    };
    host.setPointerCapture?.(event.pointerId);
  };

  const move = (event: PointerEvent) => {
    if (!gesture || event.pointerId !== gesture.pointer) return;
    const dx = (event.clientX - gesture.lastX) / zoom;
    const dy = (event.clientY - gesture.lastY) / zoom;
    gesture.lastX = event.clientX;
    gesture.lastY = event.clientY;
    gesture.engaged = true;
    if (gesture.kind === "move") ondrift(dx, dy);
    else {
      inFlight = {
        ...shown,
        w: Math.max(frameMin.w, shown.w + dx),
        h: Math.max(frameMin.h, shown.h + dy),
      };
    }
  };

  const up = (event: PointerEvent) => {
    if (!gesture || event.pointerId !== gesture.pointer) return;
    const { kind, engaged } = gesture;
    gesture = null;
    if (kind === "move") {
      if (engaged) onland(true);
      return;
    }
    if (inFlight) oncommit(inFlight);
    inFlight = null;
  };

  const cancel = (event: PointerEvent) => {
    if (!gesture || event.pointerId !== gesture.pointer) return;
    const { kind, engaged } = gesture;
    gesture = null;
    inFlight = null;
    if (kind === "move" && engaged) onland(false);
  };

  /** Renaming happens in place, in the title. */
  let editing = $state(false);
  let draft = $state("");
  let field = $state<HTMLInputElement>();

  const rename = () => {
    if (frame.locked) return;
    draft = frame.name;
    editing = true;
  };
  $effect(() => {
    if (editing && field) {
      field.focus();
      field.select();
    }
  });
  const commitName = () => {
    if (!editing) return;
    editing = false;
    const name = draft.trim();
    if (name && name !== frame.name) onrename(name);
  };
  const fieldKey = (event: KeyboardEvent) => {
    if (event.key === "Enter") {
      event.preventDefault();
      commitName();
    } else if (event.key === "Escape") {
      event.preventDefault();
      editing = false;
    }
  };

  /** The frame's own keys, for a title that has focus: Delete removes
   *  an unlocked frame, Enter renames. */
  const key = (event: KeyboardEvent) => {
    if (event.target !== host) return;
    if ((event.key === "Delete" || event.key === "Backspace") && !frame.locked) {
      event.preventDefault();
      ondelete();
    } else if (event.key === "Enter") {
      event.preventDefault();
      rename();
    }
  };

  const stop = (event: Event) => event.stopPropagation();
</script>

<div
  class="frame"
  class:locked={frame.locked}
  class:lifted
  style:left="{shown.x}px"
  style:top="{shown.y}px"
  style:width="{shown.w}px"
  style:height="{shown.h}px"
>
  <!-- The title bar sits above the box so it reads as a label rather
       than a header, and so the box itself is exactly the membership
       region: a tile whose centre is under the title is not inside. -->
  <div
    class="title"
    role="button"
    tabindex="0"
    aria-label="Frame {frame.name}"
    bind:this={host}
    style:top="-{frameTitle}px"
    style:height="{frameTitle}px"
    onpointerdown={(event) => begin(event, "move")}
    onpointermove={move}
    onpointerup={up}
    onpointercancel={cancel}
    ondblclick={rename}
    onkeydown={key}
  >
    <button
      class="lock"
      title={frame.locked ? "Unlock" : "Lock: nothing inside moves"}
      aria-label={frame.locked ? "Unlock frame" : "Lock frame"}
      onpointerdown={stop}
      onclick={() => onlock(!frame.locked)}
    >
      {frame.locked ? "🔒" : "🔓"}
    </button>
    {#if editing}
      <input
        class="name"
        bind:this={field}
        bind:value={draft}
        aria-label="Frame name"
        spellcheck="false"
        onpointerdown={stop}
        onkeydown={fieldKey}
        onblur={commitName}
      />
    {:else}
      <span class="name">{frame.name}</span>
    {/if}
    {#if !frame.locked}
      <button
        class="delete"
        title="Delete the frame (its tiles stay)"
        aria-label="Delete frame {frame.name}"
        onpointerdown={stop}
        onclick={ondelete}
      >
        ✕
      </button>
    {/if}
  </div>
  {#if !frame.locked}
    <div
      class="grip"
      aria-hidden="true"
      onpointerdown={(event) => begin(event, "resize")}
      onpointermove={move}
      onpointerup={up}
      onpointercancel={cancel}
    ></div>
  {/if}
</div>

<style>
  .frame {
    position: absolute;
    border: 1px dashed var(--border);
    border-radius: 8px;
    background: var(--frame-fill, color-mix(in srgb, var(--band) 8%, transparent));
    /* The body is behind the tiles and lets the pointer through: a press
       on a frame that is not on its title or grip is a press on the
       backdrop, which pans. */
    pointer-events: none;
  }
  .frame.lifted {
    border-color: var(--accent);
  }
  .frame.locked {
    border-style: solid;
  }
  .title {
    position: absolute;
    left: 0;
    display: flex;
    align-items: center;
    gap: 6px;
    padding: 0 6px 0 4px;
    max-width: 100%;
    background: var(--bg-raised);
    border: 1px solid var(--border);
    border-bottom: none;
    border-radius: 6px 6px 0 0;
    color: var(--muted);
    font-size: 11px;
    letter-spacing: 0.04em;
    cursor: grab;
    pointer-events: auto;
    box-sizing: border-box;
  }
  .frame.locked .title {
    cursor: default;
  }
  .frame.lifted .title {
    cursor: grabbing;
    color: var(--accent);
  }
  .title:focus-visible {
    outline: 1px solid var(--aim);
  }
  .name {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    color: var(--text);
  }
  input.name {
    width: 14ch;
    padding: 0 2px;
    background: var(--field);
    border: 1px solid var(--accent);
    border-radius: 3px;
    color: var(--text-bright);
    font: inherit;
  }
  input.name:focus {
    outline: none;
  }
  .lock,
  .delete {
    padding: 0 3px;
    border: none;
    background: transparent;
    color: var(--muted);
    font-size: 11px;
    line-height: 1;
    cursor: pointer;
  }
  .lock:hover,
  .delete:hover {
    color: var(--accent);
  }
  .grip {
    position: absolute;
    right: -1px;
    bottom: -1px;
    width: 18px;
    height: 18px;
    cursor: nwse-resize;
    pointer-events: auto;
    background: linear-gradient(
      135deg,
      transparent 0 55%,
      var(--muted) 55% 62%,
      transparent 62% 75%,
      var(--muted) 75% 82%,
      transparent 82%
    );
  }
</style>
