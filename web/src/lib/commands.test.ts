// @vitest-environment jsdom
//
// The dispatcher, driven by ids. What matters here is that every id the
// keymap can produce actually does something, and that the ones with
// consequences do the right one.
import { beforeEach, describe, expect, test, vi } from "vitest";

const calls: string[] = [];
vi.mock("./api", () => ({
  api: {
    interrupt: (id: string) => {
      calls.push(`interrupt:${id}`);
      return Promise.resolve();
    },
    clearAttention: (id: string) => {
      calls.push(`seen:${id}`);
      return Promise.resolve();
    },
    remove: (id: string) => {
      calls.push(`remove:${id}`);
      return Promise.resolve();
    },
    mark: (id: string, mark: string) => {
      calls.push(`mark:${id}:${mark}`);
      return Promise.resolve();
    },
  },
}));

import { bindings } from "./keys";
import { known, run, ui } from "./commands.svelte";
import { fleet } from "./state.svelte";
import type { Agent } from "./types";

function agent(id: string, extra: Partial<Agent> = {}): Agent {
  return {
    id,
    provider: "claude",
    name: id,
    task: `task ${id}`,
    cwd: "/",
    created_at: "2026-08-19T00:00:00Z",
    activity: "working",
    process_live: true,
    workspace: {
      id: "ws",
      kind: "git",
      name: "ws",
      root: "/",
      execution_root: "/",
    },
    ...extra,
  } as Agent;
}

beforeEach(() => {
  calls.length = 0;
  fleet.agents = [agent("a"), agent("b"), agent("c")];
  fleet.selectedID = "a";
  fleet.workspaceID = "";
  ui.view = "roster";
  ui.pane = "terminal";
  ui.walkedIn = false;
  ui.zoomed = false;
  ui.palette = false;
  ui.keys = false;
  ui.dispatching = false;
  ui.history = false;
  ui.composing = false;
  ui.pending = "";
});

describe("moving through the roster", () => {
  test("j and k step, and wrap", () => {
    run("down");
    expect(fleet.selectedID).toBe("b");
    run("up");
    expect(fleet.selectedID).toBe("a");
    run("up");
    expect(fleet.selectedID).toBe("c");
  });

  test("gg and G reach the ends", () => {
    run("last");
    expect(fleet.selectedID).toBe("c");
    run("first");
    expect(fleet.selectedID).toBe("a");
  });

  test("an empty roster moves nowhere rather than throwing", () => {
    fleet.agents = [];
    fleet.selectedID = "";
    run("down");
    run("first");
    run("last");
    expect(fleet.selectedID).toBe("");
  });
});

// alt+n/alt+p exist because the person wants whoever has been blocked
// longest — not whoever is next in the roster.
describe("the attention queue", () => {
  test("walks agents needing a person, oldest first", () => {
    fleet.agents = [
      agent("fresh", {
        attention: "question",
        attention_at: "2026-08-19T10:00:00Z",
      }),
      agent("calm"),
      agent("stale", {
        attention: "approval",
        attention_at: "2026-08-19T08:00:00Z",
      }),
    ];
    fleet.selectedID = "calm";

    run("queue-next");
    expect(fleet.selectedID).toBe("stale");
    run("queue-next");
    expect(fleet.selectedID).toBe("fresh");
    run("queue-next");
    expect(fleet.selectedID).toBe("stale");
  });

  test("skips agents that are merely working", () => {
    run("queue-next");
    expect(fleet.selectedID).toBe("a");
  });
});

describe("walking in and out", () => {
  test("Enter walks in, and lands on the terminal tab", () => {
    ui.pane = "diff";
    run("walk-in");
    expect(ui.walkedIn).toBe(true);
    expect(ui.pane).toBe("terminal");
  });

  test("with nothing selected there is nowhere to walk", () => {
    fleet.selectedID = "";
    run("walk-in");
    expect(ui.walkedIn).toBe(false);
  });

  test("Ctrl-space walks out", () => {
    run("walk-in");
    run("walk-out");
    expect(ui.walkedIn).toBe(false);
  });

  test("leaving the terminal's tab lets go of the keyboard", () => {
    run("walk-in");
    run("pane-transcript");
    expect(ui.walkedIn).toBe(false);
    expect(ui.pane).toBe("transcript");
  });

  test("a view that has no terminal lets go of it too", () => {
    run("walk-in");
    run("view-wall");
    expect(ui.walkedIn).toBe(false);
  });
});

describe("agent actions", () => {
  test("interrupt, seen, and delete reach the API for the selected agent", () => {
    run("interrupt");
    run("seen");
    run("delete");
    expect(calls).toEqual(["interrupt:a", "seen:a", "remove:a"]);
  });

  test("with nothing selected they do nothing at all", () => {
    fleet.selectedID = "";
    run("interrupt");
    run("seen");
    run("delete");
    expect(calls).toEqual([]);
  });
});

describe("the palette's destinations", () => {
  test("jumping to an agent selects it and shows the roster", () => {
    ui.view = "canvas";
    run("select-agent", "c");
    expect(fleet.selectedID).toBe("c");
    expect(ui.view).toBe("roster");
  });

  test("jumping to a workspace selects its first agent", () => {
    fleet.agents = [
      agent("elsewhere", {
        workspace: {
          id: "other",
          kind: "git",
          name: "other",
          root: "/o",
          execution_root: "/o",
        },
      }),
      agent("here"),
    ];
    fleet.selectedID = "here";

    run("select-workspace", "other");
    expect(fleet.workspaceID).toBe("other");
    expect(fleet.selectedID).toBe("elsewhere");
  });

  test("a workspace with no agents clears the selection rather than lying", () => {
    run("select-workspace", "empty");
    expect(fleet.selectedID).toBe("");
  });
});

describe("marking", () => {
  test("each mark key sends the mark it names", () => {
    run("mark-working");
    run("mark-attention");
    run("mark-clear");
    expect(calls).toEqual(["mark:a:working", "mark:a:attention", "mark:a:"]);
  });
});

// The contract between the two tables: every key the keymap can produce
// has to name a command the dispatcher runs. A binding that dispatches
// nothing is a key advertised in `?` that does nothing when pressed.
describe("the keymap and the dispatcher agree", () => {
  test("every binding dispatches something", () => {
    const dangling = bindings
      .map((binding) => binding.id)
      .filter((id) => !known(id));
    expect(dangling).toEqual([]);
  });

  test("nothing the palette offers is unbuilt", () => {
    const offered = bindings
      .filter((binding) => binding.palette)
      .map((binding) => binding.id)
      .filter((id) => !known(id));
    expect(offered).toEqual([]);
  });
});
