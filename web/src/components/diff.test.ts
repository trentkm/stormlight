// @vitest-environment jsdom
//
// The diff pane, mounted for real: line classification, the replace-
// wholesale poll, the fault posture, and the one-in-flight guard.
import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";
import { flushSync, mount, unmount } from "svelte";

type View = { diff: string; ok: boolean };

const responses: View[] = [];
const gates: Array<Promise<void>> = [];
let calls = 0;

vi.mock("../lib/api", () => ({
  api: {
    diff: async () => {
      calls++;
      if (gates.length > 0) await gates.shift();
      return responses.length > 1 ? responses.shift()! : responses[0];
    },
  },
}));

import Diff from "./Diff.svelte";

const leaked: Array<() => void> = [];
afterEach(() => {
  while (leaked.length) leaked.pop()!();
  vi.useRealTimers();
});

function mountPane() {
  const target = document.createElement("div");
  document.body.append(target);
  const pane = mount(Diff, { target, props: { id: "a1" } });
  flushSync();
  let closed = false;
  const close = () => {
    if (closed) return;
    closed = true;
    unmount(pane);
    target.remove();
  };
  leaked.push(close);
  return close;
}

async function settle() {
  await vi.advanceTimersByTimeAsync(0);
  flushSync();
}

async function pollTick() {
  await vi.advanceTimersByTimeAsync(5000);
  flushSync();
}

beforeEach(() => {
  vi.useFakeTimers();
  responses.length = 0;
  gates.length = 0;
  calls = 0;
});

const sample = [
  "diff --git a/f.go b/f.go",
  "index 111..222 100644",
  "--- a/f.go",
  "+++ b/f.go",
  "@@ -1,2 +1,2 @@",
  " context line",
  "-removed line",
  "+added line",
].join("\n");

describe("the diff pane", () => {
  test("classifies every line by what it is", async () => {
    responses.push({ diff: sample + "\n", ok: true });
    const done = mountPane();
    await settle();

    // Svelte scoping appends its hash class; the kind is the first.
    const kinds = [...document.querySelectorAll(".diff pre span")].map(
      (span) => span.classList[0],
    );
    expect(kinds).toEqual([
      "file",
      "meta",
      "meta",
      "meta",
      "hunk",
      "ctx",
      "del",
      "add",
    ]);
    done();
  });

  // Position, not prefix: inside a hunk, "---" and "+++" are ordinary
  // deletions and additions of content that starts with "--" or "++" —
  // a SQL comment, a Lua comment, a signature. Colouring those as file
  // metadata hides exactly the lines a reviewer came to read.
  test("dashes inside a hunk are changes, not metadata", async () => {
    const tricky = [
      "diff --git a/q.sql b/q.sql",
      "--- a/q.sql",
      "+++ b/q.sql",
      "@@ -1,2 +1,2 @@",
      "--- a sql comment",
      "+++ a replacement comment",
    ].join("\n");
    responses.push({ diff: tricky + "\n", ok: true });
    const done = mountPane();
    await settle();

    const kinds = [...document.querySelectorAll(".diff pre span")].map(
      (span) => span.classList[0],
    );
    expect(kinds).toEqual(["file", "meta", "meta", "hunk", "del", "add"]);
    done();
  });

  test("a shrunken diff replaces what was shown", async () => {
    responses.push(
      { diff: sample + "\n", ok: true },
      { diff: "", ok: true },
    );
    const done = mountPane();
    await settle();
    expect(document.querySelectorAll(".diff pre span").length).toBe(8);

    await pollTick();
    expect(document.querySelector(".diff pre")).toBeNull();
    expect(document.querySelector(".empty")?.textContent).toContain(
      "No changes",
    );
    done();
  });

  test("a fault keeps the diff on screen", async () => {
    responses.push({ diff: sample + "\n", ok: true });
    const done = mountPane();
    await settle();

    responses.length = 0;
    responses.push({ diff: "", ok: false });
    await pollTick();

    expect(document.querySelectorAll(".diff pre span").length).toBe(8);
    done();
  });

  test("an agent with nothing to answer says why", async () => {
    responses.push({ diff: "", ok: false });
    const done = mountPane();
    await settle();

    expect(document.querySelector(".empty")?.textContent).toContain(
      "remote, or its directory is not a git repository",
    );
    done();
  });

  test("an answer arriving after unmount touches nothing", async () => {
    let release!: () => void;
    gates.push(new Promise<void>((resolve) => (release = resolve)));
    responses.push({ diff: sample + "\n", ok: true });
    const done = mountPane();
    await settle();

    // The pane closes while its first fetch is still in flight.
    done();
    release();
    await settle();

    expect(document.querySelector(".diff")).toBeNull();
  });

  test("a fetch that outlives the interval never stacks", async () => {
    let release!: () => void;
    gates.push(new Promise<void>((resolve) => (release = resolve)));
    responses.push({ diff: sample + "\n", ok: true });
    const done = mountPane();
    await settle();
    await pollTick();
    await pollTick();

    release();
    await settle();

    expect(calls).toBe(1);
    done();
  });
});
