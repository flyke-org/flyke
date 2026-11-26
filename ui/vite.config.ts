import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

// `npm run dev` proxies the API to a running control plane
// (default: the local one started by `flyke server --dev` or `flyke run`).
const target = process.env.FLYKE_SERVER ?? "http://127.0.0.1:8742";

export default defineConfig({
  plugins: [react()],
  build: {
    // dist/app is embedded into the Go binary by ui/embed.go.
    outDir: "dist/app",
    emptyOutDir: true,
    // The lazily loaded Playground chunk carries Monaco (~3 MB, ~800 kB gzip).
    chunkSizeWarningLimit: 3500,
  },
  server: {
    port: 5173,
    proxy: {
      "/api": { target, changeOrigin: true },
      "/healthz": { target, changeOrigin: true },
      "/version": { target, changeOrigin: true },
    },
  },
});
