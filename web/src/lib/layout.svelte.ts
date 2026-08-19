import { place, tileSize, type Box } from "./canvas";

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

function storageKey(workspaceID: string): string {
  return storagePrefix + (workspaceID || "all");
}

/** The bounds a stored box must live inside to be believed. Storage is
 *  a trust boundary: `1e999` parses to Infinity, and one absurd box is
 *  enough to turn the camera to NaN or wedge placement — so anything
 *  outside these is discarded, not repaired. */
const positionLimit = 1_000_000;
const extentLimit = 10_000;

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

function load(workspaceID: string): Layout {
  try {
    const raw = localStorage.getItem(storageKey(workspaceID));
    if (!raw) return {};
    const parsed: unknown = JSON.parse(raw);
    if (typeof parsed !== "object" || parsed === null || Array.isArray(parsed)) {
      return {};
    }
    const layout: Layout = {};
    for (const [id, box] of Object.entries(parsed)) {
      const candidate = box as Partial<Box>;
      if (sound(candidate)) {
        layout[id] = {
          x: candidate.x,
          y: candidate.y,
          w: candidate.w,
          h: candidate.h,
        };
      }
    }
    return layout;
  } catch {
    return {};
  }
}

function save(workspaceID: string, layout: Layout): void {
  try {
    localStorage.setItem(storageKey(workspaceID), JSON.stringify(layout));
  } catch {
    // A layout that cannot persist still works for the session.
  }
}

/**
 * One workspace's layout, reactive. Created per canvas mount rather
 * than as module state: the workspace can change under the canvas, and
 * a fresh store per (mount, workspace) is simpler than one store that
 * must notice.
 */
export function canvasLayout(workspaceID: string) {
  const tiles: Layout = $state(load(workspaceID));

  return {
    get tiles() {
      return tiles;
    },
    /**
     * The box for an agent, minting one for an agent never placed.
     * Placement considers every stored box, so a new agent lands in
     * free space rather than on top of someone's arrangement.
     */
    boxFor(id: string): Box {
      if (!tiles[id]) {
        tiles[id] = place(Object.values(tiles), tileSize);
        save(workspaceID, tiles);
      }
      return tiles[id];
    },
    /** A drag or resize, committed. */
    put(id: string, box: Box): void {
      tiles[id] = box;
      save(workspaceID, tiles);
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
