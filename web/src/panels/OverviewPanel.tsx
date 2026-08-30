import {
  Activity,
  AlertTriangle,
  ArrowRight,
  Bot,
  Boxes,
  Brain,
  Cable,
  CheckCircle2,
  CircleDollarSign,
  Clock3,
  DatabaseZap,
  Gauge,
  MessageSquareText,
  Sparkles,
  Workflow,
} from "lucide-react";
import { useEffect, useMemo, useState, type ReactNode } from "react";
import { api, type UsageBucket, type UsageReport } from "../api";
import { formatUsageCost, type SupportedCurrency, validCNYRate } from "../currency";
import { useI18n } from "../i18n";
import { useAsync } from "../useAsync";
import { usePolling } from "../hooks/usePolling";
import { runtimeLabel } from "./agents/agentUtils";
import {
  summarizeChannelHealth,
  summarizeModelHealth,
  type OverviewHealthSummary,
} from "./overviewHealth";

const STAT_RANGES = ["today", "7d", "30d"] as const;
type StatRange = (typeof STAT_RANGES)[number];
type TrendDimension = "total" | "framework" | "machine";
type TrendSeries = { id: string; label: string; tokens: number; values: number[] };

export function OverviewPanel() {
  const { language, t } = useI18n();
  const [range, setRange] = useState<StatRange>("today");
  const [displayCurrency, setDisplayCurrency] = useState<SupportedCurrency>("cny");
  const [trendDimension, setTrendDimension] = useState<TrendDimension>("total");
  const bounds = useMemo(() => usageRangeBounds(range), [range]);
  const usage = useAsync(
    () => api.usage("hourly", bounds.from, bounds.to),
    [range, bounds.from, bounds.to],
  );
  const currencyPreferences = useAsync(() => api.menubarSettings(), []);
  const providers = useAsync(() => api.providers(), []);
  const channels = useAsync(() => api.channels(), []);
  const providerMonitor = useAsync(() => api.providerMonitor(), []);

  const cnyRate = validCNYRate(currencyPreferences.data?.cny_rate ?? 7);
  const providerRows = providers.data ?? [];
  const channelHealth = useMemo(
    () => summarizeChannelHealth(channels.data ?? []),
    [channels.data],
  );
  const modelHealth = useMemo(
    () => summarizeModelHealth(providerMonitor.data, providerRows.filter((provider) => provider.enabled).length),
    [providerMonitor.data, providerRows],
  );
  const healthAttentionCount = channelHealth.issues
    + modelHealth.issues
    + modelHealth.pending
    + Number(Boolean(channels.error))
    + Number(Boolean(providerMonitor.error));
  const healthLoading = channels.loading || providerMonitor.loading || providers.loading;
  const healthConfiguredCount = channelHealth.total + modelHealth.total;
  const totals = usage.data?.totals;
  const totalTokens = totals
    ? totals.input_tokens + totals.output_tokens + totals.cache_read_tokens + totals.cache_write_tokens
    : 0;
  const estimatedTokens = totals?.estimated_tokens ?? 0;
  const exactCoverage = totalTokens > 0 ? Math.max(0, Math.round((1 - estimatedTokens / totalTokens) * 100)) : 100;
  const topRuntimes = usage.data?.by_runtime
    ? [...usage.data.by_runtime].filter((item) => item.tokens > 0).sort((left, right) => right.tokens - left.tokens).slice(0, 3)
    : [];
  const hourlyBuckets = useMemo(
    () => normalizeHourlyBuckets(usage.data?.buckets ?? [], bounds.from, bounds.to),
    [bounds.from, bounds.to, usage.data?.buckets],
  );
  const trendSeries = useMemo(
    () => buildTrendSeries(trendDimension, hourlyBuckets, usage.data ?? undefined, totalTokens, t),
    [hourlyBuckets, t, totalTokens, trendDimension, usage.data],
  );

  useEffect(() => {
    if (currencyPreferences.data?.currency) setDisplayCurrency(currencyPreferences.data.currency);
  }, [currencyPreferences.data?.currency]);

  usePolling(channels.reload, 30_000);
  usePolling(providerMonitor.reload, 30_000);

  function changeCurrency(next: SupportedCurrency) {
    setDisplayCurrency(next);
    if (currencyPreferences.data) {
      void api.saveMenubarSettings({ ...currencyPreferences.data, currency: next }).catch(() => undefined);
    }
  }

  const updatedAt = useMemo(
    () => new Date().toLocaleTimeString(),
    [channels.data, providerMonitor.data, providers.data, usage.data],
  );

  return (
    <div className="page-stack">
      <section className="surface overview-statistics">
        <div className="surface-header statistics-header">
          <div>
            <h2>{t("overview.statistics")}</h2>
            <p className="subtle-copy">{t("overview.statisticsHint")}</p>
          </div>
          <div className="statistics-header-actions">
            <div className="activity-line overview-updated-at">
              <Clock3 size={14} />
              <span>{t("overview.lastUpdated")} {updatedAt}</span>
            </div>
            <div className="segmented statistics-range" aria-label={t("overview.statisticsRange")}>
              {STAT_RANGES.map((value) => (
                <button
                  key={value}
                  type="button"
                  className={range === value ? "active" : ""}
                  aria-pressed={range === value}
                  onClick={() => setRange(value)}
                >
                  {t(`range.${value}`)}
                </button>
              ))}
            </div>
          </div>
        </div>

        {usage.error && <div className="surface-body error">{usage.error}</div>}
        {(usage.data?.warnings ?? []).map((warning) => <div className="surface-body warning" key={warning}>{warning}</div>)}
        <div className={`surface-body statistics-body${usage.loading ? " is-loading" : ""}`}>
          <div className="statistics-summary-grid">
            <section className="statistics-summary-card usage-summary-card">
              <header>
                <span className="statistics-summary-title"><Gauge size={19} />{t("overview.usageCard")}</span>
                <div className="currency-toggle" aria-label={t("usage.currency")}>
                  <button className={displayCurrency === "cny" ? "active" : ""} type="button" onClick={() => changeCurrency("cny")}>¥ RMB</button>
                  <button className={displayCurrency === "usd" ? "active" : ""} type="button" onClick={() => changeCurrency("usd")}>$ USD</button>
                </div>
              </header>
              <div className="usage-summary-metrics">
                <SummaryMetric
                  icon={<Gauge size={17} />}
                  label={t("overview.totalTokens")}
                  value={totals ? fmt(totalTokens) : "—"}
                  detail={totals && estimatedTokens > 0 ? t("usage.estimatedDetail", { tokens: fmt(estimatedTokens), coverage: exactCoverage }) : undefined}
                />
                <SummaryMetric icon={<MessageSquareText size={17} />} label={t("overview.sessionConversations")} value={totals ? fmt(totals.sessions ?? 0) : "—"} />
                <SummaryMetric icon={<CircleDollarSign size={17} />} label={t("overview.estimatedAmount")} value={totals ? formatUsageCost(totals.cost_usd, displayCurrency, cnyRate, language) : "—"} />
              </div>
            </section>

            <section className="statistics-summary-card framework-ranking-card">
              <header>
                <span className="statistics-summary-title"><Bot size={19} />{t("overview.frameworkRanking")}</span>
                <span className="ranking-top-label">TOP 3</span>
              </header>
              <div className="framework-ranking-list">
                {topRuntimes.length > 0 ? topRuntimes.map((runtime, index) => (
                  <FrameworkRank
                    key={runtime.runtime}
                    rank={index + 1}
                    runtime={runtime.runtime}
                    tokens={runtime.tokens}
                    share={totalTokens > 0 ? runtime.tokens / totalTokens : 0}
                  />
                )) : <div className="framework-ranking-empty">{t("overview.noFrameworkUsage")}</div>}
              </div>
            </section>
          </div>

          <div className="usage-trend-card">
            <div className="usage-trend-heading">
              <div>
                <h3>{t("overview.tokenTrend")}</h3>
                <span>{t(`range.${range}`)}</span>
              </div>
              <div className="usage-trend-actions">
                <span className="usage-trend-total">{fmt(totalTokens)}</span>
                <div className="trend-dimension-toggle" aria-label={t("overview.trendBreakdown")}>
                  {(["total", "framework", "machine"] as TrendDimension[]).map((dimension) => (
                    <button
                      key={dimension}
                      className={trendDimension === dimension ? "active" : ""}
                      type="button"
                      onClick={() => setTrendDimension(dimension)}
                    >
                      {t(`overview.trend.${dimension}`)}
                    </button>
                  ))}
                </div>
              </div>
            </div>
            <UsageTrend buckets={hourlyBuckets} series={trendSeries} range={range} totalTokens={totalTokens} />
            <div className="usage-breakdown">
              <Stat label={t("overview.input")} value={fmt(totals?.input_tokens ?? 0)} />
              <Stat label={t("overview.output")} value={fmt(totals?.output_tokens ?? 0)} />
              <Stat
                label={t("usage.cacheInput")}
                value={fmt((totals?.cache_read_tokens ?? 0) + (totals?.cache_write_tokens ?? 0))}
              />
            </div>
          </div>
        </div>
      </section>

      <div className="content-grid overview-content-grid">
        <section className="surface overview-health-surface">
          <div className="surface-header overview-health-header">
            <div>
              <h2>{t("overview.connectionHealth")}</h2>
              <p className="subtle-copy">{t("overview.connectionHealthHint")}</p>
            </div>
            <span className={`status-badge ${healthLoading || healthConfiguredCount === 0 ? "" : healthAttentionCount > 0 ? "warning" : "success"}`}>
              {healthLoading
                ? <Clock3 size={13} />
                : healthAttentionCount > 0
                  ? <AlertTriangle size={13} />
                  : <CheckCircle2 size={13} />}
              {healthLoading
                ? t("common.loading")
                : healthAttentionCount > 0
                ? t("overview.healthAttention", { count: healthAttentionCount })
                : healthConfiguredCount === 0
                  ? t("overview.healthNotConfigured")
                  : t("overview.healthAllGood")}
            </span>
          </div>
          <div className="overview-health-list">
            <HealthSummaryRow
              icon={<Cable size={19} />}
              title={t("overview.channelConnections")}
              summary={channelHealth}
              loading={channels.loading}
              error={channels.error}
              summaryText={channelHealth.total > 0
                ? t("overview.channelHealthSummary", {
                  healthy: channelHealth.healthy,
                  enabled: Math.max(0, channelHealth.total - channelHealth.inactive),
                  inactive: channelHealth.inactive,
                })
                : t("overview.noChannelsConfigured")}
              pendingLabel=""
              onResolve={() => { window.location.hash = "#channels"; }}
              t={t}
            />
            <HealthSummaryRow
              icon={<DatabaseZap size={19} />}
              title={t("overview.modelServices")}
              summary={modelHealth}
              loading={providerMonitor.loading || providers.loading}
              error={providerMonitor.error || providers.error}
              summaryText={modelHealth.total > 0
                ? t("overview.modelHealthSummary", { healthy: modelHealth.healthy, total: modelHealth.total })
                : t("overview.noModelsConfigured")}
              pendingLabel={modelHealth.pending > 0 ? t("overview.healthPendingHint") : ""}
              onResolve={() => { window.location.hash = "#gateway"; }}
              t={t}
            />
          </div>
        </section>

      </div>

    </div>
  );
}

function HealthSummaryRow({
  icon,
  title,
  summary,
  loading,
  error,
  summaryText,
  pendingLabel,
  onResolve,
  t,
}: {
  icon: ReactNode;
  title: string;
  summary: OverviewHealthSummary;
  loading: boolean;
  error: string | null;
  summaryText: string;
  pendingLabel: string;
  onResolve: () => void;
  t: (key: string, values?: Record<string, string | number>) => string;
}) {
  const needsAttention = Boolean(error) || summary.issues > 0 || summary.pending > 0;
  const statusLabel = loading
    ? t("common.loading")
    : error
      ? t("overview.healthUnavailable")
      : summary.issues > 0
        ? t("overview.healthIssues", { count: summary.issues })
        : summary.pending > 0
          ? t("overview.healthPending", { count: summary.pending })
          : summary.total === 0
            ? t("overview.healthNotConfigured")
            : t("overview.healthNormal");
  const issuePreview = error
    ? t("overview.healthLoadFailed")
    : summary.issueLabels.length > 0
      ? t("overview.healthIssuePreview", {
        names: summary.issueLabels.slice(0, 2).join("、"),
        more: summary.issueLabels.length > 2 ? ` +${summary.issueLabels.length - 2}` : "",
      })
      : pendingLabel;

  return (
    <div className="overview-health-row">
      <span className={`overview-health-icon ${needsAttention ? "warning" : "success"}`}>{icon}</span>
      <span className="overview-health-copy">
        <strong>{title}</strong>
        <span>{loading ? t("common.loading") : summaryText}</span>
        {issuePreview && <small title={error ?? summary.issueLabels.join("、")}>{issuePreview}</small>}
      </span>
      <span className={`status-badge ${needsAttention ? "warning" : summary.total > 0 ? "success" : ""}`}>
        <span className="status-dot" />
        {statusLabel}
      </span>
      {needsAttention && !loading && (
        <button className="ghost-action overview-health-action" type="button" onClick={onResolve}>
          {t("overview.resolveHealth")}
          <ArrowRight size={14} />
        </button>
      )}
    </div>
  );
}

function SummaryMetric({ icon, label, value, detail }: { icon: ReactNode; label: string; value: string; detail?: string }) {
  return (
    <div className="usage-summary-metric">
      <span className="usage-summary-metric-label">{icon}{label}</span>
      <strong>{value}</strong>
      {detail && <small>{detail}</small>}
    </div>
  );
}

function FrameworkRank({ rank, runtime, tokens, share }: { rank: number; runtime: string; tokens: number; share: number }) {
  const RuntimeIcon = runtimeIcon(runtime);
  return (
    <div className="framework-rank-row">
      <span className={`framework-rank-number rank-${rank}`}>{rank}</span>
      <span className="framework-rank-icon"><RuntimeIcon size={17} /></span>
      <span className="framework-rank-copy">
        <strong>{runtimeLabel(runtime)}</strong>
        <span>{fmt(tokens)} tokens</span>
      </span>
      <span className="framework-rank-share">{Math.round(share * 100)}%</span>
    </div>
  );
}

function runtimeIcon(runtime: string) {
  switch (runtime.trim().toLowerCase()) {
    case "claude":
    case "claudecode":
      return Sparkles;
    case "codex":
    case "codex-app":
      return Bot;
    case "cursor":
      return Workflow;
    case "gemini":
      return Sparkles;
    case "iflow":
      return Activity;
    case "kimi":
      return Brain;
    case "opencode":
      return Boxes;
    default:
      return Bot;
  }
}

function normalizeHourlyBuckets(buckets: UsageBucket[], from: string, to: string) {
  const byKey = new Map(buckets.filter((bucket) => bucket.key.includes(" ")).map((bucket) => [bucket.key, bucket]));
  const byDay = new Map(buckets.filter((bucket) => !bucket.key.includes(" ")).map((bucket) => [bucket.key, bucket]));
  const start = new Date(`${from}T00:00:00`);
  const end = new Date(`${to}T23:00:00`);
  if (Number.isNaN(start.getTime()) || Number.isNaN(end.getTime())) return buckets;
  const normalized: UsageBucket[] = [];
  for (const current = new Date(start); current <= end; current.setHours(current.getHours() + 1)) {
    const key = formatHourlyKey(current);
    const exact = byKey.get(key);
    const daily = byDay.get(formatLocalDate(current));
    normalized.push(combineHourlyBucket(key, exact, daily));
  }
  return normalized;
}

function buildTrendSeries(
  dimension: TrendDimension,
  buckets: UsageBucket[],
  report: UsageReport | undefined,
  totalTokens: number,
  t: (key: string) => string,
): TrendSeries[] {
  if (dimension === "total") {
    return [{ id: "total", label: t("overview.trend.total"), tokens: totalTokens, values: buckets.map(totalBucketTokens) }];
  }
  if (dimension === "framework") return buildFrameworkTrendSeries(buckets, report, t);
  return buildMachineTrendSeries(buckets, report, totalTokens, t);
}

function buildFrameworkTrendSeries(
  buckets: UsageBucket[],
  report: UsageReport | undefined,
  t: (key: string) => string,
): TrendSeries[] {
  const ordered = [...(report?.by_runtime ?? [])]
    .filter((runtime) => runtime.tokens > 0)
    .sort((left, right) => right.tokens - left.tokens);
  const visible = ordered.slice(0, 5);
  const hidden = new Set(ordered.slice(5).map((runtime) => runtime.runtime));
  const series = visible.map((runtime) => ({
    id: runtime.runtime,
    label: runtimeLabel(runtime.runtime),
    tokens: runtime.tokens,
    values: buckets.map((bucket) => bucket.by_runtime?.find((item) => item.runtime === runtime.runtime)?.tokens ?? 0),
  }));
  if (hidden.size > 0) {
    series.push({
      id: "other-frameworks",
      label: t("overview.trend.other"),
      tokens: ordered.slice(5).reduce((sum, runtime) => sum + runtime.tokens, 0),
      values: buckets.map((bucket) => (bucket.by_runtime ?? [])
        .filter((runtime) => hidden.has(runtime.runtime))
        .reduce((sum, runtime) => sum + runtime.tokens, 0)),
    });
  }
  return series;
}

function buildMachineTrendSeries(
  buckets: UsageBucket[],
  report: UsageReport | undefined,
  totalTokens: number,
  t: (key: string) => string,
): TrendSeries[] {
  const machines = [...(report?.by_machine ?? [])]
    .filter((machine) => totalUsageTokens(machine.totals) > 0)
    .sort((left, right) => totalUsageTokens(right.totals) - totalUsageTokens(left.totals));
  if (machines.length === 0) {
    return totalTokens > 0
      ? [{ id: "current-machine", label: t("remote.currentMachine"), tokens: totalTokens, values: buckets.map(totalBucketTokens) }]
      : [];
  }
  const visible = machines.slice(0, 5);
  const hidden = machines.slice(5);
  const timelineKeys = buckets.map((bucket) => bucket.key);
  const series = visible.map((machine) => {
    return {
      id: machine.target_id,
      label: machine.target_name,
      tokens: totalUsageTokens(machine.totals),
      values: hourlyValuesForTimeline(machine.buckets ?? [], timelineKeys),
    };
  });
  if (hidden.length > 0) {
    const hiddenValues = hidden.map((machine) => hourlyValuesForTimeline(machine.buckets ?? [], timelineKeys));
    series.push({
      id: "other-machines",
      label: t("overview.trend.other"),
      tokens: hidden.reduce((sum, machine) => sum + totalUsageTokens(machine.totals), 0),
      values: timelineKeys.map((_, index) => hiddenValues.reduce((sum, values) => sum + (values[index] ?? 0), 0)),
    });
  }
  return series;
}

function combineHourlyBucket(key: string, exact?: UsageBucket, daily?: UsageBucket): UsageBucket {
  if (!daily) return exact ?? { key, totals: emptyUsageTotals(), by_runtime: [] };
  const totals = exact ? { ...exact.totals } : emptyUsageTotals();
  addScaledUsageTotals(totals, daily.totals, 1 / 24);
  const runtimes = new Map<string, { runtime: string; tokens: number; cost_usd: number; estimated_tokens?: number }>();
  for (const runtime of exact?.by_runtime ?? []) runtimes.set(runtime.runtime, { ...runtime });
  for (const runtime of daily.by_runtime ?? []) {
    const current = runtimes.get(runtime.runtime) ?? { runtime: runtime.runtime, tokens: 0, cost_usd: 0, estimated_tokens: 0 };
    current.tokens += runtime.tokens / 24;
    current.cost_usd += runtime.cost_usd / 24;
    current.estimated_tokens = (current.estimated_tokens ?? 0) + (runtime.estimated_tokens ?? 0) / 24;
    runtimes.set(runtime.runtime, current);
  }
  return { key, totals, by_runtime: [...runtimes.values()] };
}

function hourlyValuesForTimeline(rawBuckets: UsageBucket[], timelineKeys: string[]) {
  const exact = new Map(rawBuckets.filter((bucket) => bucket.key.includes(" ")).map((bucket) => [bucket.key, totalBucketTokens(bucket)]));
  const daily = new Map(rawBuckets.filter((bucket) => !bucket.key.includes(" ")).map((bucket) => [bucket.key, totalBucketTokens(bucket) / 24]));
  return timelineKeys.map((key) => (exact.get(key) ?? 0) + (daily.get(key.slice(0, 10)) ?? 0));
}

function addScaledUsageTotals(target: UsageBucket["totals"], value: UsageBucket["totals"], scale: number) {
  target.input_tokens += value.input_tokens * scale;
  target.output_tokens += value.output_tokens * scale;
  target.cache_read_tokens += value.cache_read_tokens * scale;
  target.cache_write_tokens += value.cache_write_tokens * scale;
  target.cost_usd += value.cost_usd * scale;
  target.records += value.records * scale;
  target.sessions += value.sessions * scale;
  target.estimated_tokens += value.estimated_tokens * scale;
  target.estimated_records += value.estimated_records * scale;
}

function emptyUsageTotals() {
  return {
    input_tokens: 0,
    output_tokens: 0,
    cache_read_tokens: 0,
    cache_write_tokens: 0,
    cost_usd: 0,
    records: 0,
    sessions: 0,
    estimated_tokens: 0,
    estimated_records: 0,
  };
}

function totalUsageTokens(totals: UsageBucket["totals"]) {
  return totals.input_tokens + totals.output_tokens + totals.cache_read_tokens + totals.cache_write_tokens;
}

function formatHourlyKey(value: Date) {
  return `${formatLocalDate(value)} ${String(value.getHours()).padStart(2, "0")}:00`;
}

function UsageTrend({
  buckets,
  series,
  range,
  totalTokens,
}: {
  buckets: UsageBucket[];
  series: TrendSeries[];
  range: StatRange;
  totalTokens: number;
}) {
  const max = Math.max(...series.flatMap((item) => item.values), 1);
  const width = 920;
  const height = 180;
  const insetX = 8;
  const insetY = 12;
  const chartWidth = width - insetX * 2;
  const chartHeight = height - insetY * 2;
  const plotted = series.map((item) => {
    const points = item.values.map((value, index) => ({
      x: item.values.length <= 1 ? width / 2 : insetX + (index / (item.values.length - 1)) * chartWidth,
      y: insetY + chartHeight - (value / max) * chartHeight,
    }));
    const line = points.map((point, index) => `${index === 0 ? "M" : "L"}${point.x},${point.y}`).join(" ");
    const area = points.length
      ? `${line} L${points[points.length - 1].x},${height - insetY} L${points[0].x},${height - insetY} Z`
      : "";
    return { ...item, points, line, area };
  });
  const labels = chartLabels(buckets, range);
  const showPoints = buckets.length <= 48;

  return (
    <div className="usage-trend-wrap">
      {series.some((item) => item.id !== "total") && (
        <div className="trend-series-legend">
          {series.map((item, index) => (
            <span key={item.id}>
              <i className={`trend-series-color color-${index % 6}`} />
              <strong>{item.label}</strong>
              <small>{fmt(item.tokens)} · {totalTokens > 0 ? Math.round(item.tokens / totalTokens * 100) : 0}%</small>
            </span>
          ))}
        </div>
      )}
      <div className="usage-trend" role="img" aria-label={series.map((item) => `${item.label}: ${fmt(item.tokens)}`).join(", ")}>
        <svg viewBox={`0 0 ${width} ${height}`} preserveAspectRatio="none" aria-hidden="true">
          {[0.25, 0.5, 0.75, 1].map((ratio) => (
            <line key={ratio} x1="0" y1={height * ratio} x2={width} y2={height * ratio} className="usage-grid-line" />
          ))}
          {plotted.length === 1 && plotted[0].area && <path d={plotted[0].area} className="usage-area" />}
          {plotted.map((item, seriesIndex) => (
            <g key={item.id} className={`usage-series color-${seriesIndex % 6}`}>
              {item.line && <path d={item.line} className="usage-line" />}
              {showPoints && item.points.map((point, index) => (
                <circle key={`${item.id}-${buckets[index]?.key ?? index}`} cx={point.x} cy={point.y} r="3.3" className="usage-point" />
              ))}
            </g>
          ))}
        </svg>
        {series.length === 0 && <span className="usage-trend-empty">—</span>}
        <div className="usage-trend-labels">
          {labels.map((label, index) => <span key={`${label}-${index}`}>{label}</span>)}
        </div>
      </div>
    </div>
  );
}

function totalBucketTokens(bucket: UsageBucket) {
  return bucket.totals.input_tokens
    + bucket.totals.output_tokens
    + bucket.totals.cache_read_tokens
    + bucket.totals.cache_write_tokens;
}

function chartLabels(buckets: UsageBucket[], range: StatRange) {
  if (buckets.length === 0) return ["—"];
  const count = Math.min(6, buckets.length);
  const indexes = Array.from({ length: count }, (_, index) => Math.round(index * (buckets.length - 1) / Math.max(1, count - 1)));
  return indexes.map((index) => {
    const key = buckets[index].key;
    const [date, time = ""] = key.split(" ");
    return range === "today" ? time : `${date.slice(5)} ${time.slice(0, 2)}时`;
  });
}

function Stat({ label, value }: { label: string; value: string }) {
  return (
    <div className="stat">
      <div className="label">{label}</div>
      <div className="value">{value}</div>
    </div>
  );
}

export function fmt(n: number): string {
  if (n >= 1e9) return (n / 1e9).toFixed(2) + "B";
  if (n >= 1e6) return (n / 1e6).toFixed(2) + "M";
  if (n >= 1e3) return (n / 1e3).toFixed(2) + "K";
  return String(n);
}

function usageRangeBounds(range: StatRange) {
  const now = new Date();
  const start = new Date(now);
  if (range === "7d") start.setDate(start.getDate() - 6);
  if (range === "30d") start.setDate(start.getDate() - 29);
  return { from: formatLocalDate(start), to: formatLocalDate(now) };
}

function formatLocalDate(value: Date) {
  const year = value.getFullYear();
  const month = String(value.getMonth() + 1).padStart(2, "0");
  const day = String(value.getDate()).padStart(2, "0");
  return `${year}-${month}-${day}`;
}
