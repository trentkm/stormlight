<script lang="ts">
  /**
   * A confirmation that names its victim, the way the TUI's does. The
   * browser reduced Ctrl-x then x to an invisible two-key sequence that
   * deleted an agent outright; a person deserves to see what they are
   * about to lose, and to be able to say no.
   */
  let {
    what,
    detail,
    onconfirm,
    oncancel,
  }: {
    what: string;
    detail?: string;
    onconfirm: () => void;
    oncancel: () => void;
  } = $props();

  let panel = $state<HTMLDivElement>();

  // Focus lands here so Escape and Enter reach this dialog rather than
  // whatever had the keyboard a moment ago.
  $effect(() => {
    panel?.focus();
  });

  const key = (event: KeyboardEvent) => {
    if (event.key === "Escape") {
      event.preventDefault();
      oncancel();
    }
    if (event.key === "Enter" || event.key === "x") {
      event.preventDefault();
      onconfirm();
    }
  };
</script>

<div class="scrim" role="presentation" onclick={oncancel}>
  <div
    class="panel"
    role="alertdialog"
    aria-modal="true"
    aria-label={what}
    tabindex="-1"
    bind:this={panel}
    onclick={(event) => event.stopPropagation()}
    onkeydown={key}
  >
    <p class="what">{what}</p>
    {#if detail}<p class="detail">{detail}</p>{/if}
    <div class="row">
      <button class="danger" onclick={onconfirm}>delete</button>
      <button onclick={oncancel}>cancel</button>
      <span class="hint">x or Enter confirms · Esc cancels</span>
    </div>
  </div>
</div>

<style>
  .scrim {
    position: fixed;
    inset: 0;
    z-index: 50;
    display: flex;
    justify-content: center;
    align-items: flex-start;
    padding-top: 22vh;
    background: rgba(10, 13, 16, 0.55);
  }
  .panel {
    width: min(460px, 92vw);
    padding: 16px 18px 14px;
    background: var(--bg-raised);
    border: 1px solid var(--failed);
    border-radius: 10px;
    box-shadow: 0 24px 60px -20px rgba(0, 0, 0, 0.6);
  }
  .panel:focus {
    outline: none;
  }
  .what {
    margin: 0 0 6px;
    color: var(--text-bright);
    font-size: 13px;
  }
  .detail {
    margin: 0 0 12px;
    color: var(--muted);
    font-size: 12px;
  }
  .row {
    display: flex;
    align-items: center;
    gap: 8px;
  }
  .row button {
    padding: 3px 12px;
    font-size: 12px;
  }
  .danger {
    border-color: var(--failed);
    color: var(--failed);
  }
  .hint {
    margin-left: auto;
    color: var(--muted);
    font-size: 11px;
  }
</style>
