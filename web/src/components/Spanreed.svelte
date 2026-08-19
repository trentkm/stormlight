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
      <!-- Walking in is invisible otherwise: the keyboard changes hands
           and nothing on screen says so, which reads as the key having
           done nothing at all. -->
      {#if ui.walkedIn}
        <span class="typing">typing to this agent · ctrl-space leaves</span>
      {/if}
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
        <Terminal
          agentID={agent.id}
          focused={ui.walkedIn}
          onenter={() => run("walk-in")}
        />
      </div>
      {#if ui.pane === "transcript"}
        <Transcript id={agent.id} />
      {:else if ui.pane === "diff"}
        <Diff id={agent.id} />
      {/if}
    {/key}

    <!-- Only where there is no terminal to type into. On the terminal
         tab the agent's own prompt is the input box: walking in puts
         the keyboard there, and a second box below it asking the same
         question was the thing that made this feel like a chat app
         wearing a terminal. -->
    {#if ui.pane !== "terminal"}
      <form class="composer" onsubmit={send}>
        <span class="prompt" aria-hidden="true">›</span>
        <input
          bind:this={composer}
          bind:value={message}
          data-composer
          aria-label="Message this agent"
          spellcheck="false"
          autocomplete="off"
          onkeydown={composerKey}
        />
        <span class="hint">
          {message.trim() ? "enter sends · esc leaves it here" : "esc leaves"}
        </span>
      </form>
    {/if}
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
  /* No rule down the seam: the aimed column is said by its filled
     cursor and its lit heading, and a bright line beside them was a
     second answer to a question already answered. The pane has no
     cursor of its own, so its header carries the whole signal. */
  .pane.aimed header {
    border-bottom-color: var(--aim);
  }
  .pane.aimed .title {
    color: var(--aim);
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
  /* The terminal you are typing into is lit at its own edge — not a
     rule down the layout's seam, but the box that has the keyboard. */
  .stack.walked :global(.terminal) {
    box-shadow: inset 0 0 0 1px var(--aim);
  }
  .typing {
    color: var(--aim);
    font-size: 11px;
    letter-spacing: 0.02em;
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
  /* Scoped to the composer: `.hint` is also the empty pane's second
     line, and an unscoped opacity:0 hid it entirely. */
  .composer .hint {
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
