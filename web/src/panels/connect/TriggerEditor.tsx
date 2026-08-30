import {
  Copy,
  Pencil,
  Play,
  Save,
  Trash2,
} from "lucide-react";
import { useState } from "react";
import { Channel, Trigger } from "../../api";
import { useI18n } from "../../i18n";
import { TargetBadge } from "../../components/TargetBadge";
import {
  EVENT_OPTIONS,
  kindBadge,
} from "./connectShared";

export function TriggerCard({
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
        <TargetBadge target_id={trigger.target_id} target_name={trigger.target_name} />
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

export function HookEndpoint({ trigger }: { trigger: Trigger }) {
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

export function TriggerEditor({
  draft,
  setDraft,
  channels,
  agents,
  allowedKinds = ["cron", "webhook", "event"],
  busy,
  onSave,
  onCancel,
}: {
  draft: Partial<Trigger>;
  setDraft: (next: Partial<Trigger> | null) => void;
  channels: Channel[];
  agents: { id: string; name: string }[];
  allowedKinds?: Array<"cron" | "webhook" | "event">;
  busy: boolean;
  onSave: () => void;
  onCancel: () => void;
}) {
  const { t } = useI18n();
  const update = (patch: Partial<Trigger>) => setDraft({ ...draft, ...patch });
  const draftKind = draft.kind as "cron" | "webhook" | "event" | undefined;
  const kind = draftKind && allowedKinds.includes(draftKind) ? draftKind : allowedKinds[0];
  const canSave = Boolean((draft.name ?? "").trim());

  return (
    <div className="route-card editor-card">
      <div className="field-grid">
        <label className="field">
          <span>{t("common.name")}</span>
          <input value={draft.name ?? ""} onChange={(e) => update({ name: e.target.value })} />
        </label>
        {allowedKinds.length > 1 && (
          <label className="field">
            <span>{t("connect.kind")}</span>
            <select value={kind} onChange={(e) => update({ kind: e.target.value })}>
              {allowedKinds.includes("cron") && <option value="cron">{t("connect.kindCron")}</option>}
              {allowedKinds.includes("webhook") && <option value="webhook">{t("connect.kindWebhook")}</option>}
              {allowedKinds.includes("event") && <option value="event">{t("connect.kindEvent")}</option>}
            </select>
          </label>
        )}

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
