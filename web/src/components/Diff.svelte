<script lang="ts">
  import { api } from "../lib/api";

  /**
   * What the agent has changed, as a colorized unified diff. Replaced
   * wholesale on each poll — diffs shrink as well as grow (a commit
   * upstream, a revert), so there is no delta to accumulate. One fetch
   * in flight, ever: the interval fires on the clock, and a slow
   * answer must not stack behind itself.
   */
  let { id }: { id: string } = $props();

  let diff = $state("");
  let ok = $state(true);
  let loaded = $state(false);

  const refreshInterval = 5000;

  $effect(() => {
    const agentID = id;
    let cancelled = false;
    let fetching = false;

    const fetchOnce = async () => {
      if (fetching) return;
      fetching = true;
      try {
        const view = await api.diff(agentID);
        if (cancelled) return;
        loaded = true;
        ok = view.ok;
        // A fault posture like the transcript's: ok false with content
        // already on screen keeps the content. Unlike the transcript,
        // ok true always replaces — a shrunken diff is news.
        if (view.ok) diff = view.diff;
      } catch {
        // The roster socket owns error reporting; try again next tick.
      } finally {
        fetching = false;
      }
    };

    diff = "";
    ok = true;
    loaded = false;
    void fetchOnce();
    const poll = setInterval(fetchOnce, refreshInterval);
    return () => {
      cancelled = true;
      clearInterval(poll);
    };
  });

  /**
   * Each line and the class it earns.
   *
   * Position matters, not just the prefix: "---" and "+++" are file
   * headers only *before* the first hunk of a file. Inside a hunk they
   * are ordinary deletions and additions of content that happens to
   * start with "--" or "++" — a SQL comment, a Lua comment, an email
   * signature — and colouring those gray hides exactly the lines a
   * reviewer came to read.
   */
  const classified = $derived.by(() => {
    if (diff === "") return [];
    let inHeader = false;
    return diff
      .replace(/\n$/, "")
      .split("\n")
      .map((line) => {
        if (line.startsWith("diff --git")) {
          inHeader = true;
          return { line, kind: "file" };
        }
        if (line.startsWith("@@")) {
          inHeader = false;
          return { line, kind: "hunk" };
        }
        if (inHeader) return { line, kind: "meta" };
        if (line.startsWith("+")) return { line, kind: "add" };
        if (line.startsWith("-")) return { line, kind: "del" };
        return { line, kind: "ctx" };
      });
  });
</script>

<div class="diff">
  {#if classified.length === 0}
    <p class="empty">
      {!loaded
        ? "Reading the workspace…"
        : ok
          ? "No changes — the workspace matches its base."
          : "No diff to show: the agent is remote, or its directory is not a git repository."}
    </p>
  {:else}
    <pre>{#each classified as { line, kind }, index (index)}<span
        class={kind}>{line}
</span>{/each}</pre>
  {/if}
</div>

<style>
  .diff {
    flex: 1 1 auto;
    min-height: 0;
    overflow: auto;
    padding: 12px 16px;
    font-size: 12px;
    line-height: 1.5;
  }
  .empty {
    margin: auto;
    padding-top: 30vh;
    text-align: center;
    color: var(--muted);
    font-size: 12px;
  }
  pre {
    margin: 0;
    font: inherit;
  }
  span {
    display: block;
    white-space: pre;
  }
  .file {
    margin-top: 14px;
    color: var(--accent);
    font-weight: 700;
  }
  .meta {
    color: var(--muted);
  }
  .hunk {
    color: var(--working);
  }
  .add {
    color: var(--done);
    background: var(--add-wash);
  }
  .del {
    color: var(--failed);
    background: var(--del-wash);
  }
  .ctx {
    color: var(--text);
  }
</style>
