// @vitest-environment jsdom
import { describe, expect, test } from "vitest";
import {
  bindings,
  focusOf,
  fuzzy,
  match,
  unavailable,
  type Focus,
} from "./keys";

/** A key event as the browser would deliver it. */
function press(
  key: string,
  modifiers: {
    alt?: boolean;
    ctrl?: boolean;
    meta?: boolean;
    code?: string;
  } = {},
): KeyboardEvent {
  return {
    key,
    code: modifiers.code ?? `Key${key.toUpperCase()}`,
    altKey: modifiers.alt ?? false,
    ctrlKey: modifiers.ctrl ?? false,
    metaKey: modifiers.meta ?? false,
  } as KeyboardEvent;
}

function meaning(
  key: string,
  focus: Focus = "roster",
  modifiers = {},
  pending = "",
): string | undefined {
  return match(press(key, modifiers), focus, pending)?.id;
}

describe("the roster's keyboard", () => {
  test("moves and acts the way the TUI does", () => {
    expect(meaning("h")).toBe("pane-left");
    expect(meaning("l")).toBe("pane-right");
    expect(meaning("j")).toBe("down");
    expect(meaning("k")).toBe("up");
    expect(meaning("G")).toBe("last");
    expect(meaning("Enter")).toBe("walk-in");
    expect(meaning("n")).toBe("dispatch");
    expect(meaning("i")).toBe("message");
    expect(meaning("x")).toBe("interrupt");
    expect(meaning("M")).toBe("seen");
    expect(meaning("H")).toBe("history");
    expect(meaning("?")).toBe("help");
  });

  test("gg is two keys, not one", () => {
    const first = match(press("g"), "roster", "");
    expect(first?.id).toBe("");
    expect(first?.pending).toBe("g");
    expect(meaning("g", "roster", {}, "g")).toBe("first");
    // Anything else abandons the prefix rather than acting on it.
    expect(meaning("x", "roster", {}, "g")).toBe("");
  });

  test("m is a sequence: the mark is the second key", () => {
    const prefix = match(press("m"), "roster", "");
    expect(prefix?.id).toBe("");
    expect(prefix?.pending).toBe("m");
    expect(meaning("w", "roster", {}, "m")).toBe("mark-working");
    expect(meaning("a", "roster", {}, "m")).toBe("mark-attention");
    expect(meaning("c", "roster", {}, "m")).toBe("mark-clear");
    // Any other key abandons the sequence rather than marking wrongly.
    expect(meaning("j", "roster", {}, "m")).toBe("");
  });

  test("delete needs its Ctrl-x prefix, as in the TUI", () => {
    expect(meaning("x")).toBe("interrupt");
    const prefix = match(press("x", { ctrl: true }), "roster", "");
    expect(prefix?.pending).toBe("ctrl-x");
    expect(meaning("x", "roster", {}, "ctrl-x")).toBe("delete");
    expect(meaning("j", "roster", {}, "ctrl-x")).toBe("");
  });
});

describe("a walked-in terminal", () => {
  // The whole contract: inside, the agent gets the keystrokes. A page
  // that kept even one ordinary letter would corrupt what the person
  // is typing into their agent.
  test("keeps every ordinary key away from the page", () => {
    for (const key of ["h", "j", "k", "n", "i", "x", "G", "Enter", "?", "/"]) {
      expect(meaning(key, "terminal")).toBeUndefined();
    }
  });

  test("still lets the way out through", () => {
    expect(meaning(" ", "terminal", { ctrl: true, code: "Space" })).toBe(
      "walk-out",
    );
  });

  test("still lets the alt chords through", () => {
    expect(meaning("j", "terminal", { alt: true })).toBe("agents-next");
    expect(meaning("k", "terminal", { alt: true })).toBe("agents-previous");
    expect(meaning("n", "terminal", { alt: true })).toBe("queue-next");
    expect(meaning("p", "terminal", { alt: true })).toBe("queue-previous");
    expect(meaning("z", "terminal", { alt: true })).toBe("zoom");
    expect(meaning("t", "terminal", { alt: true })).toBe("pane-transcript");
  });

  test("every binding that fires while walked in is an alt chord or the way out", () => {
    for (const binding of bindings.filter((b) => b.whileWalkedIn)) {
      const chord = binding.keys;
      const survivable = chord.startsWith("alt+") || chord === "Ctrl-space";
      expect(
        survivable,
        `${binding.id} claims to work while walked in with "${chord}", ` +
          `which a hosted full-screen TUI would swallow`,
      ).toBe(true);
    }
  });
});

describe("what belongs to the browser", () => {
  test("chords are left alone", () => {
    for (const key of ["w", "t", "n", "r", "l"]) {
      expect(meaning(key, "roster", { meta: true })).toBeUndefined();
      expect(meaning(key, "roster", { ctrl: true })).toBeUndefined();
    }
  });

  test("except the palette, which the page claims", () => {
    expect(meaning("k", "roster", { meta: true })).toBe("palette");
    expect(meaning("k", "roster", { ctrl: true })).toBe("palette");
    // And from inside a terminal, since that is where you are when you
    // want to go somewhere else.
    expect(meaning("k", "terminal", { meta: true })).toBe("palette");
  });

  test("a text field keeps its own keys", () => {
    for (const key of ["j", "n", "x", "Enter", "?"]) {
      expect(meaning(key, "field")).toBeUndefined();
    }
  });
});

describe("the table as documentation", () => {
  test("every binding is spelled, described, and grouped", () => {
    for (const binding of bindings) {
      expect(binding.keys, binding.id).not.toBe("");
      expect(binding.what, binding.id).not.toBe("");
      expect(binding.group, binding.id).toBeTruthy();
    }
  });

  test("ids are unique — the palette keys off them", () => {
    const ids = bindings.map((binding) => binding.id);
    expect(new Set(ids).size).toBe(ids.length);
  });

  // The point of the table: what `?` lists is what the dispatcher runs.
  // A binding nothing can produce is a promise the client does not keep.
  test("every non-chord binding is reachable from a key press", () => {
    const unreachable = bindings
      .filter(
        (binding) =>
          !["palette", "first", "delete"].includes(binding.id) &&
          !binding.keys.includes(" then "),
      )
      .filter((binding) => {
        const alt = binding.keys.startsWith("alt+");
        const key = alt ? binding.keys.slice(4) : binding.keys;
        if (key === "Ctrl-space") {
          return (
            meaning(" ", "roster", { ctrl: true, code: "Space" }) !== binding.id
          );
        }
        if (key.length > 1 && key !== "Enter") return false;
        return meaning(key, "roster", alt ? { alt: true } : {}) !== binding.id;
      })
      .map((binding) => `${binding.id} (${binding.keys})`);
    expect(unreachable).toEqual([]);
  });

  test("keys the browser made impossible are named, with a reason", () => {
    expect(unavailable.length).toBeGreaterThan(0);
    for (const entry of unavailable) {
      expect(entry.keys).not.toBe("");
      expect(entry.why).not.toBe("");
    }
  });
});

// The bug this exists to prevent: xterm's keyboard is a hidden
// <textarea>, so a naive "is a text field?" test calls a walked-in
// terminal a field — and then Ctrl-space cannot get you out of it.
describe("where the keyboard is", () => {
  function inTerminal(): Element {
    const terminal = document.createElement("div");
    terminal.className = "terminal";
    const helper = document.createElement("textarea");
    terminal.append(helper);
    document.body.append(terminal);
    return helper;
  }

  test("xterm's textarea is the terminal, not a field", () => {
    expect(focusOf(inTerminal(), true)).toBe("terminal");
  });

  test("the same textarea is not the terminal when not walked in", () => {
    expect(focusOf(inTerminal(), false)).toBe("roster");
  });

  test("an ordinary input is a field either way", () => {
    const input = document.createElement("input");
    document.body.append(input);
    expect(focusOf(input, false)).toBe("field");
    expect(focusOf(input, true)).toBe("field");
  });

  test("nothing focused is the page's own keyboard", () => {
    expect(focusOf(null, false)).toBe("roster");
    expect(focusOf(document.body, true)).toBe("terminal");
  });
});

describe("fuzzy matching", () => {
  test("finds a subsequence", () => {
    expect(fuzzy("fxerr", "fix error messages")).not.toBeNull();
    expect(fuzzy("wall", "Show the wall")).not.toBeNull();
    expect(fuzzy("zzz", "fix error messages")).toBeNull();
  });

  test("ranks a tighter match ahead of a scattered one", () => {
    const tight = fuzzy("err", "error")!;
    const scattered = fuzzy("err", "e-x-r-y-r")!;
    expect(tight).toBeLessThan(scattered);
  });

  test("an empty query matches everything equally", () => {
    expect(fuzzy("", "anything")).toBe(0);
  });

  test("matching ignores case in both directions", () => {
    expect(fuzzy("SSH", "ssh remote workspaces")).not.toBeNull();
    expect(fuzzy("ssh", "SSH Remote Workspaces")).not.toBeNull();
  });
});
