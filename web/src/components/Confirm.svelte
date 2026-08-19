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

  let modal = $state<HTMLDialogElement>();
  let refuse = $state<HTMLButtonElement>();

  // A real dialog, so the browser traps focus: a Tab out of a
  // hand-rolled one leaves its keys on an element nobody is focused in.
  $effect(() => {
    modal?.showModal();
  });

  // Focus lands on cancel. Enter confirms, so the safe answer is the
  // one under the finger of somebody who did not read the question.
  $effect(() => {
    refuse?.focus();
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

<dialog
  bind:this={modal}
  aria-label={what}
  onkeydown={key}
  onclose={oncancel}
  onclick={(event) => {
    if (event.target === modal) oncancel();
  }}
>
  <div class="panel" role="alertdialog" aria-label={what}>
    <p class="what">{what}</p>
    {#if detail}<p class="detail">{detail}</p>{/if}
    <div class="row">
      <button class="danger" onclick={onconfirm}>delete</button>
      <button bind:this={refuse} onclick={oncancel}>cancel</button>
      <span class="hint">x or Enter confirms · Esc cancels</span>
    </div>
  </div>
</dialog>

<style>
  dialog {
    margin: 22vh auto auto;
    padding: 0;
    background: none;
    border: none;
  }
  dialog::backdrop {
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
