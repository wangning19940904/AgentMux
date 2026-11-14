import { Cable } from "lucide-react";
import { useEffect, useState, type ReactNode } from "react";
import type { Channel } from "./api";
import { channelAvatarRequestPath, fetchChannelAvatar } from "./api/channelAvatar";

type ChannelAvatarProps = {
  channel: Pick<Channel, "id" | "target_id" | "bot_avatar_proxy_url" | "bot_avatar_url">;
  size?: "default" | "small" | "compact" | "agent";
  fallback?: ReactNode;
};

export function ChannelAvatar({ channel, size = "default", fallback }: ChannelAvatarProps) {
  const requestPath = channel.bot_avatar_proxy_url || channel.bot_avatar_url
    ? channelAvatarRequestPath(channel)
    : "";
  const directURL = channel.bot_avatar_url || "";
  return <ChannelAvatarImage key={`${requestPath}|${directURL}`} requestPath={requestPath} directURL={directURL} size={size} fallback={fallback} />;
}

function ChannelAvatarImage({ requestPath, directURL, size, fallback }: {
  requestPath: string;
  directURL: string;
  size: NonNullable<ChannelAvatarProps["size"]>;
  fallback?: ReactNode;
}) {
  const [avatarURL, setAvatarURL] = useState("");
  const className = `channel-avatar${size !== "default" ? ` channel-avatar-${size}` : ""}`;

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

  if (fallback !== undefined) return <>{fallback}</>;

  return (
    <span className={`${className} fallback`} aria-hidden="true">
      <Cable size={size === "default" ? 17 : 15} />
    </span>
  );
}
