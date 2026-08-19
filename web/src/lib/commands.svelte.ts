import { api } from "./api";
import { act, agentsIn, fleet, workspaceList } from "./state.svelte";
import { isUrgent, type Agent } from "./types";

/**
 * What the keys and the palette both do.
 *
 * The dispatcher is one function over one enum of ids, so a key and a
 * palette entry with the same id cannot diverge in behaviour — the drift
 * the keymap table exists to prevent, kept honest on the acting side too.
 */

/** Everything the page owns that a command might move. */
export const ui = $state({
  /** Which of the three top-level views is showing. */
  view: "roster" as "roster" | "wall" | "canvas",
  /**
   * Which column the keyboard is aimed at, the way the TUI's three
   * panes work: h and l step between them, and j/k act on whichever
   * one is active — the workspace list, the roster, or the pane beside
   * it. Without this, h and l had nothing to move and j/k could only
   * ever mean "next agent".
   */
  column: "agents" as "workspaces" | "agents" | "spanreed",
  /** Which tab of the agent pane. */
  pane: "terminal" as "terminal" | "transcript" | "diff",
  /** Walked into the terminal: the agent has the keyboard. */
  walkedIn: false,
  /** The terminal fills the body. */
  zoomed: false,
  /** Overlays. */
  palette: false,
  keys: false,
  dispatching: false,
  /** The agent Ctrl-x then x is asking to delete, or "". The TUI shows
   *  a confirmation naming its victim; a browser that deleted an agent
   *  on an invisible two-key sequence would be worse, not terser. */
  confirmDelete: "",
  /** Focus the message box on next render. */
  composing: false,
  /** A two-key sequence in progress: "g" or "ctrl-x". */
  pending: "",
});

/**
 * Keeps the walked-in claim honest against where focus actually is.
 *
 * Two ways they drift, both found by review rather than by reasoning:
 * the terminal's host has padding, and a click on that band blurs the
 * agent's textarea to nothing at all — no focusin fires anywhere, so
 * only watching focus *leave* catches it. And an overlay that focuses
 * its own input is not the person walking out; the walk survives ⌘K
 * and resumes when the overlay closes.
 *
 * Called with whatever holds focus after the event has settled.
 */
export function reconcileFocus(active: Element | null): void {
  if (!ui.walkedIn) return;
  // An overlay's own focus is the overlay's business; the walk waits.
  if (active instanceof HTMLElement && active.closest("dialog")) return;
  // The hook, not `.terminal`: a watching wall cell carries xterm's own
  // class, and reading one as the walk would keep the walk alive over a
  // terminal that cannot hear it. Unreachable today — walking in forces
  // the roster view — but the cost of being right is one selector.
  if (active instanceof HTMLElement && active.closest("[data-walk-target]")) {
    return;
  }

  // Focus landed on something real outside the terminal — a roster row,
  // the composer. That is walking out, and the click meant it.
  const onSomething =
    active instanceof HTMLElement &&
    active !== document.body &&
    active !== document.documentElement;
  if (onSomething) {
    ui.walkedIn = false;
    return;
  }

  // Focus fell to nothing: the terminal host's padding swallowed a
  // click, or an overlay closed and handed focus back to no one.
  // Walked in means the terminal holds the keyboard, so give it back
  // rather than leaving a page that answers to neither.
  // The pane's own terminal, named by its hook — never `.terminal`,
  // which xterm also emits and which a wall cell would answer to first
  // in document order.
  const helper = document.querySelector<HTMLElement>(
    "[data-walk-target] textarea",
  );
  if (helper) helper.focus();
  else ui.walkedIn = false;
}

/** The columns, left to right, as the TUI orders its panes. */
const columns = ["workspaces", "agents", "spanreed"] as const;

/**
 * h and l step between columns and stop at the ends — the TUI clamps
 * rather than wrapping, so a person leaning on h lands on the
 * workspaces and stays there.
 */
function stepColumn(by: number): void {
  // A view without these columns has nothing to step through, and
  // walking in means the keyboard belongs to the agent anyway.
  if (ui.view !== "roster") return;
  // Zoomed, the rail and the roster are not on screen; stepping onto
  // one would move a cursor nobody can see.
  if (ui.zoomed) return;
  const at = columns.indexOf(ui.column);
  const next = Math.min(columns.length - 1, Math.max(0, at + by));
  ui.column = columns[next];
  // Leaving the pane lets go of the terminal; arriving is not the same
  // as walking in, which is Enter's job.
  if (ui.column !== "spanreed") ui.walkedIn = false;
}

/** j and k, meaning whatever the active column says they mean. */
function stepInColumn(by: number): void {
  if (ui.view !== "roster") {
    step(by);
    return;
  }
  switch (ui.column) {
    case "workspaces":
      stepWorkspace(by);
      return;
    case "spanreed":
      scrollPane(by);
      return;
    default:
      step(by);
  }
}

function toEnd(which: "first" | "last"): void {
  if (ui.view === "roster" && ui.column === "spanreed") {
    const scroller = paneScroller();
    if (scroller) {
      scroller.scrollTop = which === "first" ? 0 : scroller.scrollHeight;
      return;
    }
    // Nothing to scroll: fall through to the roster rather than
    // swallowing the key.
  }
  if (ui.view === "roster" && ui.column === "workspaces") {
    const list = ["", ...workspaceList().map((workspace) => workspace.id)];
    selectWorkspace(which === "first" ? list[0] : list[list.length - 1]);
    return;
  }
  const list = visible();
  if (list.length === 0) return;
  fleet.selectedID =
    which === "first" ? list[0].id : list[list.length - 1].id;
}

/** The workspace rail's own cursor: "" is the All agents row. */
function stepWorkspace(by: number): void {
  const ids = ["", ...workspaceList().map((workspace) => workspace.id)];
  const at = ids.indexOf(fleet.workspaceID);
  const next = Math.min(ids.length - 1, Math.max(0, (at === -1 ? 0 : at) + by));
  selectWorkspace(ids[next]);
}

function selectWorkspace(id: string): void {
  fleet.workspaceID = id;
  // The TUI resets the agent cursor when the workspace moves, so the
  // pane beside the roster never shows an agent from somewhere else.
  const list = visible();
  fleet.selectedID = list.length > 0 ? list[0].id : "";
}

/**
 * Whatever the aimed pane scrolls.
 *
 * Asked by tab, not by selector list. querySelector with a list returns
 * the first match in *document order*, and Spanreed keeps the terminal
 * mounted ahead of the transcript and the diff so switching tabs does
 * not churn the attachment — so a list quietly scrolled the hidden
 * terminal whenever the transcript or diff was on screen. The tab is
 * the thing that actually knows.
 */
function paneScroller(): HTMLElement | null {
  const where =
    ui.pane === "transcript"
      ? ".transcript"
      : ui.pane === "diff"
        ? ".diff"
        : "[data-walk-target] .xterm-viewport";
  return document.querySelector<HTMLElement>(where);
}

/** j/k in the aimed pane scrolls it. */
function scrollPane(by: number): void {
  const scroller = paneScroller();
  // No scroller at all — an agent with nothing open yet. Moving the
  // roster is better than doing nothing, which is what a person
  // pressing j is asking for either way.
  if (!scroller) {
    step(by);
    return;
  }
  scroller.scrollBy({ top: by * 80 });
}

/** The agents the roster is showing, in the order it shows them. */
function visible(): Agent[] {
  return agentsIn(fleet.workspaceID);
}

function indexOfSelected(list: Agent[]): number {
  return list.findIndex((agent) => agent.id === fleet.selectedID);
}

function step(by: number): void {
  const list = visible();
  if (list.length === 0) return;
  const at = indexOfSelected(list);
  const next = at === -1 ? 0 : (at + by + list.length) % list.length;
  fleet.selectedID = list[next].id;
}

/**
 * The attention queue: agents blocked on a person, oldest first. This
 * is the ordering the TUI's alt+n/alt+p walk, and the reason it exists
 * — the person wants the one that has been waiting longest, not the one
 * that happens to be next in the roster.
 */
function queue(): Agent[] {
  return fleet.agents
    .filter(isUrgent)
    .sort((a, b) =>
      (a.attention_at ?? a.created_at).localeCompare(
        b.attention_at ?? b.created_at,
      ),
    );
}

function stepQueue(by: number): void {
  const waiting = queue();
  if (waiting.length === 0) return;
  const at = waiting.findIndex((agent) => agent.id === fleet.selectedID);
  const next = at === -1 ? 0 : (at + by + waiting.length) % waiting.length;
  fleet.selectedID = waiting[next].id;
  // The roster, because that is where the answer gets typed — but not
  // walked in: the TUI's queue keys move the cursor and leave walking
  // in to Enter.
  ui.view = "roster";
}

/** Runs the command an id names. Unknown ids are ignored rather than
 *  thrown: the keymap and the palette are the only callers, and a typo
 *  there should surface as a key that does nothing, not a broken page. */
export function run(id: string, argument?: string): void {
  switch (id) {
    // Movement, aimed at whichever column has the keyboard.
    case "down":
      stepInColumn(1);
      return;
    case "up":
      stepInColumn(-1);
      return;
    case "first":
      toEnd("first");
      return;
    case "last":
      toEnd("last");
      return;
    case "agents-next":
      step(1);
      return;
    case "agents-previous":
      step(-1);
      return;
    case "queue-next":
      stepQueue(1);
      return;
    case "queue-previous":
      stepQueue(-1);
      return;
    case "pane-left":
      stepColumn(-1);
      return;
    case "pane-right":
      stepColumn(1);
      return;

    // The terminal
    case "walk-in":
      if (fleet.selectedID === "") return;
      ui.view = "roster";
      ui.column = "spanreed";
      ui.pane = "terminal";
      ui.walkedIn = true;
      return;
    case "walk-out":
      // One step out, landing on the roster with zoom collapsed —
      // exactly what the TUI's seam key does (ptykeys.go). Leaving the
      // keyboard aimed at the pane instead left j and k with nothing to
      // move, which is worse than where they started.
      ui.walkedIn = false;
      ui.zoomed = false;
      ui.column = "agents";
      return;
    case "zoom": {
      // Zoom hides the rail and the roster, so it brings the view that
      // has a terminal and aims the keyboard at what is left —
      // otherwise j moved a cursor nobody could see. And it walks in,
      // because the TUI's zoom does (model.go sets the interaction
      // pane with it): a terminal filling the body while ignoring what
      // you type is the wrong half of the gesture.
      if (fleet.selectedID === "") return;
      const zooming = !(ui.zoomed && ui.view === "roster");
      ui.view = "roster";
      ui.zoomed = zooming;
      if (zooming) {
        ui.column = "spanreed";
        ui.pane = "terminal";
        ui.walkedIn = true;
      }
      return;
    }

    // Views and panes
    case "view-roster":
      ui.view = "roster";
      return;
    case "view-wall":
      ui.view = "wall";
      ui.walkedIn = false;
      return;
    case "view-canvas":
      ui.view = "canvas";
      ui.walkedIn = false;
      return;
    // A pane key brings its view with it — setting a tab only the
    // roster renders, from the wall, changed nothing anyone could see
    // — but only when there is an agent whose pane it would be. The
    // palette already refuses to offer these without one; the key has
    // to refuse too, or it performs a navigation nobody asked for.
    case "pane-terminal":
      if (fleet.selectedID === "") return;
      ui.view = "roster";
      ui.pane = "terminal";
      return;
    case "pane-transcript":
      if (fleet.selectedID === "") return;
      ui.view = "roster";
      ui.pane = "transcript";
      ui.walkedIn = false;
      return;
    case "pane-diff":
      if (fleet.selectedID === "") return;
      ui.view = "roster";
      ui.pane = "diff";
      ui.walkedIn = false;
      return;

    // Overlays
    case "palette":
      ui.palette = !ui.palette;
      return;
    case "help":
      ui.keys = !ui.keys;
      return;
    case "dispatch":
      ui.dispatching = true;
      return;
    case "message":
      if (fleet.selectedID === "") return;
      ui.view = "roster";
      ui.walkedIn = false;
      ui.composing = true;
      return;

    // Agent actions
    case "interrupt":
      if (fleet.selectedID === "") return;
      void act(() => api.interrupt(fleet.selectedID));
      return;
    case "seen":
      if (fleet.selectedID === "") return;
      void act(() => api.clearAttention(fleet.selectedID));
      return;
    case "delete":
      // Asks; the confirmation does the deleting.
      if (fleet.selectedID === "") return;
      ui.confirmDelete = fleet.selectedID;
      return;
    case "delete-confirmed": {
      const id = ui.confirmDelete || fleet.selectedID;
      ui.confirmDelete = "";
      if (id === "") return;
      void act(async () => {
        await api.remove(id);
        if (fleet.selectedID === id) fleet.selectedID = "";
      });
      return;
    }

    // Palette destinations
    case "select-agent":
      if (argument) {
        fleet.selectedID = argument;
        ui.view = "roster";
        ui.column = "agents";
      }
      return;
    case "select-workspace":
      if (argument !== undefined) {
        ui.view = "roster";
        ui.column = "agents";
        selectWorkspace(argument);
      }
      return;

    case "mark-working":
      if (fleet.selectedID === "") return;
      void act(() => api.mark(fleet.selectedID, "working"));
      return;
    case "mark-attention":
      if (fleet.selectedID === "") return;
      void act(() => api.mark(fleet.selectedID, "attention"));
      return;
    case "mark-clear":
      if (fleet.selectedID === "") return;
      void act(() => api.mark(fleet.selectedID, ""));
      return;
    default:
      return;
  }
}

/** The workspaces the palette lists — re-exported so the palette does
 *  not reach past this module for its data. */
export { workspaceList };
