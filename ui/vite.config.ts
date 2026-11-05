import path from "node:path";
import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";
import tailwindcss from "@tailwindcss/vite";

// base "./" keeps every asset URL relative, so the same build works when the daemon serves it
// at / and inside the Wails window (wails://wails/). Routing is hash based (#/agents/x), so no
// server-side fallback is needed for deep links.
export default defineConfig({
  base: "./",
  plugins: [react(), tailwindcss()],
  resolve: { alias: { "@": path.resolve(import.meta.dirname, "src") } },
  build: {
    outDir: "dist",
    emptyOutDir: true, // public/.keep is copied back, so ui/dist/.keep survives a build
    chunkSizeWarningLimit: 1500,
    target: "es2022",
  },
  server: {
    port: 5173,
    strictPort: false,
    // `pnpm dev` against a running daemon: same-origin /api, so no CORS needed.
    proxy: {
      "/api": { target: process.env.TMX_URL ?? "http://127.0.0.1:8787", ws: true, changeOrigin: true },
    },
  },
});
