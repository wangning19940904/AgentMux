import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

// The build output goes to dist/ and is embedded into the Go binary via
// go:embed (see web/embed.go). During dev, API calls are proxied to the Go
// daemon on :8765.
export default defineConfig({
  plugins: [react()],
  build: {
    outDir: "dist",
    emptyOutDir: true,
  },
  server: {
    port: 5173,
    proxy: {
      "/api": "http://127.0.0.1:8765",
    },
  },
});
