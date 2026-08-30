import { useEffect, useState, type ReactNode } from "react";
import { Activity, ChevronDown, ChevronUp, Cloud, Database, PlugZap, RefreshCw, ShieldCheck, Unplug } from "lucide-react";
import { api, type CursorUsageSourceStatus, type MenubarSettings } from "../api";
import { CatalogPagination, useCatalogPagination } from "../components/CatalogPagination";
import { formatUsageCost, SupportedCurrency, validCNYRate } from "../currency";
import { useI18n } from "../i18n";
import { useAsync } from "../useAsync";
import { TargetBadge, targetKey } from "../components/TargetBadge";
import { fmt } from "./OverviewPanel";

const PERIODS = ["hourly", "daily", "weekly", "monthly", "session", "blocks"];
const RANGES = ["all", "today", "7d", "month", "custom"] as const;
type UsageRange = (typeof RANGES)[number];

export function UsagePanel() {
  const { language, t } = useI18n();
  const [period, setPeriod] = useState("daily");
  const [range, setRange] = useState<UsageRange>("all");
  const today = formatLocalDate(new Date());
  const [customFrom, setCustomFrom] = useState(today);
  const [customTo, setCustomTo] = useState(today);
  const bounds = usageRangeBounds(range, customFrom, customTo);
  const { data, error, loading } = useAsync(
    () => api.usage(period, bounds.from, bounds.to),
    [period, bounds.from, bounds.to],
  );
  const currencyPreferences = useAsync(() => api.menubarSettings(), []);
  const cursorSources = useAsync(() => api.usageSources(), []);
  const [preferences, setPreferences] = useState<MenubarSettings | null>(null);
  const [currencyStatus, setCurrencyStatus] = useState<"idle" | "saving" | "saved" | "error">("idle");
  const [sourceBusy, setSourceBusy] = useState("");
  const [sourceError, setSourceError] = useState("");
  const [periodTableExpanded, setPeriodTableExpanded] = useState(false);
  const periodPagination = useCatalogPagination(
    data?.buckets ?? [],
    `${period}:${bounds.from}:${bounds.to}`,
  );

  useEffect(() => {
    if (currencyPreferences.data) setPreferences(currencyPreferences.data);
  }, [currencyPreferences.data]);

  useEffect(() => {
    if (!(cursorSources.data ?? []).some((source) => source.syncing)) return;
    const interval = window.setInterval(() => cursorSources.reload(), 2000);
    return () => window.clearInterval(interval);
  }, [cursorSources.data]);

  const currency = preferences?.currency ?? "cny";
  const cnyRate = validCNYRate(preferences?.cny_rate ?? 7);
  const formatCost = (costUSD: number) => formatUsageCost(costUSD, currency, cnyRate, language);

  const updateCurrencyPreferences = (patch: Partial<MenubarSettings>) => {
    setPreferences((current) => (current ? { ...current, ...patch } : current));
    setCurrencyStatus("idle");
  };

  const saveCurrencyPreferences = async () => {
    if (!preferences) return;
    setCurrencyStatus("saving");
    try {
      const saved = await api.saveMenubarSettings({
        ...preferences,
        cny_rate: validCNYRate(preferences.cny_rate),
      });
      setPreferences(saved);
      setCurrencyStatus("saved");
    } catch {
      setCurrencyStatus("error");
    }
  };

  const updateCustomFrom = (value: string) => {
    setCustomFrom(value);
    if (customTo && value > customTo) setCustomTo(value);
  };

  const updateCustomTo = (value: string) => {
    setCustomTo(value);
    if (customFrom && value < customFrom) setCustomFrom(value);
  };

  async function runCursorAction(source: CursorUsageSourceStatus, action: "connect" | "sync" | "repair" | "disconnect") {
    if (action === "connect" && !window.confirm(t("usage.cursorConsent"))) return;
    if (action === "disconnect" && !window.confirm(t("usage.cursorDisconnectConfirm"))) return;
    const key = targetKey(source.target_id, source.source);
    setSourceBusy(`${key}:${action}`);
    setSourceError("");
    try {
      await api.cursorUsageAction(action, source.target_id);
      await cursorSources.reload();
    } catch (actionError) {
      setSourceError(String(actionError));
    } finally {
      setSourceBusy("");
    }
  }

  return (
    <div className="page-stack">
      <section className="surface">
        <div className="surface-header">
          <div>
            <h2>{t("usage.title")}</h2>
            <p className="subtle-copy">
              {t("usage.range")}: {t(`range.${range}`)} · {t("usage.groupBy")}: {t(`period.${period}`)}
            </p>
          </div>
          <div className="control-row usage-controls">
            <a className="ghost-action" href="#observability/traces">
              <Activity size={14} />
              {t("usage.viewTraces")}
            </a>
            <label className="control-row">
              <span className="muted">{t("usage.range")}</span>
              <select value={range} onChange={(e) => setRange(e.target.value as UsageRange)}>
                {RANGES.map((value) => (
                  <option key={value} value={value}>
                    {t(`range.${value}`)}
                  </option>
                ))}
              </select>
            </label>
            {range === "custom" && (
              <>
                <label className="control-row">
                  <span className="muted">{t("usage.from")}</span>
                  <input type="date" value={customFrom} onChange={(e) => updateCustomFrom(e.target.value)} />
                </label>
                <label className="control-row">
                  <span className="muted">{t("usage.to")}</span>
                  <input type="date" value={customTo} onChange={(e) => updateCustomTo(e.target.value)} />
                </label>
              </>
            )}
            <label className="control-row">
              <span className="muted">{t("usage.groupBy")}</span>
              <select value={period} onChange={(e) => setPeriod(e.target.value)}>
                {PERIODS.map((p) => (
                  <option key={p} value={p}>
                    {t(`period.${p}`)}
                  </option>
                ))}
              </select>
            </label>
            <label className="control-row">
              <span className="muted">{t("usage.currency")}</span>
              <select
                value={currency}
                disabled={!preferences}
                onChange={(e) =>
                  updateCurrencyPreferences({ currency: e.target.value as SupportedCurrency })
                }
              >
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
                step="0.01"
                value={preferences?.cny_rate ?? 7}
                disabled={!preferences}
                onChange={(e) => updateCurrencyPreferences({ cny_rate: Number(e.target.value) })}
              />
            </label>
            <button
              className="ghost-action"
              type="button"
              disabled={!preferences || currencyStatus === "saving"}
              onClick={() => void saveCurrencyPreferences()}
            >
              {currencyStatus === "saving" ? "…" : t("common.save")}
            </button>
            {currencyStatus === "saved" && <span className="muted">{t("menubar.saved")}</span>}
            {currencyStatus === "error" && <span className="error">{t("menubar.saveError")}</span>}
          </div>
        </div>
      </section>

      <section className="surface cursor-usage-sources">
        <div className="surface-header">
          <div>
            <h2>{t("usage.sourcesTitle")}</h2>
            <p className="subtle-copy">{t("usage.sourcesHint")}</p>
          </div>
          <button className="ghost-action" type="button" onClick={cursorSources.reload}><RefreshCw size={14} />{t("common.refresh")}</button>
        </div>
        {cursorSources.error && <div className="surface-body error">{cursorSources.error}</div>}
        {sourceError && <div className="surface-body error">{sourceError}</div>}
        <div className="cursor-source-grid">
          {(cursorSources.data ?? []).map((source) => (
            <CursorSourceCard
              key={targetKey(source.target_id, source.source)}
              source={source}
              busy={sourceBusy}
              onAction={runCursorAction}
              t={t}
            />
          ))}
          {!cursorSources.loading && (cursorSources.data ?? []).length === 0 && <div className="empty-state">{t("usage.sourcesEmpty")}</div>}
        </div>
      </section>

      {error && <div className="surface surface-body error">{error}</div>}
      {(data?.warnings ?? []).map((warning) => <div className="surface surface-body warning" key={warning}>{warning}</div>)}
      {loading && <div className="surface surface-body muted">{t("common.loading")}</div>}

      {data && (
        <>
          <div className="metrics-grid usage-metrics-grid">
            <Stat label={t("usage.estimatedCost")} value={formatCost(data.totals.cost_usd)} />
            <Stat label={t("overview.input")} value={fmt(data.totals.input_tokens)} />
            <Stat label={t("overview.output")} value={fmt(data.totals.output_tokens)} />
            <Stat label={t("usage.cacheInput")} value={fmt(data.totals.cache_read_tokens + data.totals.cache_write_tokens)} />
            <Stat label={t("overview.records")} value={String(data.totals.records)} />
            <Stat label={t("overview.sessions")} value={String(data.totals.sessions ?? 0)} />
            <Stat
              label={t("usage.estimatedTokens")}
              value={fmt(data.totals.estimated_tokens ?? 0)}
              detail={estimatedCoverageLabel(data.totals.estimated_tokens ?? 0, totalUsageTokens(data.totals), t)}
            />
          </div>

          <section className="surface usage-period-section">
            <div className="surface-header">
              <div>
                <h2>
                  {t("usage.byPeriod")} · {t(`period.${period}`)}
                </h2>
                <p className="subtle-copy">{t("usage.periodRows", { count: data.buckets.length })}</p>
              </div>
              <button
                className="ghost-action"
                type="button"
                aria-expanded={periodTableExpanded}
                aria-controls="usage-period-table"
                onClick={() => setPeriodTableExpanded((expanded) => !expanded)}
              >
                {periodTableExpanded ? <ChevronUp size={15} /> : <ChevronDown size={15} />}
                {periodTableExpanded ? t("usage.collapsePeriodTable") : t("usage.expandPeriodTable")}
              </button>
            </div>
            {periodTableExpanded && (
              <div id="usage-period-table">
                <div className="table-wrap">
                  <table>
                    <thead>
                      <tr>
                        <th>{t(`period.${period}`)}</th>
                        <th>{t("overview.input")}</th>
                        <th>{t("overview.output")}</th>
                        <th>{t("usage.cacheInput")}</th>
                        <th>{t("usage.estimatedCost")}</th>
                        <th>{t("usage.quality")}</th>
                      </tr>
                    </thead>
                    <tbody>
                      {periodPagination.pageItems.map((b) => (
                        <tr key={b.key}>
                          <td>{b.key}</td>
                          <td>{fmt(b.totals.input_tokens)}</td>
                          <td>{fmt(b.totals.output_tokens)}</td>
                          <td>{fmt(b.totals.cache_read_tokens + b.totals.cache_write_tokens)}</td>
                          <td>{formatCost(b.totals.cost_usd)}</td>
                          <td>{(b.totals.estimated_tokens ?? 0) > 0 ? <span className="status-badge warning">{t("usage.estimated")}</span> : <span className="status-badge success">{t("usage.exact")}</span>}</td>
                        </tr>
                      ))}
                    </tbody>
                  </table>
                </div>
                {periodPagination.totalPages > 1 && (
                  <CatalogPagination
                    page={periodPagination.page}
                    totalPages={periodPagination.totalPages}
                    start={periodPagination.start}
                    end={periodPagination.end}
                    total={periodPagination.total}
                    onChange={periodPagination.setPage}
                  />
                )}
              </div>
            )}
          </section>

          <section className="surface">
            <div className="surface-header">
              <h2>{t("usage.byModel")}</h2>
            </div>
            <div className="table-wrap">
              <table>
                <thead>
                  <tr>
                    <th>{t("usage.model")}</th>
                    <th>{t("usage.tokens")}</th>
                    <th>{t("usage.cost")}</th>
                    <th>{t("usage.quality")}</th>
                  </tr>
                </thead>
                <tbody>
                  {data.by_model.map((m) => (
                    <tr key={m.model}>
                      <td>{m.model}</td>
                      <td>{fmt(m.tokens)}</td>
                      <td>{formatCost(m.cost_usd)}</td>
                      <td className="usage-inline-quality">{(m.estimated_tokens ?? 0) > 0 ? <span className="status-badge warning">{t("usage.estimated")}</span> : <span className="status-badge success">{t("usage.exact")}</span>}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          </section>
        </>
      )}
    </div>
  );
}

function usageRangeBounds(range: UsageRange, customFrom: string, customTo: string) {
  const now = new Date();
  const today = formatLocalDate(now);
  switch (range) {
    case "today":
      return { from: today, to: today };
    case "7d": {
      const start = new Date(now);
      start.setDate(start.getDate() - 6);
      return { from: formatLocalDate(start), to: today };
    }
    case "month":
      return {
        from: formatLocalDate(new Date(now.getFullYear(), now.getMonth(), 1)),
        to: today,
      };
    case "custom":
      return { from: customFrom, to: customTo };
    default:
      return { from: "", to: "" };
  }
}

function formatLocalDate(value: Date) {
  const year = value.getFullYear();
  const month = String(value.getMonth() + 1).padStart(2, "0");
  const day = String(value.getDate()).padStart(2, "0");
  return `${year}-${month}-${day}`;
}

function CursorSourceCard({
  source,
  busy,
  onAction,
  t,
}: {
  source: CursorUsageSourceStatus;
  busy: string;
  onAction: (source: CursorUsageSourceStatus, action: "connect" | "sync" | "repair" | "disconnect") => void;
  t: (key: string, params?: Record<string, string | number>) => string;
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
          <span>{t("usage.cursorEstimated", { tokens: fmt(source.estimated_tokens) })}</span>
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

function totalUsageTokens(totals: { input_tokens: number; output_tokens: number; cache_read_tokens: number; cache_write_tokens: number }) {
  return totals.input_tokens + totals.output_tokens + totals.cache_read_tokens + totals.cache_write_tokens;
}

function estimatedCoverageLabel(estimated: number, total: number, t: (key: string, params?: Record<string, string | number>) => string) {
  if (estimated <= 0 || total <= 0) return t("usage.exact");
  return t("usage.coveragePercent", { coverage: Math.max(0, Math.round((1 - estimated / total) * 100)) });
}

function sourceStatusLabel(value: string, t: (key: string) => string) {
  const key = `usage.sourceStatus.${value.trim().toLowerCase()}`;
  const translated = t(key);
  return translated === key ? value.replace(/_/g, " ") : translated;
}

function Stat({ label, value, detail }: { label: string; value: string; detail?: string }) {
  return (
    <div className="metric-card">
      <div>
        <div className="label">{label}</div>
        <div className="value">{value}</div>
        {detail && <div className="muted metric-detail">{detail}</div>}
      </div>
    </div>
  );
}
