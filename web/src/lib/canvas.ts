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

export const homeView: View = { x: 40, y: 40, z: 1 };

/** Zoom bounds: far enough out to survey a large fleet, close enough in
 *  to read one terminal — beyond either, the view is just lost. */
export const minZoom = 0.1;
export const maxZoom = 4;

/** The default size of a tile nobody has resized, stage units. */
export const tileSize = { w: 440, h: 300 };
export const tileMin = { w: 200, h: 140 };

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
  const z = clamp(view.z * factor, minZoom, maxZoom);
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
  return {
    x: (viewport.w - (right - left) * z) / 2 - left * z,
    y: (viewport.h - (bottom - top) * z) / 2 - top * z,
    z,
  };
}

function overlaps(a: Box, b: Box): boolean {
  return (
    a.x < b.x + b.w + placeGap &&
    b.x < a.x + a.w + placeGap &&
    a.y < b.y + b.h + placeGap &&
    b.y < a.y + a.h + placeGap
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

/** A resize clamped to the smallest tile still worth drawing. */
export function resized(box: Box, w: number, h: number): Box {
  return { ...box, w: Math.max(tileMin.w, w), h: Math.max(tileMin.h, h) };
}
