// @vitest-environment jsdom
//
// The canvas, mounted for real. Two families of failure live only at
// this layer: reactivity wiring (the wall's attach storm, which no unit
// test of attach() could see) and gesture bookkeeping (a drag that
// commits a click, a click that commits a drag, a layout that forgets).
import { beforeEach, describe, expect, test, vi } from "vitest";
import { flushSync, mount, unmount } from "svelte";
import type { Agent } from "../lib/types";

const lifecycle: string[] = [];

vi.mock("../lib/terminal", () => ({
  attach: (_term: unknown, _fit: unknown, id: string) => {
    lifecycle.push(`attach:${id}`);
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

function mountCanvas(onopen: () => void = () => {}) {
  const target = document.createElement("div");
  document.body.append(target);
  const canvas = mount(Canvas, { target, props: { onopen } });
  flushSync();
  return () => {
    unmount(canvas);
    target.remove();
  };
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

  test("a departed agent's spot is freed for the next arrival", () => {
    const done = mountCanvas();
    push({ id: "a" }, { id: "b" });
    const vacated = boxOf(tileFor("b"));

    push({ id: "a" });
    push({ id: "a" }, { id: "late" });

    expect(boxOf(tileFor("late"))).toEqual(vacated);
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
