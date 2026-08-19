<script lang="ts">
  import { fleet, agentsIn, workspaceList } from "../lib/state.svelte";
  import { isUrgent } from "../lib/types";

  const counts = (workspaceID: string) => {
    const agents = agentsIn(workspaceID);
    return {
      working: agents.filter((a) => a.process_live && !isUrgent(a)).length,
      urgent: agents.filter((a) => a.process_live && isUrgent(a)).length,
    };
  };
</script>

<nav class="rail">
  <div class="heading">WORKSPACES</div>
  <button
    class="row"
    class:selected={fleet.workspaceID === ""}
    onclick={() => (fleet.workspaceID = "")}
  >
    <span class="name">All agents</span>
    <span class="count">{fleet.agents.length}</span>
  </button>
  {#each workspaceList() as workspace (workspace.id)}
    {@const tally = counts(workspace.id)}
    <button
      class="row"
      class:selected={fleet.workspaceID === workspace.id}
      onclick={() => (fleet.workspaceID = workspace.id)}
      title={workspace.root}
    >
      <span class="name">{workspace.name}</span>
      {#if workspace.host}
        <span class="host">{workspace.host}</span>
      {/if}
      {#if tally.working}
        <span class="count" style:color="var(--working)">●{tally.working}</span>
      {/if}
      {#if tally.urgent}
        <span class="count" style:color="var(--waiting)">◆{tally.urgent}</span>
      {/if}
    </button>
  {/each}
</nav>

<style>
  .rail {
    width: 220px;
    flex: 0 0 auto;
    display: flex;
    flex-direction: column;
    background: var(--bg-rail);
    border-right: 1px solid var(--border);
    padding: 12px 0;
    overflow-y: auto;
  }
  .heading {
    padding: 0 16px 8px;
    font-size: 10px;
    letter-spacing: 0.14em;
    color: var(--muted);
  }
  .row {
    display: flex;
    align-items: center;
    gap: 8px;
    width: 100%;
    padding: 7px 14px;
    background: none;
    border: none;
    border-left: 2px solid transparent;
    color: var(--text);
    font: inherit;
    text-align: left;
    cursor: pointer;
  }
  .row:hover {
    background: var(--hover-bg);
  }
  .row.selected {
    background: var(--selected-bg);
    border-left-color: var(--ice);
    color: var(--text-bright);
  }
  .name {
    flex: 1 1 auto;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .host {
    font-size: 11px;
    color: var(--accent);
  }
  .count {
    font-size: 11px;
    color: var(--muted);
  }
</style>
