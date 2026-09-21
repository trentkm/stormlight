// The selection against the roster, with a launch in flight. The server
// answers a dispatch before the roster it pushes has the agent, so the
// selection made from that answer has to survive a push or two that
// does not know about it yet — and only those.
import { afterEach, beforeEach, expect, test, vi } from "vitest";

vi.mock("./api", () => ({ api: {}, roster: () => () => {} }));

import { fleet, receiveRoster, selectLaunched } from "./state.svelte";
import type { Agent, Workspace } from "./types";

function workspace(id: string): Workspace {
  return { id, kind: "git", name: id, root: `/repos/${id}`, execution_root: `/repos/${id}` };
}

const alpha = workspace("ws-alpha");
const beta = workspace("ws-beta");

function agent(id: string, ws: Workspace): Agent {
  return {
    id,
    provider: "claude",
    name: id,
    task: `task ${id}`,
    cwd: ws.execution_root,
    created_at: "2026-09-20T00:00:00Z",
    activity: "working",
    process_live: true,
    workspace: ws,
  } as Agent;
}

const a1 = agent("a1", alpha);
const a2 = agent("a2", beta);
const fresh = agent("a3", beta);

beforeEach(() => {
  vi.useFakeTimers();
  fleet.agents = [];
  fleet.selectedID = "";
  fleet.workspaceID = "";
  receiveRoster([a1, a2]);
  fleet.selectedID = "a1";
});

afterEach(() => {
  // A launch left standing would leak into the next test's rosters.
  receiveRoster([]);
  vi.useRealTimers();
});

test("a launched agent stays selected through a roster that lacks it", () => {
  selectLaunched(fresh);
  expect(fleet.selectedID).toBe("a3");
  receiveRoster([a1, a2]);
  expect(fleet.selectedID).toBe("a3");
  receiveRoster([a1, a2, fresh]);
  expect(fleet.selectedID).toBe("a3");
});

test("once seen, the launched agent is an ordinary selection", () => {
  selectLaunched(fresh);
  receiveRoster([a1, a2, fresh]);
  // Deleted after it arrived: the selection goes with it, as any would.
  receiveRoster([a1, a2]);
  expect(fleet.selectedID).toBe("a1");
});

test("a selection that outlived its agent is still dropped", () => {
  receiveRoster([a2]);
  expect(fleet.selectedID).toBe("a2");
});

test("moving the selection elsewhere ends the wait", () => {
  selectLaunched(fresh);
  fleet.selectedID = "a2";
  // The user went to a2; the launch no longer protects anything, and a
  // roster without a2 falls back the ordinary way.
  receiveRoster([a1]);
  expect(fleet.selectedID).toBe("a1");
});

test("a launch that never arrives is given up on", () => {
  selectLaunched(fresh);
  vi.advanceTimersByTime(10_001);
  receiveRoster([a1, a2]);
  expect(fleet.selectedID).toBe("a1");
});

test("the rail follows a launch into a workspace it was not showing", () => {
  fleet.workspaceID = "ws-alpha";
  selectLaunched(fresh);
  expect(fleet.workspaceID).toBe("ws-beta");
});

test("All agents is left alone by a launch", () => {
  fleet.workspaceID = "";
  selectLaunched(fresh);
  expect(fleet.workspaceID).toBe("");
});
