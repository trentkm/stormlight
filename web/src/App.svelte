<script lang="ts">
  import { claimToken } from "./lib/api";
  import { run, ui } from "./lib/commands.svelte";
  import { focusOf, match } from "./lib/keys";
  import { fleet, start } from "./lib/state.svelte";
  import { isUrgent } from "./lib/types";
  import Canvas from "./components/Canvas.svelte";
  import Dispatch from "./components/Dispatch.svelte";
  import Keys from "./components/Keys.svelte";
  import Palette from "./components/Palette.svelte";
  import Wall from "./components/Wall.svelte";
  import Roster from "./components/Roster.svelte";
  import Spanreed from "./components/Spanreed.svelte";
  import WorkspaceRail from "./components/WorkspaceRail.svelte";

  const authorized = claimToken();

  $effect(() => {
    if (!authorized) return;
    return start();
  });

  /**
   * One listener for the whole page.
   *
   * It runs in the capture phase so the page's keys are decided before
   * anything nested can swallow them — but it takes only what the
   * keymap claims, and preventDefault fires only for a key that
   * actually meant something. Everything else reaches whatever it was
   * going to reach, which is how a walked-in terminal keeps its
   * keystrokes and the browser keeps its chords.
   */
  function key(event: KeyboardEvent) {
    // Overlays run their own keyboards; the page's keys wait.
    if (ui.palette || ui.keys) return;
    const found = match(
      event,
      focusOf(document.activeElement, ui.walkedIn),
      ui.pending,
    );
    if (!found) {
      ui.pending = "";
      return;
    }
    event.preventDefault();
    event.stopPropagation();
    ui.pending = found.pending ?? "";
    if (found.id !== "") run(found.id);
  }

  const urgent = $derived(fleet.agents.filter(isUrgent).length);
  const working = $derived(
    fleet.agents.filter((a) => a.process_live && !isUrgent(a)).length,
  );
</script>

<svelte:window onkeydowncapture={key} />

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
        <button
          class:on={ui.view === "roster"}
          onclick={() => run("view-roster")}
          title="1"
        >
          roster
        </button>
        <button
          class:on={ui.view === "wall"}
          onclick={() => run("view-wall")}
          title="2"
        >
          wall
        </button>
        <button
          class:on={ui.view === "canvas"}
          onclick={() => run("view-canvas")}
          title="3"
        >
          canvas
        </button>
      </div>
      <button onclick={() => run("dispatch")} title="n">dispatch</button>
      <button class="ghost" onclick={() => run("palette")} title="⌘K">⌘K</button>
      <button class="ghost" onclick={() => run("help")} title="?">?</button>
      <span class="spacer"></span>
      {#if urgent}
        <span class="tally waiting">◆ {urgent} needs input</span>
      {/if}
      <span class="tally working">● {working} working</span>
    </header>

    <div class="body">
      <WorkspaceRail />
      {#if ui.view === "wall"}
        <Wall onopen={() => run("view-roster")} />
      {:else if ui.view === "canvas"}
        <Canvas onopen={() => run("view-roster")} />
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

  {#if ui.dispatching}
    <Dispatch onclose={() => (ui.dispatching = false)} />
  {/if}
  {#if ui.palette}
    <Palette
      onrun={(id, argument) => run(id, argument)}
      onclose={() => (ui.palette = false)}
    />
  {/if}
  {#if ui.keys}
    <Keys onclose={() => (ui.keys = false)} />
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
  .ghost {
    padding: 2px 8px;
    color: var(--muted);
    font-size: 11px;
  }
  .ghost:hover {
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
