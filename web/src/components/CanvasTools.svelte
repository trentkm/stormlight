<script lang="ts">
  import { run, ui, type Tool } from "../lib/commands.svelte";

  /**
   * The toolbar across the top of the canvas: one button per tool, the
   * way Excalidraw's is laid out, with the letter that picks it. The
   * tool in hand is lit. Choosing one is the same command the letter
   * runs, so the two cannot drift.
   */
  const tools: Array<{ tool: Tool; key: string; name: string; glyph: string }> = [
    { tool: "select", key: "v", name: "Select", glyph: "↖" },
    { tool: "hand", key: "h", name: "Hand", glyph: "✋" },
    { tool: "rect", key: "r", name: "Rectangle", glyph: "▭" },
    { tool: "ellipse", key: "o", name: "Ellipse", glyph: "◯" },
    { tool: "line", key: "l", name: "Line", glyph: "╱" },
    { tool: "arrow", key: "a", name: "Arrow", glyph: "→" },
    { tool: "pencil", key: "p", name: "Pencil", glyph: "✎" },
    { tool: "text", key: "t", name: "Text", glyph: "A" },
    { tool: "frame", key: "f", name: "Frame", glyph: "⬚" },
  ];
</script>

<div class="tools" role="toolbar" aria-label="Canvas tools">
  {#each tools as entry (entry.tool)}
    <button
      class:on={ui.tool === entry.tool}
      title="{entry.name} ({entry.key})"
      aria-label={entry.name}
      aria-pressed={ui.tool === entry.tool}
      onclick={() => run(`tool-${entry.tool}`)}
      onpointerdown={(event) => event.preventDefault()}
    >
      <span class="glyph" aria-hidden="true">{entry.glyph}</span>
      <span class="key" aria-hidden="true">{entry.key}</span>
    </button>
  {/each}
</div>

<style>
  .tools {
    position: absolute;
    top: 10px;
    left: 50%;
    transform: translateX(-50%);
    display: flex;
    gap: 2px;
    padding: 4px;
    background: var(--bg-raised);
    border: 1px solid var(--border);
    border-radius: 8px;
    box-shadow: 0 4px 16px var(--shadow-lift);
    z-index: 1;
  }
  button {
    position: relative;
    width: 34px;
    height: 32px;
    padding: 0;
    border: none;
    border-radius: 5px;
    background: transparent;
    color: var(--text);
    font-size: 15px;
    line-height: 1;
    cursor: pointer;
  }
  button:hover {
    background: var(--hover-bg);
  }
  button.on {
    background: var(--band);
    color: var(--band-ink);
  }
  .glyph {
    display: block;
    padding-top: 1px;
  }
  .key {
    position: absolute;
    right: 3px;
    bottom: 1px;
    font-size: 8px;
    color: var(--muted);
  }
  button.on .key {
    color: var(--band-ink);
    opacity: 0.7;
  }
</style>
