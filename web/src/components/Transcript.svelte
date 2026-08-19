<script lang="ts">
  import { api } from "../lib/api";
  import type { TranscriptEntry } from "../lib/types";

  /**
   * The conversation, rendered natively. The terminal shows what the
   * agent's screen shows — one alternate-screen frame — while this pane
   * shows everything said and done, from the provider's own transcript,
   * as HTML the browser lays out and the reader scrolls and selects.
   *
   * Polled rather than pushed: the transcript file settles between
   * turns, the server caches remote reads, and a fetch every few
   * seconds while the pane is open costs less than a push channel
   * earns. `live` gates the loop so a closed pane costs nothing.
   */
  let { id, live }: { id: string; live: boolean } = $props();

  let entries = $state<TranscriptEntry[]>([]);
  let loaded = $state(false);
  let scroller: HTMLDivElement;

  const refreshInterval = 3000;

  $effect(() => {
    if (!live) return;
    const agentID = id;
    let cancelled = false;

    const fetchOnce = async () => {
      try {
        const transcript = await api.transcript(agentID);
        if (cancelled) return;
        const first = !loaded;
        const grew = transcript.entries.length !== entries.length;
        entries = transcript.entries;
        loaded = true;
        if (grew) follow(first);
      } catch {
        // The roster socket owns error reporting; a missed transcript
        // poll just tries again.
      }
    };

    // Follow the conversation the way a terminal does: jump to the
    // newest turn on open, and stay pinned there only while already at
    // the bottom — reading history is never yanked away.
    const follow = (force: boolean) => {
      requestAnimationFrame(() => {
        if (!scroller) return;
        const nearBottom =
          scroller.scrollHeight - scroller.scrollTop - scroller.clientHeight <
          120;
        if (force || nearBottom) {
          scroller.scrollTop = scroller.scrollHeight;
        }
      });
    };

    entries = [];
    loaded = false;
    void fetchOnce();
    const poll = setInterval(fetchOnce, refreshInterval);
    return () => {
      cancelled = true;
      clearInterval(poll);
    };
  });
</script>

<div class="transcript" bind:this={scroller}>
  {#if entries.length === 0}
    <p class="empty">
      {loaded
        ? "No conversation yet — the transcript appears after the first turn."
        : "Loading conversation…"}
    </p>
  {:else}
    {#each entries as entry, index (index)}
      {#if entry.kind === "prompt"}
        <div class="entry prompt">
          <span class="mark">❯</span>
          <pre>{entry.text}</pre>
        </div>
      {:else if entry.kind === "reply"}
        <div class="entry reply">
          <span class="mark">⏺</span>
          <pre>{entry.text}</pre>
        </div>
      {:else if entry.kind === "tool"}
        <div class="entry tool">
          <span class="mark">⏺</span>
          <p><span class="tool-name">{entry.tool}</span><span class="tool-arg">({entry.arg ?? ""})</span></p>
        </div>
      {:else if entry.kind === "result"}
        <div class="entry result">
          <span class="mark">⎿</span>
          <pre>{entry.text}{#if entry.hidden}{"\n"}… +{entry.hidden} lines{/if}</pre>
        </div>
      {/if}
    {/each}
  {/if}
</div>

<style>
  .transcript {
    flex: 1 1 auto;
    min-height: 0;
    overflow-y: auto;
    padding: 14px 16px;
    font-size: 12.5px;
    line-height: 1.55;
  }
  .empty {
    margin: auto;
    padding-top: 30vh;
    text-align: center;
    color: var(--muted);
    font-size: 12px;
  }
  .entry {
    display: flex;
    gap: 8px;
    align-items: flex-start;
  }
  .entry pre,
  .entry p {
    margin: 0;
    min-width: 0;
    white-space: pre-wrap;
    overflow-wrap: anywhere;
    font: inherit;
  }
  .mark {
    flex: 0 0 auto;
    width: 1.2em;
    text-align: center;
  }
  .prompt {
    margin-top: 14px;
  }
  .prompt .mark {
    color: var(--working);
    font-weight: 700;
  }
  .prompt pre {
    color: var(--text-bright);
    font-weight: 600;
  }
  .reply {
    margin-top: 14px;
  }
  .reply .mark {
    color: var(--done);
  }
  .reply pre {
    color: var(--text);
  }
  .tool {
    margin-top: 14px;
  }
  .tool .mark {
    color: var(--working);
  }
  .tool-name {
    color: var(--text);
    font-weight: 600;
  }
  .tool-arg {
    color: var(--muted);
  }
  .result {
    margin-top: 2px;
    padding-left: 14px;
  }
  .result .mark,
  .result pre {
    color: var(--muted);
  }
</style>
