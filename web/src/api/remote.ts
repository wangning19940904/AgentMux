// Remote host operations that need custom error shaping (connection guidance)
// rather than the generic fetch helpers.
import { apiPath, postChecked } from "./client";
import { RemoteConnectionError } from "./types";
import type { DiscoveredRemoteHost, RemoteImportResult, RemoteTestResult, RemoteUpdateResult } from "./types";

export async function testRemoteHost(id: string, trustOnFirstUse: boolean): Promise<RemoteTestResult> {
  const path = `/api/v1/remote/hosts/test?id=${encodeURIComponent(id)}`;
  const response = await fetch(apiPath(path), {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ trust_on_first_use: trustOnFirstUse }),
  });
  const payload = (await response.json().catch(() => ({}))) as Record<string, unknown>;
  if (!response.ok) {
    throw new RemoteConnectionError(
      typeof payload.error === "string" ? payload.error : `${path}: ${response.status}`,
      typeof payload.code === "string" ? payload.code : undefined,
      typeof payload.host_key_fingerprint === "string" ? payload.host_key_fingerprint : undefined,
    );
  }
  return payload as unknown as RemoteTestResult;
}

export async function updateRemoteHost(id: string): Promise<RemoteUpdateResult> {
  return postChecked<RemoteUpdateResult>(
    `/api/v1/remote/hosts/update?id=${encodeURIComponent(id)}`,
    {},
  );
}

export async function statusRemoteHost(id: string): Promise<RemoteTestResult> {
  return postChecked<RemoteTestResult>(
    `/api/v1/remote/hosts/status?id=${encodeURIComponent(id)}`,
    {},
  );
}

export async function importRemoteHost(
  host: DiscoveredRemoteHost,
  trustOnFirstUse: boolean,
): Promise<RemoteImportResult> {
  const path = "/api/v1/remote/hosts/import";
  const response = await fetch(apiPath(path), {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({
      ...host,
      remote_addr: "127.0.0.1:8765",
      trust_on_first_use: trustOnFirstUse,
    }),
  });
  const payload = (await response.json().catch(() => ({}))) as Record<string, unknown>;
  if (!response.ok) {
    throw new RemoteConnectionError(
      typeof payload.error === "string" ? payload.error : `${path}: ${response.status}`,
      typeof payload.code === "string" ? payload.code : undefined,
      typeof payload.host_key_fingerprint === "string" ? payload.host_key_fingerprint : undefined,
    );
  }
  return payload as unknown as RemoteImportResult;
}
