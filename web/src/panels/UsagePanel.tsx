import {
  Activity, Bot, Boxes, Brain, CircleDollarSign, Clock3, FolderGit2, Gauge, MessageSquareText, MonitorDot,
  RefreshCw, Sparkles, Workflow,
} from "lucide-react";
import { useCallback, useEffect, useId, useMemo, useState, type ReactNode } from "react";
import { api, type MenubarSettings, type SuperAgent, type UsageReport, type UsageTotals } from "../api";
import { CatalogPagination, useCatalogPagination } from "../components/CatalogPagination";
import { formatUsageCost, validCNYRate } from "../currency";
import { usePolling } from "../hooks/usePolling";
import { useI18n } from "../i18n";
import { useCachedResource } from "../useCachedResource";
import { runtimeLabel } from "./agents/agentUtils";
import { agentLabel, emptyTotals, formatShare, formatTokens, totalTokens, type AgentNames } from "./usage/format";
import { UsageFilterBar, type FilterOption, type FilterOptions } from "./usage/UsageFilterBar";
import { UsageCurrencySettings, UsageSources } from "./usage/UsageSources";
import { UsageTrendChart } from "./usage/UsageTrendChart";
import { prefetchUsage, useUsageReport, type UsageRequest } from "./usage/usageData";
import {
  formatLocalDate, includesToday, rangeBounds, resolvePeriod, toggleFilterValue, viewFromHashQuery, viewToHashQuery,
  type FilterDimension, type UsageView,
} from "./usage/usageQuery";
import { TREND_DIMENSIONS, buildTrendSeries, timelineFor, type TrendDimension } from "./usage/usageTrend";

// Kept for callers that still import the formatter from this page.
export { formatTokens as fmt } from "./usage/format";

const USAGE_TAB = "usage";
const LIVE_REFRESH_MS = 60_000;

function viewFromLocation(): UsageView {
  const hash = window.location.hash.replace(/^#\/?/, "");
  const [tab, query = ""] = hash.split("?", 2);
  return viewFromHashQuery(tab.split("/", 1)[0] === USAGE_TAB ? query : "");
}

// The view lives in the route so a link reopens the same slice. It is written
// without a history entry: stepping through every filter with Back is noise.
function writeViewToLocation(view: UsageView) {
  const query = viewToHashQuery(view);
  const next = `#${USAGE_TAB}${query ? `?${query}` : ""}`;
  if (window.location.hash !== next) window.history.replaceState(window.history.state, "", next);
}

// prefetchDefaultUsage warms the view the dashboard opens with, when its
// navigation entry is pointed at.
export function prefetchDefaultUsage() {
  const view = viewFromLocation();
  const bounds = rangeBounds(view, formatLocalDate(new Date()));
  prefetchUsage({
    period: resolvePeriod(view.granularity, bounds), from: bounds.from, to: bounds.to,
    filter: { models: view.filters.models, runtimes: view.filters.runtimes, agents: view.filters.agents },
    machines: view.filters.machines,
  });
}

type DetailTab = "period" | "model" | "framework" | "agent" | "machine";

export function UsagePanel() {
  const { language, t } = useI18n();
  const [view, setView] = useState<UsageView>(viewFromLocation);
  const [today, setToday] = useState(() => formatLocalDate(new Date()));
  const [trendDimension, setTrendDimension] = useState<TrendDimension>("total");

  const updateView = useCallback((next: UsageView) => {
    setView(next);
    writeViewToLocation(next);
  }, []);
  useEffect(() => {
    const onHashChange = () => {
      if (window.location.hash.replace(/^#\/?/, "").split(/[/?]/, 1)[0] === USAGE_TAB) setView(viewFromLocation());
    };
    window.addEventListener("hashchange", onHashChange);
    return () => window.removeEventListener("hashchange", onHashChange);
  }, []);

  const bounds = useMemo(() => rangeBounds(view, today), [view, today]);
  const period = resolvePeriod(view.granularity, bounds);
  const request = useMemo<UsageRequest>(() => ({
    period,
    from: bounds.from,
    to: bounds.to,
    filter: { models: view.filters.models, runtimes: view.filters.runtimes, agents: view.filters.agents },
    machines: view.filters.machines,
  }), [period, bounds.from, bounds.to, view.filters]);
  const live = includesToday(bounds, today);
  const usage = useUsageReport(request, { pollMs: LIVE_REFRESH_MS, live });
  // A range that follows today moves with the date, also across midnight
  // while the page stays open.
  const followDate = useCallback(() => {
    const current = formatLocalDate(new Date());
    setToday((previous) => (previous === current ? previous : current));
  }, []);
  usePolling(followDate, LIVE_REFRESH_MS);

  const settings = useCachedResource<MenubarSettings>("menubarSettings", () => api.menubarSettings(), 60_000);
  const [savedSettings, setSavedSettings] = useState<MenubarSettings | null>(null);
  const preferences = savedSettings ?? settings.data;
  const cnyRate = validCNYRate(preferences?.cny_rate ?? 7);
  const currency = preferences?.currency ?? "cny";
  const formatCost = useCallback((costUSD: number) => formatUsageCost(costUSD, currency, cnyRate, language), [currency, cnyRate, language]);

  // Names for the agent dimension. They only label values; the page does not
  // wait for them.
  const agentInstances = useCachedResource("agentInstances", () => api.agentInstances());
  const superAgents = useCachedResource("unifiedAgents", () => api.superAgents().then((items) => Array.isArray(items) ? items : []).catch(() => [] as SuperAgent[]));
  const channels = useCachedResource("channels", () => api.channels());
  const agentNames = useMemo<AgentNames>(() => {
    const agents = new Map<string, string>();
    for (const agent of agentInstances.data ?? []) if (agent.name) agents.set(agent.id, agent.name);
    for (const agent of superAgents.data ?? []) {
      if (!agent.name) continue;
      agents.set(agent.id, agent.name);
      // A super agent's router and workers record usage under derived IDs.
      agents.set(agent.id.replace(/^sa_/, "sar_"), agent.name);
    }
    const channelNames = new Map<string, string>();
    for (const channel of channels.data ?? []) channelNames.set(channel.id, channel.bot_name || channel.name);
    return { agents, channels: channelNames };
  }, [agentInstances.data, superAgents.data, channels.data]);
  const labelAgent = useCallback((key: string) => {
    const worker = key.match(/^agent:saw_(sa_[^_]+)_(.+)$/);
    if (worker) {
      const name = agentNames.agents.get(worker[1]);
      if (name) return `${name} · ${runtimeLabel(worker[2], t)}`;
    }
    return agentLabel(key, agentNames, t);
  }, [agentNames, t]);
  const labelMachine = useCallback((id: string, name: string) => (id === "local" ? t("remote.localMachine") : name || id), [t]);
  const labelModel = useCallback((model: string) => model.trim() || t("overview.unknownModel"), [t]);

  const report = usage.report;
  const totals = report?.totals;
  const tokens = totals ? totalTokens(totals) : 0;
  const cacheTokens = (totals?.cache_read_tokens ?? 0) + (totals?.cache_write_tokens ?? 0);
  const estimatedTokens = totals?.estimated_tokens ?? 0;
  const exactCoverage = tokens > 0 ? Math.max(0, Math.round((1 - estimatedTokens / tokens) * 100)) : 100;

  const filterOptions = useMemo<FilterOptions>(() => ({
    machines: usage.targets.map((target) => ({ value: target.id, label: labelMachine(target.id, target.name) })),
    runtimes: (report?.facets?.runtimes ?? []).map((facet) => ({ value: facet.value, label: runtimeLabel(facet.value, t), tokens: facet.tokens })),
    models: (report?.facets?.models ?? []).map((facet) => ({ value: facet.value, label: labelModel(facet.value), tokens: facet.tokens })),
    agents: (report?.facets?.agents ?? []).map((facet): FilterOption => ({ value: facet.value, label: labelAgent(facet.value), title: facet.value, tokens: facet.tokens })),
  }), [usage.targets, report?.facets, labelMachine, labelModel, labelAgent, t]);

  const toggleFilter = useCallback((dimension: FilterDimension, value: string) => {
    updateView({ ...view, filters: toggleFilterValue(view.filters, dimension, value) });
  }, [updateView, view]);

  const now = useMemo(() => new Date(), [usage.updatedAt, today]);
  const timeline = useMemo(() => timelineFor(report, bounds, period, now), [report, bounds, period, now]);
  const trendSeries = useMemo(() => buildTrendSeries(trendDimension, report, timeline, period, {
    total: t("overview.trend.total"),
    other: t("overview.trend.other"),
    unknownModel: t("overview.unknownModel"),
    unattributed: t("overview.trend.unattributedModel"),
    runtime: (id) => runtimeLabel(id, t),
    machine: labelMachine,
  }), [trendDimension, report, timeline, period, t, labelMachine]);

  const machines = report?.by_machine ?? [];
  const showMachines = usage.targets.length > 1 || machines.length > 1;
  const rangeLabel = view.preset === "custom"
    ? (bounds.from === bounds.to ? bounds.from : `${bounds.from} – ${bounds.to}`)
    : t(`range.${view.preset}`);
  const updatedAt = usage.updatedAt ? new Date(usage.updatedAt).toLocaleTimeString() : "—";
  const dimmed = usage.stale || (usage.loading && !report);

  return (
    <div className="page-stack usage-dashboard">
      <section className="surface usage-dashboard-header">
        <div className="surface-header statistics-header">
          <div>
            <h2>{t("usage.title")}</h2>
            <p className="subtle-copy">{t("overview.statisticsHint")}</p>
          </div>
          <div className="statistics-header-actions">
            <div className="activity-line overview-updated-at" aria-live="polite">
              <Clock3 size={14} />
              <span>{t("overview.lastUpdated")} {updatedAt}</span>
            </div>
            <button className="ghost-action" type="button" onClick={() => void usage.reload()} disabled={usage.loading}>
              <RefreshCw size={14} className={usage.loading ? "spin" : undefined} />
              {t("common.refresh")}
            </button>
          </div>
        </div>
        <div className="surface-body">
          <UsageFilterBar
            view={view}
            bounds={bounds}
            period={period}
            today={today}
            options={filterOptions}
            showMachines={showMachines}
            onChange={updateView}
          />
          {usage.pending.length > 0 && (
            <p className="usage-pending-note" role="status">
              {t("usage.pendingMachines", { names: usage.pending.map((target) => labelMachine(target.id, target.name)).join(", ") })}
            </p>
          )}
        </div>
      </section>

      {usage.error && <div className="surface surface-body error" role="alert">{usage.error}</div>}
      {(report?.warnings ?? []).map((warning) => <div className="surface surface-body warning" key={warning}>{warning}</div>)}

      <section className={`surface overview-statistics${dimmed ? " is-loading" : ""}`} aria-busy={usage.loading}>
        <div className="surface-body statistics-body">
          <div className="statistics-summary-grid">
            <section className="statistics-summary-card usage-summary-card">
              <header>
                <span className="statistics-summary-title"><Gauge size={19} />{t("overview.usageCard")}</span>
                <span className="usage-summary-range">{rangeLabel}</span>
              </header>
              <div className="usage-summary-metrics">
                <SummaryMetric
                  icon={<Gauge size={17} />}
                  label={t("overview.totalTokens")}
                  value={totals ? formatTokens(tokens) : "—"}
                  detail={totals ? <>
                    {t("overview.includedCache", { tokens: formatTokens(cacheTokens) })}
                    {estimatedTokens > 0 && <><br />{t("usage.estimatedDetail", { tokens: formatTokens(estimatedTokens), coverage: exactCoverage })}</>}
                  </> : undefined}
                />
                <SummaryMetric
                  icon={<MessageSquareText size={17} />}
                  label={t("overview.sessionConversations")}
                  value={totals ? formatTokens(totals.sessions ?? 0) : "—"}
                  detail={totals ? t("usage.requestsDetail", { count: formatTokens(totals.records ?? 0) }) : t("overview.sessionScope")}
                />
                <SummaryMetric
                  icon={<CircleDollarSign size={17} />}
                  label={t("overview.estimatedAmount")}
                  value={(
                    <span className="usage-cost-values">
                      <span>{totals ? formatUsageCost(totals.cost_usd, "cny", cnyRate, language) : "—"}<small>RMB</small></span>
                      <span>{totals ? formatUsageCost(totals.cost_usd, "usd", cnyRate, language) : "—"}<small>USD</small></span>
                    </span>
                  )}
                  detail={t("overview.exchangeRate", { rate: cnyRate })}
                />
              </div>
              <div className="usage-breakdown">
                <Stat label={t("usage.uncachedInput")} value={totals ? formatTokens(totals.input_tokens) : "—"} />
                <Stat label={t("overview.output")} value={totals ? formatTokens(totals.output_tokens) : "—"} />
                <Stat label={t("usage.cacheRead")} value={totals ? formatTokens(totals.cache_read_tokens) : "—"} />
                <Stat label={t("usage.cacheWrite")} value={totals ? formatTokens(totals.cache_write_tokens) : "—"} />
              </div>
              <details className="usage-pricing-note">
                <summary>{t("overview.pricingDetails")}</summary>
                <p>{t("overview.pricingFormula")}</p>
                <p>{t("overview.pricingSource")}</p>
              </details>
            </section>

            <div className={`overview-rankings${showMachines ? " with-machines" : ""}`}>
              <UsageRanking
                title={t("overview.frameworkRanking")}
                icon={<Bot size={19} />}
                emptyText={t("overview.noFrameworkUsage")}
                totalTokens={tokens}
                selected={view.filters.runtimes}
                onToggle={(value) => toggleFilter("runtimes", value)}
                items={(report?.by_runtime ?? []).map((runtime) => {
                  const Icon = runtimeIcon(runtime.runtime);
                  return { id: runtime.runtime, label: runtimeLabel(runtime.runtime, t), tokens: runtime.tokens, icon: <Icon size={17} /> };
                })}
              />
              <UsageRanking
                title={t("overview.modelRanking")}
                icon={<Brain size={19} />}
                emptyText={t("overview.noModelUsage")}
                totalTokens={tokens}
                selected={view.filters.models}
                onToggle={(value) => toggleFilter("models", value)}
                items={(report?.by_model ?? []).map((model) => ({
                  id: model.model, label: labelModel(model.model), tokens: model.tokens, icon: <Brain size={17} />,
                }))}
              />
              <UsageRanking
                title={t("usage.agentRanking")}
                icon={<FolderGit2 size={19} />}
                emptyText={t("usage.noAgentUsage")}
                totalTokens={tokens}
                selected={view.filters.agents}
                onToggle={(value) => toggleFilter("agents", value)}
                items={(report?.by_agent ?? []).map((agent) => ({
                  id: agent.agent, label: labelAgent(agent.agent), title: agent.agent, tokens: agent.tokens,
                  icon: agent.agent.startsWith("agent:") || agent.agent.startsWith("channel:") ? <Bot size={17} /> : <FolderGit2 size={17} />,
                }))}
              />
              {showMachines && (
                <UsageRanking
                  title={t("usage.machineRanking")}
                  icon={<MonitorDot size={19} />}
                  emptyText={t("usage.noMachineUsage")}
                  totalTokens={tokens}
                  selected={view.filters.machines}
                  onToggle={(value) => toggleFilter("machines", value)}
                  items={machines.map((machine) => ({
                    id: machine.target_id, label: labelMachine(machine.target_id, machine.target_name),
                    tokens: totalTokens(machine.totals), icon: <MonitorDot size={17} />,
                  }))}
                />
              )}
            </div>
          </div>

          <div className="usage-trend-card">
            <div className="usage-trend-heading">
              <div>
                <h3>{t("overview.tokenTrend")}</h3>
                <span>{rangeLabel} · {t(`period.${period}`)}</span>
              </div>
              <div className="usage-trend-actions">
                <span className="usage-trend-total">{formatTokens(tokens)}</span>
                <div className="trend-dimension-toggle" role="group" aria-label={t("overview.trendBreakdown")}>
                  {TREND_DIMENSIONS.filter((dimension) => dimension !== "machine" || showMachines).map((dimension) => (
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
                </div>
              </div>
            </div>
            <UsageTrendChart keys={timeline} series={trendSeries} period={period} totalTokens={tokens} loading={usage.loading && !report} />
          </div>
        </div>
      </section>

      {report && (
        <UsageDetails
          report={report}
          periodLabel={t(`period.${period}`)}
          resetKey={usage.reportKey}
          showMachines={showMachines}
          formatCost={formatCost}
          labels={{ model: labelModel, agent: labelAgent, machine: labelMachine }}
        />
      )}

      <section className="surface usage-settings-surface">
        <div className="surface-header">
          <div>
            <h2>{t("usage.displayTitle")}</h2>
            <p className="subtle-copy">{t("usage.displayHint")}</p>
          </div>
          <a className="ghost-action" href="#observability/traces">
            <Activity size={14} />
            {t("usage.viewTraces")}
          </a>
        </div>
        <div className="surface-body">
          <UsageCurrencySettings settings={preferences ?? null} onSaved={setSavedSettings} />
        </div>
      </section>

      <UsageSources />
    </div>
  );
}

function SummaryMetric({ icon, label, value, detail }: { icon: ReactNode; label: string; value: ReactNode; detail?: ReactNode }) {
  return (
    <div className="usage-summary-metric">
      <span className="usage-summary-metric-label">{icon}{label}</span>
      <strong>{value}</strong>
      {detail && <small>{detail}</small>}
    </div>
  );
}

function Stat({ label, value }: { label: string; value: string }) {
  return (
    <div className="stat">
      <div className="label">{label}</div>
      <div className="value">{value}</div>
    </div>
  );
}

type UsageRankingItem = { id: string; label: string; title?: string; tokens: number; icon: ReactNode };
const RANKING_TOP = 3;

// A ranking of one dimension. A row is also a filter: choosing it narrows the
// whole dashboard to that value, and choosing it again clears it.
function UsageRanking({ title, icon, emptyText, totalTokens: total, items, selected, onToggle }: {
  title: string;
  icon: ReactNode;
  emptyText: string;
  totalTokens: number;
  items: UsageRankingItem[];
  selected: string[];
  onToggle: (id: string) => void;
}) {
  const { t } = useI18n();
  const [expanded, setExpanded] = useState(false);
  const listID = useId();
  const ranked = items.filter((item) => item.tokens > 0)
    .sort((left, right) => right.tokens - left.tokens || left.id.localeCompare(right.id));
  const visible = expanded ? ranked : ranked.slice(0, RANKING_TOP);
  return (
    <section className="statistics-summary-card framework-ranking-card" aria-label={title}>
      <header>
        <span className="statistics-summary-title">{icon}{title}</span>
        <div className="ranking-actions">
          <span className="ranking-top-label">{expanded ? t("overview.rankingAll") : `TOP ${RANKING_TOP}`}</span>
          {ranked.length > RANKING_TOP && (
            <button className="ranking-expand" type="button" aria-expanded={expanded} aria-controls={listID} onClick={() => setExpanded(!expanded)}>
              {expanded ? t("overview.rankingCollapse") : t("overview.rankingExpand", { count: ranked.length })}
            </button>
          )}
        </div>
      </header>
      <div className="framework-ranking-list" id={listID}>
        {visible.length > 0 ? visible.map((item, index) => {
          const active = selected.includes(item.id);
          return (
            <button
              key={item.id}
              type="button"
              className={`framework-rank-row${active ? " selected" : ""}`}
              aria-pressed={active}
              title={t(active ? "usage.rankingClearFilter" : "usage.rankingFilter", { name: item.title ?? item.label })}
              onClick={() => onToggle(item.id)}
            >
              <span className={`framework-rank-number rank-${index + 1}`}>{index + 1}</span>
              <span className="framework-rank-icon">{item.icon}</span>
              <span className="framework-rank-copy">
                <strong>{item.label}</strong>
                <span>{formatTokens(item.tokens)} tokens</span>
              </span>
              <span className="framework-rank-share">{formatShare(item.tokens, total)}</span>
            </button>
          );
        }) : <div className="framework-ranking-empty">{emptyText}</div>}
      </div>
    </section>
  );
}

type DetailRow = { key: string; label: string; title?: string; tokens: number; cost: number; estimated: number; totals?: UsageTotals };

// The table behind the charts: every figure the dashboard draws is readable
// here as a number.
function UsageDetails({ report, periodLabel, resetKey, showMachines, formatCost, labels }: {
  report: UsageReport;
  periodLabel: string;
  resetKey: string;
  showMachines: boolean;
  formatCost: (costUSD: number) => string;
  labels: { model: (id: string) => string; agent: (id: string) => string; machine: (id: string, name: string) => string };
}) {
  const { t } = useI18n();
  const [tab, setTab] = useState<DetailTab>("period");
  const activeTab = tab === "machine" && !showMachines ? "period" : tab;
  const rows = useMemo<DetailRow[]>(() => {
    switch (activeTab) {
      case "model":
        return (report.by_model ?? []).map((item) => ({ key: item.model, label: labels.model(item.model), tokens: item.tokens, cost: item.cost_usd, estimated: item.estimated_tokens ?? 0 }));
      case "framework":
        return (report.by_runtime ?? []).map((item) => ({ key: item.runtime, label: runtimeLabel(item.runtime, t), tokens: item.tokens, cost: item.cost_usd, estimated: item.estimated_tokens ?? 0 }));
      case "agent":
        return (report.by_agent ?? []).map((item) => ({ key: item.agent, label: labels.agent(item.agent), title: item.agent, tokens: item.tokens, cost: item.cost_usd, estimated: 0 }));
      case "machine":
        return (report.by_machine ?? []).map((item) => ({
          key: item.target_id, label: labels.machine(item.target_id, item.target_name), tokens: totalTokens(item.totals),
          cost: item.totals.cost_usd, estimated: item.totals.estimated_tokens ?? 0, totals: item.totals,
        }));
      default:
        // Newest first: the current period is what a reader looks for.
        return [...(report.buckets ?? [])].reverse().map((bucket) => ({
          key: bucket.key, label: bucket.key, tokens: totalTokens(bucket.totals), cost: bucket.totals.cost_usd,
          estimated: bucket.totals.estimated_tokens ?? 0, totals: bucket.totals,
        }));
    }
  }, [activeTab, report, labels, t]);
  const sorted = useMemo(() => (activeTab === "period" ? rows : [...rows].sort((left, right) => right.tokens - left.tokens)), [activeTab, rows]);
  const pagination = useCatalogPagination(sorted, `${resetKey}:${activeTab}`);
  const breakdown = activeTab === "period" || activeTab === "machine";
  const tabs: Array<{ id: DetailTab; label: string }> = [
    { id: "period", label: periodLabel },
    { id: "framework", label: t("usage.filter.runtimes") },
    { id: "model", label: t("usage.filter.models") },
    { id: "agent", label: t("usage.filter.agents") },
    ...(showMachines ? [{ id: "machine" as const, label: t("usage.filter.machines") }] : []),
  ];
  const total = sorted.reduce((sum, row) => sum + row.tokens, 0);
  return (
    <section className="surface usage-details-surface">
      <div className="surface-header">
        <div>
          <h2>{t("usage.details")}</h2>
          <p className="subtle-copy">{t("usage.periodRows", { count: sorted.length })}</p>
        </div>
        <div className="segmented" role="group" aria-label={t("usage.detailsBy")}>
          {tabs.map((item) => (
            <button key={item.id} type="button" className={activeTab === item.id ? "active" : ""} aria-pressed={activeTab === item.id} onClick={() => setTab(item.id)}>
              {item.label}
            </button>
          ))}
        </div>
      </div>
      <div className="table-wrap">
        <table className="usage-details-table">
          <thead>
            <tr>
              <th>{tabs.find((item) => item.id === activeTab)?.label}</th>
              <th className="numeric">{t("overview.totalTokens")}</th>
              {breakdown ? (
                <>
                  <th className="numeric">{t("usage.uncachedInput")}</th>
                  <th className="numeric">{t("overview.output")}</th>
                  <th className="numeric">{t("usage.cacheRead")}</th>
                  <th className="numeric">{t("usage.cacheWrite")}</th>
                  <th className="numeric">{t("overview.sessions")}</th>
                </>
              ) : <th className="numeric">{t("usage.share")}</th>}
              <th className="numeric">{t("usage.estimatedCost")}</th>
              {activeTab !== "agent" && <th>{t("usage.quality")}</th>}
            </tr>
          </thead>
          <tbody>
            {pagination.pageItems.map((row) => {
              const rowTotals = row.totals ?? emptyTotals();
              return (
                <tr key={row.key}>
                  <td title={row.title}>{row.label}</td>
                  <td className="numeric">{formatTokens(row.tokens)}</td>
                  {breakdown ? (
                    <>
                      <td className="numeric">{formatTokens(rowTotals.input_tokens)}</td>
                      <td className="numeric">{formatTokens(rowTotals.output_tokens)}</td>
                      <td className="numeric">{formatTokens(rowTotals.cache_read_tokens)}</td>
                      <td className="numeric">{formatTokens(rowTotals.cache_write_tokens)}</td>
                      <td className="numeric">{formatTokens(rowTotals.sessions ?? 0)}</td>
                    </>
                  ) : <td className="numeric">{formatShare(row.tokens, total)}</td>}
                  <td className="numeric">{formatCost(row.cost)}</td>
                  {activeTab !== "agent" && (
                    <td>{row.estimated > 0
                      ? <span className="status-badge warning">{t("usage.estimated")}</span>
                      : <span className="status-badge success">{t("usage.exact")}</span>}</td>
                  )}
                </tr>
              );
            })}
            {sorted.length === 0 && (
              <tr><td colSpan={breakdown ? 9 : 5} className="muted">{t("usage.noUsageInRange")}</td></tr>
            )}
          </tbody>
        </table>
      </div>
      {pagination.totalPages > 1 && (
        <CatalogPagination
          page={pagination.page}
          totalPages={pagination.totalPages}
          start={pagination.start}
          end={pagination.end}
          total={pagination.total}
          onChange={pagination.setPage}
        />
      )}
    </section>
  );
}

function runtimeIcon(runtime: string) {
  switch (runtime.trim().toLowerCase()) {
    case "claude":
    case "claudecode":
    case "claude-desktop":
    case "claude-unknown":
    case "gemini":
      return Sparkles;
    case "cursor":
      return Workflow;
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
