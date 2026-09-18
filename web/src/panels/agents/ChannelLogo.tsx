import { Bot, Cable } from "lucide-react";
import type { Channel } from "../../api";
import { ChannelAvatar } from "../../ChannelAvatar";
import { CHANNEL_BRAND_LOGOS, type ChannelBrandLogo } from "./channelLogoData";

const CHANNEL_BRAND_ALIASES: Record<string, ChannelBrandLogo> = {
  feishu: "feishu",
  lark: "lark",
  wechat: "wechat",
  wecom: "wechat",
  wechat_work: "wechat",
  dingtalk: "dingtalk",
  ding: "dingtalk",
  telegram: "telegram",
  slack: "slack",
  discord: "discord",
};

export function channelBrandKey(type: string): ChannelBrandLogo | null {
  return CHANNEL_BRAND_ALIASES[type.trim().toLowerCase()] ?? null;
}

export function ChannelLogo({ channel }: { channel: Pick<Channel, "name" | "type"> }) {
  const brand = channelBrandKey(channel.type);
  if (!brand) {
    return (
      <span className="agent-channel-logo fallback" aria-hidden="true">
        <Cable size={14} />
      </span>
    );
  }
  return (
    <span className="agent-channel-logo" aria-hidden="true">
      <img src={CHANNEL_BRAND_LOGOS[brand]} alt="" />
    </span>
  );
}

export function ChannelLogoGroup({
  channels,
  emptyLabel,
  label,
}: {
  channels: Channel[];
  emptyLabel: string;
  label: string;
}) {
  if (channels.length === 0) {
    return (
      <span className="agent-channel-group empty" title={emptyLabel} aria-label={emptyLabel}>
        <Cable size={14} aria-hidden="true" />
        <span>{emptyLabel}</span>
      </span>
    );
  }

  return (
    <span className="agent-channel-group" role="group" aria-label={label}>
      {channels.map((channel) => {
        const botName = channel.bot_name || channel.name || channel.type;
        const title = [channel.type, botName, channel.name !== botName ? channel.name : ""].filter(Boolean).join(" · ");
        return (
          <span className="agent-channel-badge" key={`${channel.target_id || "local"}::${channel.id}`} title={title} role="img" aria-label={title}>
            <ChannelLogo channel={channel} />
            <span className="agent-channel-separator" aria-hidden="true">:</span>
            <ChannelAvatar channel={channel} size="compact" fallback={
              <span className="channel-avatar channel-avatar-compact fallback" aria-hidden="true"><Bot size={14} /></span>
            } />
            <span className="agent-channel-name">{botName}</span>
          </span>
        );
      })}
    </span>
  );
}
