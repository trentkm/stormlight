// @vitest-environment jsdom
//
// Which workspace the dispatch form opens on. The form's job is to
// need no fields touched in the common case — "another agent, here" —
// and "here" is whatever the dashboard is aimed at, not the first name
// the catalog happens to sort to.
import { afterEach, beforeEach, expect, test } from "vitest";
import { flushSync, mount, unmount } from "svelte";
import Dispatch from "./Dispatch.svelte";
import { fleet } from "../lib/state.svelte";
import type { Agent, Workspace } from "../lib/types";

function workspace(id: string, name: string): Workspace {
  return {
    id,
    kind: "git",
    name,
    root: `/repos/${name}`,
    execution_root: `/repos/${name}`,
  };
}

function agent(id: string, ws: Workspace): Agent {
  return {
    id,
    provider: "claude",
    name: id,
    task: `task ${id}`,
    cwd: ws.execution_root,
    created_at: "2026-08-18T00:00:00Z",
    activity: "working",
    process_live: true,
    workspace: ws,
  } as Agent;
}

// Sorted by name in the rail, so "alpha" is the one a first-in-the-list
// default would pick — every case below asserts against something else.
const alpha = workspace("ws-alpha", "alpha");
const beta = workspace("ws-beta", "beta");
const gamma = workspace("ws-gamma", "gamma");

const leaked: Array<() => void> = [];
afterEach(() => {
  while (leaked.length) leaked.pop()!();
});

beforeEach(() => {
  // jsdom ships the element without its modal behaviour; the form calls
  // showModal on mount, and a missing method would fail every test here
  // for a reason that has nothing to do with what they assert.
  HTMLDialogElement.prototype.showModal ??= function (this: HTMLDialogElement) {
    this.open = true;
  };
  HTMLDialogElement.prototype.close ??= function (this: HTMLDialogElement) {
    this.open = false;
  };

  fleet.agents = [];
  fleet.workspaces = [alpha, beta, gamma];
  fleet.providers = [{ ID: "claude", Label: "Claude", Path: "/usr/bin/claude", Available: true }];
  fleet.selectedID = "";
  fleet.workspaceID = "";
});

/** Opens the form and returns the workspace its select landed on. */
function openedOn(): string {
  const target = document.createElement("div");
  document.body.append(target);
  const form = mount(Dispatch, { target, props: { onclose: () => {} } });
  flushSync();
  leaked.push(() => {
    unmount(form);
    target.remove();
  });
  const selects = [...target.querySelectorAll("select")];
  const chosen = selects.find((select) =>
    [...select.options].some((option) => option.value.startsWith("ws-")),
  );
  return chosen?.value ?? "";
}

test("opens on the workspace the rail has highlighted", () => {
  fleet.workspaceID = "ws-gamma";
  fleet.agents = [agent("a1", beta)];
  fleet.selectedID = "a1";
  expect(openedOn()).toBe("ws-gamma");
});

test("falls back to the selected agent's workspace under All agents", () => {
  fleet.workspaceID = "";
  fleet.agents = [agent("a1", beta)];
  fleet.selectedID = "a1";
  expect(openedOn()).toBe("ws-beta");
});

test("falls back to the first workspace when nothing is aimed at", () => {
  expect(openedOn()).toBe("ws-alpha");
});

test("ignores a highlight naming a workspace the catalog dropped", () => {
  fleet.workspaceID = "ws-gone";
  expect(openedOn()).toBe("ws-alpha");
});
