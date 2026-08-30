import {
  Pencil,
  RefreshCw,
  Trash2,
} from "lucide-react";
import { Channel } from "../../api";
import { ChannelAvatar } from "../../ChannelAvatar";
import { useI18n } from "../../i18n";
import { OwnerBadge } from "../agents/AgentsPanel";
import { TargetBadge } from "../../components/TargetBadge";
import {
  formatChannelTime,
  stateBadge,
} from "./connectShared";

export function ChannelCard({
  channel,
  busy,
  onEdit,
  onToggle,
  onRestart,
  onDelete,
}: {
  channel: Channel;
  busy: string;
  onEdit: () => void;
  onToggle: () => void;
  onRestart: () => void;
  onDelete: () => void;
}) {
  const { t } = useI18n();
  const badge = stateBadge(channel.state, channel.enabled, t);
  const displayName = channel.bot_name || channel.name;
  const subtitle = [
    channel.bot_name && channel.name !== channel.bot_name ? channel.name : "",
    channel.type,
    channel.agent_name || t("connect.noAgent"),
  ]
    .filter(Boolean)
    .join(" · ");
  const healthTimes = [
    channel.last_heartbeat_at ? `${t("connect.lastHeartbeat")}: ${formatChannelTime(channel.last_heartbeat_at)}` : "",
    channel.last_inbound_at ? `${t("connect.lastInbound")}: ${formatChannelTime(channel.last_inbound_at)}` : "",
  ].filter(Boolean);
  return (
    <div className="route-card">
      <div className="agent-list-main">
        <ChannelAvatar channel={channel} />
        <span className="channel-card-copy">
          <strong title={displayName}>{displayName}</strong>
          <small title={subtitle}>{subtitle}</small>
        </span>
        <span className={`status-badge ${badge.className}`}>
          <span className="status-dot" />
          {badge.label}
        </span>
        <OwnerBadge resource={channel} />
        <TargetBadge target_id={channel.target_id} target_name={channel.target_name} />
      </div>
      {channel.error && <div className="session-notice error">{channel.error}</div>}
      {healthTimes.length > 0 && <div className="channel-health-meta">{healthTimes.join(" · ")}</div>}
      <div className="table-actions">
        <button className="ghost-action" onClick={onEdit}>
          <Pencil size={14} />
          {t("common.edit")}
        </button>
        <button className="ghost-action" disabled={busy === `toggle-${channel.id}`} onClick={onToggle}>
          {channel.enabled ? t("common.disable") : t("common.enable")}
        </button>
        <button
          className="ghost-action"
          disabled={!channel.enabled || busy === `restart-${channel.id}`}
          onClick={onRestart}
        >
          <RefreshCw size={14} />
          {t("connect.restart")}
        </button>
        <button className="ghost-action danger-action" disabled={busy === `delete-${channel.id}`} onClick={onDelete}>
          <Trash2 size={14} />
          {t("common.delete")}
        </button>
      </div>
    </div>
  );
}
