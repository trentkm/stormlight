import { api, roster } from "./api";
import type { Agent, Provider, Workspace } from "./types";

/**
 * The roster, live. One event socket serves the whole page — the server
 * pushes the entire roster whenever it changes, so there is nothing to
 * poll and nothing to merge.
 */
export const fleet = $state({
  agents: [] as Agent[],
  workspaces: [] as Workspace[],
  providers: [] as Provider[],
  selectedID: "",
  workspaceID: "",
  error: "",
});

export function start(): () => void {
  void refreshCatalog();
  return roster((agents) => {
    fleet.agents = agents;
    // A selection that outlived its agent — deleted here or elsewhere —
    // is not a selection.
    if (fleet.selectedID && !agents.some((a) => a.id === fleet.selectedID)) {
      fleet.selectedID = "";
    }
    if (!fleet.selectedID && agents.length > 0) {
      fleet.selectedID = agents[0].id;
    }
  });
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
