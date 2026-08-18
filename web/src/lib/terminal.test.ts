import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";
import { attach, type Connection } from "./terminal";

// The terminal client is the one part of this page with a protocol to get
// wrong, and every way it has been got wrong so far was invisible to the
// Go tests: they never open a socket, and they never render a pane.

class FakeSocket {
  static live: FakeSocket[] = [];
  static readonly OPEN = 1;

  readyState = 0;
  binaryType = "";
  sent: Array<Uint8Array | string> = [];
  onopen: (() => void) | null = null;
  onmessage: ((event: { data: unknown }) => void) | null = null;
  onclose: (() => void) | null = null;
  closed = false;

  constructor(readonly url: string) {
    FakeSocket.live.push(this);
  }

  open() {
    this.readyState = FakeSocket.OPEN;
    this.onopen?.();
  }
  deliverControl(control: unknown) {
    this.onmessage?.({ data: JSON.stringify(control) });
  }
  deliverBytes(text: string) {
    this.onmessage?.({ data: new TextEncoder().encode(text).buffer });
  }
  send(data: Uint8Array | string) {
    this.sent.push(data);
  }
  close() {
    this.closed = true;
    this.readyState = 3;
    this.onclose?.();
  }
}

/** A terminal that records what it was asked to do, in order. */
function fakeTerminal() {
  const calls: string[] = [];
  const queue: Array<() => void> = [];
  return {
    calls,
    cols: 100,
    rows: 30,
    write(data: Uint8Array | string, done?: () => void) {
      if (typeof data === "string" && data === "") {
        // xterm defers a bare callback until everything queued ahead of
        // it has been parsed; that ordering is the point of using it.
        queue.push(() => done?.());
        return;
      }
      const text =
        typeof data === "string" ? data : new TextDecoder().decode(data);
      calls.push(`write:${JSON.stringify(text)}`);
      done?.();
    },
    resize(cols: number, rows: number) {
      calls.push(`resize:${cols}x${rows}`);
      this.cols = cols;
      this.rows = rows;
    },
    onData: () => ({ dispose: () => {} }),
    onBinary: () => ({ dispose: () => {} }),
    /** Runs the callbacks xterm would run once the queue drained. */
    drain() {
      while (queue.length) queue.shift()!();
    },
  };
}

const fitAddon = { fit: () => {} };

function setup(options: { laidOut?: boolean } = {}) {
  const term = fakeTerminal();
  const states: Connection[] = [];
  const attachment = attach(
    term as never,
    fitAddon as never,
    "agent-one",
    () => options.laidOut ?? true,
    (state) => states.push(state),
  );
  return { term, attachment, states, socket: () => FakeSocket.live.at(-1)! };
}

beforeEach(() => {
  FakeSocket.live = [];
  vi.stubGlobal("WebSocket", FakeSocket);
  vi.stubGlobal("sessionStorage", {
    getItem: () => "test-token",
    setItem: () => {},
    removeItem: () => {},
  });
  vi.stubGlobal("window", {
    location: { href: "http://127.0.0.1:7331/", origin: "http://127.0.0.1:7331" },
    setTimeout: (fn: () => void, ms: number) => globalThis.setTimeout(fn, ms),
    clearTimeout: (id: number) => globalThis.clearTimeout(id),
    history: { replaceState: () => {} },
  });
  vi.useFakeTimers();
});

afterEach(() => {
  vi.useRealTimers();
  vi.unstubAllGlobals();
});

describe("attaching", () => {
  test("a laid-out pane opens at its own size", () => {
    const { socket } = setup({ laidOut: true });
    expect(socket().url).toContain("cols=100");
    expect(socket().url).toContain("rows=30");
  });

  // The terminal belongs to the session and every viewer shares it, so a
  // size named here moves it for the dashboard too — and nothing puts the
  // dashboard's size back.
  test("a pane with no layout names no size at all", () => {
    const { socket } = setup({ laidOut: false });
    expect(socket().url).not.toContain("cols=");
    expect(socket().url).not.toContain("rows=");
  });
});

describe("state and output", () => {
  test("a seed replaces the replica rather than extending it", () => {
    const { term, socket } = setup();
    socket().open();
    socket().deliverBytes("first output");
    socket().deliverControl({ type: "seed" });
    socket().deliverBytes("exact state");

    expect(term.calls).toEqual([
      'write:"first output"',
      // RIS through the write queue, so bytes already queued are parsed
      // before the reset rather than after it.
      'write:"\\u001bc"',
      'write:"exact state"',
    ]);
  });

  test("a resize is applied behind the output that preceded it", () => {
    const { term, socket } = setup();
    socket().open();
    socket().deliverBytes("wrapped for the old width");
    socket().deliverControl({ type: "resize", cols: 80, rows: 24 });

    // Not yet: the resize waits on the queue.
    expect(term.calls).toEqual(['write:"wrapped for the old width"']);
    term.drain();
    expect(term.calls).toEqual([
      'write:"wrapped for the old width"',
      "resize:80x24",
    ]);
  });
});

describe("size negotiation", () => {
  // The daemon's size is the daemon's fact. Treating it as someone
  // else's leaves the next fit() measuring the same grid, comparing it
  // to a stale record, and snapping the replica back to a size nobody
  // else is using.
  test("a size pushed by the daemon is not argued with", () => {
    const { term, attachment, socket } = setup();
    socket().open();
    socket().sent.length = 0;

    socket().deliverControl({ type: "resize", cols: 80, rows: 24 });
    term.drain();
    attachment.fit();

    expect(socket().sent).toEqual([]);
  });

  test("a size this viewer measured is sent once", () => {
    const { term, attachment, socket } = setup();
    socket().open();
    socket().sent.length = 0;

    term.cols = 120;
    term.rows = 40;
    attachment.fit();
    attachment.fit();

    expect(socket().sent).toEqual([
      JSON.stringify({ type: "resize", cols: 120, rows: 40 }),
    ]);
  });

  test("a size no terminal can be is never sent", () => {
    const { term, attachment, socket } = setup();
    socket().open();
    socket().sent.length = 0;

    term.cols = 2;
    term.rows = 1;
    attachment.fit();

    expect(socket().sent).toEqual([]);
  });
});

describe("losing the connection", () => {
  // The socket ends for reasons this side cannot tell apart — the agent
  // exited, the server restarted, the laptop slept. Freezing a black pane
  // with no explanation is the wrong answer to all of them.
  test("a closed socket reattaches, and says so while it is gone", () => {
    const { states, socket } = setup();
    socket().open();
    expect(states).toEqual(["live"]);

    socket().close();
    expect(states).toEqual(["live", "reconnecting"]);

    vi.advanceTimersByTime(500);
    expect(FakeSocket.live).toHaveLength(2);
    FakeSocket.live[1].open();
    expect(states).toEqual(["live", "reconnecting", "live"]);
  });

  test("retries back off rather than hammering a server that is gone", () => {
    const { socket } = setup();
    socket().open();

    socket().close();
    vi.advanceTimersByTime(500);
    FakeSocket.live[1].close();
    vi.advanceTimersByTime(500);
    // Still two: the second wait is longer than the first.
    expect(FakeSocket.live).toHaveLength(2);
    vi.advanceTimersByTime(500);
    expect(FakeSocket.live).toHaveLength(3);
  });

  test("closing stops the retries and detaches the handler", () => {
    const { attachment, socket } = setup();
    socket().open();
    const first = socket();

    attachment.close();
    first.close();
    vi.advanceTimersByTime(60_000);

    expect(FakeSocket.live).toHaveLength(1);
    expect(first.onmessage).toBeNull();
  });
});
