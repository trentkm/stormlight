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

/**
 * The backstop for the batch below, in milliseconds.
 *
 * A frame is the cadence that matters, and in a tab being painted the
 * frame always wins this — which is the point of the gap. A hidden tab
 * is served no frames at all, though, and a replica that simply stopped
 * applying output would be stale when the tab came back, having held
 * every unapplied byte until then. The timer is what drains that tab;
 * the browser throttles it to about a second, which bounds what a hidden
 * tab holds without ever pacing a visible one.
 */
const batchBackstop = 250;

/**
 * RIS, as bytes, so it batches with the output around it rather than
 * breaking the run into two writes.
 */
const reset = new Uint8Array([0x1b, 0x63]);

/**
 * DECSET 2026, begin and end: the terminal is told to hold its paint
 * until the whole thing has landed.
 *
 * Rebuilding a replica is a reset followed by a screen and everything
 * that scrolled off above it, and that is far more than a parser gets
 * through before it yields to let the renderer run. Painted halfway, the
 * reset has happened and the history has not: an empty buffer, the view
 * at its first line, and a pane that appears to leap to the top and back
 * a moment later. Wrapped, there is no halfway to paint — the same
 * mechanism the agents themselves use to keep a frame from being seen
 * half drawn.
 */
const beginSync = new Uint8Array([0x1b, 0x5b, 0x3f, 0x32, 0x30, 0x32, 0x36, 0x68]);
const endSync = new Uint8Array([0x1b, 0x5b, 0x3f, 0x32, 0x30, 0x32, 0x36, 0x6c]);

/** rebuild is state to replace a replica with, made unpaintable-halfway. */
function rebuild(state: Uint8Array): Uint8Array {
  const whole = new Uint8Array(
    beginSync.length + reset.length + state.length + endSync.length,
  );
  let at = 0;
  for (const part of [beginSync, reset, state, endSync]) {
    whole.set(part, at);
    at += part.length;
  }
  return whole;
}

/**
 * Whether this page batches at all.
 *
 * `?batch=off` writes every message straight through the way it did
 * before batching existed, so the two can be compared by reloading. The
 * batch buys atomicity and costs a frame of latency — output waits for a
 * frame to be handed over and then waits for another to be painted —
 * and which of those matters more is a question about a real agent on a
 * real screen rather than one to settle by reasoning.
 */
function batching(): boolean {
  try {
    return new URL(window.location.href).searchParams.get("batch") !== "off";
  } catch {
    return true;
  }
}

/**
 * Whether this page narrates what the socket is doing.
 *
 * A replica rebuilt is a screen that jumps, and the two reasons — the
 * daemon resyncing a viewer that fell behind, and the socket coming back
 * after a drop — look identical from the buffer's side. They do not look
 * identical from here.
 */
function narrating(): boolean {
  try {
    return new URL(window.location.href).searchParams.get("probe") !== "0";
  } catch {
    return false;
  }
}

function usable(cols: number, rows: number): boolean {
  return cols >= minCols && rows >= minRows;
}

export type Connection = "live" | "reconnecting";

export interface Attachment {
  /** Re-measures and tells the daemon; call on layout changes. */
  fit(): void;
  close(): void;
}

export interface Options {
  /**
   * watching attaches without ever speaking: no keystrokes, and no size.
   *
   * The wall is why this exists. A terminal belongs to its session and
   * every viewer shares it, so a grid of small cells that each announced
   * their geometry would reflow the whole fleet to the size of the
   * smallest tile — and the dashboard reading one of those agents would
   * watch it happen. A watcher renders whatever size the terminal
   * already is, which the seed tells it.
   */
  watching?: boolean;
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
  options: Options = {},
): Attachment {
  const watching = options.watching ?? false;
  let socket: WebSocket | null = null;
  // Zero until this viewer has measured itself: it has asserted no
  // geometry, so it has none to compare against.
  let announced = { cols: 0, rows: 0 };
  let retry = retryFloor;
  let retryTimer: number | undefined;
  let closed = false;

  /**
   * Output reaches xterm one batch per frame, never one write per
   * message.
   *
   * A busy agent sends output in hundreds of small messages a second,
   * and handing each one to term.write() from the message handler
   * starves the renderer: the replica keeps parsing — its buffer stays
   * current — while the screen stops repainting entirely, then snaps
   * forward when the agent goes quiet. That blank-and-catch-up is the
   * flash you see while an agent is working, and it is a scheduling
   * problem rather than a throughput one: the same bytes, delivered in
   * one batch per frame, paint continuously.
   *
   * A batch is a queue rather than a buffer because not everything in it
   * is bytes. A resize has to land behind the output it follows (see the
   * resize notice below), so it rides the same queue as a callback on
   * the write in front of it — which is what keeps a repaint from being
   * reflowed by a size that belongs after it.
   */
  const batched = batching();
  const narrate = narrating();
  let seeds = 0;
  let sockets = 0;
  let batch: Array<Uint8Array | (() => void)> = [];
  let frame: number | undefined;
  let backstop: number | undefined;

  const flush = () => {
    if (frame !== undefined) window.cancelAnimationFrame?.(frame);
    window.clearTimeout(backstop);
    frame = backstop = undefined;
    const queued = batch;
    batch = [];
    let run: Uint8Array[] = [];
    let size = 0;
    // One write per run of bytes, and the run ends where an action has to
    // be ordered against it.
    const writeRun = (done?: () => void) => {
      if (size === 0) {
        if (done) term.write("", done);
        return;
      }
      const joined = new Uint8Array(size);
      let at = 0;
      for (const chunk of run) {
        joined.set(chunk, at);
        at += chunk.length;
      }
      run = [];
      size = 0;
      term.write(joined, done);
    };
    for (const item of queued) {
      if (typeof item === "function") {
        writeRun(item);
        continue;
      }
      run.push(item);
      size += item.length;
    }
    writeRun();
  };

  const enqueue = (item: Uint8Array | (() => void)) => {
    if (!batched) {
      if (typeof item === "function") term.write("", item);
      else term.write(item);
      return;
    }
    batch.push(item);
    if (frame !== undefined || backstop !== undefined) return;
    frame = window.requestAnimationFrame?.(flush);
    backstop = window.setTimeout(flush, batchBackstop);
  };

  const open = () => {
    if (closed) return;
    // A pane that has not been laid out has no geometry worth asserting,
    // and this terminal is shared: any size named here moves the agent's
    // real terminal for every viewer, the dashboard included, and nothing
    // moves it back. Naming none leaves it where it is until this pane
    // has a shape and fit() can speak for it (#155).
    if (!watching && isLaidOut()) {
      // Re-measure rather than trusting term.cols, which may hold a size
      // the daemon pushed rather than one this pane has. A watcher skips
      // it: its grid is whatever the terminal already is, and measuring
      // its own tile would only invite it to say so.
      fitAddon.fit();
    }
    const measured = !watching && isLaidOut() && usable(term.cols, term.rows);
    const opening = measured
      ? { cols: term.cols, rows: term.rows }
      : { cols: 0, rows: 0 };
    announced = opening;

    sockets++;
    if (narrate) {
      console.log(
        `flicker: opening terminal socket #${sockets}` +
          (sockets > 1 ? " — THE SOCKET DROPPED AND CAME BACK" : ""),
      );
    }
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
          seeds++;
          if (narrate) {
            console.log(
              `flicker: the daemon seeded this replica (#${seeds} on socket ` +
                `#${sockets})` +
                (seeds > 1
                  ? " — A RESYNC: this viewer fell behind and was handed state"
                  : ""),
            );
          }
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
          enqueue(() => {
            if (closed) return;
            term.resize(cols, rows);
          });
          // The daemon just told us the size, so it is ours now. Without
          // this the next fit() measures the same grid, compares it to a
          // stale record, and quietly snaps the replica back to a size
          // nobody else is using.
          //
          // This is also how a viewer that asserted nothing learns the
          // geometry its seed was wrapped for: the size arrives just
          // ahead of the state it belongs to.
          announced = { cols, rows };
        }
        return;
      }
      const bytes = new Uint8Array(event.data as ArrayBuffer);
      if (replacing) {
        replacing = false;
        // The reset rides with the state it precedes, in one write, held
        // behind a synchronized update.
        //
        // Through the write queue rather than term.reset(), which is
        // synchronous: bytes already handed to write() but not yet parsed
        // would land on the fresh screen after the reset — the doubled
        // screen this exists to prevent, on the one path where there is
        // certain to be output in flight. And wrapped, because a replica
        // is rebuilt often enough — every attach, and every resync of a
        // viewer that fell behind — that a half-drawn one is the flicker
        // rather than a curiosity.
        enqueue(rebuild(bytes));
        return;
      }
      enqueue(bytes);
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

  // A watcher registers no input handlers at all, rather than dropping
  // what they produce: a cell that quietly swallowed keystrokes would
  // look like an agent ignoring you.
  const input = watching
    ? { dispose: () => {} }
    : term.onData((data) => {
        const encoded = new TextEncoder().encode(data);
        const bytes = new Uint8Array(new ArrayBuffer(encoded.length));
        bytes.set(encoded);
        send(bytes);
      });
  const binary = watching
    ? { dispose: () => {} }
    : term.onBinary((data) => {
        const bytes = new Uint8Array(new ArrayBuffer(data.length));
        for (let i = 0; i < data.length; i++) {
          bytes[i] = data.charCodeAt(i) & 255;
        }
        send(bytes);
      });

  const fit = () => {
    // A watcher never claims a size; it renders the one it was given.
    if (watching) return;
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
      // Nothing queued outlives the terminal it was queued for: the
      // batch is dropped rather than flushed, because the flush would
      // write into an emulator this pane is about to dispose.
      if (frame !== undefined) window.cancelAnimationFrame?.(frame);
      window.clearTimeout(backstop);
      frame = backstop = undefined;
      batch = [];
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
