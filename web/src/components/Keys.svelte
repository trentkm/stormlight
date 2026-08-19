<script lang="ts">
  import { bindings, notYet, unavailable } from "../lib/keys";

  /**
   * `?`. What this client actually binds, read from the same table the
   * dispatcher runs — so the list cannot describe a key that does
   * nothing — plus the TUI keys the browser made impossible, named with
   * their reason. A key that silently is not here is worse than one
   * that explains itself.
   */
  let { onclose }: { onclose: () => void } = $props();

  const groups = ["Navigate", "Act", "Panes", "View"] as const;

  const key = (event: KeyboardEvent) => {
    // "Any key closes", the way the TUI's help modal does — but a
    // terminal never delivers a lone Shift, and a browser chord is the
    // browser's. Swallowing either would make this overlay close when
    // nobody pressed anything, and eat ⌘R on the way.
    if (["Shift", "Control", "Alt", "Meta", "CapsLock"].includes(event.key)) {
      return;
    }
    if (event.metaKey || event.ctrlKey) return;
    event.preventDefault();
    onclose();
  };
</script>

<svelte:window onkeydown={key} />

<!-- svelte-ignore a11y_click_events_have_key_events -->
<!-- The window handler above closes on any key at all, so this scrim
     needs no keyboard affordance of its own; the click is the mouse's
     way to the same door. -->
<div class="scrim" role="presentation" onclick={onclose}>
  <div
    class="panel"
    role="dialog"
    aria-modal="true"
    aria-label="Keys"
    tabindex="-1"
    onclick={(event) => event.stopPropagation()}
  >
    <h2>Keys</h2>
    {#each groups as group (group)}
      {@const rows = bindings.filter((binding) => binding.group === group)}
      {#if rows.length > 0}
        <section>
          <h3>{group}</h3>
          {#each rows as binding (binding.id)}
            <p class="row">
              <kbd>{binding.keys}</kbd>
              <span class="what">{binding.what}</span>
              {#if binding.whileWalkedIn}
                <span class="note">works inside the terminal</span>
              {/if}
            </p>
          {/each}
        </section>
      {/if}
    {/each}
    <section class="gone">
      <h3>Not here yet</h3>
      {#each notYet as entry (entry.keys)}
        <p class="row">
          <kbd>{entry.keys}</kbd>
          <span class="what">{entry.what}</span>
        </p>
      {/each}
    </section>
    <section class="gone">
      <h3>Not available in a browser</h3>
      {#each unavailable as entry (entry.keys)}
        <p class="row">
          <kbd>{entry.keys}</kbd>
          <span class="what">{entry.why}</span>
        </p>
      {/each}
    </section>
    <p class="dismiss">any key closes</p>
  </div>
</div>

<style>
  .scrim {
    position: fixed;
    inset: 0;
    z-index: 40;
    display: flex;
    justify-content: center;
    align-items: flex-start;
    padding-top: 8vh;
    background: rgba(10, 13, 16, 0.55);
  }
  .panel {
    width: min(680px, 92vw);
    max-height: 80vh;
    overflow-y: auto;
    padding: 18px 22px 14px;
    background: var(--bg-raised);
    border: 1px solid var(--border-bright);
    border-radius: 10px;
    box-shadow: 0 24px 60px -20px rgba(0, 0, 0, 0.6);
  }
  h2 {
    margin: 0 0 12px;
    color: var(--band);
    font-size: 15px;
  }
  h3 {
    margin: 14px 0 6px;
    color: var(--ice);
    font-size: 12px;
    font-weight: 600;
    letter-spacing: 0.06em;
  }
  .row {
    display: flex;
    align-items: baseline;
    gap: 10px;
    margin: 0 0 3px;
    font-size: 12.5px;
  }
  kbd {
    flex: 0 0 auto;
    min-width: 8.5em;
    padding: 0 5px;
    border: 1px solid var(--border-bright);
    border-radius: 3px;
    color: var(--text-bright);
    font: inherit;
    font-size: 11.5px;
    text-align: center;
  }
  .what {
    color: var(--muted);
  }
  .note {
    color: var(--working);
    font-size: 11px;
  }
  .gone kbd {
    color: var(--muted);
  }
  .dismiss {
    margin: 16px 0 0;
    color: var(--muted);
    font-size: 11.5px;
    text-align: center;
  }
</style>
