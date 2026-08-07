// Observability session handling: exchanges a loopback nonce for a SameSite
// session (plus a bearer token inside the Wails WebView) and retries once on
// 401 so an expired session heals transparently.
import { apiPath } from "./client";

let observationSessionPromise: Promise<void> | null = null;
let observationBearer = "";

async function ensureObservationSession(): Promise<void> {
  if (observationSessionPromise) return observationSessionPromise;
  observationSessionPromise = (async () => {
    const nonceResponse = await fetch(apiPath("/api/v1/observability/session/nonce"), {
      credentials: "include",
    });
    if (!nonceResponse.ok) throw new Error(`observability nonce: ${nonceResponse.status}`);
    const noncePayload = (await nonceResponse.json()) as { nonce?: string };
    if (!noncePayload.nonce) throw new Error("observability nonce missing");
    const sessionHeaders = new Headers({ "Content-Type": "application/json" });
    if (
      window.location.hostname.toLowerCase() === "wails.localhost" ||
      Boolean(window.go?.main?.App)
    ) {
      // The native WebView cannot rely on the daemon's SameSite cookie.
      // Identify the trusted Wails flow so the server also returns a
      // short-lived, memory-only bearer for subsequent observability calls.
      sessionHeaders.set("X-AgentMux-Desktop", "1");
    }
    const sessionResponse = await fetch(apiPath("/api/v1/observability/session"), {
      method: "POST",
      credentials: "include",
      headers: sessionHeaders,
      body: JSON.stringify({ nonce: noncePayload.nonce }),
    });
    const sessionPayload = (await sessionResponse.json().catch(() => ({}))) as {
      error?: string;
      session_token?: string;
    };
    if (!sessionResponse.ok) {
      throw new Error(sessionPayload.error || `observability session: ${sessionResponse.status}`);
    }
    observationBearer = sessionPayload.session_token || "";
  })().catch((error) => {
    observationSessionPromise = null;
    throw error;
  });
  return observationSessionPromise;
}

export async function observationFetch(path: string, init: RequestInit = {}, retry = true): Promise<Response> {
  await ensureObservationSession();
  const headers = new Headers(init.headers);
  if (observationBearer) headers.set("Authorization", `Bearer ${observationBearer}`);
  const response = await fetch(apiPath(path), { ...init, headers, credentials: "include" });
  if (response.status === 401 && retry) {
    observationBearer = "";
    observationSessionPromise = null;
    return observationFetch(path, init, false);
  }
  return response;
}

export async function observationGet<T>(path: string): Promise<T> {
  const response = await observationFetch(path);
  if (!response.ok) throw new Error(`${path}: ${response.status}`);
  return response.json() as Promise<T>;
}

export async function observationPost<T>(path: string, body: unknown): Promise<T> {
  const response = await observationFetch(path, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(body),
  });
  const payload = (await response.json().catch(() => ({}))) as Record<string, unknown>;
  if (!response.ok) {
    const message = typeof payload.error === "string" ? payload.error : `${path}: ${response.status}`;
    throw new Error(message);
  }
  return payload as T;
}
