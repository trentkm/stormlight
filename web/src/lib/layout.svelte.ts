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
      if (
        typeof candidate.x === "number" &&
        typeof candidate.y === "number" &&
        typeof candidate.w === "number" &&
        typeof candidate.h === "number"
      ) {
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
    /**
     * Forget agents the roster no longer knows. Ids never recur, so a
     * dead agent's spot is genuinely free — without this, every fleet
     * ever run keeps a ghost tile's worth of reserved space forever.
     */
    prune(liveIDs: Set<string>): void {
      let dropped = false;
      for (const id of Object.keys(tiles)) {
        if (!liveIDs.has(id)) {
          delete tiles[id];
          dropped = true;
        }
      }
      if (dropped) save(workspaceID, tiles);
    },
  };
}
