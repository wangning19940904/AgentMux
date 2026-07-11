import { FormEvent, useEffect, useMemo, useState } from "react";
import {
  Activity,
  AlertTriangle,
  Bot,
  CheckCircle2,
  Clock,
  Database,
  Eye,
  FileText,
  Lightbulb,
  LockKeyhole,
  Network,
  Plug,
  RefreshCw,
  Search,
  Server,
  ShieldCheck,
  Wrench,
  XCircle,
  Zap,
} from "lucide-react";
import {
  ObservationCoverage,
  ObservationEvent,
  ObservationInsight,
  ObservationIntegration,
  ObservationIntegrationActionResult,
  ObservationIntegrationCoverage,
  ObservationSettings,
  ObservationSpan,
  ObservationTrace,
  ObservationTraceDetail,
  ObservationUsage,
  api,
} from "../api";
import { useI18n } from "../i18n";
import { useAsync } from "../useAsync";

type ObservabilityView = "overview" | "traces" | "insights" | "integrations";

type ObservabilityRoute = {
  view: ObservabilityView;
  traceID: string;
  sessionID: string;
  agentID: string;
};

const OBSERVABILITY_VIEWS: ObservabilityView[] = ["overview", "traces", "insights", "integrations"];

export function ObservabilityPanel() {
  const { t } = useI18n();
  const [route, setRoute] = useState<ObservabilityRoute>(readObservabilityRoute);

  useEffect(() => {
    const onHashChange = () => setRoute(readObservabilityRoute());
    window.addEventListener("hashchange", onHashChange);
    return () => window.removeEventListener("hashchange", onHashChange);
  }, []);

  function navigate(view: ObservabilityView, traceID = "", params?: { sessionID?: string; agentID?: string }) {
    const path = traceID
      ? `observability/traces/${encodeURIComponent(traceID)}`
      : `observability/${view}`;
    const query = new URLSearchParams();
    if (params?.sessionID) query.set("session_id", params.sessionID);
    if (params?.agentID) query.set("agent_id", params.agentID);
    const nextHash = `#${path}${query.size > 0 ? `?${query.toString()}` : ""}`;
    if (window.location.hash === nextHash) {
      setRoute(readObservabilityRoute());
    } else {
      window.location.hash = nextHash;
    }
  }

  return (
    <div className="page-stack observability-page">
      <section className="surface observability-hero">
        <div>
          <span className="observability-eyebrow">
            <Activity size={15} />
            {t("observability.liveTelemetry")}
          </span>
          <h2>{t("observability.title")}</h2>
          <p className="subtle-copy">{t("observability.subtitle")}</p>
        </div>
        <div className="segmented observability-tabs" aria-label={t("observability.title")}>
          {OBSERVABILITY_VIEWS.map((view) => (
            <button key={view} className={route.view === view ? "active" : ""} onClick={() => navigate(view)}>
              {t(`observability.${view}`)}
            </button>
          ))}
        </div>
      </section>

      {route.view === "overview" && <ObservationOverview onOpenTrace={(traceID) => navigate("traces", traceID)} />}
      {route.view === "traces" && (
        <TraceExplorer
          initialAgentID={route.agentID}
          initialSessionID={route.sessionID}
          selectedTraceID={route.traceID}
          onSelectTrace={(traceID) => navigate("traces", traceID)}
          onCloseTrace={() => navigate("traces", "", { sessionID: route.sessionID, agentID: route.agentID })}
        />
      )}
      {route.view === "insights" && <InsightsPanel onOpenTrace={(traceID) => navigate("traces", traceID)} />}
      {route.view === "integrations" && <IntegrationsPanel />}
    </div>
  );
}

function ObservationOverview({ onOpenTrace }: { onOpenTrace: (traceID: string) => void }) {
  const { t, language } = useI18n();
  const overview = useAsync(() => api.observationOverview(), []);
  const settings = useAsync(() => api.observationSettings(), []);
  const data = overview.data;
  const usage = data?.usage ?? {};

  return (
    <>
      {overview.error && <LoadError error={overview.error} reload={overview.reload} />}
      {overview.loading && <LoadingSurface />}
      {data && (
        <>
          <div className="metrics-grid observability-metrics">
            <Metric label={t("observability.tracesMetric")} value={formatNumber(data.traces ?? 0, language)} icon={<Network size={19} />} />
            <Metric label={t("observability.modelRequests")} value={formatNumber(data.model_requests ?? 0, language)} icon={<Zap size={19} />} />
            <Metric label={t("observability.toolCalls")} value={formatNumber(data.tool_calls ?? 0, language)} icon={<Wrench size={19} />} />
            <Metric label={t("observability.totalTokens")} value={formatCompact(usage.total_tokens ?? tokenTotal(usage), language)} icon={<Database size={19} />} />
            <Metric
              label={t("observability.errorRate")}
              value={formatPercent(data.error_rate ?? rate(data.failed_traces, data.traces))}
              icon={<AlertTriangle size={19} />}
              tone={(data.error_rate ?? 0) > 0.05 ? "danger" : "success"}
            />
          </div>

          <div className="observability-overview-grid">
            <section className="surface">
              <div className="surface-header">
                <div>
                  <h2>{t("observability.coverage")}</h2>
                  <p className="subtle-copy">{t("observability.coverageHint")}</p>
                </div>
                <button className="ghost-action" onClick={overview.reload}>
                  <RefreshCw size={14} />
                  {t("common.refresh")}
                </button>
              </div>
              <CoverageList coverage={data.coverage ?? []} />
            </section>

            <SettingsCard settings={settings.data} loading={settings.loading} error={settings.error} />
          </div>

          <section className="surface">
            <div className="surface-header">
              <div>
                <h2>{t("observability.recentTraces")}</h2>
                <p className="subtle-copy">{t("observability.recentTracesHint")}</p>
              </div>
            </div>
            <TraceTable traces={data.recent_traces ?? []} language={language} onOpenTrace={onOpenTrace} />
          </section>
        </>
      )}
    </>
  );
}

function CoverageList({ coverage }: { coverage: ObservationCoverage[] }) {
  const { t, language } = useI18n();
  if (coverage.length === 0) return <div className="empty-state">{t("observability.noCoverage")}</div>;
  return (
    <div className="coverage-list">
      {coverage.map((item, index) => (
        <article key={`${item.source}-${index}`}>
          <span className={`coverage-icon ${toneForStatus(item.status || item.quality)}`}>
            <SourceIcon source={item.source} />
          </span>
          <div>
            <strong>{sourceLabel(item.source)}</strong>
            <span>{item.detail || `${formatNumber(item.events ?? item.traces ?? 0, language)} ${t("observability.events")}`}</span>
          </div>
          <div className="coverage-state">
            <StatusBadge value={item.status || item.quality || "unknown"} />
            {item.last_seen_at && <time>{formatDate(item.last_seen_at, language)}</time>}
          </div>
        </article>
      ))}
    </div>
  );
}

function SettingsCard({ settings, loading, error }: { settings: ObservationSettings | null; loading: boolean; error: string | null }) {
  const { t } = useI18n();
  return (
    <section className="surface observability-settings-card">
      <div className="surface-header">
        <div>
          <h2>{t("observability.capturePolicy")}</h2>
          <p className="subtle-copy">{t("observability.capturePolicyHint")}</p>
        </div>
        {settings && <StatusBadge value={settings.enabled ? "enabled" : "disabled"} />}
      </div>
      {loading && <div className="surface-body muted">{t("common.loading")}</div>}
      {error && <div className="surface-body error">{error}</div>}
      {settings && (
        <div className="settings-facts">
          <Fact icon={<Eye size={16} />} label={t("observability.contentCapture")} value={settings.capture_content || "metadata"} />
          <Fact icon={<LockKeyhole size={16} />} label={t("observability.keyStatus")} value={settings.key_status || (settings.metadata_only ? "metadata-only" : "encrypted")} />
          <Fact icon={<Clock size={16} />} label={t("observability.contentRetention")} value={`${settings.content_retention_days ?? 0} ${t("observability.days")}`} />
          <Fact icon={<Database size={16} />} label={t("observability.detailRetention")} value={`${settings.detail_retention_days ?? 0} ${t("observability.days")}`} />
          <Fact icon={<FileText size={16} />} label={t("observability.backfillWindow")} value={`${settings.backfill_days ?? 0} ${t("observability.days")}`} />
          <Fact
            icon={<Server size={16} />}
            label={t("observability.exporters")}
            value={`${settings.exporters?.filter((item) => item.enabled !== false).length ?? 0}`}
          />
        </div>
      )}
      {(settings?.exporters ?? []).map((exporter, index) => (
        <div className="exporter-row" key={exporter.name || `${exporter.type}-${index}`}>
          <div>
            <strong>{exporter.name || exporter.type || "OTLP"}</strong>
            <span className="mono">{exporter.endpoint || t("observability.localExporter")}</span>
          </div>
          <span className={exporter.include_content ? "status-badge warning" : "status-badge success"}>
            {exporter.include_content ? t("observability.contentExportOn") : t("observability.metadataOnly")}
          </span>
        </div>
      ))}
    </section>
  );
}

function TraceExplorer({
  initialAgentID,
  initialSessionID,
  selectedTraceID,
  onSelectTrace,
  onCloseTrace,
}: {
  initialAgentID: string;
  initialSessionID: string;
  selectedTraceID: string;
  onSelectTrace: (traceID: string) => void;
  onCloseTrace: () => void;
}) {
  const { t, language } = useI18n();
  const [draft, setDraft] = useState({
    agentID: initialAgentID,
    runtimeID: "",
    sessionID: initialSessionID,
    status: "",
    source: "",
  });
  const [filters, setFilters] = useState({ ...draft, limit: 50, offset: 0 });

  useEffect(() => {
    if (initialAgentID === draft.agentID && initialSessionID === draft.sessionID) return;
    const next = { ...draft, agentID: initialAgentID, sessionID: initialSessionID };
    setDraft(next);
    setFilters({ ...next, limit: 50, offset: 0 });
    // URL deep links are authoritative; draft intentionally is not a dependency.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [initialAgentID, initialSessionID]);

  const tracesQuery = useAsync(() => api.observationTraces(filters), [
    filters.agentID,
    filters.runtimeID,
    filters.sessionID,
    filters.status,
    filters.source,
    filters.limit,
    filters.offset,
  ]);
  const traces = Array.isArray(tracesQuery.data) ? tracesQuery.data : tracesQuery.data?.traces ?? [];
  const total = Array.isArray(tracesQuery.data) ? undefined : tracesQuery.data?.total;

  function applyFilters(event: FormEvent) {
    event.preventDefault();
    setFilters({ ...draft, limit: filters.limit, offset: 0 });
  }

  return (
    <section className="surface observability-traces-surface">
      <form className="observability-filters" onSubmit={applyFilters}>
        <label>
          <span>{t("observability.agentID")}</span>
          <input value={draft.agentID} onChange={(event) => setDraft({ ...draft, agentID: event.target.value })} placeholder="agent_id" />
        </label>
        <label>
          <span>{t("observability.runtime")}</span>
          <input value={draft.runtimeID} onChange={(event) => setDraft({ ...draft, runtimeID: event.target.value })} placeholder="claudecode / codex" />
        </label>
        <label>
          <span>{t("observability.sessionID")}</span>
          <input value={draft.sessionID} onChange={(event) => setDraft({ ...draft, sessionID: event.target.value })} placeholder="session_id" />
        </label>
        <label>
          <span>{t("common.status")}</span>
          <select value={draft.status} onChange={(event) => setDraft({ ...draft, status: event.target.value })}>
            <option value="">{t("observability.allStatuses")}</option>
            <option value="ok">OK</option>
            <option value="error">Error</option>
            <option value="running">Running</option>
            <option value="cancelled">Cancelled</option>
          </select>
        </label>
        <label>
          <span>{t("common.source")}</span>
          <input value={draft.source} onChange={(event) => setDraft({ ...draft, source: event.target.value })} placeholder="engine / hook / otel" />
        </label>
        <button className="action" type="submit">
          <Search size={14} />
          {t("observability.applyFilters")}
        </button>
      </form>

      <div className={`observability-trace-layout ${selectedTraceID ? "has-detail" : ""}`}>
        <div className="trace-browser">
          <div className="trace-browser-head">
            <div>
              <strong>{t("observability.traceResults")}</strong>
              <span>{total === undefined ? traces.length : total} {t("observability.tracesMetric").toLowerCase()}</span>
            </div>
            <button className="ghost-action icon-only" onClick={tracesQuery.reload} title={t("common.refresh")}>
              <RefreshCw size={15} />
            </button>
          </div>
          {tracesQuery.error && <LoadError error={tracesQuery.error} reload={tracesQuery.reload} compact />}
          {tracesQuery.loading && <div className="empty-state">{t("common.loading")}</div>}
          {!tracesQuery.loading && !tracesQuery.error && traces.length === 0 && <div className="empty-state">{t("observability.noTraces")}</div>}
          <div className="trace-list" role="list">
            {traces.map((trace) => (
              <TraceListItem
                key={trace.trace_id}
                trace={trace}
                language={language}
                active={selectedTraceID === trace.trace_id}
                onClick={() => onSelectTrace(trace.trace_id)}
              />
            ))}
          </div>
          <div className="trace-pagination">
            <button
              className="ghost-action"
              disabled={filters.offset === 0}
              onClick={() => setFilters({ ...filters, offset: Math.max(0, filters.offset - filters.limit) })}
            >
              {t("observability.previous")}
            </button>
            <span>{traces.length === 0 ? "0–0" : `${filters.offset + 1}–${filters.offset + traces.length}`}</span>
            <button
              className="ghost-action"
              disabled={traces.length < filters.limit}
              onClick={() => setFilters({ ...filters, offset: filters.offset + filters.limit })}
            >
              {t("observability.next")}
            </button>
          </div>
        </div>

        {selectedTraceID ? (
          <TraceDetail traceID={selectedTraceID} onClose={onCloseTrace} />
        ) : (
          <div className="trace-detail-empty">
            <Network size={30} />
            <strong>{t("observability.selectTrace")}</strong>
            <span>{t("observability.selectTraceHint")}</span>
          </div>
        )}
      </div>
    </section>
  );
}

function TraceListItem({ trace, active, language, onClick }: { trace: ObservationTrace; active: boolean; language: string; onClick: () => void }) {
  const { t } = useI18n();
  return (
    <button className={active ? "active" : ""} onClick={onClick} role="listitem">
      <span className={`trace-status-dot ${toneForStatus(trace.status)}`} />
      <span className="trace-list-main">
        <span>
          <strong>{trace.name || trace.agent_name || t("observability.agentTurn")}</strong>
          <time>{formatDate(trace.started_at, language)}</time>
        </span>
        <span className="trace-list-identity">
          <code>{shortID(trace.trace_id)}</code>
          <span>{trace.runtime_id || trace.source || "—"}</span>
        </span>
        <span className="trace-list-metrics">
          <span>{formatDuration(durationBetween(trace.started_at, trace.ended_at))}</span>
          <span>{formatCompact(trace.usage?.total_tokens ?? tokenTotal(trace.usage), language)} tok</span>
          <span>{trace.span_count ?? 0} {t("observability.spans")}</span>
        </span>
        {(trace.quality && trace.quality !== "complete") || !trace.ended_at ? (
          <span className="trace-quality-row">
            {trace.quality && trace.quality !== "complete" && <QualityBadge quality={trace.quality} />}
            {!trace.ended_at && <span className="status-badge warning">{t("observability.missingEnd")}</span>}
          </span>
        ) : null}
      </span>
    </button>
  );
}

function TraceDetail({ traceID, onClose }: { traceID: string; onClose: () => void }) {
  const { t, language } = useI18n();
  const detail = useAsync(() => api.observationTrace(traceID), [traceID]);

  if (detail.loading) return <div className="trace-detail-loading">{t("common.loading")}</div>;
  if (detail.error) return <LoadError error={detail.error} reload={detail.reload} />;
  if (!detail.data) return <div className="trace-detail-empty">{t("observability.traceNotFound")}</div>;

  return <TraceDetailContent detail={detail.data} language={language} onClose={onClose} />;
}

function TraceDetailContent({ detail, language, onClose }: { detail: ObservationTraceDetail; language: string; onClose: () => void }) {
  const { t } = useI18n();
  const trace = detail.trace;
  const spans = detail.spans ?? [];
  const events = detail.events ?? [];
  const sortedSpans = useMemo(
    () => [...spans].sort((a, b) => (a.sequence ?? 0) - (b.sequence ?? 0) || dateMillis(a.started_at) - dateMillis(b.started_at)),
    [spans]
  );
  const depths = useMemo(() => spanDepths(sortedSpans), [sortedSpans]);
  const eventsBySpan = useMemo(() => {
    const grouped = new Map<string, ObservationEvent[]>();
    events.forEach((event) => grouped.set(event.span_id, [...(grouped.get(event.span_id) ?? []), event]));
    return grouped;
  }, [events]);
  const usage = trace.usage ?? {};

  return (
    <div className="trace-detail">
      <header className="trace-detail-header">
        <div>
          <button className="trace-back" onClick={onClose}>← {t("observability.allTraces")}</button>
          <h2>{trace.name || trace.agent_name || t("observability.agentTurn")}</h2>
          <span className="mono">{trace.trace_id}</span>
        </div>
        <div className="trace-detail-badges">
          <StatusBadge value={trace.status || "unset"} />
          {trace.quality && <QualityBadge quality={trace.quality} />}
        </div>
      </header>

      {trace.error && <ErrorNotice error={trace.error.message || trace.error.code || t("observability.unknownError")} />}

      <div className="trace-summary-grid">
        <Fact label={t("observability.agent")} value={trace.agent_name || trace.agent_id || "—"} />
        <Fact label={t("observability.runtime")} value={trace.runtime_id || "—"} />
        <Fact label={t("observability.duration")} value={formatDuration(durationBetween(trace.started_at, trace.ended_at))} />
        <Fact label={t("observability.totalTokens")} value={formatCompact(usage.total_tokens ?? tokenTotal(usage), language)} />
        <Fact label={t("observability.cacheTokens")} value={formatCompact((usage.cache_read_tokens ?? 0) + (usage.cache_write_tokens ?? 0), language)} />
        <Fact label={t("observability.cost")} value={formatCost(usage.cost_usd)} />
      </div>

      <section className="trace-identifiers">
        <Identifier label={t("observability.sessionID")} value={trace.session_id} />
        <Identifier label={t("observability.turnID")} value={trace.turn_id} />
        <Identifier label={t("observability.conversationID")} value={trace.conversation_id} />
        <Identifier label={t("common.source")} value={trace.source} />
      </section>

      <div className="timeline-heading">
        <div>
          <h3>{t("observability.timeline")}</h3>
          <span>{spans.length} {t("observability.spans")} · {events.length} {t("observability.events")}</span>
        </div>
        <span className="muted">{formatDate(trace.started_at, language)}</span>
      </div>

      {sortedSpans.length === 0 ? (
        <div className="empty-state">{t("observability.noSpans")}</div>
      ) : (
        <div className="trace-timeline">
          {sortedSpans.map((span) => (
            <SpanTimelineItem
              key={span.span_id}
              depth={depths.get(span.span_id) ?? 0}
              events={eventsBySpan.get(span.span_id) ?? []}
              language={language}
              span={span}
            />
          ))}
        </div>
      )}
    </div>
  );
}

function SpanTimelineItem({ span, depth, events, language }: { span: ObservationSpan; depth: number; events: ObservationEvent[]; language: string }) {
  const { t } = useI18n();
  const usage = span.usage ?? {};
  const payloadRef = span.payload_ref?.id || span.payload_id;
  const startEvent = events.find((event) => event.lifecycle === "start") ?? events[0];
  const endEvent = events.find((event) => event.lifecycle === "end") ?? events[events.length - 1];
  const input = span.tool_input ?? span.attributes?.tool_input ?? span.attributes?.input ?? startEvent?.attributes?.tool_input ?? startEvent?.attributes?.input ?? startEvent?.content;
  const output = span.tool_output ?? span.attributes?.tool_output ?? span.attributes?.output ?? span.content ?? endEvent?.attributes?.tool_output ?? endEvent?.attributes?.output ?? endEvent?.content;
  const hasPartial = span.quality && span.quality !== "complete";

  return (
    <article className={`timeline-item kind-${safeClass(span.kind)}`} style={{ "--trace-depth": Math.min(depth, 5) } as React.CSSProperties}>
      <div className={`timeline-node ${toneForStatus(span.status)}`}>
        <SpanIcon kind={span.kind} />
      </div>
      <div className="timeline-card">
        <header>
          <div>
            <span className="timeline-kind">{span.kind}</span>
            <strong>{span.name || span.tool?.name || span.model?.resolved || span.kind}</strong>
          </div>
          <div className="timeline-state">
            {hasPartial && <QualityBadge quality={span.quality || "partial"} />}
            {!span.ended_at && <span className="status-badge warning">{t("observability.missingEnd")}</span>}
            <StatusBadge value={span.status || "unset"} />
          </div>
        </header>

        <div className="timeline-meta">
          <span><Clock size={13} /> {formatDuration(span.duration_ms ?? durationBetween(span.started_at, span.ended_at))}</span>
          {span.model?.ttft_ms !== undefined && <span>{t("observability.ttft")} {formatDuration(span.model.ttft_ms)}</span>}
          {(usage.total_tokens ?? tokenTotal(usage)) > 0 && <span>{formatCompact(usage.total_tokens ?? tokenTotal(usage), language)} tok</span>}
          {(usage.cache_read_tokens ?? 0) > 0 && <span>{t("observability.cacheReadShort")} {formatCompact(usage.cache_read_tokens ?? 0, language)}</span>}
          {(usage.cost_usd ?? 0) > 0 && <span>{formatCost(usage.cost_usd)}</span>}
          <span>{formatDate(span.started_at, language)}</span>
        </div>

        {span.model && (
          <div className="timeline-facts">
            <Identifier label={t("observability.model")} value={modelLabel(span.model.requested, span.model.resolved)} />
            <Identifier label={t("observability.requestID")} value={span.model.request_id} />
            <Identifier label={t("observability.attempt")} value={span.model.attempt === undefined ? "" : String(span.model.attempt)} />
            <Identifier label={t("observability.finishReason")} value={span.model.finish_reason} />
          </div>
        )}

        {span.tool && (
          <div className="timeline-facts">
            <Identifier label={t("observability.tool")} value={span.tool.name} />
            <Identifier label={t("observability.callID")} value={span.tool.call_id} />
            <Identifier label={t("observability.inputBytes")} value={span.tool.input_bytes ? formatBytes(span.tool.input_bytes) : ""} />
            <Identifier label={t("observability.outputBytes")} value={span.tool.output_bytes ? formatBytes(span.tool.output_bytes) : ""} />
          </div>
        )}

        <div className="timeline-provenance">
          {span.source && <span className="pill">{sourceLabel(span.source)}</span>}
          {(span.provenance ?? []).map((source) => <span className="pill" key={source}>{source}</span>)}
          {payloadRef && <span className="pill mono">payload:{shortID(payloadRef)}</span>}
        </div>

        {span.error && <ErrorNotice error={span.error.message || span.error.code || t("observability.unknownError")} />}
        <PayloadPreview label={t("observability.toolInput")} value={input} />
        <PayloadPreview label={t("observability.toolOutput")} value={output} />

        {events.length > 0 && (
          <details className="span-events">
            <summary>{events.length} {t("observability.events")}</summary>
            <div>
              {events.map((event) => (
                <article key={event.event_id}>
                  <span className={`trace-status-dot ${toneForStatus(event.status)}`} />
                  <div>
                    <strong>{event.name || event.kind}</strong>
                    <span>{event.lifecycle || "event"} · {formatDate(event.time, language)}</span>
                  </div>
                  {event.payload_ref && <code>payload:{shortID(event.payload_ref.id)}</code>}
                </article>
              ))}
            </div>
          </details>
        )}
      </div>
    </article>
  );
}

function InsightsPanel({ onOpenTrace }: { onOpenTrace: (traceID: string) => void }) {
  const { t, language } = useI18n();
  const [draft, setDraft] = useState({ agentID: "", status: "open", ruleID: "" });
  const [filters, setFilters] = useState({ ...draft, limit: 100 });
  const query = useAsync(() => api.observationInsights(filters), [filters.agentID, filters.status, filters.ruleID, filters.limit]);
  const insights = Array.isArray(query.data) ? query.data : query.data?.insights ?? [];

  function apply(event: FormEvent) {
    event.preventDefault();
    setFilters({ ...draft, limit: filters.limit });
  }

  return (
    <>
      <section className="surface">
        <div className="surface-header insights-heading">
          <div>
            <h2>{t("observability.insightsTitle")}</h2>
            <p className="subtle-copy">{t("observability.insightsHint")}</p>
          </div>
          <span className="status-badge success"><Lightbulb size={13} /> {t("observability.suggestionOnly")}</span>
        </div>
        <form className="observability-filters compact" onSubmit={apply}>
          <label>
            <span>{t("observability.agentID")}</span>
            <input value={draft.agentID} onChange={(event) => setDraft({ ...draft, agentID: event.target.value })} placeholder="agent_id" />
          </label>
          <label>
            <span>{t("common.status")}</span>
            <select value={draft.status} onChange={(event) => setDraft({ ...draft, status: event.target.value })}>
              <option value="">{t("observability.allStatuses")}</option>
              <option value="open">Open</option>
              <option value="acknowledged">Acknowledged</option>
              <option value="resolved">Resolved</option>
            </select>
          </label>
          <label>
            <span>{t("observability.ruleID")}</span>
            <input value={draft.ruleID} onChange={(event) => setDraft({ ...draft, ruleID: event.target.value })} placeholder="tool_failure_rate" />
          </label>
          <button className="action" type="submit"><Search size={14} /> {t("observability.applyFilters")}</button>
          <button className="ghost-action" type="button" onClick={query.reload}><RefreshCw size={14} /> {t("common.refresh")}</button>
        </form>
      </section>

      {query.error && <LoadError error={query.error} reload={query.reload} />}
      {query.loading && <LoadingSurface />}
      {!query.loading && !query.error && insights.length === 0 && <section className="surface empty-state">{t("observability.noInsights")}</section>}
      <div className="insights-grid">
        {insights.map((insight) => <InsightCard key={insight.id} insight={insight} language={language} onOpenTrace={onOpenTrace} />)}
      </div>
    </>
  );
}

function InsightCard({ insight, language, onOpenTrace }: { insight: ObservationInsight; language: string; onOpenTrace: (traceID: string) => void }) {
  const { t } = useI18n();
  const traceIDs = Array.from(new Set([insight.trace_id, ...(insight.related_trace_ids ?? [])].filter((value): value is string => Boolean(value))));
  return (
    <article className={`surface insight-card severity-${safeClass(insight.severity || "info")}`}>
      <header>
        <span className={`insight-icon ${toneForSeverity(insight.severity)}`}><Lightbulb size={18} /></span>
        <div>
          <div className="insight-labels">
            <span className="pill mono">{insight.rule_id}</span>
            <StatusBadge value={insight.severity || insight.status || "info"} />
            {insight.only_suggestion !== false && <span className="status-badge success">{t("observability.suggestionOnly")}</span>}
          </div>
          <h3>{insight.title}</h3>
          {insight.summary && <p>{insight.summary}</p>}
        </div>
      </header>
      {insight.suggestion && (
        <div className="insight-suggestion">
          <strong>{t("observability.recommendation")}</strong>
          <p>{insight.suggestion}</p>
        </div>
      )}
      <div className="insight-evidence">
        <Fact label={t("observability.sampleSize")} value={formatNumber(insight.sample_size ?? 0, language)} />
        <Fact label={t("observability.confidence")} value={formatConfidence(insight.confidence)} />
        <Fact label={t("observability.tokenSavings")} value={formatCompact(insight.estimated_token_savings ?? 0, language)} />
        <Fact label={t("observability.costSavings")} value={formatCost(insight.estimated_cost_savings_usd)} />
      </div>
      {traceIDs.length > 0 && (
        <div className="related-traces">
          <strong>{t("observability.relatedTraces")}</strong>
          <div>
            {traceIDs.slice(0, 8).map((traceID) => (
              <button className="ghost-action mono" key={traceID} onClick={() => onOpenTrace(traceID)}>{shortID(traceID)}</button>
            ))}
          </div>
        </div>
      )}
      {insight.updated_at && <time className="insight-updated">{t("common.updated")} {formatDate(insight.updated_at, language)}</time>}
    </article>
  );
}

function IntegrationsPanel() {
  const { t, language } = useI18n();
  const query = useAsync(() => api.observationIntegrations(), []);
  const integrations = Array.isArray(query.data) ? query.data : query.data?.integrations ?? [];
  const [busy, setBusy] = useState("");
  const [results, setResults] = useState<Record<string, ObservationIntegrationActionResult>>({});
  const [actionError, setActionError] = useState<Record<string, string>>({});

  async function runAction(host: string, action: "preview" | "install" | "repair" | "uninstall" | "doctor") {
    const key = `${host}:${action}`;
    setBusy(key);
    setActionError((current) => ({ ...current, [host]: "" }));
    try {
      const result = await api.observationIntegrationAction(host, action);
      setResults((current) => ({ ...current, [host]: result }));
      if (action !== "preview") query.reload();
    } catch (error) {
      setActionError((current) => ({ ...current, [host]: String(error) }));
    } finally {
      setBusy("");
    }
  }

  return (
    <>
      <section className="surface">
        <div className="surface-header integrations-heading">
          <div>
            <h2>{t("observability.integrationsTitle")}</h2>
            <p className="subtle-copy">{t("observability.integrationsHint")}</p>
          </div>
          <button className="ghost-action" onClick={query.reload}><RefreshCw size={14} /> {t("common.refresh")}</button>
        </div>
        <div className="integration-principle">
          <ShieldCheck size={18} />
          <span><strong>{t("observability.additiveCoexistence")}</strong> {t("observability.additiveCoexistenceHint")}</span>
        </div>
      </section>

      {query.error && <LoadError error={query.error} reload={query.reload} />}
      {query.loading && <LoadingSurface />}
      {!query.loading && !query.error && integrations.length === 0 && <section className="surface empty-state">{t("observability.noIntegrations")}</section>}
      <div className="integrations-grid">
        {integrations.map((integration) => (
          <IntegrationCard
            key={integration.host}
            integration={integration}
            language={language}
            busy={busy}
            result={results[integration.host]}
            error={actionError[integration.host]}
            onAction={runAction}
          />
        ))}
      </div>
    </>
  );
}

function IntegrationCard({
  integration,
  language,
  busy,
  result,
  error,
  onAction,
}: {
  integration: ObservationIntegration;
  language: string;
  busy: string;
  result?: ObservationIntegrationActionResult;
  error?: string;
  onAction: (host: string, action: "preview" | "install" | "repair" | "uninstall" | "doctor") => void;
}) {
  const { t } = useI18n();
  const host = integration.host;
  const owners = integration.owners ?? (integration.owner ? [integration.owner] : []);
  const findings = [...(integration.findings ?? []), ...(result?.findings ?? [])];
  const conflicts = [
    ...(integration.conflicts ?? []),
    ...(result?.conflicts ?? []),
    ...findings.filter((item) => item.blocking || item.severity === "error").map((item) => item.message),
  ];
  const warnings = [
    ...(integration.warnings ?? []),
    ...(result?.warnings ?? []),
    ...findings.filter((item) => !item.blocking && item.severity !== "error").map((item) => item.message),
  ];
  const installedByStatus = ["healthy", "pending_trust", "drift", "conflict", "installed"].includes(integration.status || "");
  const installed = integration.installed ?? integration.plugin?.enabled ?? (integration.plugin?.status === "installed" || installedByStatus);

  return (
    <article className={`surface integration-card ${integration.drift ? "has-drift" : ""}`}>
      <header>
        <span className="integration-host-icon"><HostIcon host={host} /></span>
        <div>
          <h2>{integration.name || hostLabel(host)}</h2>
          <span>{integration.version ? `v${integration.version}` : t("observability.nativePlugin")}</span>
        </div>
        <StatusBadge value={integration.drift ? "drift" : integration.pending_trust ? "pending_trust" : integration.status || (installed ? "installed" : "not_installed")} />
      </header>

      <div className="integration-coverage-grid">
        <IntegrationCoverage label={t("observability.plugin")} value={integration.plugin ?? coverageValue(integration.coverage?.plugin)} fallback={installed ? "installed" : "not_installed"} />
        <IntegrationCoverage label={t("observability.trust")} value={coverageValue(integration.pending_trust ? "pending_trust" : integration.trust || integration.coverage?.trust)} fallback="unknown" />
        <IntegrationCoverage label="OTel" value={integration.otel ?? coverageValue(integration.coverage?.otel)} />
        <IntegrationCoverage label={t("observability.transcript")} value={integration.transcript ?? coverageValue(integration.coverage?.transcript)} />
        <IntegrationCoverage label={t("observability.proxy")} value={integration.proxy ?? coverageValue(integration.coverage?.proxy)} />
      </div>

      <div className="integration-owner-row">
        <strong>{t("observability.detectedOwners")}</strong>
        <div>
          {owners.length > 0 ? owners.map((owner) => <span className="pill" key={owner}>{owner}</span>) : <span className="muted">{t("common.none")}</span>}
        </div>
      </div>

      {integration.drift && <ErrorNotice warning error={t("observability.driftDetected")} />}
      {conflicts.map((item) => <ErrorNotice key={item} error={item} />)}
      {warnings.map((item) => <ErrorNotice key={item} warning error={item} />)}
      {error && <ErrorNotice error={error} />}

      <div className="integration-actions">
        <button className="ghost-action" disabled={busy !== ""} onClick={() => onAction(host, "preview")}>{t("observability.preview")}</button>
        <button className="ghost-action" disabled={busy !== ""} onClick={() => onAction(host, "doctor")}><ShieldCheck size={14} /> {t("observability.doctor")}</button>
        {!installed ? (
          <button className="action" disabled={busy !== ""} onClick={() => onAction(host, "install")}><Plug size={14} /> {t("observability.install")}</button>
        ) : (
          <>
            <button className="action" disabled={busy !== ""} onClick={() => onAction(host, "repair")}><Wrench size={14} /> {t("observability.repair")}</button>
            <button className="ghost-action danger-action" disabled={busy !== ""} onClick={() => onAction(host, "uninstall")}>{t("observability.uninstall")}</button>
          </>
        )}
      </div>

      {busy.startsWith(`${host}:`) && <div className="integration-result muted">{t("observability.runningAction")}</div>}
      {result && (
        <div className="integration-result">
          <div>
            {result.ok === false ? <XCircle size={15} /> : <CheckCircle2 size={15} />}
            <strong>{result.message || result.status || t("observability.actionComplete")}</strong>
          </div>
          {(result.changes ?? []).map((change) => <span key={change}>• {change}</span>)}
          {(result.actions ?? []).map((action, index) => <span key={`${action.kind}-${action.target}-${index}`}>• {action.reason || `${action.kind || "action"}: ${action.target || ""}`}</span>)}
          {(result.preserved ?? []).map((item) => <span key={item}>• {item}</span>)}
          {result.pending_trust && <span>• {t("observability.pendingTrustHint")}</span>}
          {result.preview !== undefined && <pre>{pretty(result.preview)}</pre>}
        </div>
      )}

      {integration.updated_at && <time className="integration-updated">{t("common.updated")} {formatDate(integration.updated_at, language)}</time>}
    </article>
  );
}

function IntegrationCoverage({ label, value, fallback = "unavailable" }: { label: string; value?: ObservationIntegrationCoverage; fallback?: string }) {
  const state = value?.status || value?.quality || (value?.enabled ? "enabled" : value?.available ? "available" : fallback);
  return (
    <div>
      <span>{label}</span>
      <StatusBadge value={state || fallback} />
      {value?.detail && <small>{value.detail}</small>}
    </div>
  );
}

function TraceTable({ traces, language, onOpenTrace }: { traces: ObservationTrace[]; language: string; onOpenTrace: (traceID: string) => void }) {
  const { t } = useI18n();
  if (traces.length === 0) return <div className="empty-state">{t("observability.noTraces")}</div>;
  return (
    <div className="table-wrap">
      <table className="observability-trace-table">
        <thead><tr><th>{t("observability.trace")}</th><th>{t("observability.agent")}</th><th>{t("observability.duration")}</th><th>{t("observability.totalTokens")}</th><th>{t("common.source")}</th><th>{t("common.status")}</th></tr></thead>
        <tbody>
          {traces.map((trace) => (
            <tr key={trace.trace_id} onClick={() => onOpenTrace(trace.trace_id)} tabIndex={0} onKeyDown={(event) => event.key === "Enter" && onOpenTrace(trace.trace_id)}>
              <td><strong>{trace.name || shortID(trace.trace_id)}</strong><span className="table-secondary">{formatDate(trace.started_at, language)}</span></td>
              <td>{trace.agent_name || trace.agent_id || "—"}</td>
              <td>{formatDuration(durationBetween(trace.started_at, trace.ended_at))}</td>
              <td>{formatCompact(trace.usage?.total_tokens ?? tokenTotal(trace.usage), language)}</td>
              <td>{sourceLabel(trace.source || "")}</td>
              <td><StatusBadge value={trace.status || "unset"} /></td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

function Metric({ label, value, icon, tone = "accent" }: { label: string; value: string; icon: React.ReactNode; tone?: string }) {
  return (
    <div className={`metric-card observability-metric tone-${tone}`}>
      <span className="metric-icon">{icon}</span>
      <div><div className="label">{label}</div><div className="value">{value}</div></div>
    </div>
  );
}

function Fact({ icon, label, value }: { icon?: React.ReactNode; label: string; value: string }) {
  return <div className="observation-fact">{icon && <span>{icon}</span>}<div><small>{label}</small><strong>{value || "—"}</strong></div></div>;
}

function Identifier({ label, value }: { label: string; value?: string }) {
  if (!value) return null;
  return <span className="identifier"><small>{label}</small><code>{value}</code></span>;
}

function PayloadPreview({ label, value }: { label: string; value: unknown }) {
  if (value === undefined || value === null || value === "") return null;
  return <details className="payload-preview"><summary>{label}</summary><pre>{pretty(value)}</pre></details>;
}

function ErrorNotice({ error, warning = false }: { error: string; warning?: boolean }) {
  return <div className={`observation-error-notice ${warning ? "warning" : ""}`}>{warning ? <AlertTriangle size={15} /> : <XCircle size={15} />}<span>{error}</span></div>;
}

function LoadError({ error, reload, compact = false }: { error: string; reload: () => void; compact?: boolean }) {
  const { t } = useI18n();
  return <section className={compact ? "load-error compact" : "surface load-error"}><AlertTriangle size={18} /><div><strong>{t("observability.loadFailed")}</strong><span>{error}</span></div><button className="ghost-action" onClick={reload}>{t("common.retry")}</button></section>;
}

function LoadingSurface() {
  const { t } = useI18n();
  return <section className="surface surface-body muted">{t("common.loading")}</section>;
}

function StatusBadge({ value }: { value: string }) {
  const { t } = useI18n();
  const normalized = value || "unknown";
  const key = `observability.state.${normalized.toLowerCase()}`;
  const translated = t(key);
  return <span className={`status-badge ${toneForStatus(normalized)}`}><span className="status-dot" />{translated === key ? statusLabel(normalized) : translated}</span>;
}

function QualityBadge({ quality }: { quality: string }) {
  const { t } = useI18n();
  const key = `observability.state.${quality.toLowerCase()}`;
  const translated = t(key);
  return <span className={`status-badge ${quality === "complete" ? "success" : "warning"}`}>{t("observability.quality")}: {translated === key ? statusLabel(quality) : translated}</span>;
}

function SpanIcon({ kind }: { kind: string }) {
  if (kind.includes("tool")) return <Wrench size={15} />;
  if (kind.includes("model")) return <Zap size={15} />;
  if (kind.includes("hook")) return <Plug size={15} />;
  if (kind.includes("permission")) return <ShieldCheck size={15} />;
  if (kind.includes("reply")) return <FileText size={15} />;
  if (kind.includes("run") || kind.includes("turn")) return <Bot size={15} />;
  return <Activity size={15} />;
}

function SourceIcon({ source }: { source: string }) {
  if (source.includes("proxy")) return <Network size={16} />;
  if (source.includes("hook")) return <Plug size={16} />;
  if (source.includes("transcript")) return <FileText size={16} />;
  if (source.includes("otel")) return <Activity size={16} />;
  return <Database size={16} />;
}

function HostIcon({ host }: { host: string }) {
  if (host.toLowerCase().includes("claude")) return <Bot size={21} />;
  if (host.toLowerCase().includes("codex")) return <Zap size={21} />;
  return <Server size={21} />;
}

function readObservabilityRoute(): ObservabilityRoute {
  const raw = window.location.hash.replace(/^#\/?/, "");
  const [path, queryString = ""] = raw.split("?", 2);
  const parts = path.split("/").filter(Boolean).map((part) => decodeURIComponent(part));
  const view = parts[0] === "observability" && OBSERVABILITY_VIEWS.includes(parts[1] as ObservabilityView)
    ? parts[1] as ObservabilityView
    : "overview";
  const params = new URLSearchParams(queryString);
  return {
    view,
    traceID: view === "traces" ? parts[2] || params.get("trace_id") || "" : "",
    sessionID: params.get("session_id") || "",
    agentID: params.get("agent_id") || "",
  };
}

function spanDepths(spans: ObservationSpan[]) {
  const byID = new Map(spans.map((span) => [span.span_id, span]));
  const result = new Map<string, number>();
  function depth(span: ObservationSpan, visited: Set<string>): number {
    if (result.has(span.span_id)) return result.get(span.span_id) ?? 0;
    if (!span.parent_span_id || visited.has(span.span_id)) return 0;
    const parent = byID.get(span.parent_span_id);
    if (!parent) return 0;
    const nextVisited = new Set(visited).add(span.span_id);
    const value = depth(parent, nextVisited) + 1;
    result.set(span.span_id, value);
    return value;
  }
  spans.forEach((span) => result.set(span.span_id, depth(span, new Set())));
  return result;
}

function tokenTotal(usage?: ObservationUsage) {
  if (!usage) return 0;
  return (usage.input_tokens ?? 0) + (usage.output_tokens ?? 0);
}

function durationBetween(start?: string, end?: string) {
  if (!start || !end) return 0;
  return Math.max(0, dateMillis(end) - dateMillis(start));
}

function dateMillis(value: string) {
  const millis = new Date(value).getTime();
  return Number.isFinite(millis) ? millis : 0;
}

function formatDate(value: string | undefined, language: string) {
  if (!value) return "—";
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return "—";
  return date.toLocaleString(language === "zh" ? "zh-CN" : "en-US", { dateStyle: "medium", timeStyle: "medium" });
}

function formatNumber(value: number, language: string) {
  return new Intl.NumberFormat(language === "zh" ? "zh-CN" : "en-US").format(value || 0);
}

function formatCompact(value: number, language: string) {
  return new Intl.NumberFormat(language === "zh" ? "zh-CN" : "en-US", { notation: "compact", maximumFractionDigits: 1 }).format(value || 0);
}

function formatPercent(value: number) {
  const normalized = value > 1 ? value / 100 : value;
  return `${(normalized * 100).toFixed(normalized > 0 && normalized < 0.01 ? 1 : 0)}%`;
}

function formatConfidence(value?: number) {
  if (value === undefined) return "—";
  return formatPercent(value);
}

function formatCost(value?: number) {
  if (value === undefined) return "$0.00";
  return `$${value.toFixed(value > 0 && value < 0.01 ? 4 : 2)}`;
}

function formatDuration(ms: number) {
  if (!ms) return "0ms";
  if (ms < 1000) return `${Math.round(ms)}ms`;
  if (ms < 60_000) return `${(ms / 1000).toFixed(ms < 10_000 ? 1 : 0)}s`;
  return `${Math.floor(ms / 60_000)}m ${Math.round((ms % 60_000) / 1000)}s`;
}

function formatBytes(bytes: number) {
  if (bytes < 1024) return `${bytes} B`;
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`;
  return `${(bytes / (1024 * 1024)).toFixed(1)} MB`;
}

function shortID(value: string) {
  return value.length <= 14 ? value : `${value.slice(0, 8)}…${value.slice(-4)}`;
}

function rate(numerator?: number, denominator?: number) {
  return denominator ? (numerator ?? 0) / denominator : 0;
}

function pretty(value: unknown) {
  if (typeof value === "string") return value;
  try {
    return JSON.stringify(value, null, 2);
  } catch {
    return String(value);
  }
}

function safeClass(value: string) {
  return value.toLowerCase().replace(/[^a-z0-9_-]+/g, "-");
}

function toneForStatus(value?: string) {
  const state = (value || "").toLowerCase();
  if (["ok", "success", "complete", "enabled", "installed", "available", "healthy", "trusted", "active"].includes(state)) return "success";
  if (["error", "failed", "failure", "disabled", "unavailable", "conflict", "danger"].includes(state)) return "danger";
  if (["running", "pending", "pending_trust", "partial", "inferred", "legacy", "drift", "warning", "cancelled"].includes(state)) return "warning";
  return "neutral";
}

function toneForSeverity(value?: string) {
  const severity = (value || "").toLowerCase();
  if (severity === "critical" || severity === "high" || severity === "error") return "danger";
  if (severity === "medium" || severity === "warning") return "warning";
  return "success";
}

function statusLabel(value: string) {
  return value.replace(/_/g, " ");
}

function coverageValue(status?: string): ObservationIntegrationCoverage | undefined {
  return status ? { status } : undefined;
}

function sourceLabel(value: string) {
  if (!value) return "—";
  const known: Record<string, string> = {
    "agentnexus.internal": "AgentNexus",
    "codex.app_server": "Codex app-server",
    "claude.otel": "Claude OTel",
    "codex.otel": "Codex OTel",
    proxy: "Proxy",
    transcript: "Transcript",
  };
  return known[value] || value;
}

function hostLabel(value: string) {
  const lower = value.toLowerCase();
  if (lower.includes("claude")) return "Claude Code";
  if (lower.includes("codex")) return "Codex";
  return value;
}

function modelLabel(requested?: string, resolved?: string) {
  if (requested && resolved && requested !== resolved) return `${requested} → ${resolved}`;
  return resolved || requested || "";
}
