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
  // Four stops: walking right ends where you type, because in a
  // browser the composer is a real place the keyboard can be.
  test("l walks right to the end and stops", () => {
    ui.column = "workspaces";
    run("pane-right");
    expect(ui.column).toBe("agents");
    run("pane-right");
    expect(ui.column).toBe("spanreed");
    run("pane-right");
    expect(ui.column).toBe("composer");
    run("pane-right");
    expect(ui.column).toBe("composer");
  });

  test("h walks left to the end and stops", () => {
    ui.column = "composer";
    run("pane-left");
    expect(ui.column).toBe("spanreed");
    run("pane-left");
    expect(ui.column).toBe("agents");
    run("pane-left");
    expect(ui.column).toBe("workspaces");
    run("pane-left");
    expect(ui.column).toBe("workspaces");
  });

  test("landing on the composer lands in it", () => {
    document.body.innerHTML = '<input data-composer />';
    const box = document.querySelector<HTMLElement>("[data-composer]")!;
    ui.column = "spanreed";

    run("pane-right");

    // A column you cannot type in would be a stop that does nothing.
    expect(document.activeElement).toBe(box);
  });

  test("stepping away from it gives the keyboard back", () => {
    document.body.innerHTML = '<input data-composer />';
    const box = document.querySelector<HTMLElement>("[data-composer]")!;
    ui.column = "spanreed";
    run("pane-right");
    expect(document.activeElement).toBe(box);

    run("pane-left");

    expect(document.activeElement).not.toBe(box);
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

/**
 * Nine mutations survived the first version of this file, all of them
 * in the half of the feature that scrolls rather than selects. These
 * are the tests that kill them.
 */
describe("j and k in the pane", () => {
  /**
   * Spanreed's real shape: the terminal stays mounted — and stays
   * *first* — while the transcript or diff renders after it, so tab
   * switches do not churn the attachment. Building only the tab under
   * test hid a selector that took whatever came first in the document,
   * which was always the hidden terminal.
   */
  function spanreed(tab: "terminal" | "transcript" | "diff") {
    document.body.innerHTML =
      '<div data-walk-target><div class="xterm-viewport"></div></div>' +
      (tab === "transcript" ? '<div class="transcript"></div>' : "") +
      (tab === "diff" ? '<div class="diff"></div>' : "");
    ui.pane = tab;
    ui.column = "spanreed";
    const shape = (element: HTMLElement) => {
      Object.defineProperty(element, "scrollHeight", { value: 1000 });
      // scrollBy's signature admits a number as well as options; the
      // code only ever passes options, and the double says so.
      element.scrollBy = (options?: number | ScrollToOptions) => {
        element.scrollTop += (options as ScrollToOptions)?.top ?? 0;
      };
      return element;
    };
    return {
      terminal: shape(
        document.querySelector<HTMLElement>(".xterm-viewport")!,
      ),
      visible: shape(
        document.querySelector<HTMLElement>(
          tab === "terminal" ? ".xterm-viewport" : `.${tab}`,
        )!,
      ),
    };
  }

  test("scrolls the transcript, not the terminal behind it", () => {
    const { visible, terminal } = spanreed("transcript");

    run("down");

    expect(visible.scrollTop).toBeGreaterThan(0);
    // The terminal is still mounted, hidden, and first in the document:
    // scrolling it would move nothing anyone can see.
    expect(terminal.scrollTop).toBe(0);
    // And the roster did not move underneath it either.
    expect(fleet.selectedID).toBe("a");
  });

  test("scrolls the diff, not the terminal behind it", () => {
    const { visible, terminal } = spanreed("diff");

    run("down");

    expect(visible.scrollTop).toBeGreaterThan(0);
    expect(terminal.scrollTop).toBe(0);
  });

  // The TUI's j/k scroll the PTY's scrollback in the interaction pane;
  // leaving the terminal out made four keys dead on the tab people are
  // on most.
  test("scrolls the terminal's scrollback", () => {
    const { terminal } = spanreed("terminal");

    run("down");

    expect(terminal.scrollTop).toBeGreaterThan(0);
  });

  test("with nothing to scroll, moves the roster rather than nothing", () => {
    document.body.innerHTML = "";
    ui.column = "spanreed";

    run("down");

    expect(fleet.selectedID).toBe("b");
  });

  test("gg and G reach the ends of what is scrolling", () => {
    const { visible, terminal } = spanreed("transcript");
    visible.scrollTop = 400;

    run("first");
    expect(visible.scrollTop).toBe(0);
    run("last");
    expect(visible.scrollTop).toBe(1000);
    expect(terminal.scrollTop).toBe(0);
  });

  test("gg and G fall through when there is nothing to scroll", () => {
    document.body.innerHTML = "";
    ui.column = "spanreed";

    run("last");

    expect(fleet.selectedID).toBe("c");
  });
});

describe("walking out lands somewhere the keys work", () => {
  // The regression this file exists to prevent: walking out left the
  // keyboard aimed at a pane whose tab does not scroll, so j, k, gg
  // and G all did nothing — worse than before the columns existed.
  test("Ctrl-space puts the keyboard back on the roster", () => {
    run("walk-in");
    expect(ui.column).toBe("spanreed");

    run("walk-out");

    expect(ui.column).toBe("agents");
    run("down");
    expect(fleet.selectedID).toBe("b");
  });

  test("and collapses zoom, as the TUI's seam key does", () => {
    run("walk-in");
    run("zoom");
    expect(ui.zoomed).toBe(true);

    run("walk-out");

    expect(ui.zoomed).toBe(false);
  });
});

describe("keys that need an agent to mean anything", () => {
  test("a pane key with nothing selected navigates nowhere", () => {
    fleet.selectedID = "";
    ui.view = "wall";

    run("pane-terminal");
    run("pane-transcript");
    run("pane-diff");

    expect(ui.view).toBe("wall");
  });

  test("zoom with nothing selected does nothing at all", () => {
    fleet.selectedID = "";
    ui.view = "wall";

    run("zoom");

    expect(ui.zoomed).toBe(false);
    expect(ui.view).toBe("wall");
  });

  // The TUI's zoom sets the interaction pane with it; a terminal
  // filling the body while ignoring what you type is half a gesture.
  test("zoom brings the terminal tab it is zooming", () => {
    run("pane-diff");
    expect(ui.pane).toBe("diff");

    run("zoom");

    // Walking in to a tab that is display:none would be a terminal
    // filling the body and showing nothing.
    expect(ui.pane).toBe("terminal");
  });

  test("leaving the roster disarms zoom", () => {
    run("zoom");
    run("view-wall");
    expect(ui.zoomed).toBe(false);
    run("view-roster");
    expect(ui.zoomed).toBe(false);
  });

  test("zoom walks in", () => {
    run("zoom");
    expect(ui.zoomed).toBe(true);
    expect(ui.walkedIn).toBe(true);
    expect(ui.pane).toBe("terminal");
  });

  test("zoom from another view zooms rather than un-zooming", () => {
    run("zoom");
    ui.view = "wall";

    run("zoom");

    expect(ui.view).toBe("roster");
    expect(ui.zoomed).toBe(true);
  });
});

describe("keys that need their view to mean anything", () => {
  test("every pane key from the wall brings the roster with it", () => {
    for (const [id, tab] of [
      ["pane-diff", "diff"],
      ["pane-transcript", "transcript"],
      ["pane-terminal", "terminal"],
    ] as const) {
      ui.view = "wall";
      run(id);
      expect(ui.view, id).toBe("roster");
      expect(ui.pane, id).toBe(tab);
    }
  });

  test("zoom from the canvas does too, and aims at the pane", () => {
    ui.view = "canvas";
    run("zoom");
    expect(ui.view).toBe("roster");
    expect(ui.zoomed).toBe(true);
    expect(ui.column).toBe("spanreed");
  });

  // Zoomed, the rail and the roster are not on screen; stepping onto
  // one would move a cursor nobody can see.
  test("h and l do not step onto a hidden column", () => {
    run("zoom");
    expect(ui.column).toBe("spanreed");
    run("pane-left");
    expect(ui.column).toBe("spanreed");
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

  test("a vanished workspace lands on All agents rather than jumping", () => {
    fleet.workspaceID = "gone";
    ui.column = "workspaces";

    run("up");

    expect(fleet.workspaceID).toBe("");
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
/**
 * The seam that keeps splitting.
 *
 * Three rounds running, a change to the dispatcher did not reach the
 * table that describes the keys: h/l's wording, zoom's new meaning,
 * zoom's new requirement. Each was found by a person reading both
 * files side by side, which is not a thing that scales.
 *
 * So: ask the dispatcher what it actually requires, by running every
 * binding with an agent and without one, and require the table to say
 * the same thing. A command that does nothing without a selection is
 * one the palette must not offer without a selection.
 */
describe("the table declares what the dispatcher requires", () => {
  // Two worlds again, for the same reason the inertness test needs
  // them: a command that is already where it is going moves nothing,
  // and that is not the same as needing an agent to go there.
  function settleWorld(withAgent: boolean, where: "cold" | "hot") {
    calls.length = 0;
    document.body.innerHTML = "";
    fleet.agents = [agent("a"), agent("b"), agent("c")];
    fleet.selectedID = withAgent ? (where === "cold" ? "a" : "c") : "";
    fleet.workspaceID = "";
    Object.assign(ui, {
      view: where === "cold" ? "roster" : "wall",
      pane: where === "cold" ? "terminal" : "diff",
      walkedIn: false,
      zoomed: false,
      palette: false,
      keys: false,
      dispatching: false,
      confirmDelete: "",
      composing: false,
      pending: "",
      column: where === "cold" ? "agents" : "spanreed",
    });
  }

  function moves(id: string, withAgent: boolean): boolean {
    return (["cold", "hot"] as const).some((where) =>
      movesIn(id, withAgent, where),
    );
  }

  function movesIn(
    id: string,
    withAgent: boolean,
    where: "cold" | "hot",
  ): boolean {
    settleWorld(withAgent, where);
    const before = JSON.stringify({
      ui: { ...ui },
      selected: fleet.selectedID,
      workspace: fleet.workspaceID,
      calls: [...calls],
    });
    run(id);
    return (
      JSON.stringify({
        ui: { ...ui },
        selected: fleet.selectedID,
        workspace: fleet.workspaceID,
        calls: [...calls],
      }) !== before
    );
  }

  test("needsAgent means exactly what run() does", () => {
    const mismatched: string[] = [];
    for (const binding of bindings) {
      const withoutOne = moves(binding.id, false);
      const withOne = moves(binding.id, true);
      const requiresOne = withOne && !withoutOne;
      if (requiresOne !== (binding.needsAgent === true)) {
        mismatched.push(
          `${binding.id}: run() ${requiresOne ? "requires" : "does not require"}` +
            ` an agent, table says ${binding.needsAgent === true}`,
        );
      }
    }
    expect(mismatched).toEqual([]);
  });
});

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
