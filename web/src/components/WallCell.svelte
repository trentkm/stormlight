<script lang="ts">
  import { Terminal } from "@xterm/xterm";
  import { FitAddon } from "@xterm/addon-fit";
  // Imported here as well as in Terminal.svelte: this component renders
  // xterm too, and must not depend on sharing a chunk with the roster's
  // pane to be styled.
  import "@xterm/xterm/css/xterm.css";
  import { attach, type Attachment } from "../lib/terminal";
  import { statusVisual, theme } from "../lib/theme";
  import { isUrgent, type Agent } from "../lib/types";

  let {
    agent,
    scroller,
    onopen,
  }: { agent: Agent; scroller: HTMLElement | undefined; onopen: () => void } =
    $props();

  // The id, held apart from the object it came from. Every roster push
  // re-proxies every agent, so an effect that read `agent.id` directly
  // would depend on the object and re-run on every push — tearing down
  // and re-attaching every cell's terminal at the roster's cadence. A
  // derived string only changes when the id itself does.
  const id = $derived(agent.id);

  let host: HTMLDivElement;
  let viewport: HTMLDivElement;
  let screen: HTMLDivElement;
  // Attached until something says otherwise. The observer below turns
  // cells off when they scroll away, but a callback that never arrives —
  // a hidden tab defers them, and Chrome does exactly that — must leave
  // a cell showing its terminal rather than showing nothing. Costly and
  // right beats cheap and blank.
  let visible = $state(true);
  let scale = $state(1);
  let shiftX = $state(0);
  let shiftY = $state(0);

  const status = $derived(
    statusVisual(agent.activity, agent.attention, agent.process_live),
  );

  // Only what is on screen stays attached. A wall of thirty agents would
  // otherwise hold thirty daemon attachments and thirty terminals to
  // paint pixels nobody is looking at.
  $effect(() => {
    // The root must be the element that actually scrolls. Against the
    // default (viewport) root, rootMargin inflates a rectangle the wall
    // never leaves, while the .wall scroller clips unenlarged — so cells
    // would detach the instant they crossed its edge and reattach, seed
    // and all, the instant they returned.
    if (!scroller) return;
    const watcher = new IntersectionObserver(
      ([entry]) => (visible = entry.isIntersecting),
      { root: scroller, rootMargin: "200px" },
    );
    watcher.observe(host);
    return () => watcher.disconnect();
  });

  $effect(() => {
    if (!visible || !screen) return;
    // Bind the id before anything async: `id` is what this effect keys
    // on, deliberately not `agent`.
    const agentID = id;

    const term = new Terminal({
      fontFamily: 'ui-monospace, "SF Mono", Menlo, monospace',
      fontSize: 12,
      lineHeight: 1.1,
      // The DOM renderer, not WebGL: browsers allow only a handful of
      // WebGL contexts at once, and a wall wants more cells than that.
      // No scrollback — a cell cannot be scrolled, so history would be
      // rows of DOM kept for nobody, multiplied by the fleet.
      scrollback: 0,
      allowProposedApi: true,
      disableStdin: true,
      cursorBlink: false,
      theme: {
        background: theme.bgSunken,
        foreground: theme.text,
        cursor: theme.bgSunken,
      },
    });
    const fitAddon = new FitAddon();
    term.loadAddon(fitAddon);
    term.open(screen);

    // Watching: no keystrokes, and no size. The terminal is shared, so a
    // cell that announced its own geometry would reflow the agent for
    // everyone — including the dashboard reading it.
    const attachment: Attachment = attach(
      term,
      fitAddon,
      agentID,
      () => false,
      () => {},
      { watching: true },
    );

    // The cell shows the whole screen, shrunk, rather than a corner of
    // it: a wall is for recognising a shape across the room, and a
    // clipped terminal is unrecognisable in exactly the way that matters.
    const rescale = () => {
      const natural = screen.querySelector(".xterm-screen") as HTMLElement | null;
      if (!natural || !natural.offsetWidth) return;
      // Fill the cell, up or down. A wall is read at a glance across a
      // room, so an 80-column agent in a wide tile should grow into it
      // rather than sit small in a corner — and the cap that prevented
      // that bought nothing, since scaled text re-rasterizes rather than
      // blurring.
      //
      // Scale about the top-left and centre by hand. transform doesn't
      // shrink the layout box, so a terminal wider than the tile hangs
      // out past the box's right edge — and scaling about the *box*
      // centre leaves the shrunken content parked around the overflow's
      // centre, off to the bottom-right and clipped. Anchoring at the
      // corner makes the maths honest: content lands at natural × scale,
      // and the translate splits the remaining slack evenly.
      const width = viewport.clientWidth;
      const height = viewport.clientHeight;
      scale = Math.min(
        width / natural.offsetWidth,
        height / natural.offsetHeight,
      );
      shiftX = (width - natural.offsetWidth * scale) / 2;
      shiftY = (height - natural.offsetHeight * scale) / 2;
    };
    const sizes = new ResizeObserver(rescale);
    sizes.observe(host);
    const frames = setInterval(rescale, 500);

    return () => {
      clearInterval(frames);
      sizes.disconnect();
      attachment.close();
      term.dispose();
    };
  });

  const labelHeight = 26;
</script>

<div
  class="cell"
  class:urgent={isUrgent(agent)}
  class:done={!agent.process_live}
  bind:this={host}
>
  <button class="label" onclick={onopen} title={agent.task}>
    <span style:color={isUrgent(agent) ? "#1f2328" : status.color}>{status.glyph}</span>
    <span class="name">{agent.name || agent.task || agent.id.slice(0, 8)}</span>
    <span class="where">{agent.workspace?.name ?? ""}</span>
  </button>
  <!-- Two elements where one looks like enough. The outer clips; the
       inner holds the terminal at its natural size and carries the
       transform. Collapse them and the clip runs at layout size, before
       the transform — a terminal wider than the tile loses its right and
       bottom edges first, and the scale then shrinks the surviving crop:
       a corner, smaller, instead of the whole screen. -->
  <div class="screen" bind:this={viewport}>
    <div
      class="frame"
      bind:this={screen}
      style:transform="translate({shiftX}px, {shiftY}px) scale({scale})"
    ></div>
  </div>
  <!-- The whole screen is the way in, not just the label. A watching
       cell takes no keystrokes by design — the shared terminal must not
       hear from a tile — so the click anyone's hand actually makes on
       "their" terminal has to lead somewhere: to the roster, focused on
       this agent, where typing works. -->
  <button class="reach" onclick={onopen} aria-label="Open {agent.name || agent.task || agent.id}"></button>
  {#if isUrgent(agent)}
    <p class="needs">needs input</p>
  {/if}
</div>

<style>
  .cell {
    position: relative;
    display: flex;
    flex-direction: column;
    min-width: 0;
    min-height: 0;
    overflow: hidden;
    background: var(--bg-sunken);
    border: 1px solid var(--border);
    border-radius: 6px;
  }
  .cell:hover {
    border-color: var(--band);
  }
  /* After :hover on purpose: equal specificity, so order keeps the
     urgent band from being repainted by a passing pointer. */
  .cell.urgent {
    border-color: var(--waiting);
    box-shadow: 0 0 14px rgba(229, 192, 123, 0.18);
  }
  .cell.done {
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
    border: none;
    border-bottom: 1px solid var(--border);
    color: var(--text);
    font: inherit;
    font-size: 12px;
    text-align: left;
    cursor: pointer;
  }
  .cell.urgent .label {
    background: var(--waiting);
    color: #1f2328;
  }
  .label:hover {
    color: var(--band);
  }
  .cell.urgent .label:hover {
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
  .cell.urgent .where {
    color: #4a3e1e;
  }
  .screen {
    position: relative;
    flex: 1 1 auto;
    min-height: 0;
    overflow: hidden;
  }
  .frame {
    position: absolute;
    top: 0;
    left: 0;
    /* Shrink-wrap the terminal rather than inheriting the tile's width:
       xterm's screen is explicitly sized in pixels, and the frame must
       be that size for the clip above to contain all of it. */
    width: max-content;
    transform-origin: top left;
  }
  .reach {
    position: absolute;
    top: 26px;
    left: 0;
    right: 0;
    bottom: 0;
    padding: 0;
    background: transparent;
    border: none;
    cursor: pointer;
  }
  .needs {
    position: absolute;
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
</style>
