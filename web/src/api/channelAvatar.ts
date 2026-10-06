import { activeRemoteID, activeTenantScopeKey, tenantScopeHeaders } from "./client";
import type { Channel } from "./types";

const avatarPath = "/api/v1/channel-avatar";
const avatars = new Map<string, { expiresAt: number; pending: Promise<Blob> }>();

export function clearChannelAvatarCache() { avatars.clear(); }

export function channelAvatarRequestPath(channel: Pick<Channel, "id" | "target_id">): string {
  // Fleet rows carry their own target; a single-machine response uses the
  // selected target. Never send a remote daemon's loopback URL to the browser.
  const targetID = channel.target_id ?? activeRemoteID();
  const path = `${avatarPath}?id=${encodeURIComponent(channel.id)}`;
  return targetID && targetID !== "local" && targetID !== "all"
    ? `/api/v1/remote/proxy/${encodeURIComponent(targetID)}/channel-avatar?id=${encodeURIComponent(channel.id)}`
    : path;
}

export async function fetchChannelAvatar(path: string, signal: AbortSignal): Promise<Blob> {
  const key = JSON.stringify([activeTenantScopeKey(), path]);
  const cached = avatars.get(key);
  if (cached && cached.expiresAt > Date.now()) return cached.pending;
  // Individual components may unmount while another still needs this image.
  // Deduplicate requests independently of any one component's abort signal.
  if (signal.aborted) throw new DOMException("Aborted", "AbortError");
  const controller = new AbortController();
  const timeout = setTimeout(() => controller.abort(), 10_000);
  const entry = { expiresAt: Date.now() + 300_000, pending: Promise.resolve(null as unknown as Blob) };
  entry.pending = requestChannelAvatar(path, controller.signal).catch((error) => {
    if (avatars.get(key) === entry) avatars.delete(key);
    throw error;
  }).finally(() => clearTimeout(timeout));
  if (avatars.size >= 128) avatars.delete(avatars.keys().next().value!);
  avatars.set(key, entry);
  return entry.pending;
}

async function requestChannelAvatar(path: string, signal: AbortSignal): Promise<Blob> {
  // Fetch instead of a bare <img> request so Console sessions and tenant
  // previews carry the same authentication/scope headers as other API reads.
  const response = await fetch(path, {
    headers: { "X-AgentMux-Console": "1", ...tenantScopeHeaders(avatarPath) },
    signal,
  });
  if (!response.ok) throw new Error(`Channel avatar: ${response.status}`);
  const blob = await response.blob();
  // Older daemons may serve the SPA fallback for an unknown API route.
  if (!blob.type.startsWith("image/")) throw new Error("Channel avatar is not an image");
  return blob;
}
