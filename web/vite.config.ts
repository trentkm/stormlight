import { defineConfig } from "vitest/config";
import { svelte } from "@sveltejs/vite-plugin-svelte";

// The build output is embedded into the binary, so it goes to dist/ with
// relative asset paths and no hashed directory layout to configure on the
// Go side. In development, `npm run dev` proxies the API to a running
// `stormlight serve` so the page is the only thing Vite owns.
export default defineConfig({
  plugins: [svelte()],
  // Tests that mount components need Svelte's client runtime; without
  // the browser condition, vitest resolves the server build, whose
  // mount() only throws. Scoped to tests so the real build is untouched.
  test: {
    // Component tests opt into jsdom per-file with a
    // `@vitest-environment jsdom` docblock; everything else stays in
    // node, where the protocol tests already guard against the runner
    // quietly acquiring browser globals (see AGENTS.md on webstorage).
    environment: "node",
  },
  resolve: process.env.VITEST ? { conditions: ["browser"] } : undefined,
  base: "./",
  build: { outDir: "dist", emptyOutDir: true },
  server: {
    proxy: {
      "/api": {
        target: "http://127.0.0.1:7331",
        changeOrigin: false,
        ws: true,
      },
    },
  },
});
