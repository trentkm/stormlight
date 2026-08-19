<script lang="ts">
  import { agentsIn, fleet } from "../lib/state.svelte";
  import { isUrgent } from "../lib/types";
  import WallCell from "./WallCell.svelte";

  let { onopen }: { onopen: () => void } = $props();

  // Handed to every cell as its IntersectionObserver root: this is the
  // element that scrolls, and observing against the viewport instead
  // would make rootMargin inert. $state so cells created before the
  // element binds pick it up when it does.
  let scroller = $state<HTMLDivElement>();

  // Whoever needs a person comes first. The wall's whole claim is that
  // you can find that agent without reading the grid, and sorting is the
  // half of that which survives having twenty cells.
  const agents = $derived(
    [...agentsIn(fleet.workspaceID)].sort((a, b) => {
      if (isUrgent(a) !== isUrgent(b)) return isUrgent(a) ? -1 : 1;
      if (a.process_live !== b.process_live) return a.process_live ? -1 : 1;
      return (a.name || a.task).localeCompare(b.name || b.task);
    }),
  );

  const open = (id: string) => {
    fleet.selectedID = id;
    onopen();
  };
</script>

<div class="wall" bind:this={scroller}>
  {#each agents as agent (agent.id)}
    <WallCell {agent} {scroller} onopen={() => open(agent.id)} />
  {:else}
    <p class="empty">No agents to watch.</p>
  {/each}
</div>

<style>
  .wall {
    flex: 1 1 auto;
    display: grid;
    /* Cells size themselves: a fleet of three should not be three
       postage stamps in the corner of a wide screen. */
    grid-template-columns: repeat(auto-fill, minmax(360px, 1fr));
    grid-auto-rows: minmax(220px, 1fr);
    gap: 12px;
    padding: 12px;
    min-height: 0;
    overflow-y: auto;
  }
  .empty {
    color: var(--muted);
    font-size: 12px;
  }
</style>
