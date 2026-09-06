import { activeRemoteID, tenantScopeHeaders } from "./client";
import type { Channel } from "./types";

const avatarPath = "/api/v1/channel-avatar";

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
