// What this browser can do about push notifications, and the small
// conversions subscribing needs. Kept free of React so it can be tested.

export type PushBlocker =
  | "desktop-app" // the native desktop window has no Web Push
  | "insecure" // push needs HTTPS (or localhost)
  | "ios-home-screen" // iOS and iPadOS deliver push only to Home Screen apps
  | "unsupported" // no service worker or Push API in this browser
  | "no-worker" // a development build without the service worker
  | "denied"; // the person blocked notifications for this site

export type PushEnvironment = {
  protocol: string;
  hostname: string;
  isSecureContext: boolean;
  userAgent: string;
  maxTouchPoints: number;
  standalone: boolean;
  hasServiceWorker: boolean;
  hasPushManager: boolean;
  hasNotification: boolean;
  permission: NotificationPermission | "unsupported";
  workerAvailable: boolean;
};

export function isIOS(userAgent: string, maxTouchPoints: number) {
  // iPadOS reports a Mac user agent; touch support tells them apart.
  return /iPhone|iPad|iPod/.test(userAgent) || (/Macintosh/.test(userAgent) && maxTouchPoints > 1);
}

// pushBlocker returns why push cannot be turned on here, or null.
export function pushBlocker(env: PushEnvironment): PushBlocker | null {
  if (env.protocol === "wails:") return "desktop-app";
  if (!env.isSecureContext) return "insecure";
  const ios = isIOS(env.userAgent, env.maxTouchPoints);
  if (ios && !env.standalone) return "ios-home-screen";
  if (!env.hasServiceWorker || !env.hasPushManager || !env.hasNotification) return ios ? "ios-home-screen" : "unsupported";
  if (!env.workerAvailable) return "no-worker";
  if (env.permission === "denied") return "denied";
  return null;
}

export function currentPushEnvironment(workerAvailable: boolean): PushEnvironment {
  const nav = navigator as Navigator & { standalone?: boolean };
  return {
    protocol: window.location.protocol,
    hostname: window.location.hostname,
    isSecureContext: window.isSecureContext,
    userAgent: nav.userAgent,
    maxTouchPoints: nav.maxTouchPoints ?? 0,
    standalone: window.matchMedia?.("(display-mode: standalone)").matches || nav.standalone === true,
    hasServiceWorker: "serviceWorker" in navigator,
    hasPushManager: "PushManager" in window,
    hasNotification: "Notification" in window,
    permission: "Notification" in window ? Notification.permission : "unsupported",
    workerAvailable,
  };
}

// deviceLabel names this browser in the list of subscribed devices.
export function deviceLabel(userAgent: string, maxTouchPoints = 0): string {
  let device = "Computer";
  if (/iPhone/.test(userAgent)) device = "iPhone";
  else if (/iPad/.test(userAgent) || (/Macintosh/.test(userAgent) && maxTouchPoints > 1)) device = "iPad";
  else if (/Android/.test(userAgent)) device = "Android";
  else if (/Macintosh|Mac OS X/.test(userAgent)) device = "Mac";
  else if (/Windows/.test(userAgent)) device = "Windows";
  else if (/Linux/.test(userAgent)) device = "Linux";
  let browser = "Browser";
  if (/Edg\//.test(userAgent)) browser = "Edge";
  else if (/Firefox\//.test(userAgent)) browser = "Firefox";
  else if (/Chrome\/|CriOS\//.test(userAgent)) browser = "Chrome";
  else if (/Safari\//.test(userAgent)) browser = "Safari";
  return `${device} · ${browser}`;
}

// applicationServerKey turns the server's base64url VAPID key into bytes.
export function applicationServerKey(base64url: string): Uint8Array {
  const padded = base64url.replace(/-/g, "+").replace(/_/g, "/").padEnd(Math.ceil(base64url.length / 4) * 4, "=");
  const binary = atob(padded);
  const bytes = new Uint8Array(binary.length);
  for (let i = 0; i < binary.length; i += 1) bytes[i] = binary.charCodeAt(i);
  return bytes;
}

export function bytesToBase64url(buffer: ArrayBuffer | null): string {
  if (!buffer) return "";
  let binary = "";
  new Uint8Array(buffer).forEach((byte) => {
    binary += String.fromCharCode(byte);
  });
  return btoa(binary).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");
}

// subscriptionID matches the server's push.SubscriptionID: the first 16
// bytes of SHA-256 over the endpoint, in hex.
export async function subscriptionID(endpoint: string): Promise<string> {
  const digest = await crypto.subtle.digest("SHA-256", new TextEncoder().encode(endpoint));
  return Array.from(new Uint8Array(digest).slice(0, 16), (byte) => byte.toString(16).padStart(2, "0")).join("");
}

// sameKey reports whether a browser subscription was made for this server key.
export function sameKey(subscriptionKey: ArrayBuffer | null | undefined, serverKey: string): boolean {
  if (!subscriptionKey) return false;
  return bytesToBase64url(subscriptionKey) === serverKey.replace(/=+$/, "");
}
