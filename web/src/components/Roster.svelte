<script lang="ts">
  import { fleet, agentsIn } from "../lib/state.svelte";
  import { statusVisual } from "../lib/theme";
  import { isUrgent, type Agent } from "../lib/types";

  const since = (agent: Agent) => {
    const seconds = (Date.now() - Date.parse(agent.created_at)) / 1000;
    if (seconds < 60) return "now";
    if (seconds < 3600) return `${Math.floor(seconds / 60)}m`;
    return `${Math.floor(seconds / 3600)}h`;
  };

  const detail = (agent: Agent) => {
    const parts = [agent.workspace?.name, agent.provider];
    if (agent.host) parts.push(agent.host);
    if (!agent.process_live) {
      parts.push(agent.exit_code ? `exit ${agent.exit_code}` : "done");
    }
    return parts.filter(Boolean).join(" · ");
  };
</script>

<div class="roster">
  <div class="heading">
    <span>AGENTS</span>
    <span class="tally">{agentsIn(fleet.workspaceID).length}</span>
  </div>
  {#each agentsIn(fleet.workspaceID) as agent (agent.id)}
    {@const status = statusVisual(agent.activity, agent.attention, agent.process_live)}
    <button
      class="row"
      class:selected={agent.id === fleet.selectedID}
      class:urgent={isUrgent(agent)}
      onclick={() => (fleet.selectedID = agent.id)}
    >
      <span class="glyph" style:color={isUrgent(agent) ? "#1F2328" : status.glyph && status.color}>
        {status.glyph}
      </span>
      <span class="body">
        <span class="title">{agent.name || agent.task || agent.id.slice(0, 8)}</span>
        <span class="detail">{detail(agent)}</span>
      </span>
      <span class="age">{since(agent)}</span>
    </button>
  {:else}
    <p class="empty">No agents here yet.</p>
  {/each}
</div>

<style>
  .roster {
    width: 320px;
    flex: 0 0 auto;
    display: flex;
    flex-direction: column;
    border-right: 1px solid var(--border);
    padding: 12px 0;
    overflow-y: auto;
  }
  .heading {
    display: flex;
    gap: 8px;
    align-items: baseline;
    padding: 0 16px 8px;
    font-size: 10px;
    letter-spacing: 0.14em;
    color: var(--muted);
  }
  .tally {
    letter-spacing: 0;
  }
  .row {
    display: flex;
    gap: 10px;
    width: 100%;
    padding: 9px 14px;
    background: none;
    border: none;
    border-left: 2px solid transparent;
    color: var(--text);
    font: inherit;
    text-align: left;
    cursor: pointer;
  }
  .row:hover {
    background: rgba(125, 207, 255, 0.06);
  }
  .row.selected {
    background: rgba(98, 174, 239, 0.16);
    border-left-color: var(--ice);
  }
  /* Urgent outranks selection: an agent blocked on a person is the one
     thing on this screen that should be impossible to scroll past. */
  .row.urgent {
    background: var(--waiting);
    border-left-color: var(--waiting);
    color: #1f2328;
  }
  .row.urgent .detail,
  .row.urgent .age {
    color: #4a3e1e;
  }
  .body {
    display: flex;
    flex-direction: column;
    gap: 3px;
    min-width: 0;
    flex: 1 1 auto;
  }
  .title {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .detail {
    font-size: 11px;
    color: var(--muted);
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .age {
    font-size: 11px;
    color: var(--muted);
  }
  .empty {
    padding: 4px 16px;
    color: var(--muted);
    font-size: 12px;
  }
</style>
