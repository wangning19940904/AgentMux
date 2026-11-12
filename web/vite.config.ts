import { createHash } from "node:crypto";
import { readFileSync, readdirSync, statSync } from "node:fs";
import { join, relative } from "node:path";
import { defineConfig, type Plugin } from "vite";
import react from "@vitejs/plugin-react";

// Files from public/ the service worker precaches. The full-size logo is left
// out (about 1 MB); the worker caches it on first use instead.
const PRECACHE_PUBLIC = [
  "favicon.png",
  "manifest.webmanifest",
  "offline.html",
  "icons/icon-192.png",
  "icons/icon-512.png",
  "icons/maskable-192.png",
  "icons/maskable-512.png",
  "icons/apple-touch-icon.png",
];

function listFiles(dir: string): string[] {
  return readdirSync(dir).flatMap((name) => {
    const path = join(dir, name);
    return statSync(path).isDirectory() ? listFiles(path) : [path];
  });
}

// agentmuxServiceWorker emits dist/sw.js from web/sw.js with this build's
// precache list and an ID derived from every shipped file, so each build is a
// new worker the browser installs as an update.
function agentmuxServiceWorker(): Plugin {
  let publicDir = "";
  return {
    name: "agentmux-service-worker",
    apply: "build",
    configResolved(config) {
      publicDir = config.publicDir;
    },
    generateBundle(_options, bundle) {
      const template = readFileSync(new URL("./sw.js", import.meta.url), "utf8");
      const hash = createHash("sha256");
      hash.update(template);
      const bundled = Object.keys(bundle).filter((file) => !file.endsWith(".map")).sort();
      for (const file of bundled) {
        const item = bundle[file];
        hash.update(file);
        hash.update(item.type === "chunk" ? item.code : item.source);
      }
      for (const file of listFiles(publicDir).sort()) {
        hash.update(relative(publicDir, file));
        hash.update(readFileSync(file));
      }
      // index.html is cached as "/": the Go server redirects /index.html there,
      // and a cached redirect cannot answer a navigation.
      const precache = [
        "/",
        ...bundled.filter((file) => file !== "index.html").map((file) => `/${file}`),
        ...PRECACHE_PUBLIC.map((file) => `/${file}`),
      ];
      const buildLine = 'const BUILD_ID = "__BUILD_ID__";';
      const precacheLine = "const PRECACHE = __PRECACHE__;";
      if (!template.includes(buildLine) || !template.includes(precacheLine)) {
        this.error("web/sw.js no longer has the BUILD_ID and PRECACHE placeholders");
      }
      this.emitFile({
        type: "asset",
        fileName: "sw.js",
        source: template
          .replace(buildLine, `const BUILD_ID = ${JSON.stringify(hash.digest("hex").slice(0, 16))};`)
          .replace(precacheLine, `const PRECACHE = ${JSON.stringify(precache)};`),
      });
    },
  };
}

// The build output goes to dist/ and is embedded into the Go binary via
// go:embed (see web/embed.go). During dev, API calls are proxied to the Go
// daemon on :8765 (AGENTMUX_DEV_API overrides the target).
export default defineConfig({
  plugins: [react(), agentmuxServiceWorker()],
  build: {
    outDir: "dist",
    emptyOutDir: true,
  },
  server: {
    port: 5173,
    proxy: {
      "/api": process.env.AGENTMUX_DEV_API || "http://127.0.0.1:8765",
    },
  },
});
