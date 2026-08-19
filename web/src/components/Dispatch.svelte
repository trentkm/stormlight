<script lang="ts">
  import { api } from "../lib/api";
  import { act, fleet, selected, workspaceList } from "../lib/state.svelte";

  let { onclose }: { onclose: () => void } = $props();

  const available = $derived(fleet.providers.filter((p) => p.Available));
  const workspaces = $derived(workspaceList());

  let provider = $state("");
  // The workspace id, not its path: a path alone loses the host, and two
  // machines can hold the same one. Dispatching into an SSH workspace
  // with the host dropped resolves here — failing outright, or silently
  // starting the agent on the wrong machine.
  let workspaceID = $state("");
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

  // The form opens on the workspace the dashboard is already looking at:
  // the rail's highlight, or — with "All agents" showing, where the rail
  // names no one — whatever the selected agent is running in. That is the
  // same order the TUI's dispatch form resolves, and it means the common
  // case of "another one of these, here" needs no field touched. The top
  // of the list is the fallback for a dashboard aimed at nothing, not the
  // default.
  const preferred = $derived(
    [fleet.workspaceID, selected()?.workspace?.id].find((id) =>
      workspaces.some((candidate) => candidate.id === id),
    ) ??
      workspaces[0]?.id ??
      "",
  );

  $effect(() => {
    if (!provider && available.length) provider = available[0].ID;
    if (!workspaceID && preferred) workspaceID = preferred;
  });

  const workspace = $derived(workspaces.find((w) => w.id === workspaceID));

  async function dispatch(event: SubmitEvent) {
    event.preventDefault();
    if (!task.trim() || !workspace || busy) return;
    busy = true;
    await act(async () => {
      const agent = await api.dispatch({
        provider,
        task: task.trim(),
        cwd: workspace.execution_root,
        host: workspace.host ?? "",
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
      <select bind:value={workspaceID}>
        {#each workspaces as candidate (candidate.id)}
          <option value={candidate.id}>
            {candidate.name} — {candidate.execution_root}{candidate.host
              ? ` on ${candidate.host}`
              : ""}
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
      <button type="submit" class="primary" disabled={!task.trim() || !workspace || busy}>
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
    background: var(--scrim);
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
    color: var(--accent);
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
    background: var(--field);
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
