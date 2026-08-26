/**
 * The canvas's geometry, kept apart from any component so it can be
 * tested as arithmetic rather than as gestures.
 *
 * Two coordinate spaces. The *viewport* is the element on screen, in CSS
 * pixels. The *stage* is the infinite surface tiles live on; the view
 * maps it into the viewport with translate(x, y) scale(z). Tiles are
 * stored in stage units, so panning and zooming never rewrite a single
 * tile — only the view changes.
 */

export interface View {
  x: number;
  y: number;
  z: number;
}

export interface Box {
  x: number;
  y: number;
  w: number;
  h: number;
}

/**
 * A frame: a box on the stage that groups the tiles inside it. It stores
 * no members — a tile is in a frame when its centre is, which is what
 * makes dragging a tile into or out of one just dropping it there.
 */
export interface Frame extends Box {
  name: string;
  locked: boolean;
}

/**
 * A drawing on the stage. Its origin is (x, y); a closed shape spans
 * (w, h) from there, and an open one — a line, an arrow, a pencil
 * stroke — is its points, relative to the origin, so moving a shape
 * is moving its origin whatever its kind. Text is its string at the
 * origin. Nothing here knows about tiles: a drawing is a drawing.
 */
export type ShapeKind = "rect" | "ellipse" | "line" | "arrow" | "pencil" | "text";
export interface Shape {
  kind: ShapeKind;
  x: number;
  y: number;
  w: number;
  h: number;
  points?: Array<[number, number]>;
  text?: string;
}

export const homeView: View = { x: 40, y: 40, z: 1 };

/** How many points a stroke may keep, and how long a text may be. */
export const strokeLimit = 4000;
export const textLimit = 500;
/** A stroke's points closer than this to the last kept one are noise. */
export const strokeTolerance = 1.5;

/** The room a frame keeps around what it was drawn to hold, and the
 *  height of its title bar, which sits above its box. */
export const framePadding = 24;
export const frameTitle = 26;
export const frameMin = { w: 120, h: 80 };
export const frameNameLimit = 80;

/** Zoom bounds: far enough out to survey a large fleet, close enough in
 *  to read one terminal — beyond either, the view is just lost. */
export const minZoom = 0.1;
export const maxZoom = 4;

/** The default size of a tile nobody has resized, stage units. */
export const tileSize = { w: 440, h: 300 };
export const tileMin = { w: 200, h: 140 };

/** The bounds a box must live inside to be believed — enforced at both
 *  ends: load() discards stored boxes outside them, and put()/resized()
 *  clamp gestures into them, so a session can never manufacture a box
 *  that its own reload would throw away. */
export const positionLimit = 1_000_000;
export const extentLimit = 10_000;

/** The gap auto-placement keeps between tiles, and how many columns it
 *  fills before starting a new row. */
const placeGap = 24;
const placeColumns = 3;

function clamp(value: number, low: number, high: number): number {
  return Math.min(high, Math.max(low, value));
}

/** The stage point currently under a viewport point. */
export function stagePoint(
  view: View,
  point: { x: number; y: number },
): { x: number; y: number } {
  return { x: (point.x - view.x) / view.z, y: (point.y - view.y) / view.z };
}

export function panBy(view: View, dx: number, dy: number): View {
  return { ...view, x: view.x + dx, y: view.y + dy };
}

/**
 * Zoom by a factor about a viewport point, so the tile under the cursor
 * stays under the cursor. Zooming about the origin instead makes the
 * surface slide out from underneath the gesture — the single tell of a
 * canvas built without one.
 */
export function zoomAt(
  view: View,
  point: { x: number; y: number },
  factor: number,
): View {
  // The floor yields to a view already below it: fit may legitimately
  // land at z = 0.009 for a far-flung fleet, and a hard floor turned
  // the first gentle wheel tick after that into an 11x snap that threw
  // every tile off screen — the exact loss fit exists to recover from.
  // From below the floor, zooming in is continuous and zooming out
  // simply holds.
  const z = clamp(view.z * factor, Math.min(minZoom, view.z), maxZoom);
  const anchor = stagePoint(view, point);
  return { x: point.x - anchor.x * z, y: point.y - anchor.y * z, z };
}

/**
 * A view that shows every box, centered, with breathing room — the way
 * home after panning off into empty space. Zoom is capped at 1 above
 * (fitting two tiles should not blow them up past legibility) but has
 * NO floor below: fit's one promise is that everything is on screen,
 * and a tile flung ten screens away breaks that promise at any clamped
 * zoom — centering a bounding box whose middle is empty space. Tiny
 * and visible beats clamped and lost; the user zooms back in from
 * there. Non-finite boxes (a poisoned store) are ignored rather than
 * allowed to turn the camera to NaN.
 */
export function fitView(
  boxes: Box[],
  viewport: { w: number; h: number },
  padding = 48,
): View {
  const sound = boxes.filter(
    (b) =>
      Number.isFinite(b.x) &&
      Number.isFinite(b.y) &&
      Number.isFinite(b.w) &&
      Number.isFinite(b.h),
  );
  if (sound.length === 0) return homeView;
  const left = Math.min(...sound.map((b) => b.x));
  const top = Math.min(...sound.map((b) => b.y));
  const right = Math.max(...sound.map((b) => b.x + b.w));
  const bottom = Math.max(...sound.map((b) => b.y + b.h));
  const z = Math.min(
    (viewport.w - padding * 2) / Math.max(1, right - left),
    (viewport.h - padding * 2) / Math.max(1, bottom - top),
    1,
  );
  // A viewport narrower than its own padding makes that arithmetic
  // negative — scale(-0.03) renders a mirrored, inverted stage. There
  // is nothing to fit into; go home instead.
  if (z <= 0) return homeView;
  return {
    x: (viewport.w - (right - left) * z) / 2 - left * z,
    y: (viewport.h - (bottom - top) * z) / 2 - top * z,
    z,
  };
}

/**
 * The view that shows a box at its centre, no further out than half
 * size: a jump lands somewhere legible even from a far-flung survey
 * view, and stays as close as the camera already was.
 */
export function centeredOn(
  view: View,
  box: Box,
  viewport: { w: number; h: number },
): View {
  const z = Math.max(view.z, 0.5);
  return {
    x: viewport.w / 2 - (box.x + box.w / 2) * z,
    y: viewport.h / 2 - (box.y + box.h / 2) * z,
    z,
  };
}

/** Whether any part of a box is inside the viewport under this view. */
export function showing(
  view: View,
  box: Box,
  viewport: { w: number; h: number },
): boolean {
  const left = view.x + box.x * view.z;
  const top = view.y + box.y * view.z;
  return (
    left + box.w * view.z > 0 &&
    top + box.h * view.z > 0 &&
    left < viewport.w &&
    top < viewport.h
  );
}

/** Whether two boxes share any area. Touching edges do not count. */
export function intersects(a: Box, b: Box): boolean {
  return a.x < b.x + b.w && b.x < a.x + a.w && a.y < b.y + b.h && b.y < a.y + a.h;
}

/** The smallest box holding every box given; the empty union is empty. */
export function union(boxes: Box[]): Box {
  if (boxes.length === 0) return { x: 0, y: 0, w: 0, h: 0 };
  const left = Math.min(...boxes.map((b) => b.x));
  const top = Math.min(...boxes.map((b) => b.y));
  const right = Math.max(...boxes.map((b) => b.x + b.w));
  const bottom = Math.max(...boxes.map((b) => b.y + b.h));
  return { x: left, y: top, w: right - left, h: bottom - top };
}

/** The box a rubber band between two stage points describes, whichever
 *  way the hand went. */
export function spanning(
  a: { x: number; y: number },
  b: { x: number; y: number },
): Box {
  return {
    x: Math.min(a.x, b.x),
    y: Math.min(a.y, b.y),
    w: Math.abs(a.x - b.x),
    h: Math.abs(a.y - b.y),
  };
}

/** The frame a box belongs to: the innermost one — by area — whose
 *  box holds the tile's centre, or undefined outside every frame. */
export function frameOf(
  frames: Record<string, Frame>,
  box: Box,
): string | undefined {
  const cx = box.x + box.w / 2;
  const cy = box.y + box.h / 2;
  let found: string | undefined;
  let smallest = Infinity;
  for (const [id, frame] of Object.entries(frames)) {
    const inside =
      cx >= frame.x && cx <= frame.x + frame.w && cy >= frame.y && cy <= frame.y + frame.h;
    const area = frame.w * frame.h;
    if (inside && area < smallest) {
      found = id;
      smallest = area;
    }
  }
  return found;
}

/** The frame that would hold these boxes, with room to breathe. */
export function frameAround(boxes: Box[], padding = framePadding): Box {
  const whole = union(boxes);
  return {
    x: whole.x - padding,
    y: whole.y - padding,
    w: whole.w + padding * 2,
    h: whole.h + padding * 2,
  };
}

/** Placement's overlap: intersection with the gap kept around a tile. */
function overlaps(a: Box, b: Box): boolean {
  return intersects(
    { x: a.x - placeGap, y: a.y - placeGap, w: a.w + placeGap * 2, h: a.h + placeGap * 2 },
    b,
  );
}

/**
 * Where a tile nobody has placed goes: the first free slot scanning a
 * grid left-to-right, top-to-bottom. Deterministic on purpose — the
 * same fleet always lands in the same arrangement, so the canvas opens
 * looking like the wall until a hand rearranges it.
 */
export function place(existing: Box[], size = tileSize): Box {
  const stepX = size.w + placeGap;
  const stepY = size.h + placeGap;
  // Bounded, because this loop runs synchronously on the UI thread and
  // its exit depends on data from localStorage: one absurd stored box
  // that overlaps every slot would otherwise hang the tab the moment a
  // new agent needs a spot. Past the cap, below everything is always
  // free.
  for (let slot = 0; slot < 4096; slot++) {
    const candidate: Box = {
      x: (slot % placeColumns) * stepX,
      y: Math.floor(slot / placeColumns) * stepY,
      ...size,
    };
    if (!existing.some((box) => overlaps(candidate, box))) return candidate;
  }
  const bottom = Math.max(
    0,
    ...existing.filter((b) => Number.isFinite(b.y + b.h)).map((b) => b.y + b.h),
  );
  return { x: 0, y: bottom + placeGap, ...size };
}

/** A resize clamped between the smallest tile still worth drawing and
 *  the largest one load() will believe back. */
export function resized(box: Box, w: number, h: number): Box {
  return {
    ...box,
    w: clamp(w, tileMin.w, extentLimit),
    h: clamp(h, tileMin.h, extentLimit),
  };
}

/** A pencil stroke with its jitter removed: a point closer than the
 *  tolerance to the last one kept is not a movement of the hand. The
 *  first and last points are always kept. */
export function simplified(
  points: Array<[number, number]>,
  tolerance = strokeTolerance,
): Array<[number, number]> {
  if (points.length <= 2) return points.slice();
  const kept: Array<[number, number]> = [points[0]];
  for (let i = 1; i < points.length - 1; i++) {
    const [px, py] = kept[kept.length - 1];
    const [x, y] = points[i];
    if (Math.hypot(x - px, y - py) >= tolerance) kept.push(points[i]);
  }
  kept.push(points[points.length - 1]);
  return kept;
}

/** The shape a set of absolute stage points describes: its origin is
 *  their top-left, its points are relative to it. */
export function strokeOf(
  kind: "line" | "arrow" | "pencil",
  absolute: Array<[number, number]>,
): Shape {
  const left = Math.min(...absolute.map((p) => p[0]));
  const top = Math.min(...absolute.map((p) => p[1]));
  const right = Math.max(...absolute.map((p) => p[0]));
  const bottom = Math.max(...absolute.map((p) => p[1]));
  return {
    kind,
    x: left,
    y: top,
    w: right - left,
    h: bottom - top,
    points: absolute.map(([x, y]) => [x - left, y - top]),
  };
}

/** A shape clamped into what load() believes: finite, inside the
 *  world, its stroke capped in points and its text in length. */
export function boundedShape(shape: Shape): Shape {
  const bounded: Shape = {
    kind: shape.kind,
    x: clamp(shape.x, -positionLimit, positionLimit),
    y: clamp(shape.y, -positionLimit, positionLimit),
    w: clamp(shape.w, 0, extentLimit),
    h: clamp(shape.h, 0, extentLimit),
  };
  if (shape.points) {
    bounded.points = shape.points
      .slice(0, strokeLimit)
      .map(([x, y]) => [clamp(x, -extentLimit, extentLimit), clamp(y, -extentLimit, extentLimit)]);
  }
  if (shape.text !== undefined) bounded.text = shape.text.slice(0, textLimit);
  return bounded;
}

/**
 * An arrow between two boxes: it leaves the source from the middle of
 * the side facing the target and arrives at the middle of the side
 * facing back, as a cubic curve whose handles pull straight out of
 * each side. Sides are chosen by which axis the two are further apart
 * on, so side-by-side tiles connect left-to-right and stacked ones
 * top-to-bottom. `mid` is where a label sits.
 */
export interface Arrow {
  from: { x: number; y: number };
  to: { x: number; y: number };
  path: string;
  mid: { x: number; y: number };
}

export function arrowBetween(a: Box, b: Box): Arrow {
  const ac = { x: a.x + a.w / 2, y: a.y + a.h / 2 };
  const bc = { x: b.x + b.w / 2, y: b.y + b.h / 2 };
  const dx = bc.x - ac.x;
  const dy = bc.y - ac.y;
  const horizontal = Math.abs(dx) >= Math.abs(dy);
  const from = horizontal
    ? { x: dx >= 0 ? a.x + a.w : a.x, y: ac.y }
    : { x: ac.x, y: dy >= 0 ? a.y + a.h : a.y };
  const to = horizontal
    ? { x: dx >= 0 ? b.x : b.x + b.w, y: bc.y }
    : { x: bc.x, y: dy >= 0 ? b.y : b.y + b.h };
  // Handles a third of the way across, along the leaving axis.
  const reach = Math.max(40, (horizontal ? Math.abs(to.x - from.x) : Math.abs(to.y - from.y)) / 3);
  const c1 = horizontal
    ? { x: from.x + Math.sign(dx || 1) * reach, y: from.y }
    : { x: from.x, y: from.y + Math.sign(dy || 1) * reach };
  const c2 = horizontal
    ? { x: to.x - Math.sign(dx || 1) * reach, y: to.y }
    : { x: to.x, y: to.y - Math.sign(dy || 1) * reach };
  return {
    from,
    to,
    path: `M ${from.x} ${from.y} C ${c1.x} ${c1.y}, ${c2.x} ${c2.y}, ${to.x} ${to.y}`,
    // The curve's midpoint, by the bezier at t = 0.5.
    mid: {
      x: (from.x + 3 * c1.x + 3 * c2.x + to.x) / 8,
      y: (from.y + 3 * c1.y + 3 * c2.y + to.y) / 8,
    },
  };
}

/** An arrow from a box to a point the hand is still dragging. */
export function arrowToward(a: Box, point: { x: number; y: number }): Arrow {
  return arrowBetween(a, { x: point.x, y: point.y, w: 0, h: 0 });
}

/** The box, of these, under a stage point — the last drawn wins, the
 *  way the topmost tile takes a click. */
export function boxAt(
  boxes: Array<{ id: string; box: Box }>,
  point: { x: number; y: number },
): string | undefined {
  let found: string | undefined;
  for (const { id, box } of boxes) {
    if (
      point.x >= box.x &&
      point.x <= box.x + box.w &&
      point.y >= box.y &&
      point.y <= box.y + box.h
    ) {
      found = id;
    }
  }
  return found;
}

/** A frame clamped the way a box is, with its name and lock believed
 *  only in the shapes load() accepts. */
export function boundedFrame(frame: Frame): Frame {
  return {
    ...bounded(frame),
    w: clamp(frame.w, frameMin.w, extentLimit),
    h: clamp(frame.h, frameMin.h, extentLimit),
    name: frame.name.slice(0, frameNameLimit),
    locked: frame.locked === true,
  };
}

/** A box clamped into the bounds load() believes, for the commit path:
 *  a gesture that stored what reload discards would silently lose the
 *  arrangement it just made. */
export function bounded(box: Box): Box {
  return {
    x: clamp(box.x, -positionLimit, positionLimit),
    y: clamp(box.y, -positionLimit, positionLimit),
    w: clamp(box.w, tileMin.w, extentLimit),
    h: clamp(box.h, tileMin.h, extentLimit),
  };
}
