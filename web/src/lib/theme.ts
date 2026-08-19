// The palette lives in app.css, as custom properties under two themes.
// This module is what the parts of the dashboard that cannot use a
// stylesheet reach for: an inline `style:color` naming a variable, and
// the one surface that opts out of theming entirely.

/**
 * The terminal's own colors, in both themes.
 *
 * Provider output is ANSI written for a dark terminal. Repainting it as
 * page chrome would leave an agent's own blues and greens sitting on
 * white, so the terminal keeps its ground whichever way the dashboard
 * around it is dressed. These are literals rather than custom
 * properties because xterm takes a color, not a stylesheet.
 */
export const terminal = {
  background: "#14181D",
  foreground: "#D7DEE5",
  cursor: "#C6E9FF",
  selectionBackground: "#3D4245",
} as const;

/**
 * The glyph and color the roster shows for an agent's state.
 *
 * The color is a `var()` rather than a literal so a status painted
 * inline follows the theme the same way one painted in a stylesheet
 * does — there is one palette, and it is in app.css.
 */
export function statusVisual(
  activity: string,
  attention: string | undefined,
  live: boolean,
): { glyph: string; color: string } {
  if (!live) {
    return activity === "failed"
      ? { glyph: "✗", color: "var(--failed)" }
      : { glyph: "✓", color: "var(--done)" };
  }
  if (attention === "question" || attention === "approval" || attention === "auth") {
    return { glyph: "◆", color: "var(--waiting)" };
  }
  if (attention === "waiting" || activity === "idle") {
    return { glyph: "○", color: "var(--waiting)" };
  }
  return { glyph: "●", color: "var(--working)" };
}
