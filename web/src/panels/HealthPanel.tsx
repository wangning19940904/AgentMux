import { AlertTriangle, ArrowRight, Cable, CheckCircle2, Clock3, DatabaseZap, Gauge, MonitorDot, RefreshCw } from "lucide-react";
import { useCallback, useMemo, type ReactNode } from "react";
import { api, type Channel, type CursorUsageSourceStatus, type FleetView, type FrameworkSmokeResult, type Targeted } from "../api";
import { TargetBadge, targetKey } from "../components/TargetBadge";
import { usePolling } from "../hooks/usePolling";
import { useI18n } from "../i18n";
import { prefetchCachedResource, useCachedResource } from "../useCachedResource";
import { runtimeLabel } from "./agents/agentUtils";
import {
  channelIsHealthy, latestFrameworkResults, summarizeChannelHealth, summarizeFrameworkHealth, summarizeMachines,
  summarizeUsageSources, usageSourceIssue, type HealthSummary,
} from "./health/healthSummary";
import { sourceStatusLabel } from "./usage/UsageSources";

const REFRESH_MS = 30_000;
const STALE_MS = 15_000;

const loaders = {
  channelHealth: () => api.channelHealth().then((items) => items ?? []),
  frameworkHealth: () => api.frameworkHealthOverview(),
  // A machine that is not part of a fleet has no fleet to list.
  fleet: () => api.fleet().catch((): FleetView => ({ source: "local", fetched_at: new Date().toISOString(), machines: [] })),
  usageSources: () => api.usageSources(),
};

// prefetchHealth starts the page's reads when its navigation entry is pointed at.
export function prefetchHealth() {
  prefetchCachedResource("channelHealth", loaders.channelHealth, STALE_MS);
  prefetchCachedResource("frameworkHealth", loaders.frameworkHealth, STALE_MS);
  prefetchCachedResource("fleetPresence", loaders.fleet, STALE_MS);
  prefetchCachedResource("usageSources", loaders.usageSources, STALE_MS);
}

type Translate = (key: string, values?: Record<string, string | number>) => string;
type Tone = "success" | "warning" | "";

function formatWhen(value?: string) {
  if (!value) return "";
  const parsed = new Date(value);
  return Number.isNaN(parsed.getTime()) ? "" : parsed.toLocaleString();
}

// HealthPanel answers "is anything broken?" for everything AgentMux depends
// on: bot channels, agent frameworks, the machines of the fleet and the
// sources usage is collected from. Problems are listed first, each with the
// page that fixes it.
export function HealthPanel() {
  const { t } = useI18n();
  const channels = useCachedResource<Targeted<Channel>[] | Channel[]>("channelHealth", loaders.channelHealth, STALE_MS);
  const frameworks = useCachedResource<Targeted<FrameworkSmokeResult>[]>("frameworkHealth", loaders.frameworkHealth, STALE_MS);
  const fleet = useCachedResource<FleetView>("fleetPresence", loaders.fleet, STALE_MS);
  const sources = useCachedResource<Targeted<CursorUsageSourceStatus>[] | CursorUsageSourceStatus[]>("usageSources", loaders.usageSources, STALE_MS);

  const reload = useCallback(() => {
    void channels.reload();
    void frameworks.reload();
    void fleet.reload();
    void sources.reload();
  }, [channels.reload, frameworks.reload, fleet.reload, sources.reload]);
  usePolling(reload, REFRESH_MS);

  const channelList = (channels.data ?? []) as Targeted<Channel>[];
  const frameworkResults = useMemo(() => latestFrameworkResults(frameworks.data ?? []), [frameworks.data]);
  const machines = fleet.data?.machines ?? [];
  const sourceList = (sources.data ?? []) as Targeted<CursorUsageSourceStatus>[];

  const channelSummary = useMemo(() => summarizeChannelHealth(channelList), [channelList]);
  const frameworkSummary = useMemo(() => summarizeFrameworkHealth(frameworks.data ?? []), [frameworks.data]);
  const machineSummary = useMemo(() => summarizeMachines(machines), [machines]);
  const sourceSummary = useMemo(() => summarizeUsageSources(sourceList), [sourceList]);

  const resources = [channels, frameworks, fleet, sources];
  const firstLoad = resources.some((resource) => resource.loading && !resource.hasValue);
  const failed = resources.filter((resource) => resource.error && !resource.loading).length;
  const attention = channelSummary.issues + frameworkSummary.issues + machineSummary.issues + sourceSummary.issues + failed;
  const configured = channelSummary.total + frameworkSummary.total + machineSummary.total + sourceSummary.total;
  const updatedAt = Math.max(...resources.map((resource) => resource.updatedAt || 0));
  const refreshing = resources.some((resource) => resource.loading);

  const orderedChannels = useMemo(() => [...channelList].sort((left, right) => channelRank(left) - channelRank(right)
    || (left.bot_name || left.name).localeCompare(right.bot_name || right.name)), [channelList]);

  return (
    <div className="page-stack health-dashboard">
      <section className="surface">
        <div className="surface-header statistics-header">
          <div>
            <h2>{t("health.title")}</h2>
            <p className="subtle-copy">{t("health.hint")}</p>
          </div>
          <div className="statistics-header-actions">
            <span className={`status-badge ${firstLoad || configured === 0 ? "" : attention > 0 ? "warning" : "success"}`} role="status">
              {firstLoad ? <Clock3 size={13} /> : attention > 0 ? <AlertTriangle size={13} /> : <CheckCircle2 size={13} />}
              {firstLoad
                ? t("common.loading")
                : attention > 0
                  ? t("overview.healthAttention", { count: attention })
                  : configured === 0 ? t("overview.healthNotConfigured") : t("health.allGood")}
            </span>
            <div className="activity-line overview-updated-at">
              <Clock3 size={14} />
              <span>{t("overview.lastUpdated")} {updatedAt ? new Date(updatedAt).toLocaleTimeString() : "—"}</span>
            </div>
            <button className="ghost-action" type="button" onClick={reload} disabled={refreshing}>
              <RefreshCw size={14} className={refreshing ? "spin" : undefined} />
              {t("common.refresh")}
            </button>
          </div>
        </div>
        <div className="health-summary-grid">
          <SummaryTile
            icon={<Cable size={19} />} title={t("overview.channelConnections")} summary={channelSummary}
            loading={channels.loading && !channels.hasValue} error={channels.error} href="#health-channels" t={t}
            text={channelSummary.total > 0
              ? t("overview.channelHealthSummary", {
                healthy: channelSummary.healthy,
                enabled: Math.max(0, channelSummary.total - channelSummary.inactive),
                inactive: channelSummary.inactive,
              })
              : t("overview.noChannelsConfigured")}
          />
          <SummaryTile
            icon={<DatabaseZap size={19} />} title={t("overview.frameworkAvailability")} summary={frameworkSummary}
            loading={frameworks.loading && !frameworks.hasValue} error={frameworks.error} href="#health-frameworks" t={t}
            text={frameworkSummary.total > 0
              ? t("overview.frameworkHealthSummary", { healthy: frameworkSummary.healthy, total: frameworkSummary.total })
              : t("overview.noFrameworkTests")}
          />
          <SummaryTile
            icon={<MonitorDot size={19} />} title={t("health.machines")} summary={machineSummary}
            loading={fleet.loading && !fleet.hasValue} error={fleet.error} href="#health-machines" t={t}
            text={machineSummary.total > 0
              ? t("health.machinesSummary", { healthy: machineSummary.healthy, total: machineSummary.total })
              : t("health.noFleet")}
          />
          <SummaryTile
            icon={<Gauge size={19} />} title={t("health.usageSources")} summary={sourceSummary}
            loading={sources.loading && !sources.hasValue} error={sources.error} href="#health-sources" t={t}
            text={sourceSummary.total > 0
              ? t("health.sourcesSummary", { healthy: sourceSummary.healthy, total: sourceSummary.total })
              : t("health.noSourcesConnected")}
          />
        </div>
      </section>

      <HealthSection
        id="health-channels" title={t("overview.channelConnections")} hint={t("health.channelsHint")}
        error={channels.error} empty={orderedChannels.length === 0 ? t("overview.noChannelsConfigured") : ""}
        action={{ href: "#channels", label: t("health.openChannels") }}
      >
        {orderedChannels.map((channel) => {
          const healthy = channelIsHealthy(channel);
          const tone: Tone = !channel.enabled ? "" : healthy ? "success" : "warning";
          return (
            <HealthRow
              key={targetKey(channel.target_id, channel.id)}
              tone={tone}
              status={!channel.enabled ? t("health.disabled") : healthy ? t("common.connected") : t("common.disconnected")}
              title={channel.bot_name || channel.name}
              badge={<TargetBadge target_id={channel.target_id} target_name={channel.target_name} />}
              meta={[channel.type, channel.agent_name].filter(Boolean).join(" · ")}
              detail={channel.error || (channel.enabled && !healthy && channel.state ? channel.state : "")}
              when={formatWhen(channel.last_event_at || channel.last_heartbeat_at || channel.last_checked_at)}
            />
          );
        })}
      </HealthSection>

      <HealthSection
        id="health-frameworks" title={t("overview.frameworkAvailability")} hint={t("health.frameworksHint")}
        error={frameworks.error} empty={frameworkResults.length === 0 ? t("overview.noFrameworkTests") : ""}
        action={{ href: "#frameworks", label: t("health.openFrameworks") }}
      >
        {frameworkResults.map((result) => (
          <HealthRow
            key={targetKey(result.target_id, result.kind)}
            tone={result.ok ? "success" : "warning"}
            status={result.ok ? t("health.testPassed") : t("health.testFailed")}
            title={runtimeLabel(result.kind, t)}
            badge={<TargetBadge target_id={result.target_id} target_name={result.target_name} />}
            meta={[result.resolved_model || result.model, result.ok && result.latency_ms > 0 ? `${(result.latency_ms / 1000).toFixed(1)}s` : ""].filter(Boolean).join(" · ")}
            detail={result.ok ? "" : result.error_detail || result.error_kind || ""}
            when={formatWhen(result.checked_at)}
          />
        ))}
      </HealthSection>

      {machines.length > 0 && (
        <HealthSection
          id="health-machines" title={t("health.machines")} hint={t("health.machinesHint")} error={fleet.error} empty=""
          action={{ href: "#machines", label: t("health.openMachines") }}
        >
          {[...machines].sort((left, right) => Number(left.online) - Number(right.online) || left.name.localeCompare(right.name)).map((machine) => (
            <HealthRow
              key={machine.id}
              tone={machine.online ? "success" : "warning"}
              status={machine.online ? t("health.online") : t("health.offline")}
              title={machine.name || machine.id}
              badge={machine.hub_role ? <span className="target-badge">{t(`health.role.${machine.hub_role}`)}</span> : null}
              meta={machine.version ?? ""}
              detail={machine.online ? "" : t("health.offlineDetail")}
              when={machine.online ? "" : formatWhen(machine.last_seen_at)}
            />
          ))}
        </HealthSection>
      )}

      <HealthSection
        id="health-sources" title={t("health.usageSources")} hint={t("health.sourcesHint")}
        error={sources.error} empty={sourceList.length === 0 ? t("usage.sourcesEmpty") : ""}
        action={{ href: "#usage", label: t("health.openUsage") }}
      >
        {sourceList.map((source) => {
          const issue = usageSourceIssue(source);
          return (
            <HealthRow
              key={targetKey(source.target_id, source.source)}
              tone={!source.connected ? "" : issue ? "warning" : "success"}
              status={!source.connected ? t("usage.sourceStatus.not_connected") : issue ? t("health.notCollecting") : t("health.collecting")}
              title="Cursor"
              badge={<TargetBadge target_id={source.target_id} target_name={source.target_name} />}
              meta={source.connected ? `${t("usage.cursorCloud")}: ${sourceStatusLabel(source.cloud_status, t)} · Hook: ${sourceStatusLabel(source.hook?.status ?? "", t)}` : ""}
              detail={issue === source.last_error ? issue : issue ? sourceStatusLabel(issue, t) : ""}
              when={formatWhen(source.last_sync_at)}
            />
          );
        })}
      </HealthSection>
    </div>
  );
}

function channelRank(channel: Channel) {
  if (!channel.enabled) return 2;
  return channelIsHealthy(channel) ? 1 : 0;
}

function SummaryTile({ icon, title, summary, loading, error, text, href, t }: {
  icon: ReactNode;
  title: string;
  summary: HealthSummary;
  loading: boolean;
  error: string | null;
  text: string;
  href: string;
  t: Translate;
}) {
  const attention = Boolean(error) || summary.issues > 0;
  const status = loading
    ? t("common.loading")
    : error
      ? t("overview.healthUnavailable")
      : summary.issues > 0
        ? t("overview.healthIssues", { count: summary.issues })
        : summary.total === 0 ? t("overview.healthNotConfigured") : t("overview.healthNormal");
  return (
    <a className="health-summary-tile" href={href} onClick={(event) => {
      // In-page jump: the hash belongs to the router.
      event.preventDefault();
      document.getElementById(href.slice(1))?.scrollIntoView({ behavior: "smooth", block: "start" });
    }}>
      <span className={`overview-health-icon ${attention ? "warning" : summary.total > 0 ? "success" : ""}`}>{icon}</span>
      <span className="health-summary-copy">
        <strong>{title}</strong>
        <span>{loading ? t("common.loading") : error ? t("overview.healthLoadFailed") : text}</span>
      </span>
      <span className={`status-badge ${attention ? "warning" : summary.total > 0 && !loading ? "success" : ""}`}>
        <span className="status-dot" />
        {status}
      </span>
    </a>
  );
}

function HealthSection({ id, title, hint, error, empty, action, children }: {
  id: string;
  title: string;
  hint: string;
  error: string | null;
  empty: string;
  action: { href: string; label: string };
  children: ReactNode;
}) {
  return (
    <section className="surface health-section" id={id}>
      <div className="surface-header">
        <div>
          <h2>{title}</h2>
          <p className="subtle-copy">{hint}</p>
        </div>
        <a className="ghost-action" href={action.href}>
          {action.label}
          <ArrowRight size={14} />
        </a>
      </div>
      {error && <div className="surface-body error" role="alert">{error}</div>}
      {empty ? <div className="surface-body muted">{empty}</div> : <div className="health-list">{children}</div>}
    </section>
  );
}

function HealthRow({ tone, status, title, badge, meta, detail, when }: {
  tone: Tone;
  status: string;
  title: string;
  badge?: ReactNode;
  meta?: string;
  detail?: string;
  when?: string;
}) {
  return (
    <div className={`health-row${tone === "warning" ? " needs-attention" : ""}`}>
      <span className={`status-badge ${tone}`}>
        <span className="status-dot" />
        {status}
      </span>
      <span className="health-row-copy">
        <span className="health-row-title">
          <strong>{title}</strong>
          {badge}
          {meta && <small>{meta}</small>}
        </span>
        {detail && <span className="health-row-detail">{detail}</span>}
      </span>
      {when && <time className="health-row-when">{when}</time>}
    </div>
  );
}
