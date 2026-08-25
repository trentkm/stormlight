// @vitest-environment jsdom
//
// The canvas, mounted for real. Two families of failure live only at
// this layer: reactivity wiring (the wall's attach storm, which no unit
// test of attach() could see) and gesture bookkeeping (a drag that
// commits a click, a click that commits a drag, a layout that forgets).
import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";
import { flushSync, mount, unmount } from "svelte";
import type { Agent } from "../lib/types";

const lifecycle: string[] = [];
/** Every attach's contract: did it watch, did it claim layout, and can
 *  it type? Recorded so a refactor that quietly drops { watching: true }
 *  — or starts answering isLaidOut with true — fails a test instead of
 *  resizing the fleet's shared terminals. */
const contracts: Array<{
  id: string;
  watching: boolean;
  laidOut: boolean;
  typing: boolean;
}> = [];

vi.mock("../lib/terminal", () => ({
  attach: (
    _term: unknown,
    _fit: unknown,
    id: string,
    isLaidOut: () => boolean,
    _onConnection: unknown,
    options?: { watching?: boolean; typing?: boolean },
  ) => {
    lifecycle.push(`attach:${id}`);
    contracts.push({
      id,
      watching: options?.watching === true,
      laidOut: isLaidOut(),
      typing: options?.typing === true,
    });
    return {
      fit: () => {},
      close: () => lifecycle.push(`close:${id}`),
    };
  },
}));

/** What xterm does with focus, observed: a walked-in tile must ask
 *  its terminal for the keyboard, and one walked out of must let go. */
const focusCalls: string[] = [];

vi.mock("@xterm/xterm", () => ({
  Terminal: class {
    options: Record<string, unknown> = { disableStdin: true, theme: {} };
    open() {}
    loadAddon() {}
    attachCustomWheelEventHandler() {}
    resize() {}
    write() {}
    focus() {
      focusCalls.push(`focus:${this.options.disableStdin}`);
    }
    blur() {
      focusCalls.push(`blur:${this.options.disableStdin}`);
    }
    dispose() {}
  },
}));

vi.mock("@xterm/addon-fit", () => ({
  FitAddon: class {
    fit() {}
  },
}));

class StillObserver {
  observe() {}
  unobserve() {}
  disconnect() {}
}
/** The visibility observers, held so a test can scroll every tile off
 *  screen at once — jsdom lays nothing out, so nothing ever really
 *  leaves the viewport. */
const watchers: Array<(entries: Array<{ isIntersecting: boolean }>) => void> =
  [];
class HeldObserver extends StillObserver {
  constructor(callback: (entries: Array<{ isIntersecting: boolean }>) => void) {
    super();
    watchers.push(callback);
  }
}
const scrollAllAway = () => {
  for (const watcher of watchers) watcher([{ isIntersecting: false }]);
  flushSync();
};
vi.stubGlobal("IntersectionObserver", HeldObserver);
vi.stubGlobal("ResizeObserver", StillObserver);

// Not jsdom's storage and not Node's: newer Node ships an experimental
// localStorage global that shadows jsdom's and throws on use — the same
// webstorage trap AGENTS.md documents for CI. A ten-line Map is the
// same on every runner.
const stored = new Map<string, string>();
vi.stubGlobal("localStorage", {
  getItem: (key: string) => stored.get(key) ?? null,
  setItem: (key: string, value: string) => stored.set(key, String(value)),
  removeItem: (key: string) => stored.delete(key),
  clear: () => stored.clear(),
});

import Canvas from "./Canvas.svelte";
import { ui } from "../lib/commands.svelte";
import { fleet } from "../lib/state.svelte";
import { tileMin, tileSize } from "../lib/canvas";

function push(...specs: Array<Partial<Agent> & { id: string }>) {
  fleet.agents = specs.map((spec) => ({
    provider: "claude",
    name: spec.id,
    task: `task ${spec.id}`,
    cwd: "/",
    created_at: "2026-08-18T00:00:00Z",
    activity: "working",
    process_live: true,
    workspace: {
      id: "ws",
      kind: "git",
      name: "ws",
      root: "/",
      execution_root: "/",
    },
    ...spec,
  })) as Agent[];
  flushSync();
}

/** Mounts still open when a test ends (its assertion threw before it
 *  could clean up) are closed here — a leaked mount poisons every
 *  following test's DOM queries, turning one failure into a cascade. */
const leaked: Array<() => void> = [];
afterEach(() => {
  while (leaked.length) leaked.pop()!();
});

function mountCanvas(onopen: () => void = () => {}) {
  const target = document.createElement("div");
  document.body.append(target);
  const canvas = mount(Canvas, { target, props: { onopen } });
  flushSync();
  let closed = false;
  const close = () => {
    if (closed) return;
    closed = true;
    unmount(canvas);
    target.remove();
  };
  leaked.push(close);
  return close;
}

/** A pointer event jsdom can dispatch. jsdom has no PointerEvent, but
 *  listeners go by the type string, so a MouseEvent wearing one works —
 *  it only needs the pointerId the handlers compare. */
function pointer(
  type: string,
  x: number,
  y: number,
  pointerId = 1,
): MouseEvent {
  const event = new MouseEvent(type, {
    bubbles: true,
    cancelable: true,
    clientX: x,
    clientY: y,
    button: 0,
  });
  Object.defineProperty(event, "pointerId", { value: pointerId });
  return event;
}

function tileFor(name: string): HTMLElement {
  const tile = [...document.querySelectorAll<HTMLElement>(".tile")].find((t) =>
    t.querySelector(".name")?.textContent?.includes(name),
  );
  if (!tile) throw new Error(`no tile for ${name}`);
  return tile;
}

function boxOf(tile: HTMLElement) {
  return {
    x: parseFloat(tile.style.left),
    y: parseFloat(tile.style.top),
    w: parseFloat(tile.style.width),
    h: parseFloat(tile.style.height),
  };
}

beforeEach(() => {
  lifecycle.length = 0;
  contracts.length = 0;
  focusCalls.length = 0;
  watchers.length = 0;
  localStorage.clear();
  fleet.agents = [];
  fleet.selectedID = "";
  fleet.workspaceID = "";
  ui.view = "canvas";
  ui.walkedIn = false;
});

describe("the canvas against the roster", () => {
  test("tiles place themselves without overlapping", () => {
    const done = mountCanvas();
    push({ id: "a" }, { id: "b" }, { id: "c" }, { id: "d" });

    const boxes = [...document.querySelectorAll<HTMLElement>(".tile")].map(
      boxOf,
    );
    expect(boxes).toHaveLength(4);
    for (let i = 0; i < boxes.length; i++) {
      for (let j = i + 1; j < boxes.length; j++) {
        const a = boxes[i];
        const b = boxes[j];
        const apart =
          a.x + a.w <= b.x ||
          b.x + b.w <= a.x ||
          a.y + a.h <= b.y ||
          b.y + b.h <= a.y;
        expect(apart).toBe(true);
      }
    }
    done();
  });

  test("roster pushes re-attach nobody", () => {
    const done = mountCanvas();
    push({ id: "a" }, { id: "b" }, { id: "c" });
    expect(lifecycle.filter((e) => e.startsWith("attach:"))).toHaveLength(3);

    for (let i = 0; i < 5; i++) {
      push(
        { id: "a", summary: `pass ${i}` },
        { id: "b", activity: i % 2 ? "idle" : "working" },
        { id: "c" },
      );
    }

    expect(lifecycle.filter((e) => e.startsWith("attach:"))).toHaveLength(3);
    expect(lifecycle.filter((e) => e.startsWith("close:"))).toHaveLength(0);
    done();
  });

  test("an empty roster does not erase the arrangement", () => {
    const done = mountCanvas();
    push({ id: "a" });
    // Moved by hand first: auto-placement is deterministic, so an
    // arrangement that was wiped and re-minted would land back in the
    // same spot and a default position could not tell the difference.
    const tile = tileFor("a");
    tile.dispatchEvent(pointer("pointerdown", 100, 100));
    tile.dispatchEvent(pointer("pointermove", 400, 300));
    tile.dispatchEvent(pointer("pointerup", 400, 300));
    flushSync();
    const placed = boxOf(tileFor("a"));

    push();
    push({ id: "a" });

    expect(boxOf(tileFor("a"))).toEqual(placed);
    done();
  });

  // An unreachable SSH host is simply omitted from pushes while its
  // agents keep running, and their ids come back verbatim on
  // reconnect. One dropped poll must not cost anyone their
  // arrangement.
  test("an agent that vanishes and returns keeps its spot", () => {
    const done = mountCanvas();
    push({ id: "a" }, { id: "b" });
    const tile = tileFor("b");
    tile.dispatchEvent(pointer("pointerdown", 100, 100));
    tile.dispatchEvent(pointer("pointermove", 420, 280));
    tile.dispatchEvent(pointer("pointerup", 420, 280));
    flushSync();
    const kept = boxOf(tileFor("b"));

    push({ id: "a" });
    push({ id: "a" }, { id: "b" });

    expect(boxOf(tileFor("b"))).toEqual(kept);
    done();
  });

  test("a newcomer never lands on an absent agent's remembered spot", () => {
    const done = mountCanvas();
    push({ id: "a" }, { id: "b" });
    const remembered = boxOf(tileFor("b"));

    push({ id: "a" });
    push({ id: "a" }, { id: "late" });

    expect(boxOf(tileFor("late"))).not.toEqual(remembered);
    done();
  });

  // Typing is the half of watching a tile hands back; geometry never
  // is. A tile that claimed layout would resize the fleet's shared
  // terminals to the size of whatever it was scaled into.
  test("every attachment watches, can type, and claims no layout", () => {
    const done = mountCanvas();
    push({ id: "a" }, { id: "b" });

    expect(contracts.length).toBeGreaterThan(0);
    for (const contract of contracts) {
      expect(contract).toMatchObject({
        watching: true,
        laidOut: false,
        typing: true,
      });
    }
    done();
  });

  test("a finite but absurd stored box is discarded too", () => {
    // 1e300 survives Number.isFinite; only the magnitude limits catch
    // it — and believed, it would push the next minted tile toward
    // y = 1e300 and the camera toward z = 0.
    localStorage.setItem(
      "stormlight.canvas.all",
      '{"ghost":{"x":0,"y":0,"w":1e300,"h":300}}',
    );
    const done = mountCanvas();
    push({ id: "a" });

    expect(boxOf(tileFor("a"))).toEqual({ x: 0, y: 0, w: 440, h: 300 });
    done();
  });

  test("what a session arranges, its reload believes", () => {
    let done = mountCanvas();
    push({ id: "a" });
    const tile = tileFor("a");
    const grip = tile.querySelector<HTMLElement>(".grip")!;
    // A wild grip fling: without a commit-side clamp this stores
    // w = 20440, the reload's validator discards the whole box, and
    // the arrangement quietly reverts.
    grip.dispatchEvent(pointer("pointerdown", 0, 0));
    tile.dispatchEvent(pointer("pointermove", 20_000, 100));
    tile.dispatchEvent(pointer("pointerup", 20_000, 100));
    flushSync();
    const arranged = boxOf(tileFor("a"));

    done();
    done = mountCanvas();
    push({ id: "a" });

    expect(boxOf(tileFor("a"))).toEqual(arranged);
    done();
  });

  test("a tile flung past the world's edge is caught at it", () => {
    // A drag reaches put() without passing resized(), so this pins
    // put()'s own clamp: unclamped, the store holds x = 2,000,100,
    // the reload's validator discards the box, and the tile silently
    // reverts to its default spot.
    let done = mountCanvas();
    push({ id: "a" });
    const tile = tileFor("a");
    tile.dispatchEvent(pointer("pointerdown", 100, 100));
    tile.dispatchEvent(pointer("pointermove", 2_000_100, 100));
    tile.dispatchEvent(pointer("pointerup", 2_000_100, 100));
    flushSync();
    const arranged = boxOf(tileFor("a"));
    expect(arranged.x).toBe(1_000_000);

    done();
    done = mountCanvas();
    push({ id: "a" });

    expect(boxOf(tileFor("a"))).toEqual(arranged);
    done();
  });

  test("a careful drag loses none of its pixels", () => {
    const done = mountCanvas();
    push({ id: "a" });
    const tile = tileFor("a");
    const before = boxOf(tile);

    tile.dispatchEvent(pointer("pointerdown", 100, 100));
    for (let step = 1; step <= 30; step++) {
      tile.dispatchEvent(pointer("pointermove", 100 + step, 100));
    }
    tile.dispatchEvent(pointer("pointerup", 130, 100));
    flushSync();

    // All thirty pixels, not thirty minus the click threshold.
    expect(boxOf(tileFor("a")).x).toBeCloseTo(before.x + 30);
    done();
  });

  test("urgent agents are counted, and jump cycles through them", () => {
    const done = mountCanvas();
    push(
      { id: "a" },
      { id: "b", attention: "question" },
      { id: "c", attention: "question" },
    );

    const button = document.querySelector<HTMLButtonElement>(
      ".controls .urgent",
    )!;
    expect(button.textContent).toContain("2 need input");

    const stage = document.querySelector<HTMLElement>(".stage")!;
    button.click();
    flushSync();
    const first = stage.style.transform;
    button.click();
    flushSync();

    // Two urgent tiles in different spots: consecutive jumps must aim
    // the camera at different places.
    expect(stage.style.transform).not.toBe(first);
    done();
  });

  test("a poisoned store neither hangs placement nor kills the camera", () => {
    localStorage.setItem(
      "stormlight.canvas.all",
      '{"ghost":{"x":0,"y":0,"w":1e999,"h":1e999}}',
    );
    const done = mountCanvas();
    push({ id: "a" });

    // Not merely finite: the ghost must have been discarded at load,
    // so the newcomer takes the default first slot. Anything else means
    // an absurd box was believed and only papered over downstream.
    expect(boxOf(tileFor("a"))).toEqual({ x: 0, y: 0, w: 440, h: 300 });
    done();
  });
});

describe("gestures", () => {
  test("a drag moves the tile and the move survives a reload", () => {
    let done = mountCanvas();
    push({ id: "a" });
    const tile = tileFor("a");
    const before = boxOf(tile);

    tile.dispatchEvent(pointer("pointerdown", 100, 100));
    tile.dispatchEvent(pointer("pointermove", 160, 140));
    tile.dispatchEvent(pointer("pointerup", 160, 140));
    flushSync();

    const after = boxOf(tileFor("a"));
    expect(after.x).toBeCloseTo(before.x + 60);
    expect(after.y).toBeCloseTo(before.y + 40);

    // The arrangement is the user's: a fresh mount must reproduce it.
    done();
    done = mountCanvas();
    push({ id: "a" });
    expect(boxOf(tileFor("a"))).toEqual(after);
    done();
  });

  test("a drag is not a click", () => {
    let opened = 0;
    const done = mountCanvas(() => opened++);
    push({ id: "a" });
    const tile = tileFor("a");

    tile.dispatchEvent(pointer("pointerdown", 100, 100));
    tile.dispatchEvent(pointer("pointermove", 160, 140));
    tile.dispatchEvent(pointer("pointerup", 160, 140));
    flushSync();

    expect(opened).toBe(0);
    done();
  });

  test("a press that never travels walks into the agent, in place", () => {
    let opened = 0;
    const done = mountCanvas(() => opened++);
    push({ id: "a" }, { id: "b" });
    focusCalls.length = 0;

    const tile = tileFor("b");
    tile.dispatchEvent(pointer("pointerdown", 100, 100));
    tile.dispatchEvent(pointer("pointerup", 101, 101));
    flushSync();

    // The keyboard moved, the view did not.
    expect(opened).toBe(0);
    expect(fleet.selectedID).toBe("b");
    expect(ui.walkedIn).toBe(true);
    expect(ui.view).toBe("canvas");
    // The tile says so, and its terminal — the one — is the walk's
    // anchor with stdin open.
    expect(tileFor("b").classList.contains("focused")).toBe(true);
    expect(tileFor("a").classList.contains("focused")).toBe(false);
    const anchors = document.querySelectorAll("[data-walk-target]");
    expect(anchors).toHaveLength(1);
    expect(tileFor("b").contains(anchors[0])).toBe(true);
    expect(focusCalls).toEqual(["focus:false"]);
    done();
  });

  test("walking out closes the tile's keyboard again", () => {
    const done = mountCanvas();
    push({ id: "a" });
    const tile = tileFor("a");
    tile.dispatchEvent(pointer("pointerdown", 100, 100));
    tile.dispatchEvent(pointer("pointerup", 100, 100));
    flushSync();
    focusCalls.length = 0;

    ui.walkedIn = false;
    flushSync();

    expect(tileFor("a").classList.contains("focused")).toBe(false);
    expect(document.querySelectorAll("[data-walk-target]")).toHaveLength(0);
    expect(focusCalls).toEqual(["blur:true"]);
    done();
  });

  // Whether a button takes focus on click is the browser's opinion,
  // and the walk must not be: ↗ opens the roster to look, on every
  // browser, even from a tile that was being typed into.
  test("the label's open button is the way to the roster, not walked in", () => {
    let opened = 0;
    const done = mountCanvas(() => opened++);
    push({ id: "a" }, { id: "b" });
    const a = tileFor("a");
    a.dispatchEvent(pointer("pointerdown", 100, 100));
    a.dispatchEvent(pointer("pointerup", 100, 100));
    flushSync();
    expect(ui.walkedIn).toBe(true);

    tileFor("b").querySelector<HTMLButtonElement>(".open")!.click();
    flushSync();

    expect(opened).toBe(1);
    expect(fleet.selectedID).toBe("b");
    expect(ui.walkedIn).toBe(false);
    done();
  });

  // A press that begins a gesture moves no focus. Left to the browser,
  // a mousedown on tile B lands focus on B — the tile itself, or xterm's
  // textarea inside it — and blurs the terminal in A that holds the
  // keyboard: rearranging one tile ended the typing in another.
  test("a gesture's press is cancelled, so it moves no focus", () => {
    const done = mountCanvas();
    push({ id: "a" }, { id: "b" });
    const a = tileFor("a");
    a.dispatchEvent(pointer("pointerdown", 100, 100));
    a.dispatchEvent(pointer("pointerup", 100, 100));
    flushSync();

    // Another tile's screen: a drag surface, cancelled.
    const b = tileFor("b");
    const onB = pointer("pointerdown", 100, 100);
    b.querySelector<HTMLElement>(".screen")!.dispatchEvent(onB);
    expect(onB.defaultPrevented).toBe(true);
    b.dispatchEvent(pointer("pointermove", 200, 200));
    b.dispatchEvent(pointer("pointerup", 200, 200));
    flushSync();
    expect(ui.walkedIn).toBe(true);
    expect(tileFor("a").classList.contains("focused")).toBe(true);

    // The focused tile's own label and grip: gestures, cancelled.
    const onLabel = pointer("pointerdown", 100, 100);
    a.querySelector<HTMLElement>(".label")!.dispatchEvent(onLabel);
    expect(onLabel.defaultPrevented).toBe(true);
    a.dispatchEvent(pointer("pointerup", 100, 100));
    const onGrip = pointer("pointerdown", 100, 100);
    a.querySelector<HTMLElement>(".grip")!.dispatchEvent(onGrip);
    expect(onGrip.defaultPrevented).toBe(true);
    a.dispatchEvent(pointer("pointerup", 100, 100));

    // Inside xterm on the focused tile: the terminal's own press, for
    // selecting text, left alone.
    const xterm = document.createElement("div");
    xterm.className = "xterm";
    a.querySelector<HTMLElement>(".frame")!.append(xterm);
    const onTerminal = pointer("pointerdown", 100, 100);
    xterm.dispatchEvent(onTerminal);
    expect(onTerminal.defaultPrevented).toBe(false);

    // The focused tile's margin — the slack around its scaled screen —
    // is nobody's: a press there parked focus on the tile host, where
    // the walk survived and the keys reached nothing.
    const onMargin = pointer("pointerdown", 100, 100);
    a.querySelector<HTMLElement>(".screen")!.dispatchEvent(onMargin);
    expect(onMargin.defaultPrevented).toBe(true);
    done();
  });

  // The walk's anchor is the whole tile. Focus that lands on the tile
  // itself — the browser's answer to a press on its label — is still
  // inside the walk, and must not read as walking out.
  test("the walk's anchor is the tile, not the screen inside it", () => {
    const done = mountCanvas();
    push({ id: "a" });
    const a = tileFor("a");
    a.dispatchEvent(pointer("pointerdown", 100, 100));
    a.dispatchEvent(pointer("pointerup", 100, 100));
    flushSync();

    expect(a.hasAttribute("data-walk-target")).toBe(true);
    done();
  });

  // A terminal disposed for scrolling off screen takes the focus with
  // it, and the walk with the focus. The one holding the keyboard is
  // the one that must never be.
  test("the focused tile stays attached wherever the camera goes", () => {
    const done = mountCanvas();
    push({ id: "a" }, { id: "b" }, { id: "c" });
    const b = tileFor("b");
    b.dispatchEvent(pointer("pointerdown", 100, 100));
    b.dispatchEvent(pointer("pointerup", 100, 100));
    flushSync();
    lifecycle.length = 0;

    scrollAllAway();

    expect(lifecycle.filter((e) => e.startsWith("close:")).sort()).toEqual([
      "close:a",
      "close:c",
    ]);
    done();
  });

  test("walking into a tile that scrolled away brings its terminal back", () => {
    const done = mountCanvas();
    push({ id: "a" }, { id: "b" });
    scrollAllAway();
    lifecycle.length = 0;

    const b = tileFor("b");
    b.dispatchEvent(pointer("pointerdown", 100, 100));
    b.dispatchEvent(pointer("pointerup", 100, 100));
    flushSync();

    expect(lifecycle).toEqual(["attach:b"]);
    expect(focusCalls.at(-1)).toBe("focus:false");
    done();
  });

  // The screen of the tile you are typing into belongs to its terminal:
  // a press there is a selection, not a lift.
  test("the focused tile drags by its label alone", () => {
    const done = mountCanvas();
    push({ id: "a" });
    const tile = tileFor("a");
    tile.dispatchEvent(pointer("pointerdown", 100, 100));
    tile.dispatchEvent(pointer("pointerup", 100, 100));
    flushSync();
    const before = boxOf(tileFor("a"));

    const screen = tile.querySelector<HTMLElement>(".screen")!;
    screen.dispatchEvent(pointer("pointerdown", 100, 100));
    screen.dispatchEvent(pointer("pointermove", 160, 140));
    screen.dispatchEvent(pointer("pointerup", 160, 140));
    flushSync();
    expect(boxOf(tileFor("a"))).toEqual(before);

    const label = tile.querySelector<HTMLElement>(".label")!;
    label.dispatchEvent(pointer("pointerdown", 100, 100));
    label.dispatchEvent(pointer("pointermove", 160, 140));
    label.dispatchEvent(pointer("pointerup", 160, 140));
    flushSync();
    expect(boxOf(tileFor("a")).x).toBeCloseTo(before.x + 60);
    done();
  });

  // The terminal's keystrokes bubble up through the tile, which has
  // Enter bound. An Enter typed at agent a must not also be an Enter
  // pressed on a's tile — which would drag the cursor back to a from
  // wherever it had since moved.
  test("an Enter typed at the agent is not an Enter pressed on the tile", () => {
    const done = mountCanvas();
    push({ id: "a" }, { id: "b" });
    const tile = tileFor("a");
    tile.dispatchEvent(pointer("pointerdown", 100, 100));
    tile.dispatchEvent(pointer("pointerup", 100, 100));
    flushSync();
    fleet.selectedID = "b";
    flushSync();

    const screen = tile.querySelector<HTMLElement>(".screen")!;
    screen.dispatchEvent(
      new KeyboardEvent("keydown", { key: "Enter", bubbles: true }),
    );
    flushSync();
    expect(fleet.selectedID).toBe("b");

    tile.dispatchEvent(
      new KeyboardEvent("keydown", { key: "Enter", bubbles: true }),
    );
    flushSync();
    expect(fleet.selectedID).toBe("a");
    done();
  });
});

describe("the cursor and the camera", () => {
  const wheelBy = (dx: number, dy: number) => {
    document
      .querySelector<HTMLElement>(".canvas")!
      .dispatchEvent(
        new WheelEvent("wheel", { deltaX: dx, deltaY: dy, bubbles: true }),
      );
    flushSync();
  };
  const camera = () => document.querySelector<HTMLElement>(".stage")!.style.transform;

  // alt+j and alt+n move the cursor without a hand on the canvas, and
  // the tile they land on may be off the edge of the world.
  test("a selection wholly off screen is brought into view", () => {
    const done = mountCanvas();
    push({ id: "a" }, { id: "b" });
    // jsdom lays nothing out, so the viewport is 0×0 and every tile
    // is off screen by definition; the pan below makes the point
    // regardless of extent.
    wheelBy(5000, 5000);
    const panned = camera();

    fleet.selectedID = "b";
    flushSync();

    expect(camera()).not.toBe(panned);
    done();
  });

  // The camera follows the cursor, not the tile: a hand that drags the
  // selected tile away and lets go must not watch the camera chase it.
  test("dragging the selected tile away does not drag the camera after it", () => {
    const done = mountCanvas();
    push({ id: "a" }, { id: "b" });
    fleet.selectedID = "a";
    flushSync();
    const before = camera();

    const a = tileFor("a");
    a.dispatchEvent(pointer("pointerdown", 100, 100));
    a.dispatchEvent(pointer("pointermove", 5000, 5000));
    a.dispatchEvent(pointer("pointerup", 5000, 5000));
    flushSync();

    expect(camera()).toBe(before);
    done();
  });

  // A dispatched agent is selected before its tile exists; the tile is
  // minted a tick later, and that is when there is something to show.
  test("a selection made before its tile is minted is still revealed", () => {
    const done = mountCanvas();
    push({ id: "a" });
    wheelBy(5000, 5000);
    const panned = camera();

    fleet.selectedID = "late";
    flushSync();
    expect(camera()).toBe(panned);
    push({ id: "a" }, { id: "late" });

    expect(camera()).not.toBe(panned);
    done();
  });

  test("the camera follows the cursor, never the other way round", () => {
    const done = mountCanvas();
    push({ id: "a" }, { id: "b" });
    fleet.selectedID = "b";
    flushSync();
    const centred = camera();

    // Panning away from the selected tile is the hand's decision; the
    // cursor has not moved and the camera must not snap back.
    wheelBy(5000, 5000);
    expect(camera()).not.toBe(centred);
    const panned = camera();
    push({ id: "a" }, { id: "b", summary: "still b" });
    expect(camera()).toBe(panned);
    done();
  });

  test("the grip resizes without moving, clamped to the minimum", () => {
    const done = mountCanvas();
    push({ id: "a" });
    const tile = tileFor("a");
    const before = boxOf(tile);
    const grip = tile.querySelector<HTMLElement>(".grip")!;

    grip.dispatchEvent(pointer("pointerdown", 400, 400));
    tile.dispatchEvent(pointer("pointermove", 480, 460));
    tile.dispatchEvent(pointer("pointerup", 480, 460));
    flushSync();

    let after = boxOf(tileFor("a"));
    expect(after.x).toBe(before.x);
    expect(after.y).toBe(before.y);
    expect(after.w).toBeCloseTo(tileSize.w + 80);
    expect(after.h).toBeCloseTo(tileSize.h + 60);

    grip.dispatchEvent(pointer("pointerdown", 400, 400));
    tile.dispatchEvent(pointer("pointermove", -4000, -4000));
    tile.dispatchEvent(pointer("pointerup", -4000, -4000));
    flushSync();

    after = boxOf(tileFor("a"));
    expect(after.w).toBe(tileMin.w);
    expect(after.h).toBe(tileMin.h);
    done();
  });

  test("a second pointer landing mid-drag changes nothing", () => {
    let opened = 0;
    const done = mountCanvas(() => opened++);
    push({ id: "a" });
    const tile = tileFor("a");
    const before = boxOf(tile);

    tile.dispatchEvent(pointer("pointerdown", 100, 100, 1));
    tile.dispatchEvent(pointer("pointermove", 160, 100, 1));
    // A palm lands and lifts. Its down must not hijack the gesture and
    // its up must not read as a click.
    tile.dispatchEvent(pointer("pointerdown", 300, 300, 2));
    tile.dispatchEvent(pointer("pointerup", 300, 300, 2));
    tile.dispatchEvent(pointer("pointermove", 200, 100, 1));
    tile.dispatchEvent(pointer("pointerup", 200, 100, 1));
    flushSync();

    expect(opened).toBe(0);
    expect(boxOf(tileFor("a")).x).toBeCloseTo(before.x + 100);
    done();
  });

  test("a foreign pointer's cancel does not kill the drag", () => {
    const done = mountCanvas();
    push({ id: "a" });
    const tile = tileFor("a");
    const before = boxOf(tile);

    tile.dispatchEvent(pointer("pointerdown", 100, 100, 1));
    tile.dispatchEvent(pointer("pointermove", 180, 100, 1));
    // The browser claims a *secondary* touch for a native gesture.
    tile.dispatchEvent(pointer("pointercancel", 0, 0, 2));
    tile.dispatchEvent(pointer("pointermove", 260, 100, 1));
    tile.dispatchEvent(pointer("pointerup", 260, 100, 1));
    flushSync();

    expect(boxOf(tileFor("a")).x).toBeCloseTo(before.x + 160);
    done();
  });

  test("zooming mid-drag does not teleport the tile", () => {
    const done = mountCanvas();
    push({ id: "a" });
    const surface = document.querySelector<HTMLElement>(".canvas")!;
    const tile = tileFor("a");
    const before = boxOf(tile);

    tile.dispatchEvent(pointer("pointerdown", 100, 100));
    tile.dispatchEvent(pointer("pointermove", 200, 100));
    // A reflexive pinch mid-drag: zoom doubles. The 100px already
    // travelled must stay 100 stage units; only pixels moved from here
    // convert at the new zoom.
    surface.dispatchEvent(
      new WheelEvent("wheel", {
        bubbles: true,
        ctrlKey: true,
        deltaY: -Math.log(2) * 100,
      }),
    );
    flushSync();
    tile.dispatchEvent(pointer("pointermove", 210, 100));
    tile.dispatchEvent(pointer("pointerup", 210, 100));
    flushSync();

    expect(boxOf(tileFor("a")).x).toBeCloseTo(before.x + 100 + 10 / 2, 3);
    done();
  });

  test("a drag that wanders back over its origin is still not a click", () => {
    let opened = 0;
    const done = mountCanvas(() => opened++);
    push({ id: "a" });
    const tile = tileFor("a");

    tile.dispatchEvent(pointer("pointerdown", 100, 100));
    tile.dispatchEvent(pointer("pointermove", 160, 140));
    tile.dispatchEvent(pointer("pointermove", 100, 100));
    tile.dispatchEvent(pointer("pointerup", 100, 100));
    flushSync();

    expect(opened).toBe(0);
    done();
  });

  test("a drag tracks the cursor whatever the zoom", () => {
    const done = mountCanvas();
    push({ id: "a" });
    const surface = document.querySelector<HTMLElement>(".canvas")!;
    // A pinch arrives as a ctrl-wheel; this delta is exactly 2x.
    surface.dispatchEvent(
      new WheelEvent("wheel", {
        bubbles: true,
        ctrlKey: true,
        deltaY: -Math.log(2) * 100,
      }),
    );
    flushSync();
    const tile = tileFor("a");
    const before = boxOf(tile);

    // 100 screen pixels at 2x zoom is 50 stage units — a tile that
    // moved 100 would be sliding out from under the hand.
    tile.dispatchEvent(pointer("pointerdown", 100, 100));
    tile.dispatchEvent(pointer("pointermove", 200, 100));
    tile.dispatchEvent(pointer("pointerup", 200, 100));
    flushSync();

    expect(boxOf(tileFor("a")).x).toBeCloseTo(before.x + 50, 3);
    done();
  });

  test("unmounting the canvas releases every terminal", () => {
    const done = mountCanvas();
    push({ id: "a" }, { id: "b" });
    lifecycle.length = 0;

    done();

    expect(lifecycle.sort()).toEqual(["close:a", "close:b"]);
  });
});
