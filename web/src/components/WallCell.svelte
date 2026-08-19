<script lang="ts">
  import AgentScreen from "./AgentScreen.svelte";
  import { statusVisual } from "../lib/theme";
  import { isUrgent, type Agent } from "../lib/types";

  let {
    agent,
    scroller,
    onopen,
  }: { agent: Agent; scroller: HTMLElement | undefined; onopen: () => void } =
    $props();

  // The id, held apart from the object it came from. Every roster push
  // re-proxies every agent, so AgentScreen keying on the object would
  // re-attach every cell's terminal at the roster's cadence. A derived
  // string only changes when the id itself does.
  const id = $derived(agent.id);

  let host: HTMLDivElement;
  // Attached until something says otherwise. The observer below turns
  // cells off when they scroll away, but a callback that never arrives —
  // a hidden tab defers them, and Chrome does exactly that — must leave
  // a cell showing its terminal rather than showing nothing. Costly and
  // right beats cheap and blank.
  let visible = $state(true);

  const status = $derived(
    statusVisual(agent.activity, agent.attention, agent.process_live),
  );

  // Only what is on screen stays attached. A wall of thirty agents would
  // otherwise hold thirty daemon attachments and thirty terminals to
  // paint pixels nobody is looking at.
  $effect(() => {
    // The root must be the element that actually scrolls. Against the
    // default (viewport) root, rootMargin inflates a rectangle the wall
    // never leaves, while the .wall scroller clips unenlarged — so cells
    // would detach the instant they crossed its edge and reattach, seed
    // and all, the instant they returned.
    if (!scroller) return;
    const watcher = new IntersectionObserver(
      // The last entry, not the first: deliveries batch, in order, and
      // acting on the oldest would leave a fast flap parked on a stale
      // answer until the next crossing.
      (entries) => (visible = entries[entries.length - 1].isIntersecting),
      { root: scroller, rootMargin: "200px" },
    );
    watcher.observe(host);
    return () => watcher.disconnect();
  });
</script>

<div
  class="cell"
  class:urgent={isUrgent(agent)}
  class:done={!agent.process_live}
  bind:this={host}
>
  <button class="label" onclick={onopen} title={agent.task}>
    <span style:color={isUrgent(agent) ? "#1f2328" : status.color}>{status.glyph}</span>
    <span class="name">{agent.name || agent.task || agent.id.slice(0, 8)}</span>
    <span class="where">{agent.workspace?.name ?? ""}</span>
  </button>
  <AgentScreen {id} {visible} />
  <!-- The whole screen is the way in, not just the label. A watching
       cell takes no keystrokes by design — the shared terminal must not
       hear from a tile — so the click anyone's hand actually makes on
       "their" terminal has to lead somewhere: to the roster, focused on
       this agent, where typing works. -->
  <button class="reach" onclick={onopen} aria-label="Open {agent.name || agent.task || agent.id}"></button>
  {#if isUrgent(agent)}
    <p class="needs">needs input</p>
  {/if}
</div>

<style>
  .cell {
    position: relative;
    display: flex;
    flex-direction: column;
    min-width: 0;
    min-height: 0;
    overflow: hidden;
    background: var(--bg-sunken);
    border: 1px solid var(--border);
    border-radius: 6px;
  }
  .cell:hover {
    border-color: var(--band);
  }
  /* After :hover on purpose: equal specificity, so order keeps the
     urgent band from being repainted by a passing pointer. */
  .cell.urgent {
    border-color: var(--waiting);
    box-shadow: 0 0 14px rgba(229, 192, 123, 0.18);
  }
  .cell.done {
    opacity: 0.72;
  }
  .label {
    display: flex;
    align-items: center;
    gap: 8px;
    flex: 0 0 auto;
    height: 26px;
    padding: 0 10px;
    background: var(--bg-raised);
    border: none;
    border-bottom: 1px solid var(--border);
    color: var(--text);
    font: inherit;
    font-size: 12px;
    text-align: left;
    cursor: pointer;
  }
  .cell.urgent .label {
    background: var(--waiting);
    color: #1f2328;
  }
  .label:hover {
    color: var(--band);
  }
  .cell.urgent .label:hover {
    color: #1f2328;
  }
  .name {
    flex: 1 1 auto;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .where {
    color: var(--muted);
    font-size: 11px;
  }
  .cell.urgent .where {
    color: #4a3e1e;
  }
  .reach {
    position: absolute;
    top: 26px;
    left: 0;
    right: 0;
    bottom: 0;
    padding: 0;
    background: transparent;
    border: none;
    cursor: pointer;
  }
  .needs {
    position: absolute;
    /* Above the .reach overlay in paint order but transparent to the
       pointer: this banner marks the one cell the wall most wants
       clicked, and it must not shield the click it invites. */
    pointer-events: none;
    left: 0;
    right: 0;
    bottom: 0;
    margin: 0;
    padding: 3px 10px;
    background: var(--waiting);
    color: #1f2328;
    font-size: 11px;
    font-weight: 700;
  }
</style>
