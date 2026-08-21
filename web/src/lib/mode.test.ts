// @vitest-environment jsdom
import { beforeEach, describe, expect, test, vi } from "vitest";
import { chooseMode, followMode, mode, toggleMode } from "./mode.svelte";

/**
 * The theme's contract, which is mostly about what a reload sees.
 *
 * The boot script in index.html writes data-theme before the first
 * paint; everything here is what happens after it, and the thing worth
 * guarding is that the two never disagree — a module that decided the
 * theme for itself would paint one answer and store another.
 */

// Not jsdom's storage and not Node's: newer Node ships an experimental
// localStorage global that shadows jsdom's and throws on use — the same
// webstorage trap AGENTS.md documents for CI. A ten-line Map is the
// same on every runner.
const stored = new Map<string, string>();
vi.stubGlobal("localStorage", {
  getItem: (key: string) => stored.get(key) ?? null,
  setItem: (key: string, value: string) => stored.set(key, String(value)),
  removeItem: (key: string) => stored.delete(key),
  clear: () => stored.clear(),
});

/** A stand-in for the media query jsdom does not implement. */
function preferring(light: boolean) {
  const listeners: Array<(event: MediaQueryListEvent) => void> = [];
  const query = {
    matches: light,
    addEventListener: (_: string, fn: (event: MediaQueryListEvent) => void) =>
      listeners.push(fn),
    removeEventListener: (_: string, fn: (event: MediaQueryListEvent) => void) =>
      listeners.splice(listeners.indexOf(fn), 1),
  };
  window.matchMedia = (() => query) as unknown as typeof window.matchMedia;
  return {
    /** The system flipping under a dashboard left open. */
    change(nowLight: boolean) {
      query.matches = nowLight;
      for (const fn of [...listeners]) {
        fn({ matches: nowLight } as MediaQueryListEvent);
      }
    },
    get watchers() {
      return listeners.length;
    },
  };
}

beforeEach(() => {
  localStorage.clear();
  delete document.documentElement.dataset.theme;
  mode.current = "dark";
});

describe("the dashboard's theme", () => {
  test("adopts what the boot script painted, not its own guess", () => {
    document.documentElement.dataset.theme = "light";
    preferring(false);
    followMode();
    expect(mode.current).toBe("light");
    // Still light: nothing repaints over the answer already on screen.
    expect(document.documentElement.dataset.theme).toBe("light");
  });

  test("choosing a theme paints it and remembers it", () => {
    preferring(false);
    followMode();
    chooseMode("light");
    expect(mode.current).toBe("light");
    expect(document.documentElement.dataset.theme).toBe("light");
    expect(localStorage.getItem("stormlight.theme")).toBe("light");
    toggleMode();
    expect(document.documentElement.dataset.theme).toBe("dark");
    expect(localStorage.getItem("stormlight.theme")).toBe("dark");
  });

  test("follows the system while nothing has been chosen here", () => {
    document.documentElement.dataset.theme = "dark";
    const system = preferring(false);
    followMode();
    system.change(true);
    expect(mode.current).toBe("light");
    expect(document.documentElement.dataset.theme).toBe("light");
  });

  test("a choice made after the fact outranks the system", () => {
    document.documentElement.dataset.theme = "dark";
    const system = preferring(false);
    followMode();
    chooseMode("dark");
    // The listener is already attached; it has to re-read the choice
    // rather than trust what was stored when it was hooked up.
    system.change(true);
    expect(mode.current).toBe("dark");
    expect(document.documentElement.dataset.theme).toBe("dark");
  });

  test("stops listening when the effect that started it ends", () => {
    const system = preferring(false);
    const stop = followMode();
    expect(system.watchers).toBe(1);
    stop();
    expect(system.watchers).toBe(0);
  });

  test("a theme that cannot be stored still applies", () => {
    preferring(false);
    followMode();
    const setItem = localStorage.setItem;
    localStorage.setItem = () => {
      throw new Error("storage is disabled");
    };
    try {
      expect(() => chooseMode("light")).not.toThrow();
      expect(document.documentElement.dataset.theme).toBe("light");
    } finally {
      localStorage.setItem = setItem;
    }
  });
});
