<script lang="ts">
  import { api } from "../lib/api";
  import { run, ui } from "../lib/commands.svelte";
  import { act, fleet, selected } from "../lib/state.svelte";
  import { statusVisual } from "../lib/theme";
  import Terminal from "./Terminal.svelte";
  import Diff from "./Diff.svelte";
  import Transcript from "./Transcript.svelte";

  let message = $state("");
  let composer = $state<HTMLInputElement>();

  // `i` asks for the reply box; honouring it here keeps the key's
  // meaning in the dispatcher and the focus where the box is.
  $effect(() => {
    if (ui.composing && composer) {
      composer.focus();
      ui.composing = false;
    }
  });

  /**
   * The way out of the box. While it holds focus the page's keys are
   * suppressed — every letter belongs to the message — so h cannot
   * step back out and something else has to. Escape does, and so does
   * a Backspace with nothing left to delete, which is how the TUI
   * leaves its reply box.
   */
  function composerKey(event: KeyboardEvent) {
    const leaving =
      event.key === "Escape" ||
      (event.key === "Backspace" && message === "");
    if (!leaving) return;
    event.preventDefault();
    event.stopPropagation();
    composer?.blur();
    ui.column = "spanreed";
  }

  const agent = $derived(selected());

  async function send(event: SubmitEvent) {
    event.preventDefault();
    const text = message.trim();
    if (!text || !agent) return;
    message = "";
    await act(() => api.send(agent.id, text));
  }
</script>

<!-- svelte-ignore a11y_no_static_element_interactions -->
<section
  class="pane"
  class:aimed={ui.column === "spanreed" && ui.view === "roster"}
  onclickcapture={() => (ui.column = "spanreed")}
>
  {#if agent}
    {@const status = statusVisual(agent.activity, agent.attention, agent.process_live)}
    <header>
      <span style:color={status.color}>{status.glyph}</span>
      <span class="title">{agent.name || agent.task}</span>
      <nav class="panes">
        <button
          class:on={ui.pane === "terminal"}
          onclick={() => run("pane-terminal")}
          title="t"
        >
          terminal
        </button>
        <button
          class:on={ui.pane === "transcript"}
          onclick={() => run("pane-transcript")}
          title="T"
        >
          transcript
        </button>
        <button
          class:on={ui.pane === "diff"}
          onclick={() => run("pane-diff")}
          title="d"
        >
          diff
        </button>
      </nav>
      <span class="spacer"></span>
      <button onclick={() => run("interrupt")} title="x">interrupt</button>
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
      <div
        class="stack"
        class:hidden={ui.pane !== "terminal"}
        class:walked={ui.walkedIn}
      >
        <Terminal agentID={agent.id} focused={ui.walkedIn} />
      </div>
      {#if ui.pane === "transcript"}
        <Transcript id={agent.id} />
      {:else if ui.pane === "diff"}
        <Diff id={agent.id} />
      {/if}
    {/key}

    <!-- A prompt, not a form field. This is where you talk to the
         agent, and the agent lives one line above it in the same
         typeface — a rounded input with a send button beside it read
         like a chat app bolted to a terminal. -->
    <form
      class="composer"
      class:aimed={ui.column === "composer" && ui.view === "roster"}
      onsubmit={send}
    >
      <span class="prompt" aria-hidden="true">›</span>
      <input
        bind:this={composer}
        bind:value={message}
        data-composer
        placeholder="message this agent"
        aria-label="Message this agent"
        spellcheck="false"
        autocomplete="off"
        onfocus={() => (ui.column = "composer")}
        onkeydown={composerKey}
      />
      <span class="hint" aria-hidden="true">
        {message.trim() ? "enter to send" : "esc to leave"}
      </span>
    </form>
  {:else}
    <div class="empty">
      <p>No agent selected.</p>
      <p class="hint">Pick one from the roster, or dispatch a new one.</p>
    </div>
  {/if}
</section>

<style>
  /* Aimed, not walked in: the keyboard scrolls this pane, but the
     agent is not listening yet — that is Enter. */
  .pane.aimed {
    box-shadow: inset 2px 0 0 -1px var(--aim);
  }
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
    color: var(--accent);
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
    background: var(--field);
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
  /* Walked in, the terminal says so: the seam the TUI paints in accent
     when the portal has the keyboard. */
  .stack.walked :global(.terminal) {
    box-shadow: inset 0 0 0 1px var(--ice);
  }
  .stack.hidden {
    display: none;
  }
  .danger:hover {
    border-color: var(--failed);
    color: var(--failed);
  }
  /* One surface with the terminal above it: same ground, same
     typeface, a prompt where a prompt belongs. */
  .composer {
    display: flex;
    align-items: baseline;
    gap: 8px;
    flex: 0 0 auto;
    padding: 7px 12px;
    background: var(--bg-sunken);
    border-top: 1px solid var(--border);
  }
  .composer.aimed {
    border-top-color: var(--aim);
    box-shadow: inset 0 2px 0 -1px var(--aim);
  }
  .prompt {
    flex: 0 0 auto;
    color: var(--working);
    font-weight: 600;
  }
  input {
    flex: 1 1 auto;
    padding: 0;
    background: none;
    border: none;
    color: var(--text-bright);
    font: inherit;
  }
  input::placeholder {
    color: var(--muted);
  }
  input:focus {
    outline: none;
  }
  .hint {
    flex: 0 0 auto;
    color: var(--muted);
    font-size: 11px;
    opacity: 0;
    transition: opacity 0.15s;
  }
  .composer:focus-within .hint {
    opacity: 1;
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
