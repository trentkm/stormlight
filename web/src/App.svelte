<script lang="ts">
  import { claimToken } from "./lib/api";
  import { fleet, start } from "./lib/state.svelte";
  import { isUrgent } from "./lib/types";
  import Dispatch from "./components/Dispatch.svelte";
  import Wall from "./components/Wall.svelte";
  import Roster from "./components/Roster.svelte";
  import Spanreed from "./components/Spanreed.svelte";
  import WorkspaceRail from "./components/WorkspaceRail.svelte";

  const authorized = claimToken();
  let dispatching = $state(false);
  let view = $state<"roster" | "wall">("roster");

  $effect(() => {
    if (!authorized) return;
    return start();
  });

  const urgent = $derived(fleet.agents.filter(isUrgent).length);
  const working = $derived(
    fleet.agents.filter((a) => a.process_live && !isUrgent(a)).length,
  );
</script>

{#if !authorized || fleet.lost}
  <main class="gate">
    <h1>Stormlight</h1>
    <p>
      {fleet.lost ||
        "This page needs the token stormlight serve printed. Open the URL it gave you."}
    </p>
  </main>
{:else}
  <div class="app">
    <header class="top">
      <span class="wordmark">Stormlight</span>
      <div class="views">
        <button class:on={view === "roster"} onclick={() => (view = "roster")}>
          roster
        </button>
        <button class:on={view === "wall"} onclick={() => (view = "wall")}>
          wall
        </button>
      </div>
      <button onclick={() => (dispatching = true)}>dispatch</button>
      <span class="spacer"></span>
      {#if urgent}
        <span class="tally waiting">◆ {urgent} needs input</span>
      {/if}
      <span class="tally working">● {working} working</span>
    </header>

    <div class="body">
      <WorkspaceRail />
      {#if view === "wall"}
        <Wall onopen={() => (view = "roster")} />
      {:else}
        <Roster />
        <Spanreed />
      {/if}
    </div>

    {#if fleet.error}
      <p class="error" role="alert">
        {fleet.error}
        <button onclick={() => (fleet.error = "")}>dismiss</button>
      </p>
    {/if}
  </div>

  {#if dispatching}
    <Dispatch onclose={() => (dispatching = false)} />
  {/if}
{/if}

<style>
  .app {
    display: flex;
    flex-direction: column;
    height: 100vh;
  }
  .top {
    display: flex;
    align-items: center;
    gap: 14px;
    height: 46px;
    flex: 0 0 auto;
    padding: 0 14px;
    background: var(--bg-raised);
    border-bottom: 1px solid var(--border);
  }
  .wordmark {
    font-size: 15px;
    font-weight: 700;
    letter-spacing: 0.04em;
    background: linear-gradient(90deg, #7aa2f7, #7dcfff, #c8f7ef);
    -webkit-background-clip: text;
    background-clip: text;
    color: transparent;
  }
  .spacer {
    flex: 1 1 auto;
  }
  .views {
    display: flex;
    gap: 2px;
    padding: 3px;
    background: var(--bg-sunken);
    border: 1px solid var(--border);
    border-radius: 6px;
  }
  .views button {
    padding: 2px 12px;
    border: none;
    color: var(--muted);
  }
  .views button.on {
    background: var(--band);
    color: var(--band-ink);
    font-weight: 500;
  }
  .views button:hover:not(.on) {
    color: var(--band);
  }
  .tally {
    font-size: 12px;
  }
  .tally.waiting {
    color: var(--waiting);
  }
  .tally.working {
    color: var(--working);
  }
  .body {
    display: flex;
    flex: 1 1 auto;
    min-height: 0;
  }
  .error {
    display: flex;
    gap: 10px;
    align-items: center;
    margin: 0;
    padding: 6px 14px;
    background: #552b29;
    color: #f3f5f6;
    font-size: 12px;
  }
  .gate {
    margin: auto;
    max-width: 40ch;
    padding: 40px;
    text-align: center;
    color: var(--muted);
  }
  .gate h1 {
    color: var(--band);
  }
</style>
