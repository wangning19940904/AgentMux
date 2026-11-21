import {
  Plus,
  RefreshCw,
} from "lucide-react";
import { useMemo, useState } from "react";
import { Channel, api } from "../../api";
import { useI18n } from "../../i18n";
import { usePolling } from "../../hooks/usePolling";
import { useAsync } from "../../useAsync";
import { targetKey } from "../../components/TargetBadge";
import {
  CHANNEL_FIELDS,
  EMPTY_CHANNEL,
  defaultChannelConfig,
  preferredDefaultPlatform,
} from "./connectShared";
import { ChannelCard } from "./ChannelCard";
import { ChannelEditor } from "./ChannelEditor";
import { TriggerList } from "./TriggerList";

// Triggers have their own page (TriggersPanel); Connect keeps channels and
// schedules.
export type ConnectView = "channels" | "schedules";

export function ConnectPanel({ view = "channels" }: { view?: ConnectView }) {
  const { t } = useI18n();
  const channels = useAsync(() => api.channels(), []);
  const triggers = useAsync(() => api.triggers(), []);
  const agents = useAsync(() => api.agentInstances(), []);
  const platforms = useAsync(() => api.platforms(), []);

  const [notice, setNotice] = useState("");
  const [noticeError, setNoticeError] = useState(false);
  const [busy, setBusy] = useState("");
  const [channelDraft, setChannelDraft] = useState<Partial<Channel> | null>(null);

  const channelItems = channels.data ?? [];
  const allTriggerItems = triggers.data ?? [];
  const triggerItems = allTriggerItems.filter((trigger) => trigger.kind === "cron");
  const agentOptions = (agents.data ?? []).filter((a) => !a.id.startsWith("config:"));
  const platformOptions = platforms.data ?? [];

  usePolling(channels.reload, 15_000);

  const metrics = useMemo(() => {
    const active = triggerItems.filter((trigger) => trigger.enabled).length;
    return { active };
  }, [triggerItems]);


  const unhealthyChannels = channelItems.filter(
    (channel) => channel.enabled && ["reconnecting", "degraded", "error"].includes(channel.state ?? ""),
  );

  async function run(action: string, fn: () => Promise<unknown>, doneMessage: string) {
    setBusy(action);
    setNotice("");
    setNoticeError(false);
    try {
      await fn();
      if (doneMessage) setNotice(doneMessage);
      await Promise.all([channels.reload(), triggers.reload()]);
    } catch (err) {
      setNotice(err instanceof Error ? err.message : String(err));
      setNoticeError(true);
    } finally {
      setBusy("");
    }
  }

  const saveChannel = (draft: Partial<Channel>) =>
    run("save-channel", async () => {
      await api.upsertChannel(draft);
      setChannelDraft(null);
    }, t("connect.channelSaved"));

  return (
    <div className="page-stack connect-page">
      {view !== "channels" && (
        <div className="surface connect-summary">
          <div className="surface-header">
            <div>
              <p className="subtle-copy">{t("connect.schedulesHelp")}</p>
            </div>
            <button className="ghost-action" onClick={() => Promise.all([channels.reload(), triggers.reload()])}>
              <RefreshCw size={15} />
              {t("common.refresh")}
            </button>
          </div>
          <div className="surface-body agent-metrics">
            <Summary label={t("connect.totalSchedules")} value={triggerItems.length} />
            <Summary label={t("connect.activeSchedules")} value={metrics.active} />
          </div>
        </div>
      )}

      {notice && <div className={`session-notice${noticeError ? " error" : ""}`} role={noticeError ? "alert" : "status"}>{notice}</div>}

      {view === "channels" && unhealthyChannels.length > 0 && (
        <div className="session-notice error channel-health-alert" role="alert">
          <strong>{t("connect.healthAlertTitle")}</strong>
          <span>
            {unhealthyChannels.map((channel) => channel.bot_name || channel.name).join(", ")} · {t("connect.healthAlertHint")}
          </span>
        </div>
      )}

      {view === "channels" && (
        <div className="surface">
          <div className="surface-header">
            <div>
              <h2>{t("connect.channelsList")}</h2>
            </div>
            <button
              className="action"
              onClick={() => {
                const type = preferredDefaultPlatform(platformOptions);
                setChannelDraft({ ...EMPTY_CHANNEL, type, config: defaultChannelConfig(type) });
              }}
            >
              <Plus size={16} />
              {t("connect.newChannel")}
            </button>
          </div>
          <div className="surface-body agent-mini-list">
            {channelDraft && (
              <ChannelEditor
                draft={channelDraft}
                setDraft={setChannelDraft}
                platforms={platformOptions.length > 0 ? platformOptions : Object.keys(CHANNEL_FIELDS)}
                agents={agentOptions.map((a) => ({ id: a.id, name: a.name, runtime_id: a.runtime_id }))}
                busy={busy === "save-channel"}
                onSave={() => channelDraft && saveChannel(channelDraft)}
                onAutoSave={(draft) => saveChannel(draft)}
                onCancel={() => setChannelDraft(null)}
              />
            )}
            {channels.loading && <div className="empty-state">{t("common.loading")}</div>}
            {!channels.loading && channelItems.length === 0 && !channelDraft && (
              <div className="empty-state">{t("connect.noChannels")}</div>
            )}
            {channelItems.map((ch) => (
              <ChannelCard
                key={targetKey(ch.target_id, ch.id)}
                channel={ch}
                busy={busy}
                onEdit={() => setChannelDraft({ ...ch, config: { ...(ch.config ?? {}) } })}
                onToggle={() =>
                  run(`toggle-${ch.id}`, () => api.upsertChannel({ ...ch, enabled: !ch.enabled }), "")
                }
                onRestart={() => run(`restart-${ch.id}`, () => api.restartChannel(ch.id, ch.target_id), t("connect.channelRestarted"))}
                onDelete={() => run(`delete-${ch.id}`, () => api.deleteChannel(ch.id, ch.target_id), t("connect.channelDeleted"))}
              />
            ))}
          </div>
          {!channels.loading && (
            <div className="surface-footer connect-list-count">
              {t("connect.channelCount", { count: channelItems.length })}
            </div>
          )}
        </div>
      )}

      {view === "schedules" && (
        <TriggerList
          triggers={allTriggerItems}
          loading={triggers.loading}
          channels={channelItems}
          agents={agentOptions.map((a) => ({ id: a.id, name: a.name }))}
          kinds={["cron"]}
          title={t("connect.schedulesList")}
          newLabel={t("connect.newSchedule")}
          emptyLabel={t("connect.noSchedules")}
          onChanged={triggers.reload}
        />
      )}
    </div>
  );
}

export function Summary({ label, value }: { label: string; value: number }) {
  return (
    <div className="summary-stat">
      <span>{label}</span>
      <strong>{value}</strong>
    </div>
  );
}
