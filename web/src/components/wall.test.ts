// @vitest-environment jsdom
//
// The wall, mounted for real. These tests exist because the failure
// they pin is invisible to unit tests of attach(): it lives in how
// Svelte's reactivity connects the roster to the cells. Every roster
// push re-proxies every agent, so a cell effect that depended on the
// agent *object* would tear down and re-attach its terminal at the
// roster's cadence — a wall that quietly reconnects the entire fleet
// every 700ms while looking perfectly calm.
import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";
import { flushSync, mount, unmount } from "svelte";
import type { Agent } from "../lib/types";

// One record of every terminal lifecycle event, in order. What matters
// in every test below is not what the cells rendered but how many times
// they reached for the daemon.
const lifecycle: string[] = [];
/** Every attach's contract, recorded so a refactor that drops
 *  { watching: true } — or starts claiming layout — fails here instead
 *  of resizing the fleet's shared terminals. */
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

import Wall from "./Wall.svelte";
import { fleet } from "../lib/state.svelte";

/** A roster the way the event socket delivers one: freshly parsed
 *  objects every time, sharing nothing with the last push. */
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

function mountWall(onopen: () => void = () => {}) {
  const target = document.createElement("div");
  document.body.append(target);
  const wall = mount(Wall, { target, props: { onopen } });
  flushSync();
  let closed = false;
  const close = () => {
    if (closed) return;
    closed = true;
    unmount(wall);
    target.remove();
  };
  leaked.push(close);
  return close;
}

beforeEach(() => {
  lifecycle.length = 0;
  contracts.length = 0;
  fleet.agents = [];
  fleet.selectedID = "";
  fleet.workspaceID = "";
});

describe("the wall against the roster", () => {
  test("a roster push that changes nothing attaches nothing", () => {
    const done = mountWall();
    push({ id: "a" }, { id: "b" }, { id: "c" });
    expect(lifecycle).toEqual(["attach:a", "attach:b", "attach:c"]);

    for (let i = 0; i < 5; i++) push({ id: "a" }, { id: "b" }, { id: "c" });

    expect(lifecycle).toEqual(["attach:a", "attach:b", "attach:c"]);
    done();
  });

  test("one agent's status moving re-attaches nobody", () => {
    const done = mountWall();
    push({ id: "a" }, { id: "b" });
    lifecycle.length = 0;

    push({ id: "a", summary: "now doing something else" }, { id: "b" });
    push({ id: "a", activity: "idle", attention: "question" }, { id: "b" });

    expect(lifecycle).toEqual([]);
    done();
  });

  test("reordering — an agent turning urgent — re-attaches nobody", () => {
    const done = mountWall();
    push({ id: "a" }, { id: "b" });
    lifecycle.length = 0;

    // b turns urgent and sorts to the front; both cells must survive
    // the move.
    push({ id: "a" }, { id: "b", attention: "question" });

    expect(lifecycle).toEqual([]);
    done();
  });

  test("an agent leaving the roster closes exactly its own terminal", () => {
    const done = mountWall();
    push({ id: "a" }, { id: "b" });
    lifecycle.length = 0;

    push({ id: "b" });

    expect(lifecycle).toEqual(["close:a"]);
    done();
  });

  test("clicking a cell selects its agent and opens the roster", () => {
    let opened = 0;
    const done = mountWall(() => opened++);
    push({ id: "a" }, { id: "b" });

    const cell = [...document.querySelectorAll(".cell")].find((c) =>
      c.textContent?.includes("b"),
    )!;
    (cell.querySelector(".reach") as HTMLButtonElement).click();
    flushSync();

    expect(fleet.selectedID).toBe("b");
    expect(opened).toBe(1);
    done();
  });

  test("every attachment watches, and claims no layout", () => {
    const done = mountWall();
    push({ id: "a" }, { id: "b" });

    expect(contracts.length).toBeGreaterThan(0);
    for (const contract of contracts) {
      expect(contract).toMatchObject({ watching: true, laidOut: false });
    }
    done();
  });

  test("unmounting the wall releases every terminal", () => {
    const done = mountWall();
    push({ id: "a" }, { id: "b" });
    lifecycle.length = 0;

    done();

    expect(lifecycle.sort()).toEqual(["close:a", "close:b"]);
  });
});
