<script lang="ts">
  import { Terminal } from "@xterm/xterm";
  import { FitAddon } from "@xterm/addon-fit";
  import { attach, type Attachment } from "../lib/terminal";
  import { statusVisual, theme } from "../lib/theme";
  import { isUrgent, type Agent } from "../lib/types";

  let { agent, onopen }: { agent: Agent; onopen: () => void } = $props();

  let host: HTMLDivElement;
  let screen: HTMLDivElement;
  // Attached until something says otherwise. The observer below turns
  // cells off when they scroll away, but a callback that never arrives —
  // a hidden tab defers them, and Chrome does exactly that — must leave
  // a cell showing its terminal rather than showing nothing. Costly and
  // right beats cheap and blank.
  let visible = $state(true);
  let scale = $state(1);

  const status = $derived(
    statusVisual(agent.activity, agent.attention, agent.process_live),
  );

  // Only what is on screen stays attached. A wall of thirty agents would
  // otherwise hold thirty daemon attachments and thirty terminals to
  // paint pixels nobody is looking at.
  $effect(() => {
    const watcher = new IntersectionObserver(
      ([entry]) => (visible = entry.isIntersecting),
      { rootMargin: "200px" },
    );
    watcher.observe(host);
    return () => watcher.disconnect();
  });

  $effect(() => {
    if (!visible || !screen) return;
    const id = agent.id;

    const term = new Terminal({
      fontFamily: 'ui-monospace, "SF Mono", Menlo, monospace',
      fontSize: 12,
      lineHeight: 1.1,
      // Canvas, not WebGL: browsers allow only a handful of WebGL
      // contexts at once, and a wall wants more cells than that. The
      // cells are small and mostly idle, which is what canvas is fine at.
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
      id,
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
      scale = Math.min(
        host.clientWidth / natural.offsetWidth,
        (host.clientHeight - labelHeight) / natural.offsetHeight,
      );
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
  <div class="screen" bind:this={screen} style:transform="scale({scale})"></div>
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
    flex: 1 1 auto;
    min-height: 0;
    overflow: hidden;
    /* Grown from the middle: aspect ratio is fixed by the terminal, so
       one axis always has slack, and slack split evenly reads as a
       framed screen rather than as content that failed to fill. */
    transform-origin: center center;
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
