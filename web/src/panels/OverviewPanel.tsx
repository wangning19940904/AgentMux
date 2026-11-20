import { BrandIcon } from "../components/BrandIcon";
import { SlidingSelection } from "../components/SlidingSelection";
import {
  AlertTriangle,
  ArrowRight,
  Bot,
  Brain,
  Cable,
  CheckCircle2,
  Clock3,
  DatabaseZap,
} from "lucide-react";
import { useCallback, useId, useMemo, useState, type ReactNode } from "react";
import { api, type ModelStat, type UsageBucket, type UsageReport } from "../api";
import { formatUsageCost, validCNYRate } from "../currency";
import { useI18n } from "../i18n";
import { useAsync } from "../useAsync";
import { usePolling } from "../hooks/usePolling";
import { runtimeLabel } from "./agents/agentUtils";
import { buildModelTrendSeries } from "./overviewModelTrend";
import {
  summarizeChannelHealth,
  summarizeFrameworkHealth,
  type OverviewHealthSummary,
} from "./overviewHealth";

const STAT_RANGES = ["today", "7d", "30d"] as const;
type StatRange = (typeof STAT_RANGES)[number];
type TrendDimension = "total" | "framework" | "model" | "machine";
type TrendSeries = { id: string; label: string; tokens: number; values: number[] };

export function OverviewPanel() {
  const { language, t } = useI18n();
  const [range, setRange] = useState<StatRange>("today");
  const [trendDimension, setTrendDimension] = useState<TrendDimension>("total");
  const [today, setToday] = useState(() => formatLocalDate(new Date()));
  const bounds = useMemo(() => usageRangeBounds(range, today), [range, today]);
  const usageSnapshot = useAsync(
    async () => {
      const report = await api.usage("hourly", bounds.from, bounds.to);
      return { report, updatedAt: new Date() };
    },
    [range, bounds.from, bounds.to],
  );
  const usage = { ...usageSnapshot, data: usageSnapshot.data?.report ?? null };
  const currencyPreferences = useAsync(() => api.menubarSettings(), []);
  const channels = useAsync(() => api.channelHealth(), []);
  // Model availability is the frameworks' own "reply ok" tests.
  const frameworkHealth = useAsync(() => api.frameworkHealthOverview(), []);

  const cnyRate = validCNYRate(currencyPreferences.data?.cny_rate ?? 7);
  const channelHealth = useMemo(
    () => summarizeChannelHealth(channels.data ?? []),
    [channels.data],
  );
  const modelHealth = useMemo(
    () => summarizeFrameworkHealth(frameworkHealth.data ?? []),
    [frameworkHealth.data],
  );
  const healthAttentionCount = channelHealth.issues
    + modelHealth.issues
    + modelHealth.pending
    + Number(Boolean(channels.error))
    + Number(Boolean(frameworkHealth.error));
  const healthLoading = channels.loading || frameworkHealth.loading;
  const healthConfiguredCount = channelHealth.total + modelHealth.total;
  const totals = usage.data?.totals;
  const totalTokens = totals
    ? totals.input_tokens + totals.output_tokens + totals.cache_read_tokens + totals.cache_write_tokens
    : 0;
  const cacheTokens = (totals?.cache_read_tokens ?? 0) + (totals?.cache_write_tokens ?? 0);
  const estimatedTokens = totals?.estimated_tokens ?? 0;
  const exactCoverage = totalTokens > 0 ? Math.max(0, Math.round((1 - estimatedTokens / totalTokens) * 100)) : 100;
  const hourlyBuckets = useMemo(
    () => normalizeHourlyBuckets(usage.data?.buckets ?? [], bounds.from, bounds.to),
    [bounds.from, bounds.to, usage.data?.buckets],
  );
  const trendSeries = useMemo(
    () => buildTrendSeries(trendDimension, hourlyBuckets, usage.data ?? undefined, totalTokens, t),
    [hourlyBuckets, t, totalTokens, trendDimension, usage.data],
  );

  const refreshUsage = useCallback(() => {
    const currentDate = formatLocalDate(new Date());
    if (currentDate !== today) {
      // Changing the date reloads through useAsync with the new bounds.
      setToday(currentDate);
      return;
    }
    void usage.reload();
  }, [today, usage.reload]);
  usePolling(refreshUsage, 30_000);
  usePolling(currencyPreferences.reload, 30_000);
  usePolling(channels.reload, 30_000);
  usePolling(frameworkHealth.reload, 30_000);

  const updatedAt = usageSnapshot.data?.updatedAt.toLocaleTimeString() ?? "—";

  return (
    <div className="page-stack">
      <section className="overview-statistics">
        <div className="statistics-header">
          <div>
            <h2>{t("overview.statistics")}</h2>
            <p className="subtle-copy">{t("overview.statisticsHint")}</p>
          </div>
          <div className="statistics-header-actions">
            <SlidingSelection className="segmented statistics-range" selected={range} role="group" aria-label={t("overview.statisticsRange")}>
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
            </SlidingSelection>
            <span className="overview-updated-at">{t("overview.lastUpdated")} {updatedAt}</span>
          </div>
        </div>

        {usage.error && <div className="session-notice error" role="alert">{usage.error}</div>}
        {(usage.data?.warnings ?? []).map((warning) => <div className="session-notice warning" key={warning}>{warning}</div>)}
        <div className={`statistics-body${usage.loading ? " is-loading" : ""}`}>
          <section className="usage-summary-card" aria-label={t("overview.usageCard")}>
            <div className="usage-summary-metrics">
              <SummaryMetric
                label={t("overview.totalTokens")}
                value={totals ? fmt(totalTokens) : "—"}
                detail={totals ? <>
                  {t("overview.input")} {fmt(totals.input_tokens)} · {t("overview.output")} {fmt(totals.output_tokens)}
                  {estimatedTokens > 0 && <><br />{t("usage.estimatedDetail", { tokens: fmt(estimatedTokens), coverage: exactCoverage })}</>}
                </> : undefined}
              />
              <SummaryMetric
                label={t("usage.cacheRead")}
                value={totals ? fmt(totals.cache_read_tokens) : "—"}
                detail={totals ? `${t("usage.cacheWrite")} ${fmt(totals.cache_write_tokens)}` : undefined}
              />
              <SummaryMetric
                label={t("overview.sessionConversations")}
                value={totals ? fmt(totals.sessions ?? 0) : "—"}
                detail={totals ? `${fmt(totals.records)} ${t("overview.records")}` : undefined}
              />
              <SummaryMetric
                label={t("overview.estimatedAmount")}
                value={totals ? formatUsageCost(totals.cost_usd, "cny", cnyRate, language) : "—"}
                detail={totals ? `${formatUsageCost(totals.cost_usd, "usd", cnyRate, language)} USD` : undefined}
              />
            </div>
            <details className="usage-pricing-note">
              <summary>{t("overview.pricingDetails")}</summary>
              <p>{t("overview.includedCache", { tokens: fmt(cacheTokens) })}</p>
              <div className="usage-breakdown">
                <Stat label={t("usage.uncachedInput")} value={totals ? fmt(totals.input_tokens) : "—"} />
                <Stat label={t("overview.output")} value={totals ? fmt(totals.output_tokens) : "—"} />
                <Stat label={t("usage.cacheRead")} value={totals ? fmt(totals.cache_read_tokens) : "—"} />
                <Stat label={t("usage.cacheWrite")} value={totals ? fmt(totals.cache_write_tokens) : "—"} />
              </div>
              <p>{t("overview.exchangeRate", { rate: cnyRate })}</p>
              <p>{t("overview.pricingFormula")}</p>
              <p>{t("overview.pricingSource")}</p>
            </details>
          </section>

          <div className="usage-trend-card">
            <div className="usage-trend-heading">
              <div className="usage-trend-title">
                <h3>{t("overview.tokenTrend")}</h3>
                <span className="usage-trend-total">{fmt(totalTokens)}</span>
              </div>
              <SlidingSelection className="segmented trend-dimension-toggle" selected={trendDimension} role="group" aria-label={t("overview.trendBreakdown")}>
                {(["total", "framework", "model", "machine"] as TrendDimension[]).map((dimension) => (
                  <button
                    key={dimension}
                    className={trendDimension === dimension ? "active" : ""}
                    type="button"
                    aria-pressed={trendDimension === dimension}
                    onClick={() => setTrendDimension(dimension)}
                  >
                    {t(`overview.trend.${dimension}`)}
                  </button>
                ))}
              </SlidingSelection>
            </div>
            <UsageTrend buckets={hourlyBuckets} series={trendSeries} range={range} totalTokens={totalTokens} />
          </div>

          <div className="overview-rankings">
            <UsageRanking
              title={t("overview.frameworkRanking")}
              icon={<Bot size={17} />}
              emptyText={t("overview.noFrameworkUsage")}
              totalTokens={totalTokens}
              items={(usage.data?.by_runtime ?? []).map((runtime) => ({
                id: runtime.runtime,
                label: runtimeLabel(runtime.runtime, t),
                tokens: runtime.tokens,
                icon: <BrandIcon name={runtime.runtime} />,
              }))}
            />
            <UsageRanking
              title={t("overview.modelRanking")}
              icon={<Brain size={17} />}
              emptyText={t("overview.noModelUsage")}
              totalTokens={totalTokens}
              items={(usage.data?.by_model ?? []).map((model) => ({
                id: model.model,
                label: model.model.trim() || t("overview.unknownModel"),
                tokens: model.tokens,
                icon: <BrandIcon name={model.model} kind="model" />,
              }))}
            />
          </div>
        </div>
      </section>

      <section className="overview-health-surface">
        <div className="overview-health-header">
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
            title={t("overview.frameworkAvailability")}
            summary={modelHealth}
            loading={frameworkHealth.loading}
            error={frameworkHealth.error}
            summaryText={modelHealth.total > 0
              ? t("overview.frameworkHealthSummary", { healthy: modelHealth.healthy, total: modelHealth.total })
              : t("overview.noFrameworkTests")}
            pendingLabel=""
            onResolve={() => { window.location.hash = "#frameworks"; }}
            t={t}
          />
        </div>
      </section>

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

function SummaryMetric({ label, value, detail }: { label: string; value: ReactNode; detail?: ReactNode }) {
  return (
    <div className="usage-summary-metric">
      <strong>{value}</strong>
      <span className="usage-summary-metric-label">{label}</span>
      {detail && <small>{detail}</small>}
    </div>
  );
}

type UsageRankingItem = { id: string; label: string; tokens: number; icon: ReactNode };

function UsageRanking({ title, icon, emptyText, totalTokens, items }: {
  title: string;
  icon: ReactNode;
  emptyText: string;
  totalTokens: number;
  items: UsageRankingItem[];
}) {
  const { t } = useI18n();
  const [expanded, setExpanded] = useState(false);
  const listID = useId();
  const ranked = items.filter((item) => item.tokens > 0)
    .sort((left, right) => right.tokens - left.tokens || left.id.localeCompare(right.id));
  const visible = expanded ? ranked : ranked.slice(0, 3);
  return (
    <section className="framework-ranking-card" aria-label={title}>
      <header>
        <h3 className="statistics-summary-title">{icon}{title}</h3>
        {ranked.length > 3 && (
          <button className="ranking-expand" type="button" aria-expanded={expanded} aria-controls={listID} onClick={() => setExpanded((open) => !open)}>
            {expanded ? t("overview.rankingCollapse") : t("overview.rankingExpand", { count: ranked.length })}
          </button>
        )}
      </header>
      <div className="framework-ranking-list" id={listID}>
        {visible.length > 0 ? visible.map((item) => (
          <UsageRank key={item.id} icon={item.icon} label={item.label} tokens={item.tokens} share={totalTokens > 0 ? item.tokens / totalTokens : 0} />
        )) : <div className="framework-ranking-empty">{emptyText}</div>}
      </div>
    </section>
  );
}

function UsageRank({ icon, label, tokens, share }: { icon: ReactNode; label: string; tokens: number; share: number }) {
  return (
    <div className="framework-rank-row">
      {icon}
      <span className="framework-rank-copy">
        <strong title={label}>{label}</strong>
        <span>{fmt(tokens)} tokens</span>
      </span>
      <span className="framework-rank-share">{Math.round(share * 100)}%</span>
    </div>
  );
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
  if (dimension === "model") return buildModelTrendSeries(buckets, t);
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
    label: runtimeLabel(runtime.runtime, t),
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
  if (!daily) return exact ?? { key, totals: emptyUsageTotals(), by_model: [], by_runtime: [] };
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
  const models = new Map<string, ModelStat>();
  for (const model of exact?.by_model ?? []) models.set(model.model, { ...model });
  for (const model of daily.by_model ?? []) {
    const current = models.get(model.model) ?? { model: model.model, tokens: 0, cost_usd: 0, estimated_tokens: 0 };
    current.tokens += model.tokens / 24;
    current.cost_usd += model.cost_usd / 24;
    current.estimated_tokens = (current.estimated_tokens ?? 0) + (model.estimated_tokens ?? 0) / 24;
    models.set(model.model, current);
  }
  return { key, totals, by_model: [...models.values()], by_runtime: [...runtimes.values()] };
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
  const [hovered, setHovered] = useState<number | null>(null);
  const groups = useMemo(() => {
    const grouped = new Map<string, number[]>();
    buckets.forEach((bucket, index) => {
      const key = range === "today" ? bucket.key : bucket.key.slice(0, 10);
      const indices = grouped.get(key) ?? [];
      indices.push(index);
      grouped.set(key, indices);
    });
    return [...grouped].map(([key, indices]) => ({ key, indices }));
  }, [buckets, range]);
  const bars = groups.map((group) => ({
    key: group.key,
    values: series.map((item) => group.indices.reduce((sum, index) => sum + (item.values[index] ?? 0), 0)),
  }));
  const peak = Math.max(...bars.map((bar) => bar.values.reduce((sum, value) => sum + value, 0)), 0);
  const max = peak || 1;
  const width = 920;
  const height = 164;
  const baseline = height - 4;
  const step = width / Math.max(bars.length, 1);
  const barWidth = Math.max(1, step - (bars.length > 40 ? 2 : 4));
  const labels = range === "today"
    ? ["00", "04", "08", "12", "16", "20", "23"]
    : chartLabels(groups.map((group) => ({ key: group.key } as UsageBucket)), range);
  const selected = hovered === null ? undefined : bars[hovered];

  return (
    <div className="usage-trend-wrap">
      {series.some((item) => item.id !== "total") && (
        <div className="trend-series-legend">
          {series.map((item, index) => (
            <span key={item.id}>
              <i className={`trend-series-color color-${index % 6}`} />
              <strong title={item.label}>{item.label}</strong>
              <small>{fmt(item.tokens)} · {totalTokens > 0 ? Math.round(item.tokens / totalTokens * 100) : 0}%</small>
            </span>
          ))}
        </div>
      )}
      <div className="usage-trend" onMouseLeave={() => setHovered(null)}>
        <span className="usage-trend-maximum">{fmt(peak)}</span>
        <svg viewBox={`0 0 ${width} ${height}`} preserveAspectRatio="none" role="img"
          aria-label={series.map((item) => `${item.label}: ${fmt(item.tokens)}`).join(", ")}>
          <line x1="0" y1={baseline} x2={width} y2={baseline} className="usage-grid-line" />
          {bars.map((bar, index) => {
            let bottom = baseline;
            const total = bar.values.reduce((sum, value) => sum + value, 0);
            return (
              <g key={bar.key} className={`usage-bar-group${hovered === index ? " hovered" : ""}`}
                tabIndex={total > 0 ? 0 : undefined}
                onMouseEnter={() => setHovered(index)} onFocus={() => setHovered(index)} onBlur={() => setHovered(null)}>
                <title>{bar.key} · {fmt(total)} tokens</title>
                <rect x={index * step} y="0" width={step} height={baseline} fill="transparent" />
                {bar.values.map((value, seriesIndex) => {
                  const barHeight = value > 0 ? Math.max(2, value / max * (baseline - 18)) : 0;
                  bottom -= barHeight;
                  return value > 0 && <g key={series[seriesIndex].id} className={`usage-series color-${seriesIndex % 6}`}>
                    <rect className="usage-bar" x={index * step + 2} y={bottom} width={barWidth} height={barHeight} rx="2" />
                    <rect className="usage-bar-cap" x={index * step + 2} y={bottom} width={barWidth} height={Math.min(5, barHeight)} rx="1" />
                  </g>;
                })}
              </g>
            );
          })}
        </svg>
        {selected && <div className="usage-chart-tooltip" role="status">
          <strong>{selected.key}</strong>
          {series.map((item, index) => <span key={item.id}>{item.label}<b>{fmt(selected.values[index])}</b></span>)}
        </div>}
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
    return range === "today" ? time.slice(0, 2) : date.slice(5);
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

function usageRangeBounds(range: StatRange, today: string) {
  const start = new Date(`${today}T00:00:00`);
  if (range === "7d") start.setDate(start.getDate() - 6);
  if (range === "30d") start.setDate(start.getDate() - 29);
  return { from: formatLocalDate(start), to: today };
}

function formatLocalDate(value: Date) {
  const year = value.getFullYear();
  const month = String(value.getMonth() + 1).padStart(2, "0");
  const day = String(value.getDate()).padStart(2, "0");
  return `${year}-${month}-${day}`;
}
