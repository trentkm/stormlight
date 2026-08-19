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

/**
 * A key event as a real browser delivers it.
 *
 * The detail that matters: on macOS, holding Option composes the
 * character, so Option+J arrives as key "∆" with code "KeyJ". A double
 * that reports key "j" for an alt chord describes a browser that does
 * not exist, and hides every bug in code that reads `key`.
 */
const composed: Record<string, string> = {
  j: "∆",
  k: "˚",
  n: "˜",
  p: "π",
  z: "Ω",
  t: "†",
};

function press(
  key: string,
  modifiers: {
    alt?: boolean;
    ctrl?: boolean;
    meta?: boolean;
    code?: string;
  } = {},
): KeyboardEvent {
  const alt = modifiers.alt ?? false;
  return {
    key: alt ? (composed[key] ?? key) : key,
    code: modifiers.code ?? `Key${key.toUpperCase()}`,
    altKey: alt,
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

  test("the seam key is a toggle, as in the TUI", () => {
    expect(meaning(" ", "terminal", { ctrl: true, code: "Space" })).toBe(
      "walk-out",
    );
    expect(meaning(" ", "roster", { ctrl: true, code: "Space" })).toBe(
      "walk-in",
    );
  });

  // Ctrl-K is readline's kill-to-end-of-line. The agent has first claim
  // on it; only ⌘K is the page's from inside a terminal.
  test("Ctrl-K belongs to whoever is typing, ⌘K to the page", () => {
    // The agent's, inside a terminal; the field's, inside a field —
    // both implement it as kill-to-end-of-line. Only the page's own
    // keyboard has it spare.
    expect(meaning("k", "terminal", { ctrl: true })).toBeUndefined();
    expect(meaning("k", "field", { ctrl: true })).toBeUndefined();
    expect(meaning("k", "roster", { ctrl: true })).toBe("palette");
    expect(meaning("k", "terminal", { meta: true })).toBe("palette");
    expect(meaning("k", "field", { meta: true })).toBe("palette");
  });

  test("still lets the alt chords through", () => {
    expect(meaning("j", "terminal", { alt: true })).toBe("agents-next");
    expect(meaning("k", "terminal", { alt: true })).toBe("agents-previous");
    expect(meaning("n", "terminal", { alt: true })).toBe("queue-next");
    expect(meaning("p", "terminal", { alt: true })).toBe("queue-previous");
    expect(meaning("z", "terminal", { alt: true })).toBe("zoom");
  });

  // The old version of this test compared strings in the table against
  // other strings in the table, so it could not see a chord that fired
  // while walked in without being declared — which is exactly what ⌘K
  // was doing. This one asks match() what actually gets through.
  test("nothing reaches the page from a terminal but the declared chords", () => {
    const declared = new Set(
      bindings.filter((binding) => binding.whileWalkedIn).map((b) => b.id),
    );
    const escapes: string[] = [];
    const letters = "abcdefghijklmnopqrstuvwxyz".split("");
    const others = ["Enter", "Escape", "Tab", "Backspace", "/", "?", "1"];
    for (const key of [...letters, ...others]) {
      for (const modifiers of [
        {},
        { alt: true },
        { ctrl: true },
        { meta: true },
        { ctrl: true, alt: true },
      ]) {
        const found = match(press(key, modifiers), "terminal", "");
        if (!found || found.id === "") continue;
        if (!declared.has(found.id)) {
          escapes.push(`${JSON.stringify(modifiers)}+${key} → ${found.id}`);
        }
      }
    }
    expect(escapes).toEqual([]);
  });

  test("every declared chord is one a full-screen TUI leaves alone", () => {
    for (const binding of bindings.filter((b) => b.whileWalkedIn)) {
      const chord = binding.keys;
      const survivable =
        chord.startsWith("alt+") || chord === "Ctrl-space" || chord === "⌘K";
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
    // The alt chords are matched by physical key, so a composed macOS
    // character still reaches the right command.
    expect(meaning("j", "terminal", { alt: true })).toBe("agents-next");
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
          // The seam key is a toggle: from the roster it walks in, from
          // a terminal it walks out. Both are the same binding.
          return (
            meaning(" ", "terminal", { ctrl: true, code: "Space" }) !==
            binding.id
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
