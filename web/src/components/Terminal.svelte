<script lang="ts">
  import { Terminal } from "@xterm/xterm";
  import { FitAddon } from "@xterm/addon-fit";
  import { WebglAddon } from "@xterm/addon-webgl";
  import "@xterm/xterm/css/xterm.css";
  import { attach, type Attachment } from "../lib/terminal";
  import { theme } from "../lib/theme";

  let { agentID }: { agentID: string } = $props();

  let host: HTMLDivElement;

  $effect(() => {
    // Re-runs when the selected agent changes: one terminal per agent,
    // torn down and rebuilt rather than re-pointed, so no state from the
    // last one survives into the next.
    const id = agentID;
    if (!id || !host) return;

    const term = new Terminal({
      fontFamily: '"JetBrains Mono", ui-monospace, "SF Mono", Menlo, monospace',
      fontSize: 13,
      lineHeight: 1.2,
      allowProposedApi: true,
      theme: {
        background: theme.bgSunken,
        foreground: theme.text,
        cursor: theme.band,
        selectionBackground: "#3D4245",
      },
    });
    const fitAddon = new FitAddon();
    term.loadAddon(fitAddon);
    term.open(host);
    try {
      // The GPU renderer is what makes many live terminals affordable.
      // It is not available everywhere, and the canvas fallback is fine.
      term.loadAddon(new WebglAddon());
    } catch {
      // Fallback renderer; nothing to do.
    }
    // Measure only once the pane has a shape. Fitting a collapsed one
    // yields a two-column terminal, and this terminal is shared.
    const laidOut = () => host.offsetWidth > 0 && host.offsetHeight > 0;
    if (laidOut()) fitAddon.fit();
    let attachment: Attachment | undefined = attach(term, fitAddon, id, laidOut);
    const observer = new ResizeObserver(() => attachment?.fit());
    observer.observe(host);

    return () => {
      observer.disconnect();
      attachment?.close();
      attachment = undefined;
      term.dispose();
    };
  });
</script>

<div class="terminal" bind:this={host}></div>

<style>
  .terminal {
    flex: 1 1 auto;
    min-height: 0;
    padding: 8px 10px;
    overflow: hidden;
  }
</style>
