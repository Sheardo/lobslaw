import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

// The build output is embedded into the Go binary by
// internal/gateway/ui, so it is written straight there rather than to
// a local dist/ that a Makefile step would then have to copy. One
// artefact, one location, and nothing to forget.
//
// base is relative because the console is served from the gateway's
// root but an operator may put a reverse proxy in front of it at a
// subpath; absolute asset URLs would 404 there with no obvious cause.
export default defineConfig({
  plugins: [react()],
  base: "./",
  build: {
    outDir: "../internal/gateway/ui/dist",
    // NOT emptied by Vite. That directory holds a committed .gitkeep,
    // and without it a clean checkout has no dist/ at all — which
    // makes `go:embed all:dist` a COMPILE error for anyone who has not
    // run the web build. `make web` removes the hashed assets itself,
    // which is the only part that would otherwise accumulate.
    emptyOutDir: false,
  },
  server: {
    // Dev server talks to a locally running node, so the console can
    // be worked on without rebuilding the binary on every change.
    proxy: {
      "/v1": "http://127.0.0.1:8080",
    },
  },
});
