// Service worker registration, update handoff and theme colour for the
// installed Console. The worker itself is web/sw.js.

export const UPDATE_READY_EVENT = "agentmux:update-ready";
export const INSTALL_AVAILABLE_EVENT = "agentmux:install-available";

// Chromium offers installation through this event; Safari has no prompt and
// installs through Share → Add to Home Screen instead.
type InstallPromptEvent = Event & { prompt: () => Promise<void>; userChoice: Promise<{ outcome: string }> };
let installPrompt: InstallPromptEvent | null = null;

export function installAvailable() {
  return installPrompt !== null;
}

export async function promptInstall(): Promise<boolean> {
  const event = installPrompt;
  if (!event) return false;
  installPrompt = null;
  await event.prompt();
  const choice = await event.userChoice.catch(() => ({ outcome: "dismissed" }));
  window.dispatchEvent(new CustomEvent(INSTALL_AVAILABLE_EVENT));
  return choice.outcome === "accepted";
}

export function isStandalone() {
  return window.matchMedia?.("(display-mode: standalone)").matches || (navigator as Navigator & { standalone?: boolean }).standalone === true;
}

if (typeof window !== "undefined") {
  window.addEventListener("beforeinstallprompt", (event) => {
    event.preventDefault();
    installPrompt = event as InstallPromptEvent;
    window.dispatchEvent(new CustomEvent(INSTALL_AVAILABLE_EVENT));
  });
}

let waitingWorker: ServiceWorker | null = null;
let reloading = false;
let activeRegistration: ServiceWorkerRegistration | null = null;

export async function checkForUpdate() {
  await activeRegistration?.update().catch(() => undefined);
}

function isDesktopShell() {
  return window.location.protocol === "wails:" || Boolean(window.go?.main?.App);
}

export function serviceWorkerSupported() {
  return (
    "serviceWorker" in navigator &&
    (window.location.protocol === "https:" || window.location.protocol === "http:") &&
    !isDesktopShell()
  );
}

export function updateWaiting() {
  return waitingWorker !== null;
}

function announceWaiting(worker: ServiceWorker | null) {
  if (!worker) return;
  waitingWorker = worker;
  window.dispatchEvent(new CustomEvent(UPDATE_READY_EVENT));
}

// applyUpdate asks the waiting build to take over, then reloads once it does.
export function applyUpdate() {
  if (!waitingWorker) {
    window.location.reload();
    return;
  }
  waitingWorker.postMessage({ type: "SKIP_WAITING" });
}

// openRoute moves the Console to a path a notification pointed at. Only
// same-origin paths are accepted; the hash selects the page.
export function openRoute(url: string) {
  if (!url.startsWith("/") || url.startsWith("//")) return;
  const target = new URL(url, window.location.origin);
  if (target.pathname === window.location.pathname) {
    if (target.hash && target.hash !== window.location.hash) window.location.hash = target.hash;
    return;
  }
  window.location.assign(target.toString());
}

export async function registerServiceWorker(): Promise<ServiceWorkerRegistration | null> {
  if (!serviceWorkerSupported() || !import.meta.env.PROD) return null;
  const hadController = Boolean(navigator.serviceWorker.controller);
  navigator.serviceWorker.addEventListener("controllerchange", () => {
    // The first install claims an uncontrolled page; nothing to reload then.
    if (!hadController || reloading) return;
    reloading = true;
    window.location.reload();
  });
  navigator.serviceWorker.addEventListener("message", (event: MessageEvent) => {
    const data = event.data as { type?: string; url?: string } | null;
    if (data?.type === "OPEN_URL" && typeof data.url === "string") openRoute(data.url);
  });
  try {
    const registration = await navigator.serviceWorker.register("/sw.js", { scope: "/" });
    activeRegistration = registration;
    if (registration.waiting && navigator.serviceWorker.controller) announceWaiting(registration.waiting);
    registration.addEventListener("updatefound", () => {
      const installing = registration.installing;
      installing?.addEventListener("statechange", () => {
        if (installing.state === "installed" && navigator.serviceWorker.controller) announceWaiting(installing);
      });
    });
    // An installed app can stay open for days; look for a new build whenever
    // it comes back to the foreground.
    document.addEventListener("visibilitychange", () => {
      if (document.visibilityState === "visible") void checkForUpdate();
    });
    return registration;
  } catch (error) {
    console.warn("AgentMux service worker registration failed", error);
    return null;
  }
}

// Keep the browser chrome (status bar on phones) in the Console's own theme,
// including when the person picks light or dark by hand.
export function syncThemeColor() {
  const apply = () => {
    const colour = getComputedStyle(document.documentElement).getPropertyValue("--app-shell").trim();
    if (!colour) return;
    const metas = document.querySelectorAll<HTMLMetaElement>('meta[name="theme-color"]');
    metas.forEach((meta) => {
      meta.content = colour;
    });
  };
  apply();
  new MutationObserver(apply).observe(document.documentElement, { attributes: true, attributeFilter: ["data-theme"] });
}
