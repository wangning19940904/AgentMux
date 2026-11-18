import { Bot } from "lucide-react";
import type { Channel } from "../../api";
import { ChannelAvatar } from "../../ChannelAvatar";
import { PresetAvatar, findPreset, isImageAvatar, presetID } from "./avatars";

// AgentAvatar is an agent's portrait: the one picked for it, a preset or an
// upload, or else the portrait of the bot it answers through. The caller
// supplies this agent's channels from the same machine; the first with a bot
// portrait is used when more than one is bound.
export function AgentAvatar({ channels, avatar, className = "", iconSize = 15 }: {
  channels: Channel[];
  avatar?: string;
  className?: string;
  iconSize?: number;
}) {
  const preset = findPreset(presetID(avatar));
  if (preset) return <PresetAvatar preset={preset} className={className} iconSize={iconSize} />;
  if (isImageAvatar(avatar)) {
    return (
      <span className={`channel-avatar channel-avatar-agent agent-avatar-image${className ? ` ${className}` : ""}`} aria-hidden="true">
        <img src={avatar} alt="" />
      </span>
    );
  }
  const channel = channels.find((item) => item.bot_avatar_proxy_url || item.bot_avatar_url);
  const fallback = <span className={`provider-icon agent-avatar${className ? ` ${className}` : ""}`} aria-hidden="true"><Bot size={iconSize} /></span>;
  return channel ? <ChannelAvatar channel={channel} size="agent" fallback={fallback} /> : fallback;
}
