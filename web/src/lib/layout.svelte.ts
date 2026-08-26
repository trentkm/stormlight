import {
  bounded,
  boundedFrame,
  boundedShape,
  extentLimit,
  frameNameLimit,
  place,
  positionLimit,
  strokeLimit,
  textLimit,
  tileSize,
  type Box,
  type Frame,
  type Shape,
} from "./canvas";

/**
 * Where every tile sits, per workspace, surviving reloads.
 *
 * Stored in localStorage rather than on the server: a layout is one
 * person's arrangement of one browser's screen, not fleet state. The
 * store is read lazily and behind try/catch for the same reason the
 * API token is — storage can be absent (test runners) or throw
 * (private browsing), and a canvas that cannot remember is still a
 * canvas.
 */
const storagePrefix = "stormlight.canvas.";

type Layout = Record<string, Box>;
type Frames = Record<string, Frame>;
type Shapes = Record<string, Shape>;

/**
 * The stored shape: every tile's box keyed by agent id, and the frames
 * and drawings under two reserved keys. Agent ids are hex, so nothing
 * an agent is called can collide with them.
 */
const framesKey = "frames";
const shapesKey = "shapes";
const shapeKinds = new Set(["rect", "ellipse", "line", "arrow", "pencil", "text"]);

function storageKey(workspaceID: string): string {
  return storagePrefix + (workspaceID || "all");
}

/* Storage is a trust boundary: `1e999` parses to Infinity, and one
 * absurd box is enough to turn the camera to NaN or wedge placement —
 * so anything outside the shared limits is discarded, not repaired. */
function sound(candidate: Partial<Box>): candidate is Box {
  const { x, y, w, h } = candidate;
  return (
    typeof x === "number" &&
    typeof y === "number" &&
    typeof w === "number" &&
    typeof h === "number" &&
    Number.isFinite(x) &&
    Number.isFinite(y) &&
    Number.isFinite(w) &&
    Number.isFinite(h) &&
    Math.abs(x) <= positionLimit &&
    Math.abs(y) <= positionLimit &&
    w > 0 &&
    h > 0 &&
    w <= extentLimit &&
    h <= extentLimit
  );
}

function soundFrame(candidate: Partial<Frame>): candidate is Frame {
  return (
    typeof candidate.name === "string" &&
    candidate.name.length <= frameNameLimit &&
    typeof candidate.locked === "boolean" &&
    sound(candidate)
  );
}

function soundShape(candidate: Partial<Shape>): candidate is Shape {
  const { kind, x, y, w, h, points, text } = candidate;
  if (typeof kind !== "string" || !shapeKinds.has(kind)) return false;
  const finite = (n: unknown): n is number => typeof n === "number" && Number.isFinite(n);
  if (!finite(x) || !finite(y) || !finite(w) || !finite(h)) return false;
  if (Math.abs(x) > positionLimit || Math.abs(y) > positionLimit) return false;
  if (w < 0 || h < 0 || w > extentLimit || h > extentLimit) return false;
  if (points !== undefined) {
    if (!Array.isArray(points) || points.length > strokeLimit) return false;
    for (const point of points) {
      if (!Array.isArray(point) || point.length !== 2) return false;
      if (!finite(point[0]) || !finite(point[1])) return false;
      if (Math.abs(point[0]) > extentLimit || Math.abs(point[1]) > extentLimit) return false;
    }
  }
  if (text !== undefined && (typeof text !== "string" || text.length > textLimit)) {
    return false;
  }
  return true;
}

function load(workspaceID: string): { tiles: Layout; frames: Frames; shapes: Shapes } {
  const empty = { tiles: {}, frames: {}, shapes: {} };
  try {
    const raw = localStorage.getItem(storageKey(workspaceID));
    if (!raw) return empty;
    const parsed: unknown = JSON.parse(raw);
    if (typeof parsed !== "object" || parsed === null || Array.isArray(parsed)) {
      return empty;
    }
    const tiles: Layout = {};
    const frames: Frames = {};
    const shapes: Shapes = {};
    for (const [id, value] of Object.entries(parsed)) {
      if (id === shapesKey) {
        if (typeof value !== "object" || value === null || Array.isArray(value)) {
          continue;
        }
        for (const [shapeID, shape] of Object.entries(value)) {
          const candidate = shape as Partial<Shape>;
          if (soundShape(candidate)) {
            const kept: Shape = {
              kind: candidate.kind,
              x: candidate.x,
              y: candidate.y,
              w: candidate.w,
              h: candidate.h,
            };
            if (candidate.points) {
              kept.points = candidate.points.map(([x, y]) => [x, y]);
            }
            if (candidate.text !== undefined) kept.text = candidate.text;
            shapes[shapeID] = kept;
          }
        }
        continue;
      }
      if (id === framesKey) {
        if (typeof value !== "object" || value === null || Array.isArray(value)) {
          continue;
        }
        for (const [frameID, frame] of Object.entries(value)) {
          const candidate = frame as Partial<Frame>;
          if (soundFrame(candidate)) {
            frames[frameID] = {
              x: candidate.x,
              y: candidate.y,
              w: candidate.w,
              h: candidate.h,
              name: candidate.name,
              locked: candidate.locked,
            };
          }
        }
        continue;
      }
      const candidate = value as Partial<Box>;
      if (sound(candidate)) {
        tiles[id] = {
          x: candidate.x,
          y: candidate.y,
          w: candidate.w,
          h: candidate.h,
        };
      }
    }
    return { tiles, frames, shapes };
  } catch {
    return empty;
  }
}

function save(workspaceID: string, tiles: Layout, frames: Frames, shapes: Shapes): void {
  try {
    localStorage.setItem(
      storageKey(workspaceID),
      JSON.stringify({ ...tiles, [framesKey]: frames, [shapesKey]: shapes }),
    );
  } catch {
    // A layout that cannot persist still works for the session.
  }
}

/** An id for a frame or a drawing: local to this browser's
 *  arrangement, never sent anywhere. */
function mintID(): string {
  return typeof crypto !== "undefined" && "randomUUID" in crypto
    ? crypto.randomUUID()
    : Math.random().toString(36).slice(2);
}

/**
 * One workspace's layout, reactive. Created per canvas mount rather
 * than as module state: the workspace can change under the canvas, and
 * a fresh store per (mount, workspace) is simpler than one store that
 * must notice.
 */
export function canvasLayout(workspaceID: string) {
  const loaded = load(workspaceID);
  const tiles: Layout = $state(loaded.tiles);
  const frames: Frames = $state(loaded.frames);
  const shapes: Shapes = $state(loaded.shapes);
  const persist = () => save(workspaceID, tiles, frames, shapes);

  return {
    get tiles() {
      return tiles;
    },
    get frames() {
      return frames;
    },
    get shapes() {
      return shapes;
    },
    /**
     * The box for an agent, minting one for an agent never placed.
     * Placement considers every stored box, so a new agent lands in
     * free space rather than on top of someone's arrangement.
     */
    boxFor(id: string): Box {
      if (!tiles[id]) {
        tiles[id] = place(Object.values(tiles), tileSize);
        persist();
      }
      return tiles[id];
    },
    /** A drag or resize, committed — clamped into the bounds load()
     *  believes, so the session and its reload never disagree about
     *  what was arranged. */
    put(id: string, box: Box): void {
      tiles[id] = bounded(box);
      persist();
    },
    /** A new frame, named for its number until someone renames it. */
    addFrame(box: Box, name?: string): string {
      const id = mintID();
      const count = Object.keys(frames).length + 1;
      frames[id] = boundedFrame({
        ...box,
        name: name ?? `frame ${count}`,
        locked: false,
      });
      persist();
      return id;
    },
    /** A frame moved, resized, renamed or locked — clamped like a box. */
    putFrame(id: string, frame: Frame): void {
      frames[id] = boundedFrame(frame);
      persist();
    },
    dropFrame(id: string): void {
      delete frames[id];
      persist();
    },
    addShape(shape: Shape): string {
      const id = mintID();
      shapes[id] = boundedShape(shape);
      persist();
      return id;
    },
    putShape(id: string, shape: Shape): void {
      shapes[id] = boundedShape(shape);
      persist();
    },
    dropShape(id: string): void {
      delete shapes[id];
      persist();
    },
  };
}

/*
 * Deliberately absent: pruning. An agent missing from a roster push is
 * not gone — an unreachable SSH host is simply omitted while its
 * agents keep running, and their ids come back verbatim on reconnect.
 * A prune keyed on absence turned one dropped poll into a wiped
 * arrangement. A remembered box costs ~90 bytes and is exactly what
 * lets a returning agent find its spot; minting collides against all
 * remembered boxes, so nothing ever lands on a spot that might come
 * back.
 */
