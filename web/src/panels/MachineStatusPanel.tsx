import {
  ArrowDown,
  ArrowUp,
  CircleAlert,
  Hand,
  Laptop,
  Monitor,
  RefreshCw,
  Server,
  TriangleAlert,
} from "lucide-react";
import { useCallback, useEffect, useMemo, useRef, useState, type ReactNode } from "react";
import { activeMachineScope, api, type MachineTarget, type SessionActivity, type SystemMetrics } from "../api";
import { usePolling } from "../hooks/usePolling";
import { useI18n } from "../i18n";
import {
  downloadCounterStalled,
  formatBytes,
  formatLoad,
  formatPercent,
  formatRate,
  formatUptime,
  fullestDisk,
  isSystemDisk,
  loadingStatus,
  machineStatuses,
  machineStatusTargets,
  offlineStatus,
  sparklinePaths,
  summarizeFleet,
  type AlertKind,
  type MachineAlert,
  type MachineStatus,
} from "./machineStatusModel";

const POLL_INTERVAL_MS = 5_000;
// Matches the server's history capacity so a fresh sampler fills in from the
// right instead of stretching a few points across the whole chart.
const SPARKLINE_SLOTS = 72;

type Translate = (key: string, params?: Record<string, string | number>) => string;

function errorMessage(error: unknown) {
  return error instanceof Error ? error.message : String(error);
}

export function MachineStatusPanel() {
  const { language, t } = useI18n();
  const [targets, setTargets] = useState<MachineTarget[] | null>(null);
  const [statusByID, setStatusByID] = useState<Record<string, MachineStatus>>({});
  const [updatedAt, setUpdatedAt] = useState<number | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [pending, setPending] = useState(0);
  const inFlight = useRef(new Set<string>());
  const targetsRef = useRef<MachineTarget[]>([]);
  const mounted = useRef(true);

  // Each machine is polled on its own: an unreachable SSH host can take
  // several seconds to time out and must not delay the others. A machine is
  // never asked again while its previous read is still in flight.
  const loadTarget = useCallback(async (target: MachineTarget) => {
    if (inFlight.current.has(target.id)) return;
    inFlight.current.add(target.id);
    setPending(inFlight.current.size);
    let next: MachineStatus;
    try {
      next = machineStatuses(await api.machineStatus([target.id]))[0] ?? offlineStatus(target, "unavailable");
    } catch (loadError) {
      next = offlineStatus(target, errorMessage(loadError));
    } finally {
      inFlight.current.delete(target.id);
      if (mounted.current) setPending(inFlight.current.size);
    }
    if (!mounted.current) return;
    setStatusByID((current) => ({ ...current, [target.id]: { ...next, target: { ...next.target, name: target.name } } }));
    setUpdatedAt(Date.now());
  }, []);

  const refresh = useCallback(async () => {
    let next = targetsRef.current;
    try {
      // The host list is a cheap local read; re-reading it picks up machines
      // added or removed elsewhere in the Console.
      next = machineStatusTargets(activeMachineScope(), await api.remoteHosts());
      if (mounted.current) setError(null);
    } catch (listError) {
      if (mounted.current) setError(errorMessage(listError));
      if (!next.length) return;
    }
    if (!mounted.current) return;
    targetsRef.current = next;
    setTargets(next);
    next.forEach((target) => { void loadTarget(target); });
  }, [loadTarget]);

  useEffect(() => {
    mounted.current = true;
    void refresh();
    return () => { mounted.current = false; };
  }, [refresh]);
  usePolling(refresh, POLL_INTERVAL_MS);

  const statuses = useMemo(
    () => targets?.map((target) => statusByID[target.id] ?? loadingStatus(target)) ?? null,
    [statusByID, targets],
  );
  const summary = useMemo(() => summarizeFleet(statuses ?? []), [statuses]);
  const firstLoadPending = Boolean(statuses?.some((status) => status.state === "loading"));
  const locale = language === "zh" ? "zh-CN" : "en-US";

  return (
    <div className="machine-status-page">
      <div className="machine-status-summary" role="status">
        {statuses && !firstLoadPending && (
          <>
            <span className="machine-status-summary-item">
              <span className={`machine-status-dot ${summary.reachable === summary.total ? "online" : "offline"}`} />
              {t("machineStatus.online", { reachable: summary.reachable, total: summary.total })}
            </span>
            {summary.attention > 0 && (
              <span className={`machine-status-summary-item ${summary.critical > 0 ? "critical" : "warning"}`}>
                <TriangleAlert size={14} />
                {t("machineStatus.attention", { count: summary.attention })}
              </span>
            )}
            {summary.needsInput > 0 && (
              <span className="machine-status-summary-item warning">
                <Hand size={14} />
                {t("machineStatus.needsInputSummary", { count: summary.needsInput })}
              </span>
            )}
          </>
        )}
        <span className="machine-status-summary-meta">
          {error && statuses && <span className="machine-status-stale" title={error}>{t("machineStatus.refreshFailed")}</span>}
          {updatedAt && (
            <>
              {t("machineStatus.updated", {
                time: new Date(updatedAt).toLocaleTimeString(locale, { hour: "2-digit", minute: "2-digit", second: "2-digit" }),
              })}
              <span aria-hidden="true">·</span>
            </>
          )}
          {t("machineStatus.autoRefresh", { seconds: POLL_INTERVAL_MS / 1000 })}
        </span>
        <button className="ghost-action" type="button" onClick={() => void refresh()} aria-label={t("common.refresh")}>
          <RefreshCw size={14} className={pending > 0 ? "spin" : undefined} />
          {t("common.refresh")}
        </button>
      </div>

      {!statuses && error && (
        <section className="surface" role="alert">
          <div className="surface-header">
            <div>
              <h2>{t("machineStatus.loadFailed")}</h2>
              <p className="subtle-copy">{error}</p>
            </div>
          </div>
        </section>
      )}
      {!statuses && !error && <div className="empty-state">{t("common.loading")}</div>}
      {statuses?.length === 0 && <div className="empty-state">{t("machineStatus.empty")}</div>}

      {statuses && statuses.length > 0 && (
        <div className="machine-status-grid">
          {statuses.map((status) => <MachineCard key={status.target.id} status={status} t={t} />)}
        </div>
      )}
    </div>
  );
}

function MachineCard({ status, t }: { status: MachineStatus; t: Translate }) {
  const { target, metrics, state } = status;
  const name = target.kind === "local" ? t("machineStatus.localMachine") : target.name;
  const alertFor = (kind: AlertKind) => status.alerts.find((alert) => alert.kind === kind);
  return (
    <article className={`machine-status-card${status.severity ? ` ${status.severity}` : ""}${state === "offline" ? " offline" : ""}`}>
      <header className="machine-status-card-header">
        <OSTile metrics={metrics} />
        <div className="machine-status-card-title">
          <h3 title={name}>{name}</h3>
          <p>{hostDescription(metrics, t)}</p>
        </div>
        <span className={`machine-status-state ${state}`}>
          <span className={`machine-status-dot ${state === "online" ? "online" : state === "offline" ? "offline" : "idle"}${state === "loading" ? " pulse" : ""}`} />
          {t(`machineStatus.state.${state}`)}
        </span>
      </header>

      {status.alerts.length > 0 && (
        <div className="machine-status-alerts">
          {status.alerts.map((alert) => <AlertChip key={alert.kind} alert={alert} t={t} />)}
        </div>
      )}

      {state === "online" && metrics ? (
        <>
          {metrics.sessions && <SessionStrip sessions={metrics.sessions} t={t} />}
          <div className="machine-status-metrics">
            <CPUCell metrics={metrics} alert={alertFor("cpu") ?? alertFor("load")} t={t} />
            <MemoryCell metrics={metrics} alert={alertFor("memory")} t={t} />
            <DiskCell metrics={metrics} alert={alertFor("disk")} t={t} />
            <NetworkCell metrics={metrics} t={t} />
          </div>
          <CardFooter metrics={metrics} t={t} />
        </>
      ) : state === "loading" ? (
        <div className="machine-status-unavailable">
          <p>{t("machineStatus.connecting")}</p>
        </div>
      ) : (
        <div className="machine-status-unavailable">
          <p>{unavailableMessage(status, t)}</p>
          {status.error && state === "offline" && <code title={status.error}>{status.error}</code>}
          {state !== "unsupported" && (
            <a className="ghost-action" href="#machines">{t("machineStatus.manageMachines")}</a>
          )}
        </div>
      )}
    </article>
  );
}

function hostDescription(metrics: SystemMetrics | undefined, t: Translate) {
  if (!metrics?.host) return "—";
  const parts = [metrics.host.platform || metrics.host.os];
  if (metrics.host.cpu_cores) parts.push(t("machineStatus.cores", { count: metrics.host.cpu_cores }));
  if (metrics.memory?.total) parts.push(formatBytes(metrics.memory.total));
  return parts.join(" · ");
}

function unavailableMessage(status: MachineStatus, t: Translate) {
  switch (status.state) {
    case "outdated":
      return t("machineStatus.outdatedHint");
    case "unsupported":
      return t("machineStatus.unsupportedHint", { os: status.metrics?.host.platform || status.metrics?.host.os || "—" });
    default:
      return t("machineStatus.offlineHint");
  }
}

function OSTile({ metrics }: { metrics?: SystemMetrics }) {
  const os = metrics?.host.os ?? "";
  const id = metrics?.host.platform_id ?? "";
  const family = os === "darwin" ? "macos" : os === "linux" ? (["debian", "ubuntu"].includes(id) ? id : "linux") : os === "windows" ? "windows" : "unknown";
  const Icon = family === "macos" ? Laptop : family === "windows" ? Monitor : Server;
  return (
    <span className={`machine-status-os os-${family}`} aria-hidden="true">
      <Icon size={22} strokeWidth={1.8} />
    </span>
  );
}

function AlertChip({ alert, t }: { alert: MachineAlert; t: Translate }) {
  const Icon = alert.severity === "critical" ? CircleAlert : TriangleAlert;
  const detail = alert.kind === "disk"
    ? t("machineStatus.alertDetail.disk", {
      mount: isSystemDisk(alert.mount) ? t("machineStatus.systemDisk") : alert.mount ?? "/",
      percent: formatPercent(alert.value),
    })
    : alert.kind === "load"
      ? t("machineStatus.alertDetail.load", { ratio: alert.value.toFixed(1) })
      : t(`machineStatus.alertDetail.${alert.kind}`, { percent: formatPercent(alert.value) });
  return (
    <span className={`machine-status-alert ${alert.severity}`} title={detail}>
      <Icon size={14} strokeWidth={2.2} />
      {t(`machineStatus.alert.${alert.kind}.${alert.severity}`)}
    </span>
  );
}

function SessionStrip({ sessions, t }: { sessions: SessionActivity; t: Translate }) {
  const items = [
    { key: "needs-input", count: sessions.needs_input, label: t("machineStatus.needsInput") },
    { key: "working", count: sessions.working, label: t("machineStatus.working") },
    { key: "idle", count: sessions.idle, label: t("machineStatus.idle") },
  ];
  return (
    <div className="machine-status-sessions" title={t("machineStatus.sessionsHint")}>
      {items.map((item) => (
        <span key={item.key} className={`machine-status-session ${item.key}${item.count > 0 ? " active" : ""}`}>
          <span className="machine-status-session-dot" />
          <strong>{item.count}</strong>
          {item.label}
        </span>
      ))}
    </div>
  );
}

function MetricCell({ label, value, detail, alert, children }: {
  label: string;
  value: ReactNode;
  detail: ReactNode;
  alert?: MachineAlert;
  children?: ReactNode;
}) {
  return (
    <div className={`machine-status-metric${alert ? ` ${alert.severity}` : ""}`}>
      <span className="machine-status-metric-label">
        {label}
        {alert && (alert.severity === "critical" ? <CircleAlert size={12} /> : <TriangleAlert size={12} />)}
      </span>
      <strong className="machine-status-metric-value">{value}</strong>
      <span className="machine-status-metric-detail">{detail}</span>
      <div className="machine-status-chart">{children}</div>
    </div>
  );
}

function CPUCell({ metrics, alert, t }: { metrics: SystemMetrics; alert?: MachineAlert; t: Translate }) {
  const load = metrics.cpu?.load;
  return (
    <MetricCell
      label="CPU"
      value={formatPercent(metrics.cpu?.percent)}
      detail={load ? (
        <span title={`${formatLoad(load.one)} / ${formatLoad(load.five)} / ${formatLoad(load.fifteen)}`}>
          {t("machineStatus.load", { value: formatLoad(load.one) })}
        </span>
      ) : "—"}
      alert={alert}
    >
      <Sparkline series={[{ values: metrics.history.map((point) => point.cpu), tone: "cpu", area: true }]} max={100} />
    </MetricCell>
  );
}

function MemoryCell({ metrics, alert, t }: { metrics: SystemMetrics; alert?: MachineAlert; t: Translate }) {
  const memory = metrics.memory;
  const swap = memory?.swap_total ? t("machineStatus.swap", { used: formatBytes(memory.swap_used ?? 0), total: formatBytes(memory.swap_total) }) : undefined;
  return (
    <MetricCell
      label={t("machineStatus.memory")}
      value={formatPercent(memory?.percent)}
      detail={memory ? <span title={swap}>{formatBytes(memory.used)} / {formatBytes(memory.total)}</span> : "—"}
      alert={alert}
    >
      <Sparkline series={[{ values: metrics.history.map((point) => point.mem), tone: "memory", area: true }]} max={100} />
    </MetricCell>
  );
}

function DiskCell({ metrics, alert, t }: { metrics: SystemMetrics; alert?: MachineAlert; t: Translate }) {
  const disk = fullestDisk(metrics.disks);
  const all = (metrics.disks ?? [])
    .map((item) => `${item.mount}  ${formatPercent(item.percent)} · ${formatBytes(item.used)} / ${formatBytes(item.total)}`)
    .join("\n");
  return (
    <MetricCell
      label={t("machineStatus.disk")}
      value={formatPercent(disk?.percent)}
      detail={disk ? (
        <span title={all}>
          {isSystemDisk(disk.mount) ? "" : `${disk.mount} · `}{formatBytes(disk.used)} / {formatBytes(disk.total)}
        </span>
      ) : "—"}
      alert={alert}
    >
      {disk && (
        <div className="machine-status-disk-bars" title={all}>
          {(metrics.disks ?? []).slice(0, 4).map((item) => (
            <span key={item.mount} className="machine-status-disk-bar">
              <span
                className={item.percent >= 90 ? "critical" : item.percent >= 80 ? "warning" : undefined}
                style={{ width: `${Math.min(100, Math.max(1, item.percent))}%` }}
              />
            </span>
          ))}
        </div>
      )}
    </MetricCell>
  );
}

function NetworkCell({ metrics, t }: { metrics: SystemMetrics; t: Translate }) {
  const network = metrics.network;
  const downloadUnknown = downloadCounterStalled(metrics.history);
  return (
    <MetricCell
      label={t("machineStatus.network")}
      value={(
        <span
          className="machine-status-rate"
          title={downloadUnknown ? t("machineStatus.downloadUnavailable") : t("machineStatus.download")}
        >
          <ArrowDown size={15} strokeWidth={2.4} />{downloadUnknown ? "—" : formatRate(network?.rx_bytes_per_sec)}
        </span>
      )}
      detail={(
        <span className="machine-status-rate" title={t("machineStatus.upload")}>
          <ArrowUp size={12} strokeWidth={2.2} />{formatRate(network?.tx_bytes_per_sec)}
        </span>
      )}
    >
      <Sparkline series={[
        ...(downloadUnknown ? [] : [{ values: metrics.history.map((point) => point.rx), tone: "rx" as const }]),
        { values: metrics.history.map((point) => point.tx), tone: "tx" },
      ]} />
    </MetricCell>
  );
}

function CardFooter({ metrics, t }: { metrics: SystemMetrics; t: Translate }) {
  const uptime = formatUptime(metrics.host.boot_time);
  const parts = [metrics.host.hostname, metrics.agentmux_version && t("machineStatus.version", { version: metrics.agentmux_version })];
  if (uptime) {
    parts.push(uptime.days > 0
      ? t("machineStatus.uptimeDays", { days: uptime.days, hours: uptime.hours })
      : t("machineStatus.uptimeHours", { hours: uptime.hours, minutes: uptime.minutes }));
  }
  return <footer className="machine-status-card-footer">{parts.filter(Boolean).join(" · ")}</footer>;
}

type SparkSeries = { values: Array<number | undefined>; tone: "cpu" | "memory" | "rx" | "tx"; area?: boolean };

const SPARK_WIDTH = 120;
const SPARK_HEIGHT = 32;

function Sparkline({ series, max }: { series: SparkSeries[]; max?: number }) {
  // Series that share a chart share a scale, so download and upload compare.
  const peak = max ?? Math.max(0, ...series.flatMap((item) => item.values.filter((value): value is number => typeof value === "number")));
  return (
    <svg className="machine-status-sparkline" viewBox={`0 0 ${SPARK_WIDTH} ${SPARK_HEIGHT}`} preserveAspectRatio="none" aria-hidden="true">
      {series.map((item) => {
        const paths = sparklinePaths(item.values, SPARK_WIDTH, SPARK_HEIGHT, { max: peak || 1, slots: SPARKLINE_SLOTS });
        return (
          <g key={item.tone} className={`spark-${item.tone}`}>
            {item.area && <path className="spark-area" d={paths.area} />}
            <path className="spark-line" d={paths.line} vectorEffect="non-scaling-stroke" />
          </g>
        );
      })}
    </svg>
  );
}
