<script lang="ts">
  import { api } from "../lib/api";
  import { act, fleet, workspaceList } from "../lib/state.svelte";

  let { onclose }: { onclose: () => void } = $props();

  const available = $derived(fleet.providers.filter((p) => p.Available));
  const workspaces = $derived(workspaceList());

  let provider = $state("");
  let cwd = $state("");
  let task = $state("");
  let name = $state("");
  let mode = $state("auto");
  let busy = $state(false);
  let modal: HTMLDialogElement;

  // showModal is what makes this a dialog rather than a div pretending to
  // be one: focus is trapped, Escape closes, and the rest of the page is
  // inert without a scrim handler to maintain.
  $effect(() => {
    modal?.showModal();
  });

  $effect(() => {
    if (!provider && available.length) provider = available[0].ID;
    if (!cwd && workspaces.length) cwd = workspaces[0].execution_root;
  });

  async function dispatch(event: SubmitEvent) {
    event.preventDefault();
    if (!task.trim() || !cwd || busy) return;
    busy = true;
    await act(async () => {
      const agent = await api.dispatch({
        provider,
        task: task.trim(),
        cwd,
        name: name.trim(),
        mode,
      });
      fleet.selectedID = agent.id;
      onclose();
    });
    busy = false;
  }
</script>

<dialog bind:this={modal} onclose={onclose}>
  <form onsubmit={dispatch}>
    <h2>Dispatch an agent</h2>

    <label>
      Task
      <textarea bind:value={task} rows="3" placeholder="What should it do?"></textarea>
    </label>

    <div class="pair">
      <label>
        Provider
        <select bind:value={provider}>
          {#each available as candidate (candidate.ID)}
            <option value={candidate.ID}>{candidate.Label}</option>
          {/each}
        </select>
      </label>
      <label>
        Mode
        <select bind:value={mode}>
          <option value="auto">auto</option>
          <option value="edits">edits</option>
          <option value="ask">ask</option>
        </select>
      </label>
    </div>

    <label>
      Workspace
      <select bind:value={cwd}>
        {#each workspaces as workspace (workspace.id)}
          <option value={workspace.execution_root}>
            {workspace.name} — {workspace.execution_root}
          </option>
        {/each}
      </select>
    </label>

    <label>
      Name <span class="optional">optional</span>
      <input bind:value={name} placeholder="Named for you, not the agent" />
    </label>

    <footer>
      <button type="button" onclick={onclose}>cancel</button>
      <button type="submit" class="primary" disabled={!task.trim() || !cwd || busy}>
        {busy ? "dispatching…" : "dispatch"}
      </button>
    </footer>
  </form>
</dialog>

<style>
  dialog {
    padding: 0;
    background: none;
    border: none;
    color: var(--text);
  }
  dialog::backdrop {
    background: rgba(10, 13, 16, 0.6);
  }
  form {
    display: flex;
    flex-direction: column;
    gap: 12px;
    width: min(560px, 92vw);
    padding: 20px;
    background: var(--bg-raised);
    border: 1px solid var(--border-bright);
    border-radius: 8px;
    text-align: left;
  }
  h2 {
    margin: 0;
    font-size: 14px;
    color: var(--band);
  }
  label {
    display: flex;
    flex-direction: column;
    gap: 4px;
    font-size: 11px;
    letter-spacing: 0.08em;
    color: var(--muted);
  }
  .optional {
    text-transform: none;
    letter-spacing: 0;
  }
  .pair {
    display: grid;
    grid-template-columns: 1fr 1fr;
    gap: 12px;
  }
  input,
  select,
  textarea {
    padding: 7px 10px;
    background: var(--bg-sunken);
    border: 1px solid var(--border);
    border-radius: 4px;
    color: var(--text);
    font: inherit;
    resize: vertical;
  }
  input:focus,
  select:focus,
  textarea:focus {
    outline: none;
    border-color: var(--ice);
  }
  footer {
    display: flex;
    justify-content: flex-end;
    gap: 8px;
  }
  .primary {
    background: var(--band);
    border-color: var(--band);
    color: var(--band-ink);
  }
  .primary:disabled {
    opacity: 0.5;
  }
</style>
