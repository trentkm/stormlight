<script lang="ts">
  import { Terminal } from "@xterm/xterm";
  import { FitAddon } from "@xterm/addon-fit";
  import { WebglAddon } from "@xterm/addon-webgl";
  import "@xterm/xterm/css/xterm.css";
  import { attach, type Attachment, type Connection } from "../lib/terminal";
  import { terminal } from "../lib/theme";

  /**
   * `focused` is the walked-in state: when it is true the agent has the
   * keyboard, so xterm takes focus and every keystroke the page did not
   * claim goes down the socket. When it is false the terminal is a
   * picture — it still paints, but it must not hold focus, or the
   * roster's own keys would be typed into someone's agent.
   */
  let { agentID, focused = false }: { agentID: string; focused?: boolean } =
    $props();

  let host: HTMLDivElement;
  let connection = $state<Connection>("live");
  let term = $state<Terminal>();

  $effect(() => {
    // Re-runs when the selected agent changes: one terminal per agent,
    // torn down and rebuilt rather than re-pointed, so no state from the
    // last one survives into the next.
    const id = agentID;
    if (!id || !host) return;

    const built = new Terminal({
      fontFamily: '"JetBrains Mono", ui-monospace, "SF Mono", Menlo, monospace',
      fontSize: 13,
      lineHeight: 1.2,
      allowProposedApi: true,
      theme: {
        background: terminal.background,
        foreground: terminal.foreground,
        cursor: terminal.cursor,
        selectionBackground: terminal.selectionBackground,
      },
    });
    /**
     * The wheel scrolls the terminal, not the agent.
     *
     * An agent that has asked for mouse reporting gets wheel events as
     * input, and a CLI with a prompt reads them as history — so
     * scrolling up over the transcript cycled through past prompts
     * instead of showing what scrolled off. In the normal buffer there
     * is scrollback and scrolling it is unambiguously what was meant.
     * In the alternate screen there is none, and the wheel belongs to
     * whatever full-screen program is drawing there.
     */
    built.attachCustomWheelEventHandler((event) => {
      if (built.buffer.active.type === "alternate") return true;
      const lines = event.deltaY > 0 ? 3 : -3;
      built.scrollLines(lines);
      return false;
    });

    const fitAddon = new FitAddon();
    built.loadAddon(fitAddon);
    built.open(host);
    term = built;
    try {
      // The GPU renderer is what makes many live terminals affordable.
      // It is not available everywhere, and the canvas fallback is fine.
      built.loadAddon(new WebglAddon());
    } catch {
      // Fallback renderer; nothing to do.
    }
    // Measure only once the pane has a shape. Fitting a collapsed one
    // yields a two-column terminal, and this terminal is shared.
    const laidOut = () => host.offsetWidth > 0 && host.offsetHeight > 0;
    if (laidOut()) fitAddon.fit();
    let attachment: Attachment | undefined = attach(
      built,
      fitAddon,
      id,
      laidOut,
      (state) => (connection = state),
    );
    const observer = new ResizeObserver(() => attachment?.fit());
    observer.observe(host);

    return () => {
      observer.disconnect();
      attachment?.close();
      attachment = undefined;
      term = undefined;
      built.dispose();
    };
  });

  // Focus follows the walked-in state rather than the mouse.
  $effect(() => {
    if (!term) return;
    if (focused) term.focus();
    else term.blur();
  });

  /**
   * xterm focuses itself on mousedown, unconditionally and from its own
   * listener. Reacting to the `focused` prop cannot undo that — the
   * prop did not change — so a click on a terminal nobody walked into
   * left it holding the keyboard, and every key the page does not claim
   * went down the socket to a live agent. Watching focusin is what
   * actually catches it.
   */
  $effect(() => {
    if (!host) return;
    const stolen = () => {
      if (!focused) term?.blur();
    };
    host.addEventListener("focusin", stolen);
    return () => host.removeEventListener("focusin", stolen);
  });
</script>

<!-- data-walk-target is the walked-in keyboard's anchor, and it is a
     data attribute rather than a class because `.terminal` is not ours
     alone: xterm adds that class to its own container, so every
     watching wall cell answers to it too. Focus rules that keyed on the
     class were one shared view away from handing an agent's keyboard to
     a cell that only wanted to be looked at. -->
<div class="terminal" data-walk-target bind:this={host}>
  {#if connection === "reconnecting"}
    <!-- Said out loud, because the alternative is a frozen pane that
         looks like an agent gone quiet. -->
    <p class="reconnecting">reconnecting…</p>
  {/if}
</div>

<style>
  .terminal {
    position: relative;
    flex: 1 1 auto;
    min-height: 0;
    padding: 8px 10px;
    /* The terminal's own ground, in both themes: the padding around
       the screen has to be the screen's color, or a light dashboard
       frames the agent's output in a bright margin. */
    background: var(--term-bg);
    overflow: hidden;
  }
  .reconnecting {
    position: absolute;
    top: 8px;
    right: 12px;
    z-index: 1;
    margin: 0;
    padding: 2px 8px;
    background: var(--bg-raised);
    border: 1px solid var(--waiting);
    border-radius: 4px;
    color: var(--waiting);
    font-size: 11px;
  }
</style>
