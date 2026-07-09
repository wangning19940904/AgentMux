import { Cable } from "lucide-react";
import { useEffect, useState } from "react";
import type { Channel } from "./api";

type ChannelAvatarProps = {
  channel: Pick<Channel, "bot_avatar_proxy_url" | "bot_avatar_url">;
  size?: "default" | "small";
};

export function ChannelAvatar({ channel, size = "default" }: ChannelAvatarProps) {
  const [failed, setFailed] = useState(false);
  const avatarURL = channel.bot_avatar_proxy_url || channel.bot_avatar_url;
  const className = `channel-avatar${size === "small" ? " channel-avatar-small" : ""}`;

  useEffect(() => setFailed(false), [avatarURL]);

  if (avatarURL && !failed) {
    return (
      <span className={className} aria-hidden="true">
        <img src={avatarURL} alt="" referrerPolicy="no-referrer" onError={() => setFailed(true)} />
      </span>
    );
  }

  return (
    <span className={`${className} fallback`} aria-hidden="true">
      <Cable size={size === "small" ? 15 : 17} />
    </span>
  );
}
