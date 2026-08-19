// @vitest-environment jsdom
//
// The page, mounted, driven by keys.
//
// Everything here was invisible to the unit tests: whether a command's
// state has a consumer, whether the page's keys stand down for an
// overlay, whether a walked-in terminal really keeps the keyboard.
// Review proved the point by deleting zoom's only consumer and the
// whole focus reconciliation with the suite still green.
import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";
import { flushSync, mount, unmount } from "svelte";

vi.mock("./lib/api", () => ({
  claimToken: () => true,
  socketURL: () => "ws://test/",
  api: {
    workspaces: () => Promise.resolve([]),
    providers: () => Promise.resolve([]),
    interrupt: () => Promise.resolve(),
    clearAttention: () => Promise.resolve(),
    remove: () => Promise.resolve(),
    mark: () => Promise.resolve(),
  },
  roster: () => () => {},
}));

// The terminal is exercised by its own file; here it is a shape with
// the class the focus rules key on.
vi.mock("./components/Terminal.svelte", async () => {
  const { default: Stub } = await import("./components/TerminalStub.svelte");
  return { default: Stub };
});

vi.stubGlobal(
  "ResizeObserver",
  class {
    observe() {}
    disconnect() {}
  },
);
vi.stubGlobal(
  "IntersectionObserver",
  class {
    observe() {}
    disconnect() {}
    unobserve() {}
  },
);
// jsdom has neither dialog machinery nor matchMedia (the wall's cells
// reach for it through the view switch).
HTMLDialogElement.prototype.showModal = function () {
  this.open = true;
};
HTMLDialogElement.prototype.close = function () {
  this.open = false;
  this.dispatchEvent(new Event("close"));
};
vi.stubGlobal("matchMedia", () => ({
  matches: false,
  addEventListener() {},
  removeEventListener() {},
  // xterm still uses the legacy pair when it watches device pixel ratio.
  addListener() {},
  removeListener() {},
}));

import App from "./App.svelte";
import { ui } from "./lib/commands.svelte";
import { fleet } from "./lib/state.svelte";
import type { Agent } from "./lib/types";

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
      root: "/repo",
      execution_root: "/repo",
    },
    ...extra,
  } as Agent;
}

const leaked: Array<() => void> = [];
afterEach(() => {
  while (leaked.length) leaked.pop()!();
  document.body.innerHTML = "";
});

function page() {
  const target = document.createElement("div");
  document.body.append(target);
  const app = mount(App, { target });
  flushSync();
  let done = false;
  const close = () => {
    if (done) return;
    done = true;
    unmount(app);
    target.remove();
  };
  leaked.push(close);
  return close;
}

/** A key press on the window, where the page listens. */
function press(key: string, init: KeyboardEventInit = {}) {
  const code = /^[a-z]$/i.test(key) ? `Key${key.toUpperCase()}` : key;
  window.dispatchEvent(
    new KeyboardEvent("keydown", { key, code, bubbles: true, ...init }),
  );
  flushSync();
}

function selected(): string {
  return fleet.selectedID;
}

beforeEach(() => {
  fleet.agents = [agent("alpha"), agent("beta"), agent("gamma")];
  fleet.selectedID = "alpha";
  fleet.workspaceID = "";
  fleet.workspaces = [
    { id: "other", kind: "git", name: "other", root: "/o", execution_root: "/o" },
  ];
  fleet.error = "";
  fleet.lost = "";
  Object.assign(ui, {
    view: "roster",
    pane: "terminal",
    walkedIn: false,
    zoomed: false,
    palette: false,
    keys: false,
    dispatching: false,
    confirmDelete: "",
    composing: false,
    pending: "",
    column: "agents",
  });
});

describe("the page's keyboard", () => {
  test("j and k move the selection", () => {
    const done = page();
    press("j");
    expect(selected()).toBe("beta");
    press("k");
    expect(selected()).toBe("alpha");
    done();
  });

  test("the view keys switch views", () => {
    const done = page();
    press("2");
    expect(ui.view).toBe("wall");
    press("1");
    expect(ui.view).toBe("roster");
    done();
  });

  // zoom's state had no consumer at all in the first version, and the
  // suite was happy. This asserts something on screen changes.
  test("alt+z zooms the body, not just a flag", () => {
    const done = page();
    const body = document.querySelector(".body")!;
    expect(body.classList.contains("zoomed")).toBe(false);
    press("z", { altKey: true });
    expect(ui.zoomed).toBe(true);
    expect(document.querySelector(".body")!.classList.contains("zoomed")).toBe(
      true,
    );
    done();
  });

  test("? opens the key list, and an ordinary key closes it", () => {
    const done = page();
    press("?");
    const list = document.querySelector('[aria-label="Keys"]');
    expect(list).not.toBeNull();

    // The overlay owns its keyboard, so the key goes to the dialog.
    list!.dispatchEvent(
      new KeyboardEvent("keydown", { key: "j", code: "KeyJ", bubbles: true }),
    );
    flushSync();

    expect(ui.keys).toBe(false);
    expect(document.querySelector('[aria-label="Keys"]')).toBeNull();
    done();
  });

  // An overlay owns the keyboard while it is open; the page standing
  // down is what stops j from scrolling the roster behind a dialog.
  test("the page's keys stand down while an overlay is open", () => {
    const done = page();
    press("k", { metaKey: true });
    expect(ui.palette).toBe(true);
    const before = selected();
    press("j");
    expect(selected()).toBe(before);
    done();
  });

  test("a half-typed sequence does not survive an overlay", () => {
    const done = page();
    press("m");
    expect(ui.pending).toBe("m");
    // Opened and closed by the mouse alone: no key clears it.
    ui.keys = true;
    flushSync();
    ui.keys = false;
    flushSync();
    expect(ui.pending).toBe("");
    done();
  });
});

/**
 * h and l shipped bound, listed in `?`, and doing nothing — reported by
 * the person using it. The unit tests cover the state; these cover the
 * half that was actually missing, which is that something on screen
 * moves and the mouse and the keys agree about where the keyboard is.
 */
describe("moving between columns", () => {
  function aimed(): string {
    if (document.querySelector(".rail.aimed")) return "workspaces";
    if (document.querySelector(".roster.aimed")) return "agents";
    if (document.querySelector(".pane.aimed")) return "spanreed";
    return "none";
  }

  test("h and l move the mark, and stop at the ends", () => {
    const done = page();
    expect(aimed()).toBe("agents");

    press("h");
    expect(aimed()).toBe("workspaces");
    press("h");
    expect(aimed()).toBe("workspaces");

    press("l");
    expect(aimed()).toBe("agents");
    press("l");
    expect(aimed()).toBe("spanreed");
    press("l");
    expect(aimed()).toBe("spanreed");
    done();
  });

  test("clicking a column aims the keyboard there", () => {
    const done = page();
    expect(aimed()).toBe("agents");

    document
      .querySelector(".rail")!
      .dispatchEvent(new MouseEvent("click", { bubbles: true }));
    flushSync();

    expect(aimed()).toBe("workspaces");
    // And j now means the workspace list, not the roster.
    const before = fleet.workspaceID;
    press("j");
    expect(fleet.workspaceID).not.toBe(before);
    done();
  });

  test("j in the rail changes workspace; in the roster, the agent", () => {
    const done = page();
    press("j");
    expect(selected()).toBe("beta");

    press("h");
    press("j");
    expect(fleet.workspaceID).not.toBe("");
    done();
  });
});

/**
 * The rail renders one order and stepWorkspace walks another, and
 * nothing made them agree — reversing the rail's {#each} left every
 * test green while j moved the cursor the wrong way. These are the
 * tests that tie the two together, and the ones for the click paths
 * that had none.
 */
describe("the rail and the keyboard agree", () => {
  test("j walks the rail in the order the rail is drawn", () => {
    const done = page();
    press("h");

    const drawn = [...document.querySelectorAll(".rail .row")].map(
      (row) => row.querySelector(".name")?.textContent?.trim() ?? "",
    );
    const walked: string[] = [];
    // Start at the top, then walk down past the end.
    press("g");
    press("g");
    for (let step = 0; step < drawn.length + 1; step++) {
      const on = document.querySelector(".rail .row.selected .name");
      walked.push(on?.textContent?.trim() ?? "");
      press("j");
    }

    // The walk visits the drawn rows in the drawn order, then stops.
    expect(walked.slice(0, drawn.length)).toEqual(drawn);
    done();
  });

  test("clicking a workspace resets the agent, as j does", () => {
    const done = page();
    // An agent in another workspace, so the pane has somewhere wrong to
    // be left pointing.
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
    fleet.selectedID = "here";
    flushSync();

    const row = [...document.querySelectorAll<HTMLElement>(".rail .row")].find(
      (element) => element.textContent?.includes("other"),
    )!;
    row.click();
    flushSync();

    expect(fleet.workspaceID).toBe("other");
    expect(fleet.selectedID).toBe("far");
    done();
  });

  test("clicking the roster aims the keyboard at it", () => {
    const done = page();
    press("h");
    expect(document.querySelector(".rail.aimed")).not.toBeNull();

    document
      .querySelector(".roster")!
      .dispatchEvent(new MouseEvent("click", { bubbles: true }));
    flushSync();

    expect(document.querySelector(".roster.aimed")).not.toBeNull();
    done();
  });

  // A click inside the terminal aims the pane without walking in. That
  // is fine only because j and k scroll the terminal's scrollback; when
  // they did nothing, one click left four keys dead.
  test("clicking the pane aims it, and j still means something", () => {
    const done = page();
    document
      .querySelector(".pane")!
      .dispatchEvent(new MouseEvent("click", { bubbles: true }));
    flushSync();

    expect(document.querySelector(".pane.aimed")).not.toBeNull();
    expect(ui.walkedIn).toBe(false);
    const before = selected();
    press("j");
    // Nothing to scroll in the stub, so it falls through to the roster
    // rather than swallowing the key.
    expect(selected()).not.toBe(before);
    done();
  });
});

describe("deleting an agent", () => {
  test("Ctrl-x then x asks, naming the agent", () => {
    const done = page();
    press("x", { ctrlKey: true });
    press("x");

    const dialog = document.querySelector("[role=alertdialog]");
    expect(dialog?.textContent).toContain("alpha");
    done();
  });

  // Enter confirms, so the safe answer is the one under the finger of
  // someone who did not read the question.
  test("the cancel button holds the focus", () => {
    const done = page();
    press("x", { ctrlKey: true });
    press("x");

    expect(document.activeElement?.textContent).toBe("cancel");
    done();
  });
});

describe("walking in, from the page's side", () => {
  test("Enter walks in and the ordinary keys stop reaching the page", () => {
    const done = page();
    press("Enter");
    expect(ui.walkedIn).toBe(true);
    const before = selected();
    press("j");
    expect(selected()).toBe(before);
    done();
  });

  test("the alt chords still work from inside", () => {
    const done = page();
    press("Enter");
    // As macOS composes it.
    window.dispatchEvent(
      new KeyboardEvent("keydown", {
        key: "∆",
        code: "KeyJ",
        altKey: true,
        bubbles: true,
      }),
    );
    flushSync();
    expect(selected()).toBe("beta");
    done();
  });

  test("Ctrl-space walks out again", () => {
    const done = page();
    press("Enter");
    press(" ", { ctrlKey: true, code: "Space" } as KeyboardEventInit);
    expect(ui.walkedIn).toBe(false);
    done();
  });

  // The drift review found: focus can leave the terminal for nothing at
  // all, and a page that still believes it is walked in suppresses its
  // own keys while the agent is not listening either.
  // Focus can leave the terminal for nothing at all — a click on the
  // host's padding blurs the agent's textarea to the body — and a page
  // that keeps believing it is walked in answers to neither side.
  test("focus falling to nothing is handed back, not lost", async () => {
    const done = page();
    press("Enter");
    expect(ui.walkedIn).toBe(true);

    (document.activeElement as HTMLElement | null)?.blur();
    window.dispatchEvent(new FocusEvent("focusout", { bubbles: true }));
    await Promise.resolve();
    await Promise.resolve();
    flushSync();

    expect(ui.walkedIn).toBe(true);
    expect(document.activeElement?.closest(".terminal")).not.toBeNull();
    done();
  });

  // The other half of the same bookkeeping: when an overlay closes,
  // focus is still inside it as focusout fires, so nothing would look
  // again once the dialog was gone.
  test("closing an overlay hands the keyboard back to the agent", async () => {
    const done = page();
    press("Enter");
    press("k", { metaKey: true });
    expect(ui.palette).toBe(true);
    (document.activeElement as HTMLElement | null)?.blur();

    ui.palette = false;
    flushSync();
    await Promise.resolve();
    await Promise.resolve();
    flushSync();

    expect(ui.walkedIn).toBe(true);
    expect(document.activeElement?.closest(".terminal")).not.toBeNull();
    done();
  });

  test("focus landing on something else walks out", async () => {
    const done = page();
    press("Enter");
    expect(ui.walkedIn).toBe(true);

    const elsewhere = document.querySelector<HTMLButtonElement>(".views button");
    elsewhere?.focus();
    window.dispatchEvent(new FocusEvent("focusout", { bubbles: true }));
    await Promise.resolve();
    await Promise.resolve();
    flushSync();

    expect(ui.walkedIn).toBe(false);
    done();
  });
});
