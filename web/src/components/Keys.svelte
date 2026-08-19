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

  let modal = $state<HTMLDialogElement>();

  $effect(() => {
    modal?.showModal();
  });

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

<dialog
  bind:this={modal}
  aria-label="Keys"
  onkeydown={key}
  onclose={onclose}
  onclick={(event) => {
    if (event.target === modal) onclose();
  }}
>
  <div class="panel">
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
    <!-- Says what it does. The handler ignores lone modifiers and
         leaves browser chords alone, so "any key" would be a help
         screen lying about the keyboard. -->
    <p class="dismiss">Esc, or any ordinary key, closes</p>
  </div>
</dialog>

<style>
  dialog {
    margin: 8vh auto auto;
    padding: 0;
    background: none;
    border: none;
  }
  dialog::backdrop {
    background: var(--scrim);
  }
  .panel {
    width: min(680px, 92vw);
    max-height: 80vh;
    overflow-y: auto;
    padding: 18px 22px 14px;
    background: var(--bg-raised);
    border: 1px solid var(--border-bright);
    border-radius: 10px;
    box-shadow: 0 24px 60px -20px var(--shadow-modal);
  }
  h2 {
    margin: 0 0 12px;
    color: var(--accent);
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
