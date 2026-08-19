// @vitest-environment jsdom
//
// The transcript pane, mounted for real. What lives only at this layer:
// the delta protocol (accumulate, resync on shrink) and the fault
// posture (a server hiccup must not blank a morning's conversation).
import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";
import { flushSync, mount, unmount } from "svelte";
import type { TranscriptEntry } from "../lib/types";

type View = { entries: TranscriptEntry[]; total: number; ok: boolean };

/** The next responses the fake server will give, in order; the last
 *  one repeats. Each call records the cursor it was asked for. */
const responses: View[] = [];
const cursors: number[] = [];

vi.mock("../lib/api", () => ({
  api: {
    transcript: (_id: string, after = 0) => {
      cursors.push(after);
      const next = responses.length > 1 ? responses.shift()! : responses[0];
      return Promise.resolve(next);
    },
  },
}));

import Transcript from "./Transcript.svelte";

const leaked: Array<() => void> = [];
afterEach(() => {
  while (leaked.length) leaked.pop()!();
  vi.useRealTimers();
});

function mountPane(id = "a1") {
  const target = document.createElement("div");
  document.body.append(target);
  const pane = mount(Transcript, { target, props: { id } });
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

/** Lets the component's in-flight await land, then flushes render.
 *  Timers are faked, so this advances virtual time zero ticks. */
async function settle() {
  await vi.advanceTimersByTimeAsync(0);
  flushSync();
}

/** One tick of the 3s poll loop, virtually. */
async function pollTick() {
  await vi.advanceTimersByTimeAsync(3000);
  flushSync();
}

function texts(): string[] {
  return [...document.querySelectorAll(".entry pre, .entry p")].map(
    (el) => el.textContent ?? "",
  );
}

beforeEach(() => {
  vi.useFakeTimers();
  responses.length = 0;
  cursors.length = 0;
});

describe("the transcript pane", () => {
  test("renders the conversation and polls with its cursor", async () => {
    responses.push({
      entries: [
        { kind: "prompt", text: "ask" },
        { kind: "reply", text: "answer" },
      ],
      total: 2,
      ok: true,
    });
    const done = mountPane();
    await settle();

    expect(texts()).toEqual(["ask", "answer"]);
    expect(cursors[0]).toBe(0);
    done();
  });

  test("a delta appends instead of replacing, from the held cursor", async () => {
    responses.push(
      {
        entries: [{ kind: "prompt", text: "ask" }],
        total: 1,
        ok: true,
      },
      {
        entries: [{ kind: "reply", text: "late answer" }],
        total: 2,
        ok: true,
      },
      { entries: [], total: 2, ok: true },
    );
    const done = mountPane();
    await settle();
    expect(texts()).toEqual(["ask"]);

    await pollTick();
    expect(texts()).toEqual(["ask", "late answer"]);
    // The second poll asked only for what it lacked.
    expect(cursors[1]).toBe(1);

    await pollTick();
    expect(texts()).toEqual(["ask", "late answer"]);
    expect(cursors[2]).toBe(2);
    done();
  });

  test("a server hiccup keeps the conversation on screen", async () => {
    responses.push({
      entries: [{ kind: "prompt", text: "morning of work" }],
      total: 1,
      ok: true,
    });
    const done = mountPane();
    await settle();
    expect(texts()).toEqual(["morning of work"]);

    // The next polls report nothing-to-say. The pane must keep what it
    // has, not blank to the empty-state message.
    responses.length = 0;
    responses.push({ entries: [], total: 0, ok: false });
    await pollTick();
    await pollTick();

    expect(texts()).toEqual(["morning of work"]);
    expect(document.querySelector(".empty")).toBeNull();
    done();
  });

  test("a shrunken transcript resyncs from the start", async () => {
    responses.push(
      {
        entries: [
          { kind: "prompt", text: "old one" },
          { kind: "reply", text: "old two" },
        ],
        total: 2,
        ok: true,
      },
      // The file was replaced: total below what the pane holds.
      { entries: [], total: 0, ok: true },
      // The resync fetch, from zero.
      {
        entries: [{ kind: "prompt", text: "fresh start" }],
        total: 1,
        ok: true,
      },
    );
    const done = mountPane();
    await settle();
    expect(texts()).toEqual(["old one", "old two"]);

    // The shrink arrives on the next poll; the resync happens inside
    // the same tick.
    await pollTick();
    expect(texts()).toEqual(["fresh start"]);
    done();
  });

  test("markup in entry text is text, not markup", async () => {
    responses.push({
      entries: [{ kind: "reply", text: "<img src=x onerror=alert(1)>" }],
      total: 1,
      ok: true,
    });
    const done = mountPane();
    await settle();

    expect(document.querySelector(".transcript img")).toBeNull();
    expect(texts()[0]).toContain("<img");
    done();
  });
});
