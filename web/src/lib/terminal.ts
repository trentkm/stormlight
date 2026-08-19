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
 * usable pane and well above what a collapsed one reports.
 */
const minCols = 20;
const minRows = 4;

/** How long to wait before reattaching, and the ceiling it climbs to. */
const retryFloor = 500;
const retryCeiling = 10_000;

function usable(cols: number, rows: number): boolean {
  return cols >= minCols && rows >= minRows;
}

export type Connection = "live" | "reconnecting";

export interface Attachment {
  /** Re-measures and tells the daemon; call on layout changes. */
  fit(): void;
  close(): void;
}

/**
 * attach keeps one xterm.js instance bound to one agent's terminal, across
 * however many sockets that takes.
 *
 * The subtlety worth stating: a `seed` notice means the binary message
 * behind it *replaces* the replica rather than extending it. It arrives at
 * attach, and again whenever this viewer fell behind and the daemon sent
 * state instead of the output it missed. That is also what makes
 * reconnecting safe — a new socket opens with a seed, so the replica is
 * rebuilt rather than continued from wherever it was cut off.
 */
export function attach(
  term: Terminal,
  fitAddon: FitAddon,
  id: string,
  isLaidOut: () => boolean,
  onConnection: (state: Connection) => void = () => {},
): Attachment {
  let socket: WebSocket | null = null;
  // Zero until this viewer has measured itself: it has asserted no
  // geometry, so it has none to compare against.
  let announced = { cols: 0, rows: 0 };
  let retry = retryFloor;
  let retryTimer: number | undefined;
  let closed = false;

  const open = () => {
    if (closed) return;
    // A pane that has not been laid out has no geometry worth asserting,
    // and this terminal is shared: any size named here moves the agent's
    // real terminal for every viewer, the dashboard included, and nothing
    // moves it back. Naming none leaves it where it is until this pane
    // has a shape and fit() can speak for it (#155).
    if (isLaidOut()) {
      // Re-measure rather than trusting term.cols, which may hold a size
      // the daemon pushed rather than one this pane has.
      fitAddon.fit();
    }
    const measured = isLaidOut() && usable(term.cols, term.rows);
    const opening = measured
      ? { cols: term.cols, rows: term.rows }
      : { cols: 0, rows: 0 };
    announced = opening;

    const live = new WebSocket(
      socketURL(
        `/api/agents/${id}/terminal`,
        measured
          ? { cols: String(opening.cols), rows: String(opening.rows) }
          : {},
      ),
    );
    socket = live;
    live.binaryType = "arraybuffer";

    // Set by a seed notice and consumed by the binary message behind it.
    // The daemon pairs them one for one; a stray one degrades to a
    // repaint rather than to a screen that keeps state it was told to
    // drop.
    let replacing = false;

    live.onopen = () => {
      onConnection("live");
      // Measure now: the pane may have been laid out since this viewer
      // last spoke, and the size in the URL was read before that.
      fit();
    };

    live.onmessage = (event) => {
      if (typeof event.data === "string") {
        const control: Control = JSON.parse(event.data);
        if (control.type === "seed") {
          // The attach worked. Not `onopen`: this server upgrades before
          // it attaches, so a failed attach is an accepted socket that
          // closes a moment later — and treating that as success resets
          // the backoff, turning an unreachable daemon into two full
          // attach attempts a second, forever.
          retry = retryFloor;
          replacing = true;
          return;
        }
        if (control.type === "resize" && control.cols && control.rows) {
          const { cols, rows } = control;
          // Behind the write queue, not in front of it. term.resize is
          // synchronous while term.write is buffered, so resizing here
          // would reflow output that arrived before this notice — the
          // repaint that belongs to this size is queued behind it.
          // Deferred, so it lands behind the output it belongs to — and
          // guarded, because the queue it waits in is not drained by
          // dispose(): switching agents inside that window would resize a
          // terminal that no longer exists.
          term.write("", () => {
            if (closed) return;
            term.resize(cols, rows);
          });
          // The daemon just told us the size, so it is ours now. Without
          // this the next fit() measures the same grid, compares it to a
          // stale record, and quietly snaps the replica back to a size
          // nobody else is using.
          announced = { cols, rows };
        }
        return;
      }
      const bytes = new Uint8Array(event.data as ArrayBuffer);
      if (replacing) {
        replacing = false;
        // RIS through the write queue rather than term.reset(), which is
        // synchronous: bytes already handed to write() but not yet parsed
        // would land on the fresh screen after the reset — the doubled
        // screen this exists to prevent, on the one path where there is
        // certain to be output in flight.
        term.write("\x1bc");
      }
      term.write(bytes);
    };

    // The socket ends for reasons this side cannot tell apart: the agent
    // exited, the server restarted, the laptop slept. Reattaching answers
    // all of them — a live agent seeds a fresh replica, and a gone one
    // disappears from the roster, which takes this pane with it.
    live.onclose = () => {
      if (closed || socket !== live) return;
      onConnection("reconnecting");
      retryTimer = window.setTimeout(open, retry);
      retry = Math.min(retry * 2, retryCeiling);
    };
  };

  const send = (data: Uint8Array<ArrayBuffer> | string) => {
    if (socket?.readyState === WebSocket.OPEN) socket.send(data);
  };

  const input = term.onData((data) => {
    const encoded = new TextEncoder().encode(data);
    const bytes = new Uint8Array(new ArrayBuffer(encoded.length));
    bytes.set(encoded);
    send(bytes);
  });
  const binary = term.onBinary((data) => {
    const bytes = new Uint8Array(new ArrayBuffer(data.length));
    for (let i = 0; i < data.length; i++) {
      bytes[i] = data.charCodeAt(i) & 255;
    }
    send(bytes);
  });

  const fit = () => {
    // A collapsed or hidden pane has no opinion about geometry, and this
    // terminal is shared.
    if (!isLaidOut()) return;
    fitAddon.fit();
    if (!usable(term.cols, term.rows)) return;
    if (term.cols === announced.cols && term.rows === announced.rows) return;
    announced = { cols: term.cols, rows: term.rows };
    send(JSON.stringify({ type: "resize", cols: term.cols, rows: term.rows }));
  };

  open();

  return {
    fit,
    close() {
      closed = true;
      window.clearTimeout(retryTimer);
      input.dispose();
      binary.dispose();
      if (socket) {
        // Detach before closing: a socket in CLOSING can still dispatch a
        // buffered message, and the terminal it would write to is about
        // to be disposed.
        socket.onmessage = null;
        socket.onclose = null;
        socket.close();
      }
    },
  };
}
