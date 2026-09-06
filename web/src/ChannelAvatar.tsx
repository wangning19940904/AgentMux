import { Cable } from "lucide-react";
import { useEffect, useState } from "react";
import type { Channel } from "./api";
import { channelAvatarRequestPath, fetchChannelAvatar } from "./api/channelAvatar";

type ChannelAvatarProps = {
  channel: Pick<Channel, "id" | "target_id" | "bot_avatar_proxy_url" | "bot_avatar_url">;
  size?: "default" | "small";
};

export function ChannelAvatar({ channel, size = "default" }: ChannelAvatarProps) {
  const requestPath = channel.bot_avatar_proxy_url || channel.bot_avatar_url
    ? channelAvatarRequestPath(channel)
    : "";
  const directURL = channel.bot_avatar_url || "";
  return <ChannelAvatarImage key={`${requestPath}|${directURL}`} requestPath={requestPath} directURL={directURL} size={size} />;
}

function ChannelAvatarImage({ requestPath, directURL, size }: {
  requestPath: string;
  directURL: string;
  size: "default" | "small";
}) {
  const [avatarURL, setAvatarURL] = useState("");
  const className = `channel-avatar${size === "small" ? " channel-avatar-small" : ""}`;

  useEffect(() => {
    if (!requestPath) return;
    const controller = new AbortController();
    let objectURL = "";
    fetchChannelAvatar(requestPath, controller.signal).then((blob) => {
      if (controller.signal.aborted) return;
      objectURL = URL.createObjectURL(blob);
      setAvatarURL(objectURL);
    }).catch(() => {
      if (!controller.signal.aborted) setAvatarURL(directURL);
    });
    return () => {
      controller.abort();
      if (objectURL) URL.revokeObjectURL(objectURL);
    };
  }, [requestPath, directURL]);

  if (avatarURL) {
    return (
      <span className={className} aria-hidden="true">
        <img src={avatarURL} alt="" referrerPolicy="no-referrer"
          onError={() => setAvatarURL(avatarURL !== directURL ? directURL : "")} />
      </span>
    );
  }

  return (
    <span className={`${className} fallback`} aria-hidden="true">
      <Cable size={size === "small" ? 15 : 17} />
    </span>
  );
}
