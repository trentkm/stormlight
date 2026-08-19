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
  // Walking to an agent that needs you is walking to its terminal:
  // that is where the answer gets typed.
  ui.view = "roster";
}

/** Runs the command an id names. Unknown ids are ignored rather than
 *  thrown: the keymap and the palette are the only callers, and a typo
 *  there should surface as a key that does nothing, not a broken page. */
export function run(id: string, argument?: string): void {
  switch (id) {
    // Movement
    case "down":
      step(1);
      return;
    case "up":
      step(-1);
      return;
    case "first": {
      const list = visible();
      if (list.length > 0) fleet.selectedID = list[0].id;
      return;
    }
    case "last": {
      const list = visible();
      if (list.length > 0) fleet.selectedID = list[list.length - 1].id;
      return;
    }
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
      ui.walkedIn = false;
      return;
    case "pane-right":
      ui.view = "roster";
      return;

    // The terminal
    case "walk-in":
      if (fleet.selectedID === "") return;
      ui.view = "roster";
      ui.pane = "terminal";
      ui.walkedIn = true;
      return;
    case "walk-out":
      ui.walkedIn = false;
      return;
    case "zoom":
      ui.zoomed = !ui.zoomed;
      return;

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
    case "pane-terminal":
      ui.pane = "terminal";
      return;
    case "pane-transcript":
      ui.pane = "transcript";
      ui.walkedIn = false;
      return;
    case "pane-diff":
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
      }
      return;
    case "select-workspace":
      if (argument !== undefined) {
        fleet.workspaceID = argument;
        ui.view = "roster";
        // A workspace with agents selects its first, so the pane beside
        // the roster is not left showing an agent from somewhere else.
        const list = visible();
        fleet.selectedID = list.length > 0 ? list[0].id : "";
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
