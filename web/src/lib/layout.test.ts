import { beforeEach, describe, expect, test, vi } from "vitest";

// Storage is the trust boundary the store guards; a Map stands in for
// it here, the same way the component tests do.
const stored = new Map<string, string>();
vi.stubGlobal("localStorage", {
  getItem: (key: string) => stored.get(key) ?? null,
  setItem: (key: string, value: string) => stored.set(key, String(value)),
  removeItem: (key: string) => stored.delete(key),
  clear: () => stored.clear(),
});

import { canvasLayout } from "./layout.svelte";
import { frameMin } from "./canvas";

const key = "stormlight.canvas.ws";
const box = { x: 0, y: 0, w: 440, h: 300 };

beforeEach(() => stored.clear());

describe("frames in the store", () => {
  test("a frame survives a reload beside the tiles", () => {
    let layout = canvasLayout("ws");
    layout.put("agent", box);
    const id = layout.addFrame({ x: -24, y: -24, w: 500, h: 360 });
    layout.putFrame(id, { ...layout.frames[id], name: "batch", locked: true });

    layout = canvasLayout("ws");
    expect(layout.tiles.agent).toEqual(box);
    expect(layout.frames[id]).toEqual({
      x: -24,
      y: -24,
      w: 500,
      h: 360,
      name: "batch",
      locked: true,
    });
  });

  test("frames are named for their number until renamed", () => {
    const layout = canvasLayout("ws");
    const first = layout.addFrame(box);
    const second = layout.addFrame(box);
    expect(layout.frames[first].name).toBe("frame 1");
    expect(layout.frames[second].name).toBe("frame 2");
    expect(layout.frames[layout.addFrame(box, "docs")].name).toBe("docs");
  });

  test("a malformed frame is discarded, and its neighbours believed", () => {
    stored.set(
      key,
      JSON.stringify({
        agent: box,
        frames: {
          fine: { x: 0, y: 0, w: 200, h: 200, name: "ok", locked: false },
          noName: { x: 0, y: 0, w: 200, h: 200, locked: false },
          badLock: { x: 0, y: 0, w: 200, h: 200, name: "x", locked: "yes" },
          absurd: { x: 1e300, y: 0, w: 200, h: 200, name: "x", locked: false },
        },
      }),
    );
    const layout = canvasLayout("ws");
    expect(Object.keys(layout.frames)).toEqual(["fine"]);
    expect(layout.tiles.agent).toEqual(box);
  });

  test("a frames key that is not an object is ignored, not fatal", () => {
    stored.set(key, JSON.stringify({ agent: box, frames: [1, 2] }));
    const layout = canvasLayout("ws");
    expect(layout.frames).toEqual({});
    expect(layout.tiles.agent).toEqual(box);
  });

  test("the reserved key never becomes a tile", () => {
    stored.set(key, JSON.stringify({ frames: {} }));
    const layout = canvasLayout("ws");
    expect(layout.tiles.frames).toBeUndefined();
  });

  test("a frame shrunk to nothing is clamped, and a dropped one is gone", () => {
    const layout = canvasLayout("ws");
    const id = layout.addFrame({ x: 0, y: 0, w: 1, h: 1 });
    expect(layout.frames[id].w).toBe(frameMin.w);
    expect(layout.frames[id].h).toBe(frameMin.h);
    layout.dropFrame(id);
    expect(layout.frames[id]).toBeUndefined();
    expect(JSON.parse(stored.get(key)!).frames).toEqual({});
  });
});

describe("drawings in the store", () => {
  test("a drawing survives a reload beside the tiles and frames", () => {
    let layout = canvasLayout("ws");
    const id = layout.addShape({
      kind: "arrow",
      x: 10,
      y: 20,
      w: 30,
      h: 40,
      points: [
        [0, 0],
        [30, 40],
      ],
    });
    const label = layout.addShape({ kind: "text", x: 1, y: 2, w: 0, h: 0, text: "hi" });

    layout = canvasLayout("ws");
    expect(layout.shapes[id]).toEqual({
      kind: "arrow",
      x: 10,
      y: 20,
      w: 30,
      h: 40,
      points: [
        [0, 0],
        [30, 40],
      ],
    });
    expect(layout.shapes[label].text).toBe("hi");
  });

  test("a malformed drawing is discarded, and its neighbours believed", () => {
    stored.set(
      key,
      JSON.stringify({
        agent: box,
        shapes: {
          fine: { kind: "rect", x: 0, y: 0, w: 10, h: 10 },
          unknown: { kind: "star", x: 0, y: 0, w: 10, h: 10 },
          badPoint: { kind: "line", x: 0, y: 0, w: 1, h: 1, points: [[0, "1"]] },
          farPoint: { kind: "line", x: 0, y: 0, w: 1, h: 1, points: [[0, 1e6]] },
          longText: { kind: "text", x: 0, y: 0, w: 0, h: 0, text: "x".repeat(501) },
          negative: { kind: "rect", x: 0, y: 0, w: -1, h: 10 },
        },
      }),
    );
    const layout = canvasLayout("ws");
    expect(Object.keys(layout.shapes)).toEqual(["fine"]);
    expect(layout.tiles.agent).toEqual(box);
  });

  test("a moved drawing is clamped, and a dropped one is gone", () => {
    const layout = canvasLayout("ws");
    const id = layout.addShape({ kind: "ellipse", x: 0, y: 0, w: 10, h: 10 });
    layout.putShape(id, { ...layout.shapes[id], x: 5e6 });
    expect(layout.shapes[id].x).toBe(1_000_000);
    layout.dropShape(id);
    expect(layout.shapes[id]).toBeUndefined();
    expect(JSON.parse(stored.get(key)!).shapes).toEqual({});
  });
});
