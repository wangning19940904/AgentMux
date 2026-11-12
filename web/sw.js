/* AgentMux Console service worker.
 *
 * The build (vite.config.ts, agentmuxServiceWorker) fills in BUILD_ID and
 * PRECACHE below, so every build ships different bytes and the browser
 * installs it as an update.
 *
 * What it does:
 * - Caches the app shell of exactly one build (HTML, hashed assets, icons) so
 *   the Console opens instantly and still opens offline.
 * - Never touches API traffic, sign-in paths, or anything that is not a GET:
 *   those go straight to the network, and live data is never served from here.
 * - A new build waits until the page asks it to take over (the "Reload"
 *   prompt) or until no page uses the old one, then deletes the old cache.
 * - Shows notifications pushed by the hub and opens the right page on tap.
 */
const BUILD_ID = "__BUILD_ID__";
const PRECACHE = __PRECACHE__;
const CACHE_PREFIX = "agentmux-shell-";
const CACHE = CACHE_PREFIX + BUILD_ID;
const OFFLINE_URL = "/offline.html";

// Paths that always belong to the server. Kept in sync with server routes:
// the management API, OpenAI-compatible API, inbound hooks, console sign-in.
const NETWORK_ONLY_PREFIXES = ["/api/", "/v1/", "/hook/", "/channel-avatar", "/console/"];
const SHELL_PATHS = ["/", "/index.html"];
const STATIC_PATH = /^\/(assets\/|icons\/)|\.(?:js|css|png|svg|ico|webp|woff2?|webmanifest)$/;

// classify decides how a request is handled: "network" (not handled here),
// "shell" (the cached index.html), or "static" (a cached build file).
function classify(url, method, mode) {
  if (method !== "GET") return "network";
  if (url.origin !== self.location.origin) return "network";
  const path = url.pathname;
  if (NETWORK_ONLY_PREFIXES.some((prefix) => path === prefix.replace(/\/$/, "") || path.startsWith(prefix))) return "network";
  if (path === "/sw.js") return "network";
  if (mode === "navigate") return SHELL_PATHS.includes(path) ? "shell" : "network";
  if (SHELL_PATHS.includes(path)) return "shell";
  if (STATIC_PATH.test(path)) return "static";
  return "network";
}

self.addEventListener("install", (event) => {
  event.waitUntil(
    caches.open(CACHE).then((cache) => cache.addAll(PRECACHE.map((path) => new Request(path, { cache: "reload" })))),
  );
});

self.addEventListener("activate", (event) => {
  event.waitUntil((async () => {
    const names = await caches.keys();
    await Promise.all(names.filter((name) => name.startsWith(CACHE_PREFIX) && name !== CACHE).map((name) => caches.delete(name)));
    await self.clients.claim();
  })());
});

self.addEventListener("message", (event) => {
  const data = event.data || {};
  if (data.type === "SKIP_WAITING") self.skipWaiting();
  if (data.type === "GET_BUILD" && event.ports && event.ports[0]) event.ports[0].postMessage({ build: BUILD_ID });
});

self.addEventListener("fetch", (event) => {
  const request = event.request;
  const kind = classify(new URL(request.url), request.method, request.mode);
  if (kind === "network") return;
  if (kind === "shell") {
    event.respondWith((async () => {
      const cache = await caches.open(CACHE);
      const cached = await cache.match("/");
      if (cached) return cached;
      try {
        return await fetch(request);
      } catch {
        return (await cache.match(OFFLINE_URL)) || Response.error();
      }
    })());
    return;
  }
  event.respondWith((async () => {
    const cache = await caches.open(CACHE);
    const cached = await cache.match(request, { ignoreSearch: true });
    if (cached) return cached;
    const response = await fetch(request);
    // Files outside the precache list (the full-size logo) join this build's
    // cache once fetched; they leave with it on the next build.
    if (response.ok && response.type === "basic") cache.put(request, response.clone());
    return response;
  })());
});

// Push: the hub sends a small JSON payload with no secrets or message bodies.
self.addEventListener("push", (event) => {
  let payload = {};
  try {
    payload = event.data ? event.data.json() : {};
  } catch {
    payload = { title: "AgentMux", body: event.data ? event.data.text() : "" };
  }
  const title = payload.title || "AgentMux";
  const options = {
    body: payload.body || "",
    tag: payload.tag || undefined,
    renotify: Boolean(payload.tag),
    icon: "/icons/icon-192.png",
    badge: "/icons/icon-192.png",
    timestamp: payload.timestamp ? Date.parse(payload.timestamp) || Date.now() : Date.now(),
    data: { url: safeTarget(payload.url) },
  };
  event.waitUntil(self.registration.showNotification(title, options));
});

// safeTarget keeps a tap inside this Console: only same-origin paths.
function safeTarget(url) {
  if (typeof url !== "string" || !url.startsWith("/") || url.startsWith("//")) return "/";
  return url;
}

self.addEventListener("notificationclick", (event) => {
  event.notification.close();
  const target = safeTarget(event.notification.data && event.notification.data.url);
  event.waitUntil((async () => {
    const windows = await self.clients.matchAll({ type: "window", includeUncontrolled: true });
    const open = windows.find((client) => new URL(client.url).origin === self.location.origin);
    if (open) {
      // The page switches to the target route itself (pwa/register.ts), so an
      // open Console keeps its state instead of reloading.
      await open.focus();
      open.postMessage({ type: "OPEN_URL", url: target });
      return;
    }
    await self.clients.openWindow(target);
  })());
});

// Exposed for tests that evaluate this file with a stub `self`.
self.__agentmuxClassify = classify;
self.__agentmuxSafeTarget = safeTarget;
