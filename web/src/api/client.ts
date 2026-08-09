// HTTP client core: same-origin fetch helpers plus transparent routing of
// API calls through the selected SSH remote target.
import type { LaunchAtLoginStatus, MeetingStreamEvent, OperationProgress, RemoteHost } from "./types";

declare global {
  interface Window {
    go?: {
      main?: {
        App?: {
          SelectDirectory?: (defaultDirectory?: string) => Promise<string>;
          GetLaunchAtLogin?: () => Promise<LaunchAtLoginStatus>;
          SetLaunchAtLogin?: (enabled: boolean) => Promise<LaunchAtLoginStatus>;
          OpenLocalWebUI?: () => Promise<void>;
          OpenExternalURL?: (url: string) => Promise<void>;
        };
      };
    };
  }
}

const ACTIVE_REMOTE_KEY = "agentmux:active-remote";
export const REMOTE_HOSTS_CHANGED_EVENT = "agentmux:remote-hosts-changed";

export function activeRemoteID(): string {
  return localStorage.getItem(ACTIVE_REMOTE_KEY) ?? "";
}

export function setActiveRemoteID(id: string) {
  if (id) localStorage.setItem(ACTIVE_REMOTE_KEY, id);
  else localStorage.removeItem(ACTIVE_REMOTE_KEY);
}

export function notifyRemoteHostsChanged(hosts?: RemoteHost[]) {
  window.dispatchEvent(new CustomEvent<RemoteHost[] | undefined>(REMOTE_HOSTS_CHANGED_EVENT, {
    detail: hosts,
  }));
}

export function apiPath(path: string) {
  // All clients use same-origin API requests. Vite proxies them in development,
  // the Go web server handles them in a browser, and the Wails asset middleware
  // proxies them inside the desktop process without exposing HTTP to WebKit.
  //
  // Remote-control management always stays local. All other API calls are
  // transparently routed through the selected SSH target.
  const remoteID = activeRemoteID();
  if (
    remoteID &&
    path.startsWith("/api/v1/") &&
    !path.startsWith("/api/v1/remote/")
  ) {
    return `/api/v1/remote/proxy/${encodeURIComponent(remoteID)}/${path.slice("/api/v1/".length)}`;
  }
  return path;
}

export async function get<T>(path: string): Promise<T> {
  const res = await fetch(apiPath(path), { cache: "no-store" });
  if (!res.ok) throw new Error(`${path}: ${res.status}`);
  return res.json() as Promise<T>;
}

export async function getChecked<T>(path: string): Promise<T> {
  const res = await fetch(apiPath(path), { cache: "no-store" });
  const payload = (await res.json().catch(() => ({}))) as Record<string, unknown>;
  if (!res.ok) {
    const message = typeof payload.error === "string" ? payload.error : `${path}: ${res.status}`;
    throw new Error(message);
  }
  return payload as T;
}

export async function post<T>(path: string, body: unknown): Promise<T> {
  const res = await fetch(apiPath(path), {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(body),
  });
  if (!res.ok) throw new Error(`${path}: ${res.status}`);
  return res.json() as Promise<T>;
}

export async function put<T>(path: string, body: unknown): Promise<T> {
  const res = await fetch(apiPath(path), {
    method: "PUT",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(body),
  });
  if (!res.ok) throw new Error(`${path}: ${res.status}`);
  return res.json() as Promise<T>;
}

export async function postChecked<T>(path: string, body: unknown): Promise<T> {
  const res = await fetch(apiPath(path), {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(body),
  });
  const payload = (await res.json().catch(() => ({}))) as Record<string, unknown>;
  if (!res.ok) {
    const message = typeof payload.error === "string" ? payload.error : `${path}: ${res.status}`;
    throw new Error(message);
  }
  return payload as T;
}

export async function postProgress<T>(
  path: string,
  body: unknown,
  onProgress: (progress: OperationProgress) => void,
): Promise<T> {
  const res = await fetch(apiPath(path), {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(body),
  });
  if (!res.ok) {
    const payload = (await res.json().catch(() => ({}))) as Record<string, unknown>;
    const message = typeof payload.error === "string" ? payload.error : `${path}: ${res.status}`;
    throw new Error(message);
  }
  if (!res.body) throw new Error("Installation progress stream is unavailable.");

  const reader = res.body.getReader();
  const decoder = new TextDecoder();
  let buffer = "";
  let finalResult: T | undefined;
	let streamError = "";

  const consumeEvent = (block: string) => {
    const data = block
      .split(/\r?\n/)
      .filter((line) => line.startsWith("data:"))
      .map((line) => line.slice(5).trimStart())
      .join("\n");
    if (!data) return;
    const event = JSON.parse(data) as {
      type?: string;
      progress?: OperationProgress;
      result?: T;
		error?: string;
    };
    if (event.type === "progress" && event.progress) onProgress(event.progress);
    if (event.type === "result" && event.result !== undefined) finalResult = event.result;
		if (event.type === "error") streamError = event.error || "Operation failed.";
  };

  while (true) {
    const { done, value } = await reader.read();
    buffer += decoder.decode(value, { stream: !done });
    const blocks = buffer.split(/\r?\n\r?\n/);
    buffer = blocks.pop() ?? "";
    blocks.forEach(consumeEvent);
    if (done) break;
  }
  if (buffer.trim()) consumeEvent(buffer);
	if (streamError) throw new Error(streamError);
  if (finalResult === undefined) throw new Error("Installation progress stream ended unexpectedly.");
  return finalResult;
}

export async function streamMeetingEvents(
  signal: AbortSignal,
  onEvent: (eventName: "ready" | "meeting.changed" | "meeting.activity" | "meeting.turn", payload: MeetingStreamEvent) => void,
): Promise<void> {
  const path = "/api/v1/remote/meetings/events";
  const res = await fetch(apiPath(path), {
    cache: "no-store",
    headers: { Accept: "text/event-stream" },
    signal,
  });
  if (!res.ok) throw new Error(`${path}: ${res.status}`);
  if (!res.body) throw new Error("Meeting event stream is unavailable.");

  const reader = res.body.getReader();
  const decoder = new TextDecoder();
  let buffer = "";
  const consumeBlock = (block: string) => {
    const eventLine = block.split(/\r?\n/).find((line) => line.startsWith("event:"));
    const eventName = eventLine?.slice(6).trim();
    if (eventName === "ready" || eventName === "meeting.changed" || eventName === "meeting.activity" || eventName === "meeting.turn") {
      const data = block.split(/\r?\n/).filter((line) => line.startsWith("data:")).map((line) => line.slice(5).trimStart()).join("\n");
      let payload: MeetingStreamEvent = {};
      if (data) { try { payload = JSON.parse(data) as MeetingStreamEvent; } catch { payload = {}; } }
      onEvent(eventName, payload);
    }
  };

  while (true) {
    const { done, value } = await reader.read();
    buffer += decoder.decode(value, { stream: !done });
    const blocks = buffer.split(/\r?\n\r?\n/);
    buffer = blocks.pop() ?? "";
    blocks.forEach(consumeBlock);
    if (done) break;
  }
  if (buffer.trim()) consumeBlock(buffer);
  if (!signal.aborted) throw new Error("Meeting event stream ended unexpectedly.");
}

export async function del<T>(path: string): Promise<T> {
  const res = await fetch(apiPath(path), { method: "DELETE" });
  if (!res.ok) throw new Error(`${path}: ${res.status}`);
  return res.json() as Promise<T>;
}
