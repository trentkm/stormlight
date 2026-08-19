/**
 * The keymap, as one table.
 *
 * The dashboard's keys are its interface, so the browser client binds
 * what the TUI binds where it can. `internal/ui/model.go` is the real
 * canon — the help modal in `modals.go` is a summary and omits several
 * live bindings — and this table follows the code, not the summary.
 *
 * It is not a mirror. Some keys the browser cannot have, some the TUI
 * has that this client has not built, and three (`1`/`2`/`3`, `d`) are
 * for views the TUI does not have at all. Each of those is written
 * down: `unavailable` for impossible, `notYet` for absent.
 *
 * Three consumers read this table and nothing else: the dispatcher that
 * runs the keys, the ⌘K palette that names them, and `?` that lists
 * them. A key that exists in one and not the others is the drift this
 * table exists to prevent.
 */

/** What the keyboard belongs to at this moment. */
export type Focus =
  | "roster" // the page's own keys
  | "terminal" // walked in: the agent gets every keystroke
  | "field"; // a text input has it

export interface Binding {
  /** The action's stable name, used by the palette. */
  id: string;
  /** How the key is written for a human — TUI spelling. */
  keys: string;
  /** What it does, phrased as the TUI's help phrases it. */
  what: string;
  /** Which group it belongs to in `?` and the palette. */
  group: "Navigate" | "Act" | "View" | "Panes";
  /**
   * Whether the binding still fires while walked into a terminal.
   * Only chords a hosted full-screen TUI does not bind can be: the
   * agent has first claim on everything else.
   */
  whileWalkedIn?: boolean;
  /** Shown in the palette; absent means the palette does not offer it. */
  palette?: string;
  /** Needs an agent selected to mean anything. */
  needsAgent?: boolean;
}

/**
 * The bindings, in help-modal order.
 *
 * `event.key` matching happens in match() below; this spelling is for
 * people. Where the browser makes a TUI key impossible, the entry says
 * so in `what` rather than disappearing — a key that silently is not
 * there is worse than one that explains itself.
 */
export const bindings: Binding[] = [
  // Navigate
  {
    id: "pane-left",
    keys: "h",
    what: "move between columns; from the pane, into the terminal",
    group: "Navigate",
  },
  {
    id: "pane-right",
    keys: "l",
    what: "move between columns, the other way",
    group: "Navigate",
  },
  {
    id: "down",
    keys: "j",
    what: "move down in the aimed column; scroll the pane",
    group: "Navigate",
  },
  {
    id: "up",
    keys: "k",
    what: "move up in the aimed column; scroll the pane",
    group: "Navigate",
  },
  { id: "first", keys: "gg", what: "first item, or the top", group: "Navigate" },
  { id: "last", keys: "G", what: "last item, or the bottom", group: "Navigate" },
  {
    id: "walk-in",
    keys: "Enter",
    what: "walk into the agent's terminal",
    group: "Navigate",
    needsAgent: true,
  },
  {
    id: "walk-out",
    keys: "Ctrl-space",
    what: "leave the terminal (and walk in, from outside)",
    group: "Navigate",
    whileWalkedIn: true,
  },
  {
    id: "agents-next",
    keys: "alt+j",
    what: "next agent, from anywhere",
    group: "Navigate",
    whileWalkedIn: true,
    palette: "Next agent",
  },
  {
    id: "agents-previous",
    keys: "alt+k",
    what: "previous agent, from anywhere",
    group: "Navigate",
    whileWalkedIn: true,
    palette: "Previous agent",
  },
  {
    id: "queue-next",
    keys: "alt+n",
    what: "next agent waiting on you, oldest first",
    group: "Navigate",
    whileWalkedIn: true,
    palette: "Next agent needing you",
  },
  {
    id: "queue-previous",
    keys: "alt+p",
    what: "previous agent waiting on you",
    group: "Navigate",
    whileWalkedIn: true,
    palette: "Previous agent needing you",
  },

  // Act
  {
    id: "dispatch",
    keys: "n",
    what: "new agent",
    group: "Act",
    palette: "Dispatch a new agent",
  },
  {
    id: "message",
    keys: "i",
    what: "write a reply",
    group: "Act",
    needsAgent: true,
    palette: "Message this agent",
  },
  {
    id: "interrupt",
    keys: "x",
    what: "interrupt the selected agent",
    group: "Act",
    needsAgent: true,
    palette: "Interrupt this agent",
  },
  {
    id: "delete",
    keys: "Ctrl-x then x",
    what: "delete the selected agent",
    group: "Act",
    needsAgent: true,
    palette: "Delete this agent",
  },
  {
    id: "mark-working",
    keys: "m then w",
    what: "mark in progress",
    group: "Act",
    needsAgent: true,
    palette: "Mark in progress",
  },
  {
    id: "mark-attention",
    keys: "m then a",
    what: "mark needs attention",
    group: "Act",
    needsAgent: true,
    palette: "Mark needs attention",
  },
  {
    id: "mark-clear",
    keys: "m then c",
    what: "clear the mark",
    group: "Act",
    needsAgent: true,
    palette: "Clear the mark",
  },
  {
    id: "seen",
    keys: "M",
    what: "mark seen",
    group: "Act",
    needsAgent: true,
    palette: "Mark this agent seen",
  },

  // View
  {
    id: "view-roster",
    keys: "1",
    what: "the roster",
    group: "View",
    palette: "Show the roster",
  },
  {
    id: "view-wall",
    keys: "2",
    what: "the wall",
    group: "View",
    palette: "Show the wall",
  },
  {
    id: "view-canvas",
    keys: "3",
    what: "the canvas",
    group: "View",
    palette: "Show the canvas",
  },
  {
    id: "pane-terminal",
    keys: "t",
    what: "the terminal tab",
    group: "Panes",
    needsAgent: true,
    palette: "Show the terminal",
  },
  {
    id: "pane-transcript",
    keys: "T",
    what: "the transcript tab",
    group: "Panes",
    needsAgent: true,
    palette: "Show the transcript",
  },
  {
    id: "pane-diff",
    keys: "d",
    what: "the diff tab",
    group: "Panes",
    needsAgent: true,
    palette: "Show the diff",
  },
  {
    id: "zoom",
    keys: "alt+z",
    what: "zoom the terminal over the body, and walk into it",
    group: "View",
    needsAgent: true,
    whileWalkedIn: true,
    palette: "Zoom the terminal",
  },
  {
    id: "palette",
    keys: "⌘K / Ctrl-Alt-K",
    what: "the command palette",
    group: "View",
    whileWalkedIn: true,
    palette: undefined,
  },
  { id: "help", keys: "?", what: "this list", group: "View" },
];

/**
 * TUI keys with no browser equivalent, named rather than dropped.
 * Someone with the muscle memory deserves to be told why a key does
 * nothing here instead of being left to wonder.
 */
export const unavailable: Array<{ keys: string; why: string }> = [
  { keys: "q", why: "a browser tab is closed by the browser, not the page" },
  {
    keys: "Ctrl-q",
    why: "reserved by some browsers; walk out with Ctrl-space",
  },
  {
    keys: "Ctrl-f / Ctrl-b",
    why: "Ctrl-f is the browser's find; use j/k or the scrollbar",
  },
  {
    keys: "Ctrl-v image paste",
    why: "the terminal socket takes text; drop images into the agent's own terminal",
  },
  { keys: "F", why: "full-screen attach is a terminal-only idea" },
  {
    keys: "Tab",
    why: "it moves focus through the page; taking it would leave a keyboard user with no way to reach anything. h and l step the columns instead",
  },
];

/**
 * TUI keys this client has not built yet. Distinct from `unavailable`:
 * these are possible in a browser and simply are not here, and someone
 * with the muscle memory is owed the difference between "cannot" and
 * "not yet".
 */
/**
 * Keys that belong to the message box while it has the keyboard. They
 * are not in `bindings` because the page deliberately hears nothing
 * while you are typing — but a way out that is documented nowhere is
 * a trap, and the hint beside the box is only readable once you are
 * already in it.
 */
export const inTheBox: Array<{ keys: string; what: string }> = [
  { keys: "Enter", what: "send the message" },
  { keys: "Esc", what: "leave the box, keeping what you typed" },
  { keys: "Backspace", what: "on an empty line, leave the box" },
  { keys: "Ctrl-space", what: "leave the box" },
];

export const notYet: Array<{ keys: string; what: string }> = [
  { keys: "R", what: "rename an agent or workspace" },
  { keys: "n on workspaces", what: "add a workspace" },
  { keys: "H", what: "session history, and resuming from it" },
  { keys: "/ then n/N", what: "search the transcript" },
  { keys: ", then a/n/c", what: "sort the roster" },
  { keys: "K", what: "workspace info" },
  { keys: "o", what: "new agent with a directory picker" },
  { keys: "< / >", what: "resize the focused pane" },
];

/**
 * Where the keyboard is, from what actually has focus.
 *
 * The subtlety that cost a bug: xterm's own keyboard is a hidden
 * <textarea>, so "the active element is a text field" is true of a
 * walked-in terminal — and treating it as a field silences the very
 * chords that exist to work from inside one, the way out among them.
 * The terminal is asked about first, and it answers for itself.
 *
 * It answers to [data-walk-target] rather than to `.terminal`, which
 * xterm puts on its own container and which therefore names every
 * watching wall cell as well as the pane. Both spellings happen to
 * give the same answer here, since a watching cell is not a field
 * either — the distinction is load-bearing in reconcileFocus, which
 * decides *which* terminal receives the keyboard.
 */
export function focusOf(active: Element | null, walkedIn: boolean): Focus {
  const here: Focus = walkedIn ? "terminal" : "roster";
  if (active instanceof HTMLElement && active.closest("[data-walk-target]")) {
    return here;
  }
  // A watching cell's terminal is a picture, not a keyboard: xterm gives
  // it a hidden textarea like any other, but nothing is wired to it, so
  // calling it a field would suppress the page's keys in favour of
  // somewhere they cannot go.
  if (active instanceof HTMLElement && active.closest(".xterm")) return here;
  if (
    active instanceof HTMLInputElement ||
    active instanceof HTMLTextAreaElement ||
    (active instanceof HTMLElement && active.isContentEditable)
  ) {
    return "field";
  }
  return here;
}

/**
 * Chords the page must never take from the browser.
 *
 * Three exceptions, each deliberate. Ctrl-space is the way out of a
 * terminal — no browser binds it, and no full-screen TUI does either,
 * which is why the dashboard chose it. Ctrl-x is the delete prefix; it
 * is the system cut only with a selection, and the dashboard has none
 * to cut. ⌘K is answered before this function is ever reached.
 */
function browserOwned(event: KeyboardEvent): boolean {
  if (!event.metaKey && !event.ctrlKey) return false;
  if (event.metaKey) return true;
  if (event.code === "Space") return false;
  if (event.code === "KeyX") return false;
  return true;
}

/**
 * The binding a key event means, or undefined.
 *
 * `pending` carries a prefix already typed — "g" waiting for its second
 * "g", or Ctrl-x waiting for the "x" that confirms a delete — because
 * those are two-key sequences in the TUI and have to stay two-key here.
 */
export function match(
  event: KeyboardEvent,
  focus: Focus,
  pending: string,
): { id: string; pending?: string } | undefined {
  // The palette answers first and from anywhere — including a text
  // field, since needing to leave the box you are typing in to reach
  // the thing that takes you elsewhere is the wrong way round.
  //
  // ⌘K always; Ctrl-K only when the agent is not listening. Ctrl-K is
  // readline's kill-to-end-of-line, bound by every shell and by the
  // dashboard's own composer, and a walked-in terminal has first claim
  // on it.
  if (event.code === "KeyK" && event.metaKey) return { id: "palette" };
  // Ctrl-Alt-K is the way in from a terminal on the platforms where
  // there is no ⌘: Super never reaches the page, and plain Ctrl-K
  // belongs to the agent. (Emacs binds C-M-k to kill-sexp; an agent
  // running emacs in its terminal loses that one.)
  //
  // AltGr is reported as ctrl+alt on exactly those platforms, so the
  // chord has to say it does not mean AltGr — otherwise a layout where
  // AltGr+K types a character could not type it at all, in the
  // composer or in the agent.
  if (
    event.code === "KeyK" &&
    event.ctrlKey &&
    event.altKey &&
    !event.getModifierState?.("AltGraph")
  ) {
    return { id: "palette" };
  }
  // Ctrl-K is readline's kill-to-end-of-line. A terminal's agent has
  // first claim on it, and so does a text field — macOS implements it
  // in every input, and the dashboard's own composer is one.
  if (event.code === "KeyK" && event.ctrlKey && focus === "roster") {
    return { id: "palette" };
  }
  // The seam key, symmetric the way the TUI's is: out from inside, in
  // from outside — and ahead of the field guard, because "the way out"
  // has to mean out of the message box too, not only out of a
  // terminal.
  if (event.ctrlKey && event.code === "Space") {
    return { id: focus === "terminal" ? "walk-out" : "walk-in" };
  }
  if (focus === "field") return undefined;
  if (browserOwned(event)) return undefined;

  if (event.altKey) {
    // Keyed on physical position, not on the character produced.
    // macOS composes Option chords — Option+J is "∆", Option+N a dead
    // key — so matching event.key leaves every one of these inert on
    // the platform this is written on, and types the glyph into the
    // agent instead.
    const alt: Record<string, string> = {
      KeyJ: "agents-next",
      ArrowDown: "agents-next",
      KeyK: "agents-previous",
      ArrowUp: "agents-previous",
      KeyN: "queue-next",
      KeyP: "queue-previous",
      KeyZ: "zoom",
    };
    const id = alt[event.code];
    return id ? { id } : undefined;
  }

  // Everything below is the page's own keyboard, which a walked-in
  // terminal has taken.
  if (focus === "terminal") return undefined;

  if (pending === "g") {
    return event.key === "g" ? { id: "first" } : { id: "" };
  }
  if (pending === "ctrl-x") {
    return event.key === "x" ? { id: "delete" } : { id: "" };
  }
  if (pending === "m") {
    // The TUI's mark mode: w in progress, a needs attention, c clear.
    const marks: Record<string, string> = {
      w: "mark-working",
      a: "mark-attention",
      c: "mark-clear",
    };
    return { id: marks[event.key] ?? "" };
  }
  if (event.ctrlKey && event.key === "x") return { id: "", pending: "ctrl-x" };

  switch (event.key) {
    case "h":
      return { id: "pane-left" };
    case "l":
      return { id: "pane-right" };
    case "j":
      return { id: "down" };
    case "k":
      return { id: "up" };
    case "g":
      return { id: "", pending: "g" };
    case "G":
      return { id: "last" };
    case "Enter":
      return { id: "walk-in" };
    case "n":
      return { id: "dispatch" };
    case "i":
      return { id: "message" };
    case "x":
      return { id: "interrupt" };
    case "m":
      return { id: "", pending: "m" };
    case "M":
      return { id: "seen" };
    case "1":
      return { id: "view-roster" };
    case "2":
      return { id: "view-wall" };
    case "3":
      return { id: "view-canvas" };
    case "t":
      return { id: "pane-terminal" };
    case "T":
      return { id: "pane-transcript" };
    case "d":
      return { id: "pane-diff" };
    case "?":
      return { id: "help" };
    default:
      return undefined;
  }
}

/** Fuzzy subsequence match, the way a palette is expected to behave:
 *  "fxerr" finds "fix error messages". Returns a score where lower is
 *  a tighter match, or null for no match at all. */
export function fuzzy(query: string, candidate: string): number | null {
  if (query === "") return 0;
  const needle = query.toLowerCase();
  const hay = candidate.toLowerCase();
  let at = 0;
  let score = 0;
  let previous = -1;
  for (const character of needle) {
    const found = hay.indexOf(character, at);
    if (found === -1) return null;
    // Adjacent characters score better than scattered ones, and an
    // early match beats a late one.
    score += found - (previous + 1) + (previous === -1 ? found : 0);
    previous = found;
    at = found + 1;
  }
  return score;
}
