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

  function flag(name: string, fallback: string): string {
    try {
      return new URL(window.location.href).searchParams.get(name) ?? fallback;
    } catch {
      return fallback;
    }
  }

  /**
   * The renderer, which on this branch is the DOM one by default.
   *
   * Both paint the same bytes, so anything visible in one and not the
   * other belongs to the renderer rather than to the stream — and only
   * the DOM one leaves what it painted where it can be read, which is
   * what the probe below needs. `?gpu=on` puts the GPU renderer back.
   */
  function gpuWanted(): boolean {
    return flag("gpu", "off") === "on";
  }

  /** Armed by default on this branch; `?probe=0` turns it off. */
  function probing(): boolean {
    return flag("probe", "1") !== "0";
  }

  /**
   * A record of what this pane actually shows, taken from the renderer
   * rather than alongside it.
   *
   * `?gpu=off&probe=1` arms it. It samples on the terminal's own render
   * event, so it neither requests frames nor holds the frame loop open —
   * an earlier version sampled on requestAnimationFrame and could have
   * masked a stall in the very loop it was there to catch.
   *
   * Two things leave a mark. A paint that lands blank while the emulator
   * holds content shows up as a sample with an empty screen; a renderer
   * that stops paints nothing at all, and shows up as a gap between
   * samples. The grid and the box travel with each one, so a resize
   * happening underneath the paint is visible too. window.__flicker()
   * reads it back.
   */
  function probe(built: Terminal, box: HTMLDivElement): void {
    type Sample = {
      t: number;
      painted: number;
      held: number;
      cols: number;
      rows: number;
      w: number;
      h: number;
      /** Where the cursor sat when this paint went out. */
      cx: number;
      cy: number;
      /** Which line of the buffer the top of the view was showing. */
      top: number;
    };
    // Kept on the page rather than in this closure: selecting another
    // agent rebuilds the terminal, and a record that died with it meant
    // every reading started from nothing a moment before it was read.
    const store = window as unknown as {
      __flickerSamples?: Sample[];
      __flickerEvents?: string[];
      __flickerWaits?: number[];
    };
    const samples: Sample[] = (store.__flickerSamples ??= []);
    const events: string[] = (store.__flickerEvents ??= []);
    const latencies: number[] = (store.__flickerWaits ??= []);
    const armed = Math.round(performance.now());

    // Written bytes, watched for the two things that decide when a paint
    // happens: whether output is waiting for one, and whether the
    // terminal is inside a synchronized update — which is the only
    // reason xterm has to hold one back. Nothing paints between ?2026h
    // and ?2026l, by design, so a block that stays open is a screen that
    // stays frozen.
    const decoder = new TextDecoder();
    let pendingSince = 0;
    let syncDepth = 0;
    let syncSince = 0;
    const write = built.write.bind(built);
    built.write = (data: Parameters<Terminal["write"]>[0], done?: () => void) => {
      const now = performance.now();
      if (!pendingSince) pendingSince = now;
      const text = typeof data === "string" ? data : decoder.decode(data);
      for (const marker of text.matchAll(/\x1b\[\?2026([hl])/g)) {
        if (marker[1] === "h") {
          if (syncDepth++ === 0) syncSince = now;
        } else if (--syncDepth <= 0) {
          syncDepth = 0;
          syncSince = 0;
        }
      }
      if (syncSince && now - syncSince > 150) {
        const held = Math.round(now - syncSince);
        const line = `flicker: stuck inside a synchronized update ${held}ms`;
        events.push(line);
        console.log(line);
        syncSince = now;
      }
      return write(data, done);
    };
    // Said out loud as it happens, so catching one is a matter of
    // watching rather than of calling __flicker() at the right instant.
    let hadContent = false;
    let previous = 0;
    let lastGrid = "";
    let previousSample: Sample | undefined;
    const note = (what: string, sample: Sample) => {
      const line =
        `flicker: ${what} at t=${sample.t}ms — painted ${sample.painted}, ` +
        `held ${sample.held}, grid ${sample.cols}x${sample.rows}, ` +
        `box ${sample.w}x${sample.h}`;
      events.push(line);
      console.log(line);
    };
    built.onRender(() => {
      const painted = box.querySelector(".xterm-rows");
      let drawn = -1;
      if (painted) {
        drawn = 0;
        for (const row of painted.children) {
          if ((row.textContent ?? "").trim() !== "") drawn++;
        }
      }
      const buffer = built.buffer.active;
      let held = 0;
      for (let index = 0; index < built.rows; index++) {
        const line = buffer.getLine(buffer.viewportY + index);
        if (line && line.translateToString(true).trim() !== "") held++;
      }
      const sample: Sample = {
        t: Math.round(performance.now()),
        painted: drawn,
        held,
        cols: built.cols,
        rows: built.rows,
        w: box.offsetWidth,
        h: box.offsetHeight,
        cx: buffer.cursorX,
        cy: buffer.cursorY,
        top: buffer.viewportY,
      };
      samples.push(sample);
      if (samples.length > 6000) samples.shift();

      // The two shapes left, and they are told apart by where things
      // were when the paint went out rather than by anyone's memory of
      // what it looked like. A cursor painted at home is the drawing
      // head caught mid-frame; a view that jumped is the buffer being
      // cleared underneath it.
      if (previousSample) {
        const wasHome = sample.cx === 0 && sample.cy === 0;
        const cameFromElsewhere =
          previousSample.cx !== 0 || previousSample.cy !== 0;
        if (wasHome && cameFromElsewhere) {
          note(
            `the cursor was painted at home, from ${previousSample.cx},${previousSample.cy}`,
            sample,
          );
        }
        const jumped = Math.abs(sample.top - previousSample.top);
        if (jumped >= 3) {
          note(`the view jumped ${jumped} lines to ${sample.top}`, sample);
        }
      }
      previousSample = sample;

      if (lastGrid && lastGrid !== `${built.cols}x${built.rows}`) {
        note(`the grid changed from ${lastGrid}`, sample);
      }
      lastGrid = `${built.cols}x${built.rows}`;

      // Three shapes worth shouting about, each a different culprit.
      // The screen emptying is the terminal being cleared and not yet
      // redrawn; a blank paint is the renderer showing nothing while
      // the emulator holds a screen; a stall is the renderer stopping.
      if (held >= 5) hadContent = true;
      if (hadContent && held <= 2) {
        note("the screen emptied", sample);
      } else if (drawn >= 0 && held >= 8 && drawn * 2 < held) {
        // Not only a paint that shows nothing. A frame that shows a
        // third of the screen while the emulator holds all of it reads
        // as a flash just the same, and it is the likelier shape: a
        // screen erased and half redrawn.
        note(`a paint showed ${drawn} of ${held} rows`, sample);
      }
      // Latency, not silence. A gap between paints means nothing on its
      // own — an agent that produced nothing has nothing to paint. What
      // counts is output that waited: the time from the write that
      // carried it to the paint that showed it.
      if (pendingSince) {
        const waited = Math.round(sample.t - pendingSince);
        latencies.push(waited);
        if (latencies.length > 6000) latencies.shift();
        if (waited > 200) note(`output waited ${waited}ms to be painted`, sample);
        pendingSince = 0;
      }
      previous = sample.t;
    });

    (window as { __flicker?: () => unknown }).__flicker = () => {
      if (!samples.length) {
        return {
          renders: 0,
          note: "the renderer never reported a paint — armed at " + armed,
        };
      }
      const blanks = samples.filter(
        (s) => s.painted >= 0 && s.painted <= 2 && s.held >= 5,
      );
      const gaps: number[] = [];
      for (let index = 1; index < samples.length; index++) {
        gaps.push(samples[index].t - samples[index - 1].t);
      }
      const sorted = [...gaps].sort((a, b) => a - b);
      const drops: Sample[][] = [];
      for (const blank of blanks.slice(0, 8)) {
        const at = samples.indexOf(blank);
        drops.push(samples.slice(Math.max(0, at - 2), at + 3));
      }
      const waits = [...latencies].sort((a, b) => a - b);
      // What kind, and how many of each: the shape of the trouble in one
      // line rather than a transcript to read.
      const kinds: Record<string, number> = {};
      for (const line of events) {
        const kind = line
          .replace(/^flicker: /, "")
          .replace(/\d+/g, "N")
          .replace(/ at t=.*$/, "");
        kinds[kind] = (kinds[kind] ?? 0) + 1;
      }
      return {
        summary: kinds,
        events,
        batching: new URL(window.location.href).searchParams.get("batch") !== "off",
        paintLatencyMs: waits.length
          ? {
              median: waits[Math.floor(waits.length / 2)],
              p95: waits[Math.floor(waits.length * 0.95)],
              max: waits[waits.length - 1],
            }
          : null,
        renders: samples.length,
        seconds: +((samples[samples.length - 1].t - samples[0].t) / 1000).toFixed(1),
        domRenderer: samples[0].painted >= 0,
        blankPaints: blanks.length,
        paintedMin: Math.min(...samples.map((s) => s.painted)),
        heldMin: Math.min(...samples.map((s) => s.held)),
        gapMs: sorted.length
          ? {
              median: sorted[Math.floor(sorted.length / 2)],
              p95: sorted[Math.floor(sorted.length * 0.95)],
              max: sorted[sorted.length - 1],
            }
          : null,
        gridsSeen: [...new Set(samples.map((s) => `${s.cols}x${s.rows}`))],
        boxesSeen: [...new Set(samples.map((s) => `${s.w}x${s.h}`))],
        aroundBlanks: drops,
      };
    };
  }

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
    if (probing()) probe(built, host);
    if (gpuWanted()) {
      try {
        // The GPU renderer is what makes many live terminals affordable.
        // It is not available everywhere, and the DOM fallback is fine.
        built.loadAddon(new WebglAddon());
      } catch {
        // Fallback renderer; nothing to do.
      }
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
