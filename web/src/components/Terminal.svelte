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
  let {
    agentID,
    focused = false,
    onenter,
  }: {
    agentID: string;
    focused?: boolean;
    /** The terminal was clicked: someone wants to type here. The page
     *  decides whether that becomes a walk-in; this only asks. */
    onenter?: () => void;
  } = $props();
  /**
   * How long a pane's geometry has to hold still before it is asserted.
   *
   * Long enough that a drag lands one resize rather than one per frame,
   * short enough that letting go feels like it took.
   */
  const settleDelay = 120;

  let host: HTMLDivElement;
  let connection = $state<Connection>("live");
  let term = $state<Terminal>();

  /**
   * The agent this pane is bound to, held as a value rather than read
   * through the prop.
   *
   * `agentID` arrives as a getter over `selected().id`, and every roster
   * push re-proxies every agent — so reading it inside the effect below
   * makes that effect depend on the roster rather than on the identity
   * it names. The value never changes; the thing it was read through
   * does, once a poll.
   *
   * What that cost: the terminal was disposed and rebuilt at the
   * roster's cadence. A fresh socket, a fresh seed, and a replica
   * rebuilt from nothing — which is the view landing on the first line
   * of an empty buffer and snapping back a moment later, about once a
   * second. Worst for the providers that change something every tick;
   * codex animates its terminal title, and the title is in the roster.
   *
   * A $derived settles it. Its value is compared before anything
   * downstream is disturbed, and the same string disturbs nothing.
   * AgentScreen takes a plain string prop for the same reason, and says
   * so; this is that guard, for the pane the wall's cells borrowed it
   * from.
   */
  const boundID = $derived(agentID);

  $effect(() => {
    // Re-runs when the selected agent changes: one terminal per agent,
    // torn down and rebuilt rather than re-pointed, so no state from the
    // last one survives into the next.
    const id = boundID;
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
      // A horizontal swipe or a shift-wheel carries no vertical
      // intention; xterm's own handler bails on it, and this one runs
      // ahead of that guard, so it has to bail too — otherwise sideways
      // scrolled the transcript upward.
      if (event.deltaY === 0) return false;
      // Proportional to what the device actually reported. A fixed
      // three lines turned one trackpad flick — sixty momentum events
      // of two pixels — into ninety lines.
      const lines =
        event.deltaMode === WheelEvent.DOM_DELTA_LINE
          ? event.deltaY
          : event.deltaMode === WheelEvent.DOM_DELTA_PAGE
            ? event.deltaY * built.rows
            : event.deltaY / 20;
      // Never round a real movement away to nothing.
      const whole = Math.trunc(lines);
      built.scrollLines(whole === 0 ? Math.sign(lines) : whole);
      return false;
    });

    const fitAddon = new FitAddon();
    built.loadAddon(fitAddon);
    built.open(host);
    term = built;
    try {
      // The GPU renderer is what makes many live terminals affordable.
      // It is not available everywhere, and the DOM fallback is fine.
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
    // A layout still in motion is not a size worth asserting.
    //
    // The observer fires on every intermediate width — a window being
    // dragged, the rail and roster folding away under zoom — and this
    // terminal is shared, so each size named here reflows the replica
    // and makes the agent repaint for every viewer. Codex clears the
    // screen and its scrollback outside its synchronized-update block
    // when it does, so each one is a pane that blanks and comes back.
    // Waiting for the layout to settle turns a drag from a flash per
    // frame into a flash per resize, which is the one a terminal
    // genuinely owes you.
    let settle: number | undefined;
    const observer = new ResizeObserver(() => {
      window.clearTimeout(settle);
      settle = window.setTimeout(() => attachment?.fit(), settleDelay);
    });
    observer.observe(host);

    return () => {
      window.clearTimeout(settle);
      observer.disconnect();
      attachment?.close();
      attachment = undefined;
      term = undefined;
      built.dispose();
    };
  });

  // Focus follows the walked-in state rather than the mouse. Walking
  // in also follows the tail: the prompt you are about to type at is
  // at the bottom, and arriving somewhere scrolled up is arriving
  // where you cannot see what you are doing.
  $effect(() => {
    if (!term) return;
    if (focused) {
      term.scrollToBottom();
      term.focus();
    } else {
      term.blur();
    }
  });

  /**
   * A click on the terminal is a request to type in it.
   *
   * xterm focuses itself on mousedown from its own listener, which used
   * to be pure hazard: a click meant to read left the terminal holding
   * the keyboard, and every key the page did not claim went down the
   * socket to a live agent. So the click now *asks* — and the page
   * answers by walking in or not. If it does not, the focus xterm took
   * for itself is handed straight back.
   *
   * The claim is remembered across the moment because mousedown runs
   * before the focus it causes, and the answer arrives a render later.
   */
  $effect(() => {
    if (!host) return;
    let claiming = false;
    const claim = () => {
      claiming = true;
      onenter?.();
      queueMicrotask(() => {
        claiming = false;
      });
    };
    const stolen = () => {
      if (!focused && !claiming) term?.blur();
    };
    host.addEventListener("mousedown", claim);
    host.addEventListener("focusin", stolen);
    return () => {
      host.removeEventListener("mousedown", claim);
      host.removeEventListener("focusin", stolen);
    };
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
    /* Anchored to the bottom, because a terminal's geometry is shared:
       another viewer can hold it at more rows than this pane can show,
       and then something has to be clipped. Clipping the top costs
       scrollback that scrolls back; clipping the bottom costs the
       prompt and the status line — the two things you always need. */
    display: flex;
    flex-direction: column;
    justify-content: flex-end;
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
