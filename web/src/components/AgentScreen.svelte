<script lang="ts">
  import { Terminal } from "@xterm/xterm";
  import { FitAddon } from "@xterm/addon-fit";
  // Imported here as well as in Terminal.svelte: this component renders
  // xterm too, and must not depend on sharing a chunk with the roster's
  // pane to be styled.
  import "@xterm/xterm/css/xterm.css";
  import { attach, type Attachment } from "../lib/terminal";
  import { terminal } from "../lib/theme";

  /**
   * One agent's whole screen, watched and scaled into whatever box this
   * component is given. The wall's cells and the canvas's tiles are both
   * this plus chrome; the contract lives here so it cannot drift between
   * them.
   *
   * Watching: no size, ever. The terminal is shared, so a viewer that
   * announced its own geometry would reflow the agent for everyone —
   * including the dashboard reading it.
   *
   * Typing is the half that can be handed back. A screen that `typing`
   * names as able to speak sends keystrokes down the socket while it is
   * `focused` — what is typed lands at the shared cursor wherever this
   * screen scales it, so it needs no geometry of its own. A screen that
   * cannot type (the wall's) registers no input at all.
   */
  let {
    id,
    visible,
    typing = false,
    focused = false,
  }: { id: string; visible: boolean; typing?: boolean; focused?: boolean } =
    $props();

  let viewport: HTMLDivElement;
  let screen: HTMLDivElement;
  let scale = $state(1);
  let shiftX = $state(0);
  let shiftY = $state(0);
  let term = $state<Terminal>();

  $effect(() => {
    if (!visible || !screen) return;
    // `id` is a string prop, deliberately never the agent object: every
    // roster push re-proxies every agent, and an effect depending on the
    // object would tear down and re-attach every terminal at the
    // roster's cadence. `typing` is a capability fixed at mount, and
    // `focused` is read by the effect below, never here — the keyboard
    // changing hands must not rebuild the terminal it is handed to.
    const agentID = id;
    const canType = typing;

    const built = new Terminal({
      fontFamily: 'ui-monospace, "SF Mono", Menlo, monospace',
      fontSize: 12,
      lineHeight: 1.1,
      // The DOM renderer, not WebGL: browsers allow only a handful of
      // WebGL contexts at once, and a fleet view wants more cells than
      // that. No scrollback — this screen cannot be scrolled, so
      // history would be rows of DOM kept for nobody, multiplied by
      // the fleet.
      scrollback: 0,
      allowProposedApi: true,
      // Closed until focus opens it: an unfocused screen produces no
      // input at all, keyboard or mouse report, and the one that cannot
      // type never opens.
      disableStdin: true,
      // The cursor is drawn only while xterm has focus, which is only
      // ever the tile holding the keyboard. A watched screen shows
      // none: a block cursor on every tile reads as a fleet mid-type.
      // Said at construction rather than by rewriting the theme on
      // focus, because a theme write is a full restyle of the terminal
      // — and this runs once per tile across the fleet.
      cursorInactiveStyle: "none",
      cursorBlink: true,
      theme: {
        background: terminal.background,
        foreground: terminal.foreground,
        cursor: terminal.cursor,
      },
    });
    // The wheel is the surface's, never the agent's: there is no
    // scrollback to move, and a tile that reported the wheel to an agent
    // in mouse mode would scroll its history while the canvas panned.
    built.attachCustomWheelEventHandler(() => false);
    const fitAddon = new FitAddon();
    built.loadAddon(fitAddon);
    built.open(screen);
    term = built;

    const attachment: Attachment = attach(
      built,
      fitAddon,
      agentID,
      () => false,
      () => {},
      { watching: true, typing: canType },
    );

    // The whole screen, shrunk, rather than a corner of it: a fleet is
    // read at a glance, and a clipped terminal is unrecognisable in
    // exactly the way that matters.
    const rescale = () => {
      const natural = screen.querySelector(".xterm-screen") as HTMLElement | null;
      if (!natural || !natural.offsetWidth) return;
      // Fill the box, up or down — an 80-column agent in a wide tile
      // grows into it rather than sitting small in a corner.
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
    sizes.observe(viewport);
    const frames = setInterval(rescale, 500);

    return () => {
      clearInterval(frames);
      sizes.disconnect();
      attachment.close();
      term = undefined;
      built.dispose();
    };
  });

  // The keyboard changing hands, on the terminal that already exists.
  // Focus opens stdin; losing it closes stdin again, so a tile that was
  // typed into and left goes back to being a picture. The cursor
  // follows xterm's own focus, per the construction above.
  $effect(() => {
    if (!term || !typing) return;
    term.options.disableStdin = !focused;
    if (focused) term.focus();
    else term.blur();
  });
</script>

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

<style>
  .screen {
    position: relative;
    flex: 1 1 auto;
    min-height: 0;
    /* No background of its own: the cell or tile this is dropped into
       owns the terminal's ground (--term-bg), and a second opaque layer
       at the same color only adds a seam to antialias along. */
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
</style>
