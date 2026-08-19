/**
 * Which of app.css's two palettes the dashboard is wearing.
 *
 * The choice is applied by the boot script in index.html, synchronously,
 * before the first paint — a module runs deferred, and a theme applied
 * from here would show a frame of the wrong one. This module owns
 * everything after that: reading back what was painted, flipping it,
 * remembering the flip, and following the system while nothing has been
 * chosen here.
 *
 * Nothing touches the DOM or storage at import time. `followMode` is
 * called from an effect, so importing this file does not require a
 * browser to be present.
 */

export type Mode = "dark" | "light";

// localStorage rather than sessionStorage, and unlike the token that is
// the point: a theme is a preference, not a credential, and it should
// outlive the tab that chose it.
const key = "stormlight.theme";

function chosen(): Mode | null {
  try {
    const value = localStorage.getItem(key);
    return value === "dark" || value === "light" ? value : null;
  } catch {
    // No storage here — private browsing, a locked-down embed. The
    // theme still applies; it just will not survive a reload.
    return null;
  }
}

/** What the boot script painted. Reading the document rather than
 *  deciding again keeps one answer to "which theme": the one on screen. */
function painted(): Mode {
  return document.documentElement.dataset.theme === "light" ? "light" : "dark";
}

export const mode = $state({ current: "dark" as Mode });

function paint(next: Mode): void {
  mode.current = next;
  document.documentElement.dataset.theme = next;
}

/** Chooses a theme and remembers the choice. */
export function chooseMode(next: Mode): void {
  paint(next);
  try {
    localStorage.setItem(key, next);
  } catch {
    // As above: it applies, it just does not persist.
  }
}

export function toggleMode(): void {
  chooseMode(mode.current === "dark" ? "light" : "dark");
}

/**
 * Adopts the painted theme and tracks the system's while no choice has
 * been made here — a dashboard left open past sunset should follow the
 * machine it is on. Returns its own teardown, for an effect to hold.
 */
export function followMode(): () => void {
  mode.current = painted();
  const query = window.matchMedia("(prefers-color-scheme: light)");
  // `chosen()` is re-read per event rather than captured: a choice made
  // after this listener was attached has to stop the system overriding
  // it, and there is only ever one listener to attach.
  const follow = (event: MediaQueryListEvent) => {
    if (!chosen()) paint(event.matches ? "light" : "dark");
  };
  query.addEventListener("change", follow);
  return () => query.removeEventListener("change", follow);
}
