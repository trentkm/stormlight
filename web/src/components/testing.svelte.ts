/**
 * Reactive props for component tests.
 *
 * Runes only compile inside `.svelte` and `.svelte.ts` files, and a
 * test file has to end in `.test.ts` for vitest to collect it — so a
 * test that needs to change a prop after mounting borrows its $state
 * from here.
 */
export function reactive<T extends object>(initial: T): T {
  // $state has to initialize a declaration; the proxy it returns
  // carries the reactivity out with it.
  const value = $state(initial);
  return value;
}
