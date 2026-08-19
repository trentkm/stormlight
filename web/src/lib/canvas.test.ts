import { describe, expect, test } from "vitest";
import {
  fitView,
  homeView,
  maxZoom,
  minZoom,
  panBy,
  place,
  resized,
  stagePoint,
  tileMin,
  tileSize,
  zoomAt,
  type Box,
} from "./canvas";

describe("zooming", () => {
  test("keeps the point under the cursor under the cursor", () => {
    const view = { x: 120, y: -80, z: 0.7 };
    const cursor = { x: 400, y: 300 };
    const before = stagePoint(view, cursor);

    const after = stagePoint(zoomAt(view, cursor, 1.3), cursor);

    expect(after.x).toBeCloseTo(before.x, 6);
    expect(after.y).toBeCloseTo(before.y, 6);
  });

  test("holds at the bounds instead of sailing past them", () => {
    const view = { x: 0, y: 0, z: 1 };
    expect(zoomAt(view, { x: 0, y: 0 }, 1000).z).toBe(maxZoom);
    expect(zoomAt(view, { x: 0, y: 0 }, 0.0001).z).toBe(minZoom);
  });

  test("panning slides the view and nothing else", () => {
    expect(panBy({ x: 10, y: 20, z: 0.5 }, -3, 7)).toEqual({
      x: 7,
      y: 27,
      z: 0.5,
    });
  });
});

describe("fitting", () => {
  test("every box lands inside the viewport", () => {
    const boxes: Box[] = [
      { x: -500, y: 200, w: 440, h: 300 },
      { x: 900, y: -100, w: 200, h: 140 },
      { x: 300, y: 800, w: 440, h: 300 },
    ];
    const viewport = { w: 1000, h: 700 };

    const view = fitView(boxes, viewport);

    for (const box of boxes) {
      const left = box.x * view.z + view.x;
      const top = box.y * view.z + view.y;
      const right = (box.x + box.w) * view.z + view.x;
      const bottom = (box.y + box.h) * view.z + view.y;
      expect(left).toBeGreaterThanOrEqual(0);
      expect(top).toBeGreaterThanOrEqual(0);
      expect(right).toBeLessThanOrEqual(viewport.w);
      expect(bottom).toBeLessThanOrEqual(viewport.h);
    }
  });

  // Fit's one promise is that everything is on screen. A tile flung
  // ten screens away must not break it: there is no zoom floor here,
  // because a floor centres a bounding box whose middle is empty
  // space — a "fit" showing zero tiles.
  test("shows a far-flung outlier, however far", () => {
    const boxes: Box[] = [
      { x: 0, y: 0, w: 440, h: 300 },
      { x: 100_000, y: 0, w: 440, h: 300 },
    ];
    const viewport = { w: 1000, h: 700 };

    const view = fitView(boxes, viewport);

    for (const box of boxes) {
      const left = box.x * view.z + view.x;
      const right = (box.x + box.w) * view.z + view.x;
      expect(left).toBeGreaterThanOrEqual(0);
      expect(right).toBeLessThanOrEqual(viewport.w);
    }
  });

  test("ignores a poisoned box instead of going NaN", () => {
    const view = fitView(
      [
        { x: 0, y: 0, w: 440, h: 300 },
        { x: Infinity, y: 0, w: Infinity, h: Infinity },
      ],
      { w: 1000, h: 700 },
    );

    expect(Number.isFinite(view.x)).toBe(true);
    expect(Number.isFinite(view.y)).toBe(true);
    expect(Number.isFinite(view.z)).toBe(true);
  });

  test("never magnifies past life size", () => {
    const view = fitView([{ x: 0, y: 0, w: 100, h: 80 }], { w: 2000, h: 2000 });
    expect(view.z).toBe(1);
  });

  test("an empty canvas fits to home", () => {
    expect(fitView([], { w: 800, h: 600 })).toEqual(homeView);
  });
});

describe("placement", () => {
  function overlapping(a: Box, b: Box): boolean {
    return (
      a.x < b.x + b.w && b.x < a.x + a.w && a.y < b.y + b.h && b.y < a.y + a.h
    );
  }

  test("tiles never land on each other", () => {
    const placed: Box[] = [];
    for (let i = 0; i < 12; i++) placed.push(place(placed));

    for (let i = 0; i < placed.length; i++) {
      for (let j = i + 1; j < placed.length; j++) {
        expect(overlapping(placed[i], placed[j])).toBe(false);
      }
    }
  });

  test("the same fleet always lands in the same arrangement", () => {
    const a: Box[] = [];
    const b: Box[] = [];
    for (let i = 0; i < 5; i++) {
      a.push(place(a));
      b.push(place(b));
    }
    expect(a).toEqual(b);
  });

  test("fills a hand-cleared spot before extending the grid", () => {
    const placed: Box[] = [];
    for (let i = 0; i < 4; i++) placed.push(place(placed));
    const vacated = placed.splice(1, 1)[0];

    expect(place(placed)).toEqual(vacated);
  });

  // Bounded, because the loop's exit depends on localStorage: one
  // absurd box overlapping every slot must degrade to below-everything
  // placement, not a hung tab.
  test("an absurd box cannot hang placement", () => {
    const absurd: Box = { x: -5_000_000, y: -5_000_000, w: 10_000_000, h: 10_000_000 };

    const next = place([absurd]);

    expect(Number.isFinite(next.x)).toBe(true);
    expect(Number.isFinite(next.y)).toBe(true);
  });

  test("keeps the gap even against a hand-parked flush neighbour", () => {
    // Parked exactly where slot zero ends: with no gap in the overlap
    // test, slot zero is "free" and the newcomer lands flush against
    // this tile.
    const parked: Box = { x: tileSize.w, y: 0, w: 100, h: 300 };

    const next = place([parked]);

    expect(next.x).not.toBe(0);
  });

  test("steps around a tile someone dragged into the grid's path", () => {
    const parked: Box = { x: 0, y: 0, w: 2000, h: 100 };
    const next = place([parked]);

    expect(overlapping(next, parked)).toBe(false);
    expect(next.w).toBe(tileSize.w);
  });
});

describe("resizing", () => {
  test("clamps to the smallest tile still worth drawing", () => {
    const box: Box = { x: 5, y: 6, w: 440, h: 300 };
    expect(resized(box, 10, 10)).toEqual({
      x: 5,
      y: 6,
      w: tileMin.w,
      h: tileMin.h,
    });
  });

  test("an ordinary resize passes through untouched", () => {
    expect(resized({ x: 0, y: 0, w: 440, h: 300 }, 600, 420)).toEqual({
      x: 0,
      y: 0,
      w: 600,
      h: 420,
    });
  });
});
