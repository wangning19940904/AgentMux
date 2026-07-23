import {
  CalendarClock,
  Cable,
  CheckCircle2,
  Copy,
  Loader2,
  Pencil,
  Play,
  Plus,
  RefreshCw,
  Save,
  Smartphone,
  Trash2,
  Webhook,
  XCircle,
  Zap,
} from "lucide-react";
import { QRCodeSVG } from "qrcode.react";
import { type Dispatch, type SetStateAction, useEffect, useMemo, useRef, useState } from "react";
import { Channel, FeishuSetupPollResponse, Trigger, api } from "../api";
import { ChannelAvatar } from "../ChannelAvatar";
import { useI18n } from "../i18n";
import { useAsync } from "../useAsync";

// Config fields rendered per channel type (secret-ish values round-trip as
// "<redacted>"; the server restores originals when unchanged).
const FEISHU_FIELDS = [
  { key: "app_id", labelKey: "connect.cfgAppId" },
  { key: "app_secret", labelKey: "connect.cfgAppSecret", secret: true },
];

const FEISHU_DEFAULTS = {
  reply_scope: "dm_and_mentions",
  reply_mode: "stream_message",
  ack_reaction_enabled: "true",
  ack_reaction_emojis: "OK,THUMBSUP,MUSCLE,THANKS",
  codex_control_enabled: "false",
  allowed_user_ids: "",
  admin_user_ids: "",
  codex_max_queue: "20",
  codex_turn_timeout_minutes: "20",
};

const FEISHU_REPLY_SCOPES = [
  { value: "dm_and_mentions", labelKey: "connect.replyScopeDmMentions" },
  { value: "all", labelKey: "connect.replyScopeAll" },
  { value: "mentions_only", labelKey: "connect.replyScopeMentionsOnly" },
];

const FEISHU_REPLY_MODES = [
  { value: "stream_message", labelKey: "connect.replyModeStreamMessage" },
  { value: "stream_card", labelKey: "connect.replyModeStreamCard" },
];

const CHANNEL_FIELDS: Record<string, { key: string; labelKey: string; secret?: boolean }[]> = {
  feishu: FEISHU_FIELDS,
  lark: FEISHU_FIELDS,
  telegram: [{ key: "token", labelKey: "connect.cfgBotToken", secret: true }],
  dingtalk: [
    { key: "client_id", labelKey: "connect.cfgClientId" },
    { key: "client_secret", labelKey: "connect.cfgClientSecret", secret: true },
    { key: "robot_code", labelKey: "connect.cfgRobotCode" },
  ],
  slack: [
    { key: "bot_token", labelKey: "connect.cfgSlackBotToken", secret: true },
    { key: "app_token", labelKey: "connect.cfgSlackAppToken", secret: true },
  ],
  discord: [{ key: "token", labelKey: "connect.cfgBotToken", secret: true }],
  webhook: [
    { key: "listen", labelKey: "connect.cfgListen" },
    { key: "outbound_url", labelKey: "connect.cfgOutboundUrl" },
  ],
};

type FeishuSetupPhase = "idle" | "loading" | "scanning" | "saving" | "completed" | "expired" | "denied" | "error";

const EVENT_OPTIONS = [
  "message.received",
  "message.sent",
  "session.started",
  "session.ended",
  "cron.triggered",
  "webhook.triggered",
  "permission.requested",
  "task.queued",
  "task.started",
  "task.steered",
  "task.controller_changed",
  "task.interrupted",
  "task.completed",
  "interaction.resolved",
  "thread.bound",
  "error",
];

const EMPTY_CHANNEL: Partial<Channel> = { name: "", type: "feishu", agent_id: "", config: {}, enabled: true };
const EMPTY_TRIGGER: Partial<Trigger> = {
  name: "",
  kind: "cron",
  cron_expr: "0 9 * * *",
  prompt: "",
  session_mode: "reuse",
  enabled: true,
};

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

  useEffect(() => {
    const timer = window.setInterval(() => channels.reload(), 15_000);
    return () => window.clearInterval(timer);
  }, [channels.reload]);

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

function stateBadge(state: string | undefined, enabled: boolean, t: (key: string) => string) {
  if (!enabled) return { className: "", label: t("common.disabled") };
  switch (state) {
    case "running":
      return { className: "success", label: t("connect.stateRunning") };
    case "starting":
      return { className: "warning", label: t("connect.stateStarting") };
    case "reconnecting":
      return { className: "warning", label: t("connect.stateReconnecting") };
    case "degraded":
      return { className: "danger", label: t("connect.stateDegraded") };
    case "error":
      return { className: "danger", label: t("connect.stateError") };
    case "pending":
      return { className: "warning", label: t("connect.statePending") };
    default:
      return { className: "warning", label: t("connect.stateStopped") };
  }
}

function ChannelCard({
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

function formatChannelTime(value: string) {
  const date = new Date(value);
  return Number.isNaN(date.getTime()) ? value : date.toLocaleString();
}

function ChannelEditor({
  draft,
  setDraft,
  platforms,
  agents,
  busy,
  onSave,
  onAutoSave,
  onCancel,
}: {
  draft: Partial<Channel>;
  setDraft: Dispatch<SetStateAction<Partial<Channel> | null>>;
  platforms: string[];
  agents: { id: string; name: string; runtime_id: string }[];
  busy: boolean;
  onSave: () => void;
  onAutoSave: (draft: Partial<Channel>) => Promise<void>;
  onCancel: () => void;
}) {
  const { t } = useI18n();
  const fields = CHANNEL_FIELDS[draft.type ?? ""] ?? [];
  const isFeishuLike = draft.type === "feishu" || draft.type === "lark";
  const selectedAgent = agents.find((agent) => agent.id === draft.agent_id);
  const isCodexAgent = selectedAgent?.runtime_id === "codex";
  const setupRef = useRef({ deviceCode: "", baseUrl: "", interval: 5, cancelled: false, polling: false });
  const draftRef = useRef<Partial<Channel>>(draft);
  const [setup, setSetup] = useState<{ phase: FeishuSetupPhase; qrUrl: string; error: string }>({
    phase: "idle",
    qrUrl: "",
    error: "",
  });

  useEffect(() => {
    draftRef.current = draft;
  }, [draft]);

  useEffect(() => {
    return () => {
      setupRef.current.cancelled = true;
    };
  }, []);

  const update = (patch: Partial<Channel>) =>
    setDraft((current) => ({ ...((current ?? draft) as Partial<Channel>), ...patch }));
  const updateConfig = (key: string, value: string) =>
    setDraft((current) => {
      const base = (current ?? draft) as Partial<Channel>;
      return { ...base, config: { ...(base.config ?? {}), [key]: value } };
    });

  async function startFeishuSetup() {
    setupRef.current.cancelled = false;
    setupRef.current.polling = false;
    setSetup({ phase: "loading", qrUrl: "", error: "" });
    try {
      const res = await api.beginFeishuSetup();
      setupRef.current = {
        deviceCode: res.device_code,
        baseUrl: "",
        interval: res.interval || 5,
        cancelled: false,
        polling: false,
      };
      setSetup({ phase: "scanning", qrUrl: res.qr_url, error: "" });
      void pollFeishuSetup();
    } catch (err) {
      setSetup({ phase: "error", qrUrl: "", error: err instanceof Error ? err.message : String(err) });
    }
  }

  async function pollFeishuSetup() {
    if (setupRef.current.polling) return;
    setupRef.current.polling = true;
    try {
      while (!setupRef.current.cancelled) {
        const res = await api.pollFeishuSetup(setupRef.current.deviceCode, setupRef.current.baseUrl);
        if (setupRef.current.cancelled) return;
        if (res.base_url) setupRef.current.baseUrl = res.base_url;
        if (res.slow_down) setupRef.current.interval += 5;
        switch (res.status) {
          case "completed":
            setSetup((current) => ({ ...current, phase: "saving", error: "" }));
            {
              const nextDraft = completeFeishuDraft((draftRef.current ?? draft) as Partial<Channel>, res);
              draftRef.current = nextDraft;
              setDraft(nextDraft);
              await onAutoSave(nextDraft);
            }
            if (!setupRef.current.cancelled) {
              setSetup((current) => ({ ...current, phase: "completed", error: "" }));
            }
            return;
          case "denied":
            setSetup((current) => ({ ...current, phase: "denied", error: "" }));
            return;
          case "expired":
            setSetup((current) => ({ ...current, phase: "expired", error: "" }));
            return;
          case "error":
            setSetup((current) => ({ ...current, phase: "error", error: res.error ?? t("connect.scanError") }));
            return;
        }
        await sleep((setupRef.current.interval || 5) * 1000);
      }
    } catch (err) {
      if (!setupRef.current.cancelled) {
        setSetup((current) => ({
          ...current,
          phase: "error",
          error: err instanceof Error ? err.message : String(err),
        }));
      }
    } finally {
      setupRef.current.polling = false;
    }
  }

  function resetSetup() {
    setupRef.current.cancelled = true;
    setupRef.current.polling = false;
    setSetup({ phase: "idle", qrUrl: "", error: "" });
  }

  return (
    <div className="route-card editor-card">
      <div className="field-grid">
        <label className="field">
          <span>{t("common.name")}</span>
          <input value={draft.name ?? ""} onChange={(e) => update({ name: e.target.value })} />
        </label>
        <label className="field">
          <span>{t("connect.channelType")}</span>
          <select
            value={draft.type ?? ""}
            onChange={(e) => {
              resetSetup();
              update({ type: e.target.value, config: draft.id ? draft.config : defaultChannelConfig(e.target.value) });
            }}
          >
            {platforms.map((p) => (
              <option key={p} value={p}>
                {platformLabel(p)}
              </option>
            ))}
          </select>
        </label>
        <label className="field">
          <span>{t("connect.boundAgent")}</span>
          <select
            value={draft.agent_id ?? ""}
            onChange={(e) => {
              const agentID = e.target.value;
              const runtimeID = agents.find((agent) => agent.id === agentID)?.runtime_id;
              const config = { ...(draft.config ?? {}) };
              if (runtimeID !== "codex") config.codex_control_enabled = "false";
              update({ agent_id: agentID, config });
            }}
          >
            <option value="">{t("connect.noAgent")}</option>
            {agents.map((a) => (
              <option key={a.id} value={a.id}>
                {a.name}
              </option>
            ))}
          </select>
        </label>
        {fields.map((f) => (
          <label className="field" key={f.key}>
            <span>{t(f.labelKey)}</span>
            <input
              type={f.secret ? "password" : "text"}
              value={draft.config?.[f.key] ?? ""}
              placeholder={f.secret ? "" : undefined}
              onChange={(e) => updateConfig(f.key, e.target.value)}
            />
          </label>
        ))}
      </div>
      {isFeishuLike && (
        <>
          <FeishuSetupBox
            phase={setup.phase}
            qrUrl={setup.qrUrl}
            error={setup.error}
            platform={draft.type ?? "feishu"}
            onStart={startFeishuSetup}
            onReset={resetSetup}
          />
          <FeishuChannelOptions draft={draft} updateConfig={updateConfig} codexAgent={isCodexAgent} />
        </>
      )}
      <div className="table-actions">
        <label className="switch-row">
          <span>
            <strong>{t("common.enable")}</strong>
          </span>
          <input type="checkbox" checked={draft.enabled ?? false} onChange={(e) => update({ enabled: e.target.checked })} />
        </label>
        <button className="ghost-action" onClick={onCancel}>
          {t("common.close")}
        </button>
        <button className="action" disabled={busy || !(draft.name ?? "").trim()} onClick={onSave}>
          <Save size={15} />
          {t("common.save")}
        </button>
      </div>
    </div>
  );
}

function FeishuChannelOptions({
  draft,
  updateConfig,
  codexAgent,
}: {
  draft: Partial<Channel>;
  updateConfig: (key: string, value: string) => void;
  codexAgent: boolean;
}) {
  const { t } = useI18n();
  const config = draft.config ?? {};
  const replyMode = configValue(config, "reply_mode", FEISHU_DEFAULTS.reply_mode);
  const ackEnabled = configValue(config, "ack_reaction_enabled", FEISHU_DEFAULTS.ack_reaction_enabled) !== "false";
  const codexControl = configValue(config, "codex_control_enabled", FEISHU_DEFAULTS.codex_control_enabled) === "true";

  return (
    <div className="channel-options">
      <div className="field-grid">
        <label className="field">
          <span>{t("connect.replyScope")}</span>
          <select
            value={configValue(config, "reply_scope", FEISHU_DEFAULTS.reply_scope)}
            onChange={(e) => updateConfig("reply_scope", e.target.value)}
          >
            {FEISHU_REPLY_SCOPES.map((option) => (
              <option key={option.value} value={option.value}>
                {t(option.labelKey)}
              </option>
            ))}
          </select>
        </label>
        {codexAgent && (
          <label className="switch-row channel-option-toggle">
            <span>
              <strong>{t("connect.codexControl")}</strong>
              <small>{t("connect.codexControlHint")}</small>
            </span>
            <input
              type="checkbox"
              checked={codexControl}
              onChange={(e) => updateConfig("codex_control_enabled", e.target.checked ? "true" : "false")}
            />
          </label>
        )}
        {codexAgent && codexControl && (
          <>
            <div className="field">
              <span>{t("connect.codexCapability")}</span>
              <small>
                {draft.codex_control_capability?.state === "ready"
                  ? t("connect.codexCapabilityReady")
                  : draft.codex_control_capability?.state === "unavailable"
                    ? `${t("connect.codexCapabilityUnavailable")} ${draft.codex_control_capability.error ?? ""}`
                    : draft.codex_control_capability?.state === "disconnected"
                      ? t("connect.codexCapabilityDisconnected")
                      : t("connect.codexCapabilityPending")}
              </small>
            </div>
            <label className="field">
              <span>{t("connect.codexAllowedUsers")}</span>
              <input
                value={configValue(config, "allowed_user_ids", FEISHU_DEFAULTS.allowed_user_ids)}
                onChange={(e) => updateConfig("allowed_user_ids", e.target.value)}
                placeholder="ou_xxx, ou_yyy"
              />
            </label>
            <label className="field">
              <span>{t("connect.codexAdminUsers")}</span>
              <input
                value={configValue(config, "admin_user_ids", FEISHU_DEFAULTS.admin_user_ids)}
                onChange={(e) => updateConfig("admin_user_ids", e.target.value)}
                placeholder="ou_xxx"
              />
            </label>
            <label className="field">
              <span>{t("connect.codexMaxQueue")}</span>
              <input
                type="number"
                min={1}
                max={100}
                value={configValue(config, "codex_max_queue", FEISHU_DEFAULTS.codex_max_queue)}
                onChange={(e) => updateConfig("codex_max_queue", e.target.value)}
              />
            </label>
            <label className="field">
              <span>{t("connect.codexTurnTimeout")}</span>
              <input
                type="number"
                min={1}
                max={240}
                value={configValue(config, "codex_turn_timeout_minutes", FEISHU_DEFAULTS.codex_turn_timeout_minutes)}
                onChange={(e) => updateConfig("codex_turn_timeout_minutes", e.target.value)}
              />
            </label>
          </>
        )}
        <label className="field">
          <span>{t("connect.replyMode")}</span>
          <select value={replyMode} onChange={(e) => updateConfig("reply_mode", e.target.value)}>
            {FEISHU_REPLY_MODES.map((option) => (
              <option key={option.value} value={option.value}>
                {t(option.labelKey)}
              </option>
            ))}
            <option value="lark_cli" disabled>
              {t("connect.replyModeLarkCli")}
            </option>
          </select>
        </label>
        <label className="switch-row channel-option-toggle">
          <span>
            <strong>{t("connect.ackReaction")}</strong>
            <small>{t("connect.ackReactionHint")}</small>
          </span>
          <input
            type="checkbox"
            checked={ackEnabled}
            onChange={(e) => updateConfig("ack_reaction_enabled", e.target.checked ? "true" : "false")}
          />
        </label>
        <label className="field">
          <span>{t("connect.ackReactionEmojis")}</span>
          <input
            value={configValue(config, "ack_reaction_emojis", FEISHU_DEFAULTS.ack_reaction_emojis)}
            onChange={(e) => updateConfig("ack_reaction_emojis", e.target.value)}
          />
        </label>
      </div>
    </div>
  );
}

function FeishuSetupBox({
  phase,
  qrUrl,
  error,
  platform,
  onStart,
  onReset,
}: {
  phase: FeishuSetupPhase;
  qrUrl: string;
  error: string;
  platform: string;
  onStart: () => void;
  onReset: () => void;
}) {
  const { t } = useI18n();
  const label = platformLabel(platform);

  if (phase === "idle") {
    return (
      <div className="qr-setup">
        <span className="provider-icon">
          <Smartphone size={15} />
        </span>
        <strong>{label}</strong>
        <button className="ghost-action" onClick={onStart}>
          <Smartphone size={14} />
          {t("connect.scanSetup")}
        </button>
      </div>
    );
  }

  const message =
    phase === "loading"
      ? t("connect.scanGenerating")
      : phase === "scanning"
        ? t("connect.scanWaiting")
        : phase === "saving"
          ? t("connect.scanSaving")
          : phase === "completed"
            ? t("connect.scanCompleted")
            : phase === "expired"
              ? t("connect.scanExpired")
              : phase === "denied"
                ? t("connect.scanDenied")
                : error || t("connect.scanError");

  return (
    <div className={`qr-setup ${phase === "error" || phase === "denied" ? "error" : ""}`}>
      {phase === "loading" || phase === "saving" ? <Loader2 className="spin" size={16} /> : null}
      {phase === "completed" ? <CheckCircle2 size={16} /> : null}
      {phase === "expired" || phase === "denied" || phase === "error" ? <XCircle size={16} /> : null}
      {(phase === "scanning" || phase === "saving") && qrUrl && (
        <span className="qr-frame">
          <QRCodeSVG value={qrUrl} size={148} level="M" />
        </span>
      )}
      <span>{message}</span>
      {(phase === "expired" || phase === "denied" || phase === "error" || phase === "completed") && (
        <button className="ghost-action" onClick={phase === "completed" ? onReset : onStart}>
          <RefreshCw size={14} />
          {phase === "completed" ? t("connect.scanAgain") : t("connect.retryScan")}
        </button>
      )}
    </div>
  );
}

function preferredDefaultPlatform(platforms: string[]) {
  if (platforms.includes("feishu")) return "feishu";
  return platforms[0] ?? "feishu";
}

function defaultChannelConfig(type: string) {
  return type === "feishu" || type === "lark" ? { ...FEISHU_DEFAULTS } : {};
}

function configValue(config: Record<string, string>, key: string, fallback: string) {
  const value = config[key];
  return value === undefined || value === "" ? fallback : value;
}

function completeFeishuDraft(draft: Partial<Channel>, res: FeishuSetupPollResponse): Partial<Channel> {
  const platform = res.platform ?? draft.type ?? "feishu";
  const ownerID = res.owner_open_id ?? "";
  return {
    ...draft,
    name: (draft.name ?? "").trim() || `${platformLabel(platform)} Bot`,
    type: platform,
    enabled: draft.enabled ?? true,
    config: {
      ...defaultChannelConfig(platform),
      ...(draft.config ?? {}),
      app_id: res.app_id ?? "",
      app_secret: res.app_secret ?? "",
      allowed_user_ids: mergeChannelUserIDs(draft.config?.allowed_user_ids ?? "", ownerID),
      admin_user_ids: mergeChannelUserIDs(draft.config?.admin_user_ids ?? "", ownerID),
    },
  };
}

function mergeChannelUserIDs(existing: string, added: string) {
  const values = `${existing},${added}`
    .split(/[\s,;]+/)
    .map((value) => value.trim())
    .filter(Boolean);
  return Array.from(new Set(values)).join(",");
}

function platformLabel(platform: string) {
  switch (platform) {
    case "feishu":
      return "Feishu";
    case "lark":
      return "Lark";
    default:
      return platform;
  }
}

function sleep(ms: number) {
  return new Promise((resolve) => window.setTimeout(resolve, ms));
}

function kindBadge(kind: string, t: (key: string) => string) {
  switch (kind) {
    case "cron":
      return { icon: <CalendarClock size={14} />, label: t("connect.kindCron") };
    case "webhook":
      return { icon: <Webhook size={14} />, label: t("connect.kindWebhook") };
    default:
      return { icon: <Zap size={14} />, label: t("connect.kindEvent") };
  }
}

function TriggerCard({
  trigger,
  busy,
  onEdit,
  onRun,
  onToggle,
  onDelete,
}: {
  trigger: Trigger;
  busy: string;
  onEdit: () => void;
  onRun: () => void;
  onToggle: () => void;
  onDelete: () => void;
}) {
  const { t } = useI18n();
  const badge = kindBadge(trigger.kind, t);
  const spec =
    trigger.kind === "cron"
      ? trigger.cron_expr
      : trigger.kind === "webhook"
        ? trigger.hook_path
        : trigger.event;
  const lastStatus = trigger.last_status;
  const statusClass = lastStatus === "ok" ? "success" : lastStatus === "error" ? "danger" : "warning";
  return (
    <div className="route-card">
      <div className="agent-list-main">
        <span className="provider-icon">{badge.icon}</span>
        <span>
          <strong>{trigger.name}</strong>
          <small>
            {badge.label}
            {spec ? ` · ${spec}` : ""}
            {trigger.channel_name ? ` → ${trigger.channel_name}${trigger.chat_id ? ` (${trigger.chat_id})` : ""}` : ""}
          </small>
        </span>
        <span className={`status-badge ${trigger.enabled ? "success" : ""}`}>
          <span className="status-dot" />
          {trigger.enabled ? t("common.enabled") : t("common.disabled")}
        </span>
        {lastStatus && (
          <span className={`status-badge ${statusClass}`} title={trigger.last_error || ""}>
            {t("connect.lastRun")}: {lastStatus}
            {trigger.last_run ? ` · ${new Date(trigger.last_run).toLocaleString()}` : ""}
          </span>
        )}
      </div>
      {trigger.prompt && <p className="subtle-copy trigger-prompt">{trigger.prompt}</p>}
      {trigger.kind === "webhook" && trigger.hook_path && (
        <HookEndpoint trigger={trigger} />
      )}
      {trigger.last_error && <div className="session-notice error">{trigger.last_error}</div>}
      <div className="table-actions">
        {trigger.kind !== "event" && (
          <button className="ghost-action" disabled={busy === `run-${trigger.id}`} onClick={onRun}>
            <Play size={14} />
            {t("connect.runNow")}
          </button>
        )}
        <button className="ghost-action" onClick={onEdit}>
          <Pencil size={14} />
          {t("common.edit")}
        </button>
        <button className="ghost-action" disabled={busy === `toggle-${trigger.id}`} onClick={onToggle}>
          {trigger.enabled ? t("common.disable") : t("common.enable")}
        </button>
        <button className="ghost-action danger-action" disabled={busy === `delete-${trigger.id}`} onClick={onDelete}>
          <Trash2 size={14} />
          {t("common.delete")}
        </button>
      </div>
    </div>
  );
}

function HookEndpoint({ trigger }: { trigger: Trigger }) {
  const { t } = useI18n();
  const [copied, setCopied] = useState(false);
  const url = `${window.location.origin}${trigger.hook_path}`;
  const curl = `curl -X POST '${url}' -H 'X-Hook-Token: ${trigger.token ?? ""}' -H 'Content-Type: application/json' -d '{"prompt":"..."}'`;
  return (
    <div className="hook-endpoint">
      <code>{url}</code>
      <button
        className="ghost-action"
        title={curl}
        onClick={async () => {
          try {
            await navigator.clipboard.writeText(curl);
            setCopied(true);
            setTimeout(() => setCopied(false), 1500);
          } catch {
            // clipboard unavailable (non-secure context); ignore
          }
        }}
      >
        <Copy size={14} />
        {copied ? t("connect.copied") : t("connect.copyCurl")}
      </button>
    </div>
  );
}

function TriggerEditor({
  draft,
  setDraft,
  channels,
  agents,
  busy,
  onSave,
  onCancel,
}: {
  draft: Partial<Trigger>;
  setDraft: (next: Partial<Trigger> | null) => void;
  channels: Channel[];
  agents: { id: string; name: string }[];
  busy: boolean;
  onSave: () => void;
  onCancel: () => void;
}) {
  const { t } = useI18n();
  const update = (patch: Partial<Trigger>) => setDraft({ ...draft, ...patch });
  const kind = draft.kind ?? "cron";
  const canSave = Boolean((draft.name ?? "").trim());

  return (
    <div className="route-card editor-card">
      <div className="field-grid">
        <label className="field">
          <span>{t("common.name")}</span>
          <input value={draft.name ?? ""} onChange={(e) => update({ name: e.target.value })} />
        </label>
        <label className="field">
          <span>{t("connect.kind")}</span>
          <select value={kind} onChange={(e) => update({ kind: e.target.value })}>
            <option value="cron">{t("connect.kindCron")}</option>
            <option value="webhook">{t("connect.kindWebhook")}</option>
            <option value="event">{t("connect.kindEvent")}</option>
          </select>
        </label>

        {kind === "cron" && (
          <label className="field">
            <span>{t("connect.cronExpr")}</span>
            <input
              value={draft.cron_expr ?? ""}
              placeholder="0 9 * * *"
              onChange={(e) => update({ cron_expr: e.target.value })}
            />
          </label>
        )}

        {kind === "event" && (
          <>
            <label className="field">
              <span>{t("connect.event")}</span>
              <select value={draft.event ?? ""} onChange={(e) => update({ event: e.target.value })}>
                <option value="">{t("connect.pickEvent")}</option>
                {EVENT_OPTIONS.map((ev) => (
                  <option key={ev} value={ev}>
                    {ev}
                  </option>
                ))}
              </select>
            </label>
            <label className="field">
              <span>{t("connect.actionType")}</span>
              <select value={draft.action_type ?? "http"} onChange={(e) => update({ action_type: e.target.value })}>
                <option value="http">HTTP POST</option>
                <option value="shell">Shell</option>
              </select>
            </label>
            <label className="field wide">
              <span>{t("connect.actionTarget")}</span>
              <input
                value={draft.action_target ?? ""}
                placeholder={draft.action_type === "shell" ? "notify-send \"$HOOK_EVENT\"" : "https://example.com/callback"}
                onChange={(e) => update({ action_target: e.target.value })}
              />
            </label>
          </>
        )}

        {kind !== "event" && (
          <>
            <label className="field">
              <span>{t("connect.boundAgent")}</span>
              <select value={draft.agent_id ?? ""} onChange={(e) => update({ agent_id: e.target.value })}>
                <option value="">{t("connect.agentFromChannel")}</option>
                {agents.map((a) => (
                  <option key={a.id} value={a.id}>
                    {a.name}
                  </option>
                ))}
              </select>
            </label>
            <label className="field">
              <span>{t("connect.outputChannel")}</span>
              <select value={draft.channel_id ?? ""} onChange={(e) => update({ channel_id: e.target.value })}>
                <option value="">{t("common.none")}</option>
                {channels.map((ch) => (
                  <option key={ch.id} value={ch.id}>
                    {ch.name} ({ch.type})
                  </option>
                ))}
              </select>
            </label>
            <label className="field">
              <span>{t("connect.chatId")}</span>
              <input value={draft.chat_id ?? ""} onChange={(e) => update({ chat_id: e.target.value })} />
            </label>
            <label className="field">
              <span>{t("connect.sessionMode")}</span>
              <select value={draft.session_mode ?? "reuse"} onChange={(e) => update({ session_mode: e.target.value })}>
                <option value="reuse">{t("connect.sessionReuse")}</option>
                <option value="new_per_run">{t("connect.sessionNewPerRun")}</option>
              </select>
            </label>
            <label className="field wide">
              <span>{t("connect.prompt")}</span>
              <textarea rows={3} value={draft.prompt ?? ""} onChange={(e) => update({ prompt: e.target.value })} />
            </label>
          </>
        )}

        {kind === "event" && (
          <label className="field">
            <span>{t("connect.channelFilter")}</span>
            <select value={draft.channel_id ?? ""} onChange={(e) => update({ channel_id: e.target.value })}>
              <option value="">{t("connect.allChannels")}</option>
              {channels.map((ch) => (
                <option key={ch.id} value={ch.id}>
                  {ch.name} ({ch.type})
                </option>
              ))}
            </select>
          </label>
        )}

        {kind === "webhook" && (
          <label className="field">
            <span>{t("connect.hookToken")}</span>
            <input
              value={draft.token ?? ""}
              placeholder={t("connect.hookTokenAuto")}
              onChange={(e) => update({ token: e.target.value })}
            />
          </label>
        )}
      </div>
      <div className="table-actions">
        <label className="switch-row">
          <span>
            <strong>{t("common.enable")}</strong>
          </span>
          <input type="checkbox" checked={draft.enabled ?? false} onChange={(e) => update({ enabled: e.target.checked })} />
        </label>
        <button className="ghost-action" onClick={onCancel}>
          {t("common.close")}
        </button>
        <button className="action" disabled={busy || !canSave} onClick={onSave}>
          <Save size={15} />
          {t("common.save")}
        </button>
      </div>
    </div>
  );
}
