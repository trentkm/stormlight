<script lang="ts">
  import { bindings, fuzzy } from "../lib/keys";
  import { agentsIn, fleet, workspaceList } from "../lib/state.svelte";
  import { isUrgent, type Agent } from "../lib/types";
  import { statusVisual } from "../lib/theme";

  /**
   * ⌘K. Two kinds of entry share one list: the actions the keymap
   * defines, each naming its key so the palette teaches the keyboard
   * rather than replacing it, and the roster itself — every agent and
   * workspace, matched fuzzily, because the roster is the thing that
   * grows and scrolling it is what a palette exists to avoid.
   */
  let {
    onrun,
    onclose,
  }: {
    onrun: (id: string, argument?: string) => void;
    onclose: () => void;
  } = $props();

  interface Entry {
    id: string;
    /** What the person reads. */
    label: string;
    /** The key that does this without the palette, when one exists. */
    keys?: string;
    /** Where it sits in the list. */
    kind: "agent" | "workspace" | "action";
    /** For agents and workspaces: which one. */
    argument?: string;
    /** A second line: the agent's task, the workspace's path. */
    detail?: string;
    /** The status glyph's colour, for agents. */
    colour?: string;
    glyph?: string;
  }

  let query = $state("");
  let cursor = $state(0);
  let field: HTMLInputElement;

  const agentEntries = $derived(
    fleet.agents.map((agent: Agent) => {
      const status = statusVisual(
        agent.activity,
        agent.attention,
        agent.process_live,
      );
      return {
        id: "select-agent",
        label: agent.name || agent.task || agent.id.slice(0, 8),
        kind: "agent" as const,
        argument: agent.id,
        detail: [agent.workspace?.name, isUrgent(agent) ? "needs you" : agent.activity]
          .filter(Boolean)
          .join(" · "),
        colour: status.color,
        glyph: status.glyph,
      };
    }),
  );

  const workspaceEntries = $derived(
    workspaceList().map((workspace) => ({
      id: "select-workspace",
      label: workspace.name,
      kind: "workspace" as const,
      argument: workspace.id,
      detail: workspace.root,
    })),
  );

  const actionEntries = $derived(
    bindings
      .filter((binding) => binding.palette)
      .filter((binding) => !binding.needsAgent || fleet.selectedID !== "")
      .map((binding) => ({
        id: binding.id,
        label: binding.palette!,
        keys: binding.keys,
        kind: "action" as const,
      })),
  );

  /**
   * Agents first, then workspaces, then actions — the palette is opened
   * to go somewhere far more often than to do something, and a fuzzy
   * query sorts by how well it matched regardless.
   */
  /** How many rows are drawn — and therefore how far the cursor goes.
   *  A cursor past the last rendered row is an invisible selection that
   *  Enter would act on. */
  const shownLimit = 40;

  const entries = $derived.by(() => {
    const all: Entry[] = [
      ...agentEntries,
      ...workspaceEntries,
      ...actionEntries,
    ];
    if (query.trim() === "") return all;
    return all
      .map((entry) => {
        const score = fuzzy(
          query.trim(),
          `${entry.label} ${entry.detail ?? ""}`,
        );
        return score === null ? null : { entry, score };
      })
      .filter((scored): scored is { entry: Entry; score: number } => scored !== null)
      .sort((a, b) => a.score - b.score)
      .map((scored) => scored.entry);
  });

  const shown = $derived(entries.slice(0, shownLimit));

  // A query that reshuffles the list must not leave the cursor pointing
  // past the end of it.
  $effect(() => {
    void shown;
    if (cursor >= shown.length) cursor = Math.max(0, shown.length - 1);
  });

  $effect(() => {
    field?.focus();
  });

  const run = (entry: Entry | undefined) => {
    if (!entry) return;
    onrun(entry.id, entry.argument);
    onclose();
  };

  /**
   * The palette's own keyboard, bound to the panel rather than the
   * window.
   *
   * It used to be a window listener while the panel stopped
   * propagation — which meant every keystroke died on the panel and
   * Escape, Enter, and the arrows did nothing at all. Handling here is
   * both the fix and the reason: the keys belong to the thing that
   * owns them.
   */
  const key = (event: KeyboardEvent) => {
    // ⌘K closes what ⌘K opened.
    if (event.code === "KeyK" && (event.metaKey || event.ctrlKey)) {
      event.preventDefault();
      onclose();
      return;
    }
    switch (event.key) {
      case "Escape":
        event.preventDefault();
        onclose();
        return;
      case "Enter":
        event.preventDefault();
        run(shown[cursor]);
        return;
      case "ArrowDown":
        event.preventDefault();
        cursor = Math.min(cursor + 1, shown.length - 1);
        return;
      case "ArrowUp":
        event.preventDefault();
        cursor = Math.max(cursor - 1, 0);
        return;
    }
    // Ctrl-n/p and Ctrl-j/k move too: the palette is opened by people
    // whose hands are already on those keys.
    if (event.ctrlKey && (event.key === "n" || event.key === "j")) {
      event.preventDefault();
      cursor = Math.min(cursor + 1, shown.length - 1);
    }
    if (event.ctrlKey && (event.key === "p" || event.key === "k")) {
      event.preventDefault();
      cursor = Math.max(cursor - 1, 0);
    }
  };
</script>

<!-- The scrim closes on click; the panel stops the click reaching it. -->
<div
  class="scrim"
  role="presentation"
  onclick={onclose}
>
  <!-- The panel stops clicks reaching the scrim behind it. It is not
       itself interactive: the input and the buttons inside are, and
       Escape closes from anywhere, so there is nothing here for a
       keyboard to reach that it cannot reach already. -->
  <div
    class="panel"
    role="dialog"
    aria-modal="true"
    aria-label="Command palette"
    tabindex="-1"
    onclick={(event) => event.stopPropagation()}
    onkeydown={key}
  >
    <input
      bind:this={field}
      bind:value={query}
      class="query"
      placeholder="Go to an agent, or do something…"
      aria-label="Command palette"
      spellcheck="false"
      autocomplete="off"
    />
    {#if shown.length === 0}
      <p class="nothing">Nothing matches.</p>
    {:else}
      <ul class="entries">
        {#each shown as entry, index (entry.kind + entry.id + (entry.argument ?? ""))}
          <li>
            <button
              class:on={index === cursor}
              onclick={() => run(entry)}
              onmouseenter={() => (cursor = index)}
            >
              {#if entry.glyph}
                <span class="glyph" style:color={entry.colour}>{entry.glyph}</span>
              {:else}
                <span class="glyph kind">{entry.kind === "workspace" ? "▪" : "›"}</span>
              {/if}
              <span class="label">{entry.label}</span>
              {#if entry.detail}
                <span class="detail">{entry.detail}</span>
              {/if}
              {#if entry.keys}
                <kbd>{entry.keys}</kbd>
              {/if}
            </button>
          </li>
        {/each}
      </ul>
    {/if}
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
    padding-top: 12vh;
    background: rgba(10, 13, 16, 0.55);
  }
  .panel {
    width: min(680px, 92vw);
    max-height: 70vh;
    display: flex;
    flex-direction: column;
    background: var(--bg-raised);
    border: 1px solid var(--border-bright);
    border-radius: 10px;
    box-shadow: 0 24px 60px -20px rgba(0, 0, 0, 0.6);
    overflow: hidden;
  }
  .query {
    flex: 0 0 auto;
    padding: 12px 14px;
    background: transparent;
    border: none;
    border-bottom: 1px solid var(--border);
    color: var(--text-bright);
    font: inherit;
    font-size: 14px;
  }
  .query:focus {
    outline: none;
  }
  .entries {
    flex: 1 1 auto;
    min-height: 0;
    overflow-y: auto;
    margin: 0;
    padding: 4px;
    list-style: none;
  }
  .entries button {
    display: flex;
    align-items: baseline;
    gap: 8px;
    width: 100%;
    padding: 5px 10px;
    background: none;
    border: none;
    border-radius: 5px;
    color: var(--text);
    font: inherit;
    font-size: 12.5px;
    text-align: left;
    cursor: pointer;
  }
  .entries button.on {
    background: var(--band);
    color: var(--band-ink);
  }
  .glyph {
    flex: 0 0 auto;
    width: 1em;
  }
  .kind {
    color: var(--muted);
  }
  .label {
    flex: 0 0 auto;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .detail {
    flex: 1 1 auto;
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    color: var(--muted);
    font-size: 11.5px;
  }
  .entries button.on .detail {
    color: #3a5568;
  }
  kbd {
    flex: 0 0 auto;
    padding: 0 5px;
    border: 1px solid var(--border-bright);
    border-radius: 3px;
    color: var(--muted);
    font: inherit;
    font-size: 11px;
  }
  .entries button.on kbd {
    border-color: #3a5568;
    color: #3a5568;
  }
  .nothing {
    margin: 0;
    padding: 14px;
    color: var(--muted);
    font-size: 12px;
  }
</style>
