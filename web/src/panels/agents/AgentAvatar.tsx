import { Bot } from "lucide-react";
import type { Channel } from "../../api";
import { ChannelAvatar } from "../../ChannelAvatar";

export function AgentAvatar({ channels }: { channels: Channel[] }) {
  // The caller supplies this agent's channels from the same machine. Prefer
  // the first available bot portrait when more than one channel is bound.
  const channel = channels.find((item) => item.bot_avatar_proxy_url || item.bot_avatar_url);
  const fallback = <span className="provider-icon agent-avatar" aria-hidden="true"><Bot size={15} /></span>;
  return channel ? <ChannelAvatar channel={channel} size="agent" fallback={fallback} /> : fallback;
}
