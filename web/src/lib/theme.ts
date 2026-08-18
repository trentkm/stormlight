// The palette, lifted from internal/theme so the browser and the
// dashboard mean the same thing by a color. Dark values only: the web
// client has no terminal background to adapt to.
export const theme = {
  bg: "#171C20",
  bgRaised: "#1B2126",
  bgSunken: "#14181D",
  bgRail: "#191E23",
  border: "#2A3138",
  borderBright: "#3A4046",

  text: "#D7DEE5",
  textBright: "#F3F5F6",
  muted: "#74818B",

  // Status means exactly what it means in the TUI.
  working: "#61AFEF",
  waiting: "#E5C07B",
  done: "#72C087",
  failed: "#E06C75",

  // The icy band family is selection and focus, never status — keeping
  // them apart is what stops "selected" reading as "working".
  band: "#C6E9FF",
  ice: "#7DCFFF",
  bandInk: "#0F2338",

  code: "#8FDCCB",
} as const;

/** The glyph and color the roster shows for an agent's state. */
export function statusVisual(
  activity: string,
  attention: string | undefined,
  live: boolean,
): { glyph: string; color: string } {
  if (!live) {
    return activity === "failed"
      ? { glyph: "✗", color: theme.failed }
      : { glyph: "✓", color: theme.done };
  }
  if (attention === "question" || attention === "approval" || attention === "auth") {
    return { glyph: "◆", color: theme.waiting };
  }
  if (attention === "waiting" || activity === "idle") {
    return { glyph: "○", color: theme.waiting };
  }
  return { glyph: "●", color: theme.working };
}
