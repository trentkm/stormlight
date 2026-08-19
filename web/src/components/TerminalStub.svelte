<script lang="ts">
  /**
   * A terminal-shaped placeholder for the page-level tests: it carries
   * the class the focus rules key on and a focusable helper, and
   * nothing else. The real one is exercised by terminal.test.ts.
   */
  let { focused = false }: { agentID: string; focused?: boolean } = $props();
  let helper = $state<HTMLTextAreaElement>();

  $effect(() => {
    if (focused) helper?.focus();
    else helper?.blur();
  });
</script>

<div class="terminal" data-walk-target>
  <textarea bind:this={helper} aria-label="terminal"></textarea>
</div>

<style>
  .terminal {
    flex: 1 1 auto;
  }
</style>
