import { useEffect, useState, type ReactNode } from "react";
import { Activity, Cloud, Database, PlugZap, RefreshCw, ShieldCheck, Unplug } from "lucide-react";
import { api, type CursorUsageSourceStatus, type UsageDisplaySettings } from "../../api";
import { confirmAction } from "../../components/ConfirmDialog";
import { TargetBadge, targetKey } from "../../components/TargetBadge";
import { SupportedCurrency, validCNYRate } from "../../currency";
import { useI18n } from "../../i18n";
import { useAsync } from "../../useAsync";
import { formatTokens } from "./format";

type Translate = (key: string, params?: Record<string, string | number>) => string;

// The currency the dashboard shows costs in, and the fixed rate it converts
// with.
export function UsageCurrencySettings({ settings, onSaved }: {
  settings: UsageDisplaySettings | null;
  onSaved: (saved: UsageDisplaySettings) => void;
}) {
  const { t } = useI18n();
  const [draft, setDraft] = useState<UsageDisplaySettings | null>(settings);
  const [status, setStatus] = useState<"idle" | "saving" | "saved" | "error">("idle");
  useEffect(() => { setDraft(settings); }, [settings]);

  const update = (patch: Partial<UsageDisplaySettings>) => {
    setDraft((current) => (current ? { ...current, ...patch } : current));
    setStatus("idle");
  };
  const save = async () => {
    if (!draft) return;
    setStatus("saving");
    try {
      const saved = await api.saveUsageDisplaySettings({ ...draft, cny_rate: validCNYRate(draft.cny_rate) });
      setDraft(saved);
      onSaved(saved);
      setStatus("saved");
    } catch {
      setStatus("error");
    }
  };

  return (
    <div className="control-row usage-controls">
      <label className="control-row">
        <span className="muted">{t("usage.currency")}</span>
        <select value={draft?.currency ?? "cny"} disabled={!draft} onChange={(event) => update({ currency: event.target.value as SupportedCurrency })}>
          <option value="cny">{t("currency.cny")}</option>
          <option value="usd">{t("currency.usd")}</option>
        </select>
      </label>
      <label className="control-row">
        <span className="muted">{t("usage.cnyRate")}</span>
        <input
          className="usage-rate-input"
          type="number"
          min="0.01"
          step="0.0001"
          value={draft?.cny_rate ?? 7}
          disabled={!draft}
          onChange={(event) => update({ cny_rate: Number(event.target.value) })}
        />
      </label>
      <button className="ghost-action" type="button" disabled={!draft || status === "saving"} onClick={() => void save()}>
        {status === "saving" ? "…" : t("common.save")}
      </button>
      {status === "saved" && <span className="muted">{t("usage.displaySaved")}</span>}
      {status === "error" && <span className="error">{t("usage.displaySaveError")}</span>}
    </div>
  );
}

// Consent-gated collectors (Cursor) with their connection state per machine.
export function UsageSources() {
  const { t } = useI18n();
  const sources = useAsync(() => api.usageSources(), []);
  const [busy, setBusy] = useState("");
  const [actionError, setActionError] = useState("");

  useEffect(() => {
    if (!(sources.data ?? []).some((source) => source.syncing)) return;
    const interval = window.setInterval(() => sources.reload(), 2000);
    return () => window.clearInterval(interval);
  }, [sources.data]);

  async function run(source: CursorUsageSourceStatus, action: "connect" | "sync" | "repair" | "disconnect") {
    if (action === "connect" && !(await confirmAction(t("usage.cursorConsent")))) return;
    if (action === "disconnect" && !(await confirmAction(t("usage.cursorDisconnectConfirm"), { danger: true }))) return;
    setBusy(`${targetKey(source.target_id, source.source)}:${action}`);
    setActionError("");
    try {
      await api.cursorUsageAction(action, source.target_id);
      await sources.reload();
    } catch (error) {
      setActionError(String(error));
    } finally {
      setBusy("");
    }
  }

  return (
    <section className="surface cursor-usage-sources">
      <div className="surface-header">
        <div>
          <h2>{t("usage.sourcesTitle")}</h2>
          <p className="subtle-copy">{t("usage.sourcesHint")}</p>
        </div>
        <button className="ghost-action" type="button" onClick={sources.reload}><RefreshCw size={14} />{t("common.refresh")}</button>
      </div>
      {sources.error && <div className="surface-body error">{sources.error}</div>}
      {actionError && <div className="surface-body error">{actionError}</div>}
      <div className="cursor-source-grid">
        {(sources.data ?? []).map((source) => (
          <CursorSourceCard key={targetKey(source.target_id, source.source)} source={source} busy={busy} onAction={run} t={t} />
        ))}
        {!sources.loading && (sources.data ?? []).length === 0 && <div className="empty-state">{t("usage.sourcesEmpty")}</div>}
      </div>
    </section>
  );
}

function CursorSourceCard({ source, busy, onAction, t }: {
  source: CursorUsageSourceStatus;
  busy: string;
  onAction: (source: CursorUsageSourceStatus, action: "connect" | "sync" | "repair" | "disconnect") => void;
  t: Translate;
}) {
  const key = targetKey(source.target_id, source.source);
  const coverage = source.total_tokens > 0
    ? Math.max(0, Math.round((1 - source.estimated_tokens / source.total_tokens) * 100))
    : 100;
  const isBusy = busy.startsWith(`${key}:`) || source.syncing;
  return (
    <article className="cursor-source-card">
      <header>
        <span className="cursor-source-icon"><PlugZap size={20} /></span>
        <div>
          <strong>Cursor</strong>
          <span>{t("usage.cursorAgentScope")}</span>
          <TargetBadge target_id={source.target_id} target_name={source.target_name} />
        </div>
        <span className={`status-badge ${source.connected ? "success" : ""}`}>{source.connected ? t("common.connected") : t("common.disconnected")}</span>
      </header>
      <div className="cursor-source-status-grid">
        <SourceState icon={<ShieldCheck size={15} />} label="Hook" value={sourceStatusLabel(source.hook.status, t)} />
        <SourceState icon={<Database size={15} />} label={t("usage.cursorLocal")} value={sourceStatusLabel(source.local_status, t)} />
        <SourceState icon={<Cloud size={15} />} label={t("usage.cursorCloud")} value={sourceStatusLabel(source.cloud_status, t)} />
        <SourceState icon={<Activity size={15} />} label={t("usage.cursorCoverage")} value={`${coverage}%`} />
      </div>
      {!source.connected && <p className="cursor-source-consent">{t("usage.cursorConsentHint")}</p>}
      {source.connected && (
        <div className="cursor-source-meta">
          <span>{t("usage.cursorBackfill", { days: source.backfill_days })}: {source.backfill_complete ? t("common.complete") : t("common.inProgress")}</span>
          <span>{t("usage.cursorMatched", { count: source.cloud_matched_events })}</span>
          <span>{t("usage.cursorEstimated", { tokens: formatTokens(source.estimated_tokens) })}</span>
          {source.last_sync_at && <span>{t("common.updated")} {new Date(source.last_sync_at).toLocaleString()}</span>}
        </div>
      )}
      {source.last_error && <div className="error cursor-source-error">{source.last_error}</div>}
      <div className="cursor-source-actions">
        {!source.connected ? (
          <button className="action" type="button" disabled={isBusy} onClick={() => onAction(source, "connect")}><PlugZap size={14} />{t("usage.cursorConnect")}</button>
        ) : (
          <>
            <button className="action" type="button" disabled={isBusy} onClick={() => onAction(source, "sync")}><RefreshCw size={14} />{source.syncing ? t("usage.cursorSyncing") : t("usage.cursorSync")}</button>
            <button className="ghost-action" type="button" disabled={isBusy} onClick={() => onAction(source, "repair")}><ShieldCheck size={14} />{t("usage.cursorRepair")}</button>
            <button className="ghost-action danger-action" type="button" disabled={isBusy} onClick={() => onAction(source, "disconnect")}><Unplug size={14} />{t("usage.cursorDisconnect")}</button>
          </>
        )}
      </div>
    </article>
  );
}

function SourceState({ icon, label, value }: { icon: ReactNode; label: string; value: string }) {
  return <div><span>{icon}{label}</span><strong>{value.replace(/_/g, " ")}</strong></div>;
}

export function sourceStatusLabel(value: string, t: (key: string) => string) {
  const key = `usage.sourceStatus.${value.trim().toLowerCase()}`;
  const translated = t(key);
  return translated === key ? value.replace(/_/g, " ") : translated;
}
