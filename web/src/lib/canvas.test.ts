import { describe, expect, test } from "vitest";
import {
  extentLimit,
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
  arrowBetween,
  boundedFrame,
  boundedShape,
  boxAt,
  centeredOn,
  frameAround,
  frameMin,
  frameOf,
  intersects,
  showing,
  simplified,
  spanning,
  strokeOf,
  union,
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

  test("a viewport smaller than its padding goes home, not negative", () => {
    // (w - 2*padding) < 0 would otherwise produce scale(-0.03): a
    // mirrored, inverted stage.
    const view = fitView([{ x: 0, y: 0, w: 440, h: 300 }], { w: 80, h: 700 });

    expect(view).toEqual(homeView);
  });

  // Fit may legitimately land far below the wheel's floor. From there
  // a hard floor turned the first gentle tick into an 11x snap that
  // threw every tile off screen — zooming must be continuous from
  // wherever fit put the camera.
  test("zooming in from below the floor is continuous", () => {
    const parked = { x: 0, y: 0, z: 0.009 };

    const view = zoomAt(parked, { x: 500, y: 350 }, 1.1);

    expect(view.z).toBeCloseTo(0.0099, 6);
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
  test("even an infinite box cannot hang placement", () => {
    // Overlaps every slot ever tried; only the slot cap ends the scan.
    const next = place([{ x: 0, y: 0, w: Infinity, h: Infinity }]);

    expect(Number.isFinite(next.x)).toBe(true);
    expect(Number.isFinite(next.y)).toBe(true);
  });

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

  test("a resize cannot outgrow what a reload will believe", () => {
    const grown = resized({ x: 0, y: 0, w: 440, h: 300 }, 50_000, 50_000);

    expect(grown.w).toBe(extentLimit);
    expect(grown.h).toBe(extentLimit);
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

describe("centeredOn", () => {
  const viewport = { w: 1000, h: 600 };
  const box = { x: 2000, y: 3000, w: 400, h: 300 };

  test("puts the box's middle at the viewport's", () => {
    const view = centeredOn({ x: 0, y: 0, z: 1 }, box, viewport);
    expect(view.x + (box.x + box.w / 2) * view.z).toBe(500);
    expect(view.y + (box.y + box.h / 2) * view.z).toBe(300);
  });

  test("keeps a close camera close, and lifts a far one to half size", () => {
    expect(centeredOn({ x: 0, y: 0, z: 2 }, box, viewport).z).toBe(2);
    expect(centeredOn({ x: 0, y: 0, z: 0.05 }, box, viewport).z).toBe(0.5);
  });
});

describe("showing", () => {
  const viewport = { w: 1000, h: 600 };
  const box = { x: 0, y: 0, w: 400, h: 300 };

  test("a box entirely inside or partly across the edge is showing", () => {
    expect(showing({ x: 100, y: 100, z: 1 }, box, viewport)).toBe(true);
    // Half off the left edge: still something to see.
    expect(showing({ x: -200, y: 100, z: 1 }, box, viewport)).toBe(true);
    // Its last pixel just inside the right edge, scaled.
    expect(showing({ x: 999, y: 0, z: 0.5 }, box, viewport)).toBe(true);
  });

  test("a box wholly past any edge is not", () => {
    expect(showing({ x: -400, y: 0, z: 1 }, box, viewport)).toBe(false);
    expect(showing({ x: 1000, y: 0, z: 1 }, box, viewport)).toBe(false);
    expect(showing({ x: 0, y: -300, z: 1 }, box, viewport)).toBe(false);
    expect(showing({ x: 0, y: 600, z: 1 }, box, viewport)).toBe(false);
    // Zoom counts: at z = 0.5 the box ends at 200px, short of an
    // origin 200px past the edge.
    expect(showing({ x: -200, y: 0, z: 0.5 }, box, viewport)).toBe(false);
  });
});

describe("intersects", () => {
  const a = { x: 0, y: 0, w: 100, h: 100 };
  test("overlap counts, touching does not", () => {
    expect(intersects(a, { x: 50, y: 50, w: 100, h: 100 })).toBe(true);
    expect(intersects(a, { x: 100, y: 0, w: 100, h: 100 })).toBe(false);
    expect(intersects(a, { x: 0, y: 100, w: 100, h: 100 })).toBe(false);
    expect(intersects(a, { x: 200, y: 200, w: 10, h: 10 })).toBe(false);
  });
  test("is symmetric and contains itself", () => {
    const b = { x: 90, y: -10, w: 30, h: 30 };
    expect(intersects(a, b)).toBe(intersects(b, a));
    expect(intersects(a, a)).toBe(true);
  });
});

describe("union", () => {
  test("holds every box, tightly", () => {
    expect(
      union([
        { x: 10, y: 20, w: 30, h: 40 },
        { x: -5, y: 50, w: 10, h: 10 },
      ]),
    ).toEqual({ x: -5, y: 20, w: 45, h: 40 });
  });
  test("of nothing is nothing", () => {
    expect(union([])).toEqual({ x: 0, y: 0, w: 0, h: 0 });
  });
});

describe("spanning", () => {
  test("is the same box whichever way the hand went", () => {
    const down = spanning({ x: 10, y: 10 }, { x: 60, y: 40 });
    const up = spanning({ x: 60, y: 40 }, { x: 10, y: 10 });
    expect(down).toEqual({ x: 10, y: 10, w: 50, h: 30 });
    expect(up).toEqual(down);
  });
});

describe("frameOf", () => {
  const outer = { x: 0, y: 0, w: 1000, h: 1000, name: "outer", locked: false };
  const inner = { x: 100, y: 100, w: 300, h: 300, name: "inner", locked: false };
  const frames = { outer, inner };

  test("a tile belongs to the frame holding its centre", () => {
    expect(frameOf(frames, { x: 600, y: 600, w: 100, h: 100 })).toBe("outer");
    expect(frameOf(frames, { x: 2000, y: 0, w: 100, h: 100 })).toBeUndefined();
  });

  test("the centre decides, not the edges", () => {
    // Mostly outside, centre inside.
    expect(frameOf(frames, { x: -190, y: 500, w: 400, h: 100 })).toBe("outer");
    // Mostly inside, centre outside.
    expect(frameOf(frames, { x: 900, y: 500, w: 400, h: 100 })).toBeUndefined();
  });

  test("of overlapping frames, the innermost wins", () => {
    expect(frameOf(frames, { x: 150, y: 150, w: 100, h: 100 })).toBe("inner");
  });
});

describe("frameAround", () => {
  test("holds the boxes with room to breathe", () => {
    const box = frameAround([{ x: 100, y: 100, w: 200, h: 100 }], 24);
    expect(box).toEqual({ x: 76, y: 76, w: 248, h: 148 });
  });
});

describe("boundedFrame", () => {
  test("clamps the box, caps the name, and believes only a real lock", () => {
    const frame = boundedFrame({
      x: 0,
      y: 0,
      w: 1,
      h: 1,
      name: "x".repeat(500),
      locked: "yes" as unknown as boolean,
    });
    expect(frame.w).toBe(frameMin.w);
    expect(frame.h).toBe(frameMin.h);
    expect(frame.name).toHaveLength(80);
    expect(frame.locked).toBe(false);
  });
});

describe("simplified", () => {
  test("drops the jitter and keeps the ends", () => {
    const stroke: Array<[number, number]> = [
      [0, 0],
      [0.5, 0.2],
      [1, 0.1],
      [10, 0],
      [10.4, 0.3],
      [20, 0],
    ];
    expect(simplified(stroke, 1.5)).toEqual([
      [0, 0],
      [10, 0],
      [20, 0],
    ]);
  });
  test("leaves a dot or a dash alone", () => {
    expect(simplified([[1, 1]])).toEqual([[1, 1]]);
    expect(simplified([[1, 1], [1.1, 1]])).toEqual([[1, 1], [1.1, 1]]);
  });
});

describe("strokeOf", () => {
  test("puts the origin at the top-left and the points relative to it", () => {
    const shape = strokeOf("line", [
      [100, 50],
      [40, 90],
    ]);
    expect(shape).toEqual({
      kind: "line",
      x: 40,
      y: 50,
      w: 60,
      h: 40,
      points: [
        [60, 0],
        [0, 40],
      ],
    });
  });
});

describe("boundedShape", () => {
  test("clamps the box, caps the stroke, and trims the text", () => {
    const shape = boundedShape({
      kind: "pencil",
      x: 5e6,
      y: 0,
      w: -10,
      h: 1e6,
      points: Array.from({ length: 5000 }, (_, i) => [i, 1e6] as [number, number]),
      text: "x".repeat(1000),
    });
    expect(shape.x).toBe(1_000_000);
    expect(shape.w).toBe(0);
    expect(shape.h).toBe(10_000);
    expect(shape.points).toHaveLength(4000);
    expect(shape.points![0]).toEqual([0, 10_000]);
    expect(shape.text).toHaveLength(500);
  });
});

describe("arrowBetween", () => {
  const a = { x: 0, y: 0, w: 100, h: 50 };
  test("side by side, it leaves the facing sides at their middles", () => {
    const b = { x: 300, y: 0, w: 100, h: 50 };
    const arrow = arrowBetween(a, b);
    expect(arrow.from).toEqual({ x: 100, y: 25 });
    expect(arrow.to).toEqual({ x: 300, y: 25 });
    expect(arrow.mid).toEqual({ x: 200, y: 25 });
    // And back the other way, from the other sides.
    const back = arrowBetween(b, a);
    expect(back.from).toEqual({ x: 300, y: 25 });
    expect(back.to).toEqual({ x: 100, y: 25 });
  });
  test("stacked, it leaves the top and bottom", () => {
    const b = { x: 0, y: 400, w: 100, h: 50 };
    const arrow = arrowBetween(a, b);
    expect(arrow.from).toEqual({ x: 50, y: 50 });
    expect(arrow.to).toEqual({ x: 50, y: 400 });
    expect(arrow.path.startsWith("M 50 50 C")).toBe(true);
  });
});

describe("boxAt", () => {
  const boxes = [
    { id: "a", box: { x: 0, y: 0, w: 100, h: 100 } },
    { id: "b", box: { x: 50, y: 50, w: 100, h: 100 } },
  ];
  test("finds the box under a point, the topmost where they overlap", () => {
    expect(boxAt(boxes, { x: 10, y: 10 })).toBe("a");
    expect(boxAt(boxes, { x: 75, y: 75 })).toBe("b");
    expect(boxAt(boxes, { x: 500, y: 500 })).toBeUndefined();
  });
});
