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
import { reconcileFocus, run, ui } from "./commands.svelte";
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
  ui.confirmDelete = "";
  ui.composing = false;
  ui.pending = "";
  ui.column = "agents";
});

/**
 * h and l, which shipped bound, listed in `?`, and doing nothing.
 *
 * The test that was meant to catch it asked only whether a command
 * moved *something*: `pane-left` passed by clearing walkedIn and
 * `pane-right` by setting a view that was usually already set. So
 * these name the movement rather than accepting any movement at all.
 */
describe("moving between columns", () => {
  test("l walks right to the end and stops", () => {
    ui.column = "workspaces";
    run("pane-right");
    expect(ui.column).toBe("agents");
    run("pane-right");
    expect(ui.column).toBe("spanreed");
    run("pane-right");
    expect(ui.column).toBe("spanreed");
  });

  test("h walks left to the end and stops", () => {
    ui.column = "spanreed";
    run("pane-left");
    expect(ui.column).toBe("agents");
    run("pane-left");
    expect(ui.column).toBe("workspaces");
    run("pane-left");
    expect(ui.column).toBe("workspaces");
  });

  test("leaving the pane lets go of the terminal", () => {
    run("walk-in");
    expect(ui.walkedIn).toBe(true);
    expect(ui.column).toBe("spanreed");
    run("pane-left");
    expect(ui.walkedIn).toBe(false);
  });

  test("the wall and the canvas have no columns to walk", () => {
    ui.view = "wall";
    ui.column = "agents";
    run("pane-left");
    expect(ui.column).toBe("agents");
  });
});

describe("j and k mean what the aimed column says", () => {
  test("in the roster they change the agent", () => {
    ui.column = "agents";
    run("down");
    expect(fleet.selectedID).toBe("b");
  });

  test("in the rail they change the workspace, and reset the agent", () => {
    fleet.agents = [
      agent("here"),
      agent("far", {
        workspace: {
          id: "other",
          kind: "git",
          name: "other",
          root: "/o",
          execution_root: "/o",
        },
      }),
    ];
    fleet.workspaces = [
      { id: "other", kind: "git", name: "other", root: "/o", execution_root: "/o" },
    ];
    fleet.workspaceID = "";
    fleet.selectedID = "here";
    ui.column = "workspaces";

    run("down");

    expect(fleet.workspaceID).toBe("other");
    // The pane beside the roster must not be left showing an agent from
    // the workspace you just left.
    expect(fleet.selectedID).toBe("far");
  });

  test("the rail stops at its ends", () => {
    ui.column = "workspaces";
    fleet.workspaceID = "";
    run("up");
    expect(fleet.workspaceID).toBe("");
  });

  test("gg and G reach the ends of the aimed column", () => {
    ui.column = "agents";
    run("last");
    expect(fleet.selectedID).toBe("c");
    run("first");
    expect(fleet.selectedID).toBe("a");
  });
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
  test("interrupt and seen reach the API for the selected agent", () => {
    run("interrupt");
    run("seen");
    expect(calls).toEqual(["interrupt:a", "seen:a"]);
  });

  // The TUI shows a confirmation naming its victim. A browser that
  // deleted an agent on an invisible two-key sequence would be worse
  // than the TUI, not terser than it.
  test("delete asks before it deletes", () => {
    run("delete");
    expect(calls).toEqual([]);
    expect(ui.confirmDelete).toBe("a");

    run("delete-confirmed");
    expect(calls).toEqual(["remove:a"]);
    expect(ui.confirmDelete).toBe("");
  });

  test("a confirmation that is cancelled deletes nothing", () => {
    run("delete");
    ui.confirmDelete = "";
    expect(calls).toEqual([]);
  });

  test("the confirmation deletes what it named, not what is selected now", () => {
    run("delete");
    fleet.selectedID = "c";
    run("delete-confirmed");
    expect(calls).toEqual(["remove:a"]);
  });

  test("with nothing selected they do nothing at all", () => {
    fleet.selectedID = "";
    run("interrupt");
    run("seen");
    run("delete");
    expect(calls).toEqual([]);
    expect(ui.confirmDelete).toBe("");
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

describe("the view keys", () => {
  test("each shows what it names", () => {
    run("view-wall");
    expect(ui.view).toBe("wall");
    run("view-canvas");
    expect(ui.view).toBe("canvas");
    run("view-roster");
    expect(ui.view).toBe("roster");
  });

  test("zoom toggles, and something can read it", () => {
    run("zoom");
    expect(ui.zoomed).toBe(true);
    run("zoom");
    expect(ui.zoomed).toBe(false);
  });

  test("the overlays open", () => {
    run("palette");
    expect(ui.palette).toBe(true);
    run("help");
    expect(ui.keys).toBe(true);
    run("dispatch");
    expect(ui.dispatching).toBe(true);
  });

  test("i asks for the composer", () => {
    run("message");
    expect(ui.composing).toBe(true);
    expect(ui.walkedIn).toBe(false);
  });
});

/**
 * The walked-in claim against where focus really is. Every case here
 * was a live bug at some point in this branch's review: focus falling
 * to nothing, focus taken by an overlay, focus never leaving at all.
 */
describe("keeping the walk honest", () => {
  function make(html: string): HTMLElement {
    const host = document.createElement("div");
    host.innerHTML = html;
    document.body.append(host);
    return host.firstElementChild as HTMLElement;
  }

  beforeEach(() => {
    document.body.innerHTML = "";
    ui.walkedIn = true;
  });

  // Walked in means the terminal holds the keyboard. When focus falls
  // to nothing — the host's padding swallows a click, an overlay closes
  // handing focus back to no one — the answer is to give it back, not
  // to leave a page answering to neither the agent nor itself.
  test("focus falling to nothing is handed back to the terminal", () => {
    const helper = make(
      '<div class="terminal" data-walk-target><textarea></textarea></div>',
    ).querySelector("textarea")!;
    helper.blur();

    reconcileFocus(document.body);

    expect(ui.walkedIn).toBe(true);
    expect(document.activeElement).toBe(helper);
  });

  test("with no terminal to hand it back to, the walk ends", () => {
    reconcileFocus(null);
    expect(ui.walkedIn).toBe(false);
  });

  test("an overlay taking focus does not end it", () => {
    // ⌘K autofocuses the palette's query. That is not walking out, and
    // Escape has to put you back where you were.
    const input = make('<dialog><input /></dialog>').querySelector("input")!;
    reconcileFocus(input);
    expect(ui.walkedIn).toBe(true);
  });

  test("focus still inside the terminal keeps it", () => {
    const helper = make(
      '<div class="terminal" data-walk-target><textarea></textarea></div>',
    ).querySelector("textarea")!;
    reconcileFocus(helper);
    expect(ui.walkedIn).toBe(true);
  });

  test("focus is never handed to a watching wall cell", () => {
    // A wall cell carries xterm's own `terminal` class and comes first
    // in document order; only the pane's hook may receive the walk.
    make('<div class="terminal xterm"><textarea id="cell"></textarea></div>');
    const pane = make(
      '<div class="terminal" data-walk-target><textarea id="pane"></textarea></div>',
    ).querySelector("textarea")!;

    reconcileFocus(document.body);

    expect(document.activeElement).toBe(pane);
  });

  test("a roster button taking focus ends it", () => {
    const button = make("<button>agent</button>");
    reconcileFocus(button);
    expect(ui.walkedIn).toBe(false);
  });

  test("it does nothing at all when not walked in", () => {
    ui.walkedIn = false;
    reconcileFocus(document.body);
    expect(ui.walkedIn).toBe(false);
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

/**
 * The contract between the tables, asserted by consequence.
 *
 * The first version of this compared binding ids against a hand-written
 * list of ids the dispatcher "knows" — two lists agreeing with each
 * other, which proved nothing about whether anything ran. Six commands
 * could be gutted with the suite green, and two of them were in fact
 * dead in the shipped code. So: press every binding, and require that
 * something observable moves.
 */
describe("every binding actually does something", () => {
  /** A snapshot of everything a command could move. */
  function world() {
    return JSON.stringify({
      ui: { ...ui },
      selected: fleet.selectedID,
      workspace: fleet.workspaceID,
      calls: [...calls],
    });
  }

  /**
   * Two worlds, because half these commands are direction-dependent:
   * `h` does nothing when you are already left, `walk-out` nothing when
   * you are already out. A command that moves nothing from *either*
   * side is dead; one that moves nothing from one side is just a
   * command with somewhere to be.
   */
  function settle(where: "cold" | "hot") {
    calls.length = 0;
    fleet.agents = [
      agent("a"),
      agent("b", { attention: "question", attention_at: "2026-08-19T08:00:00Z" }),
      agent("c"),
    ];
    fleet.selectedID = where === "cold" ? "a" : "c";
    fleet.workspaceID = "";
    Object.assign(ui, {
      view: where === "cold" ? "roster" : "canvas",
      pane: where === "cold" ? "terminal" : "diff",
      walkedIn: where === "hot",
      zoomed: where === "hot",
      palette: false,
      keys: false,
      dispatching: false,
      confirmDelete: "",
      composing: false,
      pending: "",
    });
  }

  test("no binding is inert", () => {
    const inert: string[] = [];
    for (const binding of bindings) {
      const moved = (["cold", "hot"] as const).some((where) => {
        settle(where);
        const before = world();
        run(binding.id);
        return world() !== before;
      });
      if (!moved) inert.push(`${binding.id} (${binding.keys})`);
    }
    expect(inert).toEqual([]);
  });
});
