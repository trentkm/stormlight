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
/** Every attach's contract: did it watch, and did it claim layout?
 *  Recorded so a refactor that quietly drops { watching: true } — or
 *  starts answering isLaidOut with true — fails a test instead of
 *  resizing the fleet's shared terminals. */
const contracts: Array<{ id: string; watching: boolean; laidOut: boolean }> =
  [];

vi.mock("../lib/terminal", () => ({
  attach: (
    _term: unknown,
    _fit: unknown,
    id: string,
    isLaidOut: () => boolean,
    _onConnection: unknown,
    options?: { watching?: boolean },
  ) => {
    lifecycle.push(`attach:${id}`);
    contracts.push({
      id,
      watching: options?.watching === true,
      laidOut: isLaidOut(),
    });
    return {
      fit: () => {},
      close: () => lifecycle.push(`close:${id}`),
    };
  },
}));

vi.mock("@xterm/xterm", () => ({
  Terminal: class {
    open() {}
    loadAddon() {}
    resize() {}
    write() {}
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
vi.stubGlobal("IntersectionObserver", StillObserver);
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
  localStorage.clear();
  fleet.agents = [];
  fleet.selectedID = "";
  fleet.workspaceID = "";
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

  test("every attachment watches, and claims no layout", () => {
    const done = mountCanvas();
    push({ id: "a" }, { id: "b" });

    expect(contracts.length).toBeGreaterThan(0);
    for (const contract of contracts) {
      expect(contract).toMatchObject({ watching: true, laidOut: false });
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

  test("a press that never travels opens the agent", () => {
    let opened = 0;
    const done = mountCanvas(() => opened++);
    push({ id: "a" }, { id: "b" });

    const tile = tileFor("b");
    tile.dispatchEvent(pointer("pointerdown", 100, 100));
    tile.dispatchEvent(pointer("pointerup", 101, 101));
    flushSync();

    expect(opened).toBe(1);
    expect(fleet.selectedID).toBe("b");
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
