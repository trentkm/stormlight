import { api, roster } from "./api";
import type { Agent, Provider, Workspace } from "./types";

/**
 * The roster, live. One event socket serves the whole page — the server
 * pushes the entire roster whenever it changes, so there is nothing to
 * poll and nothing to merge. The catalog beside it is asked for on a
 * timer instead: it is a request rather than a stream, and a workspace
 * on a machine the server was still reaching arrives in a later answer
 * than the first.
 */
export const fleet = $state({
  agents: [] as Agent[],
  workspaces: [] as Workspace[],
  providers: [] as Provider[],
  selectedID: "",
  workspaceID: "",
  error: "",
  /** Set when the server stopped accepting this tab's token. */
  lost: "",
});

/**
 * How often the workspace catalog is re-read.
 *
 * The server does not wait on another machine to answer a listing: a
 * workspace on a host it is still reaching is simply absent from the
 * first response and present in a later one. Asking once at start-up
 * would therefore leave a remote workspace missing until a reload. The
 * catalog is small and, since nothing in it blocks on SSH any more,
 * cheap to ask for.
 */
const catalogInterval = 5000;

export function start(): () => void {
  void refreshCatalog();
  const catalog = setInterval(() => void refreshCatalog(), catalogInterval);
  const stopRoster = roster(receiveRoster, (reason) => {
    fleet.lost = reason;
  });
  return () => {
    clearInterval(catalog);
    stopRoster();
  };
}

/**
 * An agent this tab launched and has not yet seen in a roster.
 *
 * The server answers a dispatch before the roster it pushes has caught
 * up — the hub polls, and a poll already under way when the agent was
 * created lists the fleet without it. Selecting the newcomer from the
 * dispatch response and then applying that push would read the new
 * selection as one that outlived its agent and throw it away. So the
 * launch is remembered, and a roster without it is one that has not
 * arrived yet rather than one that says the agent is gone.
 */
let arriving: { id: string; until: number } | null = null;

/**
 * How long a launched agent is waited for. A roster still without it
 * past this is a launch that did not take, and the selection returns to
 * the ordinary rule.
 */
const arrivalPatience = 10_000;

/**
 * Selects an agent the server just started for this tab, and brings the
 * rail to its workspace so the selection is one you can see — the form
 * can dispatch into a workspace the rail is not showing. "All agents"
 * is left alone; it already shows everyone.
 */
export function selectLaunched(agent: Agent): void {
  fleet.selectedID = agent.id;
  const home = agent.workspace?.id ?? "";
  if (fleet.workspaceID !== "" && home !== fleet.workspaceID) {
    fleet.workspaceID = home;
  }
  arriving = { id: agent.id, until: Date.now() + arrivalPatience };
}

/** Takes a pushed roster and keeps the selection honest against it. */
export function receiveRoster(agents: Agent[]): void {
  fleet.agents = agents;
  const present = agents.some((a) => a.id === fleet.selectedID);
  // The launch is over once its agent shows up, once the selection has
  // gone elsewhere, or once it has been waited for long enough.
  if (
    arriving &&
    (present || arriving.id !== fleet.selectedID || Date.now() > arriving.until)
  ) {
    arriving = null;
  }
  // A selection that outlived its agent — deleted here or elsewhere —
  // is not a selection. One the roster has not caught up to yet is.
  if (fleet.selectedID && !present && !arriving) {
    fleet.selectedID = "";
  }
  if (!fleet.selectedID && agents.length > 0) {
    fleet.selectedID = agents[0].id;
  }
}

export async function refreshCatalog(): Promise<void> {
  try {
    const [workspaces, providers] = await Promise.all([
      api.workspaces(),
      api.providers(),
    ]);
    fleet.workspaces = workspaces;
    fleet.providers = providers;
  } catch (error) {
    fleet.error = String(error);
  }
}

/** Workspaces the catalog knows, plus any an agent is running in. */
export function workspaceList(): Workspace[] {
  const byID = new Map(fleet.workspaces.map((w) => [w.id, w]));
  for (const agent of fleet.agents) {
    if (agent.workspace?.id && !byID.has(agent.workspace.id)) {
      byID.set(agent.workspace.id, agent.workspace);
    }
  }
  return [...byID.values()].sort((a, b) => a.name.localeCompare(b.name));
}

export function agentsIn(workspaceID: string): Agent[] {
  if (!workspaceID) return fleet.agents;
  return fleet.agents.filter((agent) => agent.workspace?.id === workspaceID);
}

export function selected(): Agent | undefined {
  return fleet.agents.find((agent) => agent.id === fleet.selectedID);
}

/** Runs an action and surfaces its failure rather than swallowing it. */
export async function act(work: () => Promise<unknown>): Promise<void> {
  try {
    fleet.error = "";
    await work();
  } catch (error) {
    fleet.error = String(error);
  }
}
