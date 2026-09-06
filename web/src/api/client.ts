// HTTP client core: same-origin fetch helpers plus transparent routing of
// API calls through the selected SSH remote target.
import { beginFleetWarningUpdate, fleetWarningMessage, fleetWarningResourceKey, resetFleetWarnings } from "./fleetWarnings";
import type {
  DesktopUpdateStatus,
  FleetBatchResult,
  FleetOperation,
  LaunchAtLoginStatus,
  MachineScope,
  MeetingStreamEvent,
  OperationProgress,
  RemoteHost,
} from "./types";

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
          CheckDesktopUpdate?: () => Promise<DesktopUpdateStatus>;
          InstallDesktopUpdate?: () => Promise<DesktopUpdateStatus>;
        };
      };
    };
  }
}

const ACTIVE_REMOTE_KEY = "agentmux:active-remote";
const ACTIVE_TENANT_SCOPE_KEY = "agentmux:active-tenant-scope";
const TENANT_SCOPE_SEPARATOR = "::";
export const REMOTE_HOSTS_CHANGED_EVENT = "agentmux:remote-hosts-changed";
export const TENANT_SCOPE_CHANGED_EVENT = "agentmux:tenant-scope-changed";

export function resolveMachineScope(stored: string | null | undefined): MachineScope {
  return stored && stored.trim() ? stored : "all";
}

export function activeMachineScope(): MachineScope {
	const tenantTarget = activeTenantTargetID();
	if (tenantTarget) return tenantTarget;
  return resolveMachineScope(localStorage.getItem(ACTIVE_REMOTE_KEY));
}

export function setActiveMachineScope(scope: MachineScope) {
  const value = String(scope || "all").trim() || "all";
  localStorage.setItem(ACTIVE_REMOTE_KEY, value);
  resetFleetWarnings();
  window.dispatchEvent(new CustomEvent("agentmux:machine-scope-changed", { detail: value }));
}

export function activeRemoteID(): string {
  const scope = activeMachineScope();
  return scope === "all" || scope === "local" ? "" : scope;
}

export function setActiveRemoteID(id: string) {
  setActiveMachineScope(id || "local");
}

export function activeFleetTargetIDs(): string[] {
  const scope = activeMachineScope();
  return scope === "all" ? ["all"] : [scope];
}

export function notifyRemoteHostsChanged(hosts?: RemoteHost[]) {
  window.dispatchEvent(new CustomEvent<RemoteHost[] | undefined>(REMOTE_HOSTS_CHANGED_EVENT, {
    detail: hosts,
  }));
}

export function tenantScopeKey(tenantID: string, targetID = "local"): string {
	const tenant = tenantID.trim();
	if (!tenant) return "";
	return `${targetID.trim() || "local"}${TENANT_SCOPE_SEPARATOR}${tenant}`;
}

export function resolveTenantScope(value: string | null | undefined): { tenantID: string; targetID: string } {
	const stored = value?.trim() ?? "";
	if (!stored) return { tenantID: "", targetID: "" };
	const separator = stored.indexOf(TENANT_SCOPE_SEPARATOR);
	if (separator < 0) return { tenantID: stored, targetID: "" };
	return {
		targetID: stored.slice(0, separator).trim() || "local",
		tenantID: stored.slice(separator + TENANT_SCOPE_SEPARATOR.length).trim(),
	};
}

export function activeTenantScopeKey(): string {
	return localStorage.getItem(ACTIVE_TENANT_SCOPE_KEY) ?? "";
}

export function activeTenantScopeID(): string {
	return resolveTenantScope(activeTenantScopeKey()).tenantID;
}

export function activeTenantTargetID(): string {
	return resolveTenantScope(activeTenantScopeKey()).targetID;
}

export function setActiveTenantScopeID(id: string, targetID = "local") {
	if (id) localStorage.setItem(ACTIVE_TENANT_SCOPE_KEY, tenantScopeKey(id, targetID));
  else localStorage.removeItem(ACTIVE_TENANT_SCOPE_KEY);
  resetFleetWarnings();
  window.dispatchEvent(new CustomEvent<string>(TENANT_SCOPE_CHANGED_EVENT, { detail: id }));
	window.dispatchEvent(new CustomEvent("agentmux:machine-scope-changed", { detail: activeMachineScope() }));
}

// Marks same-origin Console requests so console-session cookie auth can
// reject cross-site requests (CSRF defense in depth alongside SameSite=Lax).
const CONSOLE_HEADER = { "X-AgentMux-Console": "1" } as const;

export function tenantScopeHeaders(path: string): Record<string, string> {
  const tenantID = activeTenantScopeID();
  const scopeable =
    (path.startsWith("/api/v1/") || path.startsWith("/v1/")) &&
    !path.startsWith("/api/v1/tenancy/");
  return tenantID && scopeable ? { "X-AgentMux-Tenant-Scope": tenantID } : {};
}

function consoleHeaders(path: string) {
  return { ...CONSOLE_HEADER, ...tenantScopeHeaders(path) };
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

async function fleetBatch<T>(
	endpoint: "query" | "execute",
	requests: FleetOperation[],
	targetIDs = activeFleetTargetIDs(),
	tenantScoped = true,
) {
  const path = `/api/v1/remote/fleet/${endpoint}`;
  const updateWarnings = beginFleetWarningUpdate(JSON.stringify([
    endpoint, tenantScoped, [...targetIDs].sort(),
    requests.map((request) => [request.method || "GET", fleetWarningResourceKey(request.path)]),
  ]));
  try {
    const res = await fetch(path, {
      method: "POST",
      cache: "no-store",
      headers: { "Content-Type": "application/json", ...(tenantScoped ? consoleHeaders(path) : CONSOLE_HEADER) },
      body: JSON.stringify({ target_ids: targetIDs, requests }),
    });
    const payload = (await res.json().catch(() => ({}))) as FleetBatchResult<T> & { error?: string };
    if (!res.ok) throw new Error(payload.error || `${path}: ${res.status}`);
    updateWarnings((payload.targets ?? []).flatMap((target) => requests.flatMap((request) => {
      const response = target.responses.find((response) => response.key === request.key);
      return response?.ok ? [] : [fleetWarningMessage(target.target.name, response?.error || "unavailable")];
    })));
    return payload;
  } catch (error) {
    updateWarnings([error instanceof Error ? error.message : String(error)]);
    throw error;
  }
}

export function fleetQuery<T>(requests: FleetOperation[], targetIDs?: string[]) {
  return fleetBatch<T>("query", requests, targetIDs);
}

// Identity controls stay in the administrator's controller scope even while
// previewing a tenant. Resource reads continue through fleetQuery and carry
// the selected tenant header.
export function fleetAdminQuery<T>(requests: FleetOperation[], targetIDs: string[] = ["all"]) {
	return fleetBatch<T>("query", requests, targetIDs, false);
}

export function fleetExecute<T>(requests: FleetOperation[], targetIDs?: string[]) {
  return fleetBatch<T>("execute", requests, targetIDs);
}

export async function get<T>(path: string): Promise<T> {
  const res = await fetch(apiPath(path), { cache: "no-store", headers: consoleHeaders(path) });
  if (!res.ok) throw new Error(`${path}: ${res.status}`);
  return res.json() as Promise<T>;
}

// System settings belong to the Console application itself even while the
// operator is inspecting a remote target, so these requests bypass routing.
export async function getLocal<T>(path: string): Promise<T> {
  const res = await fetch(path, { cache: "no-store", headers: CONSOLE_HEADER });
  if (!res.ok) throw new Error(`${path}: ${res.status}`);
  return res.json() as Promise<T>;
}

export async function getChecked<T>(path: string): Promise<T> {
  const res = await fetch(apiPath(path), { cache: "no-store", headers: consoleHeaders(path) });
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
    headers: { "Content-Type": "application/json", ...consoleHeaders(path) },
    body: JSON.stringify(body),
  });
  if (!res.ok) throw new Error(`${path}: ${res.status}`);
  return res.json() as Promise<T>;
}

export async function put<T>(path: string, body: unknown): Promise<T> {
  const res = await fetch(apiPath(path), {
    method: "PUT",
    headers: { "Content-Type": "application/json", ...consoleHeaders(path) },
    body: JSON.stringify(body),
  });
  if (!res.ok) throw new Error(`${path}: ${res.status}`);
  return res.json() as Promise<T>;
}

export async function putLocal<T>(path: string, body: unknown): Promise<T> {
  const res = await fetch(path, {
    method: "PUT",
    headers: { "Content-Type": "application/json", ...consoleHeaders(path) },
    body: JSON.stringify(body),
  });
  if (!res.ok) throw new Error(`${path}: ${res.status}`);
  return res.json() as Promise<T>;
}

export async function postChecked<T>(path: string, body: unknown): Promise<T> {
  const res = await fetch(apiPath(path), {
    method: "POST",
    headers: { "Content-Type": "application/json", ...consoleHeaders(path) },
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
    headers: { "Content-Type": "application/json", ...CONSOLE_HEADER },
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
  const path = `/api/v1/remote/meetings/events?target_id=${encodeURIComponent(activeMachineScope())}`;
  const res = await fetch(apiPath(path), {
    cache: "no-store",
    headers: { Accept: "text/event-stream", ...consoleHeaders(path) },
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
  const res = await fetch(apiPath(path), { method: "DELETE", headers: consoleHeaders(path) });
  if (!res.ok) throw new Error(`${path}: ${res.status}`);
  return res.json() as Promise<T>;
}
