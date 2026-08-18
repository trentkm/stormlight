import { Terminal } from "@xterm/xterm";
import { FitAddon } from "@xterm/addon-fit";
import { socketURL } from "./api";

/**
 * The terminal socket is deliberately not a JSON protocol.
 *
 * Binary messages are terminal bytes in both directions: the daemon's
 * exact state first, then live output, with keystrokes travelling back
 * the same way. Text messages are the control channel — a size, or a
 * notice that what follows is state rather than output — and there are
 * few of them.
 */
interface Control {
  type: "resize" | "seed";
  cols?: number;
  rows?: number;
}

/**
 * The smallest geometry worth telling the daemon about. Well below any
 * usable pane and well above the sizes a collapsed one reports.
 */
const minCols = 20;
const minRows = 4;

function usable(cols: number, rows: number): boolean {
  return cols >= minCols && rows >= minRows;
}

export interface Attachment {
  /** Re-measures and tells the daemon; call on layout changes. */
  fit(): void;
  close(): void;
}

/**
 * attach wires one agent's terminal to one xterm.js instance.
 *
 * The subtlety worth stating: a `seed` notice means the binary message
 * behind it *replaces* the replica rather than extending it. It arrives
 * at attach, and again whenever this viewer fell behind and the daemon
 * sent state instead of the output it missed. Writing it in without the
 * reset leaves the scrollback doubled and the screen a stutter, and
 * nothing later corrects it.
 */
export function attach(
  term: Terminal,
  fitAddon: FitAddon,
  id: string,
  isLaidOut: () => boolean,
): Attachment {
  // The size the attachment opens with resizes the agent's terminal
  // before a single byte arrives, and that terminal is shared. A pane
  // that has not been laid out yet measures as a couple of columns, so
  // opening at its measurement would reflow the agent's real terminal to
  // ribbon width for every viewer — the dashboard included — before this
  // one has drawn anything at all.
  const opening = usable(term.cols, term.rows)
    ? { cols: term.cols, rows: term.rows }
    : { cols: 80, rows: 24 };
  const socket = new WebSocket(
    socketURL(`/api/agents/${id}/terminal`, {
      cols: String(opening.cols),
      rows: String(opening.rows),
    }),
  );
  socket.binaryType = "arraybuffer";

  // Set by a seed notice, consumed by the binary message right behind it.
  let replacing = false;

  socket.onmessage = (event) => {
    if (typeof event.data === "string") {
      const control: Control = JSON.parse(event.data);
      if (control.type === "seed") {
        replacing = true;
        return;
      }
      if (control.type === "resize" && control.cols && control.rows) {
        // The terminal is shared, so it moves when anyone moves it. Follow
        // it rather than painting at a size nobody else is using.
        term.resize(control.cols, control.rows);
      }
      return;
    }
    const bytes = new Uint8Array(event.data as ArrayBuffer);
    if (replacing) {
      replacing = false;
      // Reset first: this is state, and it carries the scrollback the
      // replica already has.
      term.reset();
    }
    term.write(bytes);
  };

  const input = term.onData((data) => {
    if (socket.readyState === WebSocket.OPEN) {
      socket.send(new TextEncoder().encode(data));
    }
  });
  const binary = term.onBinary((data) => {
    if (socket.readyState !== WebSocket.OPEN) return;
    const bytes = new Uint8Array(data.length);
    for (let i = 0; i < data.length; i++) {
      bytes[i] = data.charCodeAt(i) & 255;
    }
    socket.send(bytes);
  });

  let announced = opening;
  const fit = () => {
    // A pane that is hidden or squeezed to nothing measures as a terminal
    // one or two columns wide, and this terminal is shared: sending that
    // size reflows the agent's real terminal for everyone, the dashboard
    // included, and the output it already produced is re-wrapped to
    // ribbon width. A collapsed viewer has no opinion about geometry.
    if (!isLaidOut()) return;
    fitAddon.fit();
    if (!usable(term.cols, term.rows)) return;
    if (term.cols === announced.cols && term.rows === announced.rows) return;
    announced = { cols: term.cols, rows: term.rows };
    if (socket.readyState === WebSocket.OPEN) {
      socket.send(JSON.stringify({
        type: "resize",
        cols: term.cols,
        rows: term.rows,
      }));
    }
  };
  socket.onopen = () => fit();

  return {
    fit,
    close() {
      input.dispose();
      binary.dispose();
      socket.close();
    },
  };
}
