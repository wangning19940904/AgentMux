import {
  Cable,
  Plus,
  RefreshCw,
  Zap,
} from "lucide-react";
import { useMemo, useState } from "react";
import { Channel, Trigger, api } from "../../api";
import { useI18n } from "../../i18n";
import { usePolling } from "../../hooks/usePolling";
import { useAsync } from "../../useAsync";
import {
  CHANNEL_FIELDS,
  EMPTY_CHANNEL,
  EMPTY_TRIGGER,
  defaultChannelConfig,
  preferredDefaultPlatform,
} from "./connectShared";
import { ChannelCard } from "./ChannelCard";
import { ChannelEditor } from "./ChannelEditor";
import { TriggerCard, TriggerEditor } from "./TriggerEditor";

export function ConnectPanel() {
  const { t } = useI18n();
  const channels = useAsync(() => api.channels(), []);
  const triggers = useAsync(() => api.triggers(), []);
  const agents = useAsync(() => api.agentInstances(), []);
  const platforms = useAsync(() => api.platforms(), []);

  const [tab, setTab] = useState<"channels" | "triggers">("channels");
  const [notice, setNotice] = useState("");
  const [busy, setBusy] = useState("");
  const [channelDraft, setChannelDraft] = useState<Partial<Channel> | null>(null);
  const [triggerDraft, setTriggerDraft] = useState<Partial<Trigger> | null>(null);

  const channelItems = channels.data ?? [];
  const triggerItems = triggers.data ?? [];
  const agentOptions = (agents.data ?? []).filter((a) => !a.id.startsWith("config:"));
  const platformOptions = platforms.data ?? [];

  usePolling(channels.reload, 15_000);

  const metrics = useMemo(() => {
    const running = channelItems.filter((c) => c.state === "running").length;
    const failed = channelItems.filter((c) => ["reconnecting", "degraded", "error"].includes(c.state ?? "")).length;
    const cron = triggerItems.filter((tr) => tr.kind === "cron" && tr.enabled).length;
    return { running, failed, cron };
  }, [channelItems, triggerItems]);

  const unhealthyChannels = channelItems.filter(
    (channel) => channel.enabled && ["reconnecting", "degraded", "error"].includes(channel.state ?? ""),
  );

  async function run(action: string, fn: () => Promise<unknown>, doneMessage: string) {
    setBusy(action);
    setNotice("");
    try {
      await fn();
      if (doneMessage) setNotice(doneMessage);
      await Promise.all([channels.reload(), triggers.reload()]);
    } catch (err) {
      setNotice(err instanceof Error ? err.message : String(err));
    } finally {
      setBusy("");
    }
  }

  const saveChannel = (draft: Partial<Channel>) =>
    run("save-channel", async () => {
      await api.upsertChannel(draft);
      setChannelDraft(null);
    }, t("connect.channelSaved"));

  const saveTrigger = (draft: Partial<Trigger>) =>
    run("save-trigger", async () => {
      await api.upsertTrigger(draft);
      setTriggerDraft(null);
    }, t("connect.triggerSaved"));

  return (
    <div className="page-stack connect-page">
      <div className="surface">
        <div className="surface-header">
          <div>
            <h2>{t("connect.title")}</h2>
            <p className="subtle-copy">{t("connect.subtitle")}</p>
          </div>
          <button className="ghost-action" onClick={() => Promise.all([channels.reload(), triggers.reload()])}>
            <RefreshCw size={15} />
            {t("common.refresh")}
          </button>
        </div>
        <div className="surface-body agent-metrics">
          <Summary label={t("connect.totalChannels")} value={channelItems.length} />
          <Summary label={t("connect.runningChannels")} value={metrics.running} />
          <Summary label={t("connect.failedChannels")} value={metrics.failed} />
          <Summary label={t("connect.totalTriggers")} value={triggerItems.length} />
          <Summary label={t("connect.activeCron")} value={metrics.cron} />
        </div>
      </div>

      <div className="connection-subtabs segmented" aria-label={t("nav.connect")}>
        <button className={tab === "channels" ? "active" : ""} onClick={() => setTab("channels")}>
          <Cable size={15} />
          <span>{t("connect.channelsTab")}</span>
        </button>
        <button className={tab === "triggers" ? "active" : ""} onClick={() => setTab("triggers")}>
          <Zap size={15} />
          <span>{t("connect.triggersTab")}</span>
        </button>
      </div>

      {notice && <div className={`session-notice ${/failed|error|invalid|required|unknown/i.test(notice) ? "error" : ""}`}>{notice}</div>}

      {unhealthyChannels.length > 0 && (
        <div className="session-notice error channel-health-alert" role="alert">
          <strong>{t("connect.healthAlertTitle")}</strong>
          <span>
            {unhealthyChannels.map((channel) => channel.bot_name || channel.name).join(", ")} · {t("connect.healthAlertHint")}
          </span>
        </div>
      )}

      {tab === "channels" && (
        <div className="surface">
          <div className="surface-header">
            <div>
              <h2>{t("connect.channelsTab")}</h2>
              <p className="subtle-copy">{t("connect.channelsHelp")}</p>
            </div>
            <button
              className="action"
              onClick={() => {
                setTriggerDraft(null);
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
                key={ch.id}
                channel={ch}
                busy={busy}
                onEdit={() => {
                  setTriggerDraft(null);
                  setChannelDraft({ ...ch, config: { ...(ch.config ?? {}) } });
                }}
                onToggle={() =>
                  run(`toggle-${ch.id}`, () => api.upsertChannel({ ...ch, enabled: !ch.enabled }), "")
                }
                onRestart={() => run(`restart-${ch.id}`, () => api.restartChannel(ch.id), t("connect.channelRestarted"))}
                onDelete={() => run(`delete-${ch.id}`, () => api.deleteChannel(ch.id), t("connect.channelDeleted"))}
              />
            ))}
          </div>
        </div>
      )}

      {tab === "triggers" && (
        <div className="surface">
          <div className="surface-header">
            <div>
              <h2>{t("connect.triggersTab")}</h2>
              <p className="subtle-copy">{t("connect.triggersHelp")}</p>
            </div>
            <button
              className="action"
              onClick={() => {
                setChannelDraft(null);
                setTriggerDraft({ ...EMPTY_TRIGGER });
              }}
            >
              <Plus size={16} />
              {t("connect.newTrigger")}
            </button>
          </div>
          <div className="surface-body agent-mini-list">
            {triggerDraft && (
              <TriggerEditor
                draft={triggerDraft}
                setDraft={setTriggerDraft}
                channels={channelItems}
                agents={agentOptions.map((a) => ({ id: a.id, name: a.name }))}
                busy={busy === "save-trigger"}
                onSave={() => triggerDraft && saveTrigger(triggerDraft)}
                onCancel={() => setTriggerDraft(null)}
              />
            )}
            {triggers.loading && <div className="empty-state">{t("common.loading")}</div>}
            {!triggers.loading && triggerItems.length === 0 && !triggerDraft && (
              <div className="empty-state">{t("connect.noTriggers")}</div>
            )}
            {triggerItems.map((tr) => (
              <TriggerCard
                key={tr.id}
                trigger={tr}
                busy={busy}
                onEdit={() => {
                  setChannelDraft(null);
                  setTriggerDraft({ ...tr });
                }}
                onRun={() => run(`run-${tr.id}`, () => api.runTrigger(tr.id), t("connect.triggerStarted"))}
                onToggle={() =>
                  run(`toggle-${tr.id}`, () => api.upsertTrigger({ ...tr, enabled: !tr.enabled }), "")
                }
                onDelete={() => run(`delete-${tr.id}`, () => api.deleteTrigger(tr.id), t("connect.triggerDeleted"))}
              />
            ))}
          </div>
        </div>
      )}
    </div>
  );
}

function Summary({ label, value }: { label: string; value: number }) {
  return (
    <div className="summary-stat">
      <span>{label}</span>
      <strong>{value}</strong>
    </div>
  );
}
