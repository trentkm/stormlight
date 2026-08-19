<script lang="ts">
  import { api } from "../lib/api";
  import { act, fleet, selected } from "../lib/state.svelte";
  import { statusVisual } from "../lib/theme";
  import Terminal from "./Terminal.svelte";
  import Transcript from "./Transcript.svelte";

  let message = $state("");
  let pane = $state<"terminal" | "transcript">("terminal");

  const agent = $derived(selected());

  async function send(event: SubmitEvent) {
    event.preventDefault();
    const text = message.trim();
    if (!text || !agent) return;
    message = "";
    await act(() => api.send(agent.id, text));
  }
</script>

<section class="pane">
  {#if agent}
    {@const status = statusVisual(agent.activity, agent.attention, agent.process_live)}
    <header>
      <span style:color={status.color}>{status.glyph}</span>
      <span class="title">{agent.name || agent.task}</span>
      <nav class="panes">
        <button
          class:on={pane === "terminal"}
          onclick={() => (pane = "terminal")}
        >
          terminal
        </button>
        <button
          class:on={pane === "transcript"}
          onclick={() => (pane = "transcript")}
        >
          transcript
        </button>
      </nav>
      <span class="spacer"></span>
      <button onclick={() => act(() => api.interrupt(agent.id))}>interrupt</button>
      {#if agent.attention}
        <button onclick={() => act(() => api.clearAttention(agent.id))}>clear</button>
      {/if}
      <button
        class="danger"
        onclick={() => act(async () => {
          await api.remove(agent.id);
          fleet.selectedID = "";
        })}
      >
        delete
      </button>
    </header>

    <!-- Keyed on the agent so switching selection builds a new terminal
         rather than re-pointing the old one at a different session. The
         terminal stays mounted (hidden) behind the transcript: its
         attachment is how the daemon knows this pane's size, and
         tearing it down on every tab switch would churn seed and
         geometry for a keystroke's worth of reading. -->
    {#key agent.id}
      <div class="stack" class:hidden={pane !== "terminal"}>
        <Terminal agentID={agent.id} />
      </div>
      {#if pane === "transcript"}
        <Transcript id={agent.id} live={true} />
      {/if}
    {/key}

    <form onsubmit={send}>
      <input
        bind:value={message}
        placeholder="Message this agent…"
        aria-label="Message this agent"
      />
      <button type="submit" disabled={!message.trim()}>send</button>
    </form>
  {:else}
    <div class="empty">
      <p>No agent selected.</p>
      <p class="hint">Pick one from the roster, or dispatch a new one.</p>
    </div>
  {/if}
</section>

<style>
  .pane {
    flex: 1 1 auto;
    display: flex;
    flex-direction: column;
    min-width: 0;
    background: var(--bg-sunken);
  }
  header {
    display: flex;
    align-items: center;
    gap: 10px;
    height: 40px;
    flex: 0 0 auto;
    padding: 0 14px;
    background: var(--bg-raised);
    border-bottom: 1px solid var(--border);
  }
  .title {
    color: var(--band);
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .spacer {
    flex: 1 1 auto;
  }
  header button {
    padding: 3px 10px;
    font-size: 11px;
  }
  .panes {
    display: flex;
    gap: 2px;
    padding: 2px;
    background: var(--bg-sunken);
    border: 1px solid var(--border);
    border-radius: 5px;
  }
  .panes button {
    padding: 1px 8px;
    border: none;
    font-size: 11px;
    color: var(--muted);
  }
  .panes button.on {
    background: var(--band);
    color: var(--band-ink);
  }
  .stack {
    display: contents;
  }
  .stack.hidden {
    display: none;
  }
  .danger:hover {
    border-color: var(--failed);
    color: var(--failed);
  }
  form {
    display: flex;
    gap: 8px;
    flex: 0 0 auto;
    padding: 8px 10px;
    background: var(--bg-raised);
    border-top: 1px solid var(--border);
  }
  input {
    flex: 1 1 auto;
    padding: 6px 10px;
    background: var(--bg-sunken);
    border: 1px solid var(--border);
    border-radius: 4px;
    color: var(--text);
    font: inherit;
  }
  input:focus {
    outline: none;
    border-color: var(--ice);
  }
  .empty {
    margin: auto;
    text-align: center;
    color: var(--muted);
  }
  .hint {
    font-size: 12px;
  }
</style>
