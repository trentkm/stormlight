import { defineConfig } from "vite";
import { svelte } from "@sveltejs/vite-plugin-svelte";

// The build output is embedded into the binary, so it goes to dist/ with
// relative asset paths and no hashed directory layout to configure on the
// Go side. In development, `npm run dev` proxies the API to a running
// `stormlight serve` so the page is the only thing Vite owns.
export default defineConfig({
  plugins: [svelte()],
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
