// @vitest-environment jsdom
//
// The palette, mounted and driven by real DOM events.
//
// Every one of these passed as a unit test of the keymap while the
// palette itself was deaf: its handler sat on `window` while its panel
// stopped propagation, so Escape, Enter and the arrows died on the way
// out and only the mouse worked. Nothing short of mounting it and
// pressing keys could see that.
import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";
import { flushSync, mount, unmount } from "svelte";

vi.mock("../lib/api", () => ({ api: {} }));

// jsdom implements <dialog> as an inert element: no showModal, no
// close event, and — the part that matters here — no focus trap. The
// palette is a dialog precisely for that trap, so these stubs let the
// component mount while the trap itself stays a browser concern.
HTMLDialogElement.prototype.showModal = function () {
  this.open = true;
};
HTMLDialogElement.prototype.close = function () {
  this.open = false;
  this.dispatchEvent(new Event("close"));
};

import Palette from "./Palette.svelte";
import { fleet } from "../lib/state.svelte";
import type { Agent } from "../lib/types";

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

const ran: Array<{ id: string; argument?: string }> = [];
let closed = 0;

const leaked: Array<() => void> = [];
afterEach(() => {
  while (leaked.length) leaked.pop()!();
});

function open() {
  const target = document.createElement("div");
  document.body.append(target);
  const palette = mount(Palette, {
    target,
    props: {
      onrun: (id: string, argument?: string) => ran.push({ id, argument }),
      onclose: () => closed++,
    },
  });
  flushSync();
  let done = false;
  const close = () => {
    if (done) return;
    done = true;
    unmount(palette);
    target.remove();
  };
  leaked.push(close);
  return close;
}

/** Presses a key on the panel, the way a person's keyboard would —
 *  including the physical code the browser always sends alongside. */
function press(key: string, init: KeyboardEventInit = {}) {
  const panel = document.querySelector('[aria-label="Command palette"]')!;
  const code = /^[a-z]$/i.test(key) ? `Key${key.toUpperCase()}` : key;
  panel.dispatchEvent(
    new KeyboardEvent("keydown", { key, code, bubbles: true, ...init }),
  );
  flushSync();
}

function rows(): string[] {
  return [...document.querySelectorAll(".entries button")].map((button) =>
    (button.textContent ?? "").replace(/\s+/g, " ").trim(),
  );
}

function highlighted(): string {
  const on = document.querySelector(".entries button.on");
  return (on?.textContent ?? "").replace(/\s+/g, " ").trim();
}

function type(value: string) {
  const input = document.querySelector<HTMLInputElement>("input.query")!;
  const setter = Object.getOwnPropertyDescriptor(
    HTMLInputElement.prototype,
    "value",
  )!.set!;
  setter.call(input, value);
  input.dispatchEvent(new Event("input", { bubbles: true }));
  flushSync();
}

beforeEach(() => {
  ran.length = 0;
  closed = 0;
  fleet.agents = [agent("alpha"), agent("beta"), agent("gamma")];
  fleet.selectedID = "alpha";
  fleet.workspaceID = "";
  fleet.workspaces = [];
});

describe("the palette's keyboard", () => {
  test("Escape closes it", () => {
    const done = open();
    press("Escape");
    expect(closed).toBe(1);
    done();
  });

  test("Enter runs the highlighted entry", () => {
    const done = open();
    press("Enter");
    expect(ran).toEqual([{ id: "select-agent", argument: "alpha" }]);
    done();
  });

  test("the arrows move the highlight", () => {
    const done = open();
    const first = highlighted();
    press("ArrowDown");
    const second = highlighted();
    expect(second).not.toBe(first);
    press("ArrowUp");
    expect(highlighted()).toBe(first);
    done();
  });

  test("Ctrl-n and Ctrl-p move it too", () => {
    const done = open();
    const first = highlighted();
    press("n", { ctrlKey: true });
    expect(highlighted()).not.toBe(first);
    press("p", { ctrlKey: true });
    expect(highlighted()).toBe(first);
    done();
  });

  test("a click on the backdrop closes it", () => {
    const done = open();
    const dialog = document.querySelector("dialog")!;
    // The backdrop is the dialog's own box, so a click outside the
    // panel lands on the dialog itself.
    dialog.dispatchEvent(new MouseEvent("click", { bubbles: true }));
    flushSync();
    expect(closed).toBe(1);
    done();
  });

  test("a click inside the panel does not", () => {
    const done = open();
    document
      .querySelector(".panel")!
      .dispatchEvent(new MouseEvent("click", { bubbles: true }));
    flushSync();
    expect(closed).toBe(0);
    done();
  });

  test("⌘K closes what ⌘K opened", () => {
    const done = open();
    press("k", { metaKey: true });
    expect(closed).toBe(1);
    done();
  });
});

describe("what the palette offers", () => {
  test("agents, workspaces, and actions that name their keys", () => {
    const done = open();
    const listed = rows().join(" | ");
    expect(listed).toContain("alpha");
    expect(listed).toContain("Dispatch a new agent");
    // A palette is how someone learns the keyboard, so the key is on
    // the row.
    expect(listed).toMatch(/Dispatch a new agent\s+n/);
    done();
  });

  test("fuzzy typing finds an agent by a scattering of its letters", () => {
    fleet.agents = [agent("fix error messages"), agent("ssh"), agent("wall")];
    const done = open();
    type("fxerr");
    expect(rows()[0]).toContain("fix error messages");
    done();
  });

  test("a query that matches nothing says so", () => {
    const done = open();
    type("zzzzzz");
    expect(document.querySelector(".nothing")).not.toBeNull();
    press("Enter");
    expect(ran).toEqual([]);
    done();
  });

  // The cursor may only point at a row that exists on screen: Enter on
  // an invisible selection acts on something the person cannot see.
  test("the highlight never leaves the rendered rows", () => {
    fleet.agents = Array.from({ length: 80 }, (_, index) =>
      agent(`agent-${index}`),
    );
    const done = open();
    for (let press_ = 0; press_ < 120; press_++) press("ArrowDown");
    expect(document.querySelectorAll(".entries button.on").length).toBe(1);
    press("Enter");
    expect(ran.length).toBe(1);
    done();
  });
});
