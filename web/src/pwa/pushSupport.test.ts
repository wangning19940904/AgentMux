import { describe, expect, it } from "vitest";
import {
  applicationServerKey,
  bytesToBase64url,
  deviceLabel,
  pushBlocker,
  sameKey,
  subscriptionID,
  type PushEnvironment,
} from "./pushSupport";

const IPHONE = "Mozilla/5.0 (iPhone; CPU iPhone OS 18_1 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.1 Mobile/15E148 Safari/604.1";
const IPAD_DESKTOP_UA = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.1 Safari/605.1.15";
const ANDROID_CHROME = "Mozilla/5.0 (Linux; Android 15; Pixel 9) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Mobile Safari/537.36";
const MAC_CHROME = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36";

const ready: PushEnvironment = {
  protocol: "https:",
  hostname: "hub.example.net",
  isSecureContext: true,
  userAgent: ANDROID_CHROME,
  maxTouchPoints: 5,
  standalone: false,
  hasServiceWorker: true,
  hasPushManager: true,
  hasNotification: true,
  permission: "default",
  workerAvailable: true,
};

describe("pushBlocker", () => {
  it("allows a secure browser with a running worker", () => {
    expect(pushBlocker(ready)).toBeNull();
  });

  it("explains each reason push cannot be turned on", () => {
    expect(pushBlocker({ ...ready, protocol: "wails:" })).toBe("desktop-app");
    expect(pushBlocker({ ...ready, protocol: "http:", isSecureContext: false })).toBe("insecure");
    // Safari on iOS hides the Push API until the app is on the Home Screen.
    expect(pushBlocker({ ...ready, userAgent: IPHONE, hasPushManager: false })).toBe("ios-home-screen");
    expect(pushBlocker({ ...ready, userAgent: IPAD_DESKTOP_UA, maxTouchPoints: 5 })).toBe("ios-home-screen");
    expect(pushBlocker({ ...ready, userAgent: IPHONE, standalone: true })).toBeNull();
    expect(pushBlocker({ ...ready, hasPushManager: false })).toBe("unsupported");
    expect(pushBlocker({ ...ready, workerAvailable: false })).toBe("no-worker");
    expect(pushBlocker({ ...ready, permission: "denied" })).toBe("denied");
    // HTTPS comes first: on plain HTTP nothing else matters yet.
    expect(pushBlocker({ ...ready, isSecureContext: false, userAgent: IPHONE })).toBe("insecure");
  });
});

describe("subscription helpers", () => {
  it("labels devices", () => {
    expect(deviceLabel(IPHONE)).toBe("iPhone · Safari");
    expect(deviceLabel(IPAD_DESKTOP_UA, 5)).toBe("iPad · Safari");
    expect(deviceLabel(ANDROID_CHROME)).toBe("Android · Chrome");
    expect(deviceLabel(MAC_CHROME)).toBe("Mac · Chrome");
  });

  it("round-trips the server key", () => {
    const key = "BOr8gBA6Xxs1dWp_yWTAgW6sxPpp3dZ9wy-pAK-8Ky1KQvHn3CJsbV41yIKFU-AH4K4b5u_X6AGtFS5XoNi-9_Q";
    const bytes = applicationServerKey(key);
    expect(bytes.length).toBe(65);
    expect(bytes[0]).toBe(4);
    expect(bytesToBase64url(bytes.buffer as ArrayBuffer)).toBe(key);
    expect(sameKey(bytes.buffer as ArrayBuffer, key)).toBe(true);
    expect(sameKey(bytes.buffer as ArrayBuffer, key.replace("B", "C"))).toBe(false);
    expect(sameKey(null, key)).toBe(false);
  });

  it("names a subscription the way the server does", async () => {
    // push.SubscriptionID in Go: hex of the first 16 bytes of SHA-256.
    expect(await subscriptionID("https://web.push.apple.com/QGuQyavXutnMH8")).toBe("d8c2f451d1ba492cdf55edeac3a8e3f9");
  });
});
