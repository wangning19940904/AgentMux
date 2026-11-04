import type { DiskUsage, FleetBatchResult, MachineTarget, MetricPoint, RemoteHost, SystemMetrics } from "../api/types";

export const MACHINE_STATUS_KEY = "metrics";

export type AlertSeverity = "critical" | "warning";
export type AlertKind = "disk" | "load" | "memory" | "cpu";

export interface MachineAlert {
  kind: AlertKind;
  severity: AlertSeverity;
  /** Percent for disk/memory/cpu, load per core for load. */
  value: number;
  /** The mount point for disk alerts. */
  mount?: string;
}

/**
 * loading: the first answer has not arrived. online: metrics available.
 * unsupported: the machine answers but has no collector for its OS.
 * outdated: its AgentMux predates the metrics API. offline: unreachable.
 */
export type MachineState = "loading" | "online" | "unsupported" | "outdated" | "offline";

export interface MachineStatus {
  target: MachineTarget;
  state: MachineState;
  metrics?: SystemMetrics;
  error?: string;
  alerts: MachineAlert[];
  severity?: AlertSeverity;
}

export interface FleetStatusSummary {
  total: number;
  reachable: number;
  attention: number;
  critical: number;
  needsInput: number;
}

// Thresholds follow common operator defaults: warn early on disks and memory,
// and only call load high once the run queue clearly exceeds the cores.
export const ALERT_THRESHOLDS = {
  disk: { warning: 80, critical: 90 },
  memory: { warning: 80, critical: 92 },
  cpu: { warning: 80, critical: 95 },
  loadPerCore: { warning: 1, critical: 1.5 },
} as const;

/** CPU alerts use a short average so a single spike does not flag a machine. */
export const CPU_ALERT_WINDOW = 6;

const ALERT_ORDER: AlertKind[] = ["disk", "memory", "load", "cpu"];

function severityFor(value: number, threshold: { warning: number; critical: number }): AlertSeverity | null {
  if (!Number.isFinite(value)) return null;
  if (value >= threshold.critical) return "critical";
  if (value >= threshold.warning) return "warning";
  return null;
}

export function recentAverage(history: MetricPoint[], field: keyof Omit<MetricPoint, "t">, window: number) {
  const values = history.slice(-window).map((point) => point[field]).filter((value): value is number => typeof value === "number");
  if (!values.length) return undefined;
  return values.reduce((sum, value) => sum + value, 0) / values.length;
}

export function fullestDisk(disks: DiskUsage[] | undefined) {
  return (disks ?? []).reduce<DiskUsage | undefined>(
    (worst, disk) => (!worst || disk.percent > worst.percent ? disk : worst),
    undefined,
  );
}

export function machineAlerts(metrics: SystemMetrics): MachineAlert[] {
  const alerts: MachineAlert[] = [];
  for (const disk of metrics.disks ?? []) {
    const severity = severityFor(disk.percent, ALERT_THRESHOLDS.disk);
    if (severity) alerts.push({ kind: "disk", severity, value: disk.percent, mount: disk.mount });
  }
  // One chip per kind: keep the worst disk only.
  const diskAlerts = alerts.splice(0);
  if (diskAlerts.length) {
    alerts.push(diskAlerts.reduce((worst, alert) => (alert.value > worst.value ? alert : worst)));
  }
  if (metrics.memory) {
    const severity = severityFor(metrics.memory.percent, ALERT_THRESHOLDS.memory);
    if (severity) alerts.push({ kind: "memory", severity, value: metrics.memory.percent });
  }
  const cores = metrics.host?.cpu_cores ?? 0;
  const load = metrics.cpu?.load?.one;
  if (cores > 0 && typeof load === "number") {
    const perCore = load / cores;
    const severity = severityFor(perCore, ALERT_THRESHOLDS.loadPerCore);
    if (severity) alerts.push({ kind: "load", severity, value: perCore });
  }
  const cpu = recentAverage(metrics.history ?? [], "cpu", CPU_ALERT_WINDOW) ?? metrics.cpu?.percent;
  if (typeof cpu === "number") {
    const severity = severityFor(cpu, ALERT_THRESHOLDS.cpu);
    if (severity) alerts.push({ kind: "cpu", severity, value: cpu });
  }
  return alerts.sort((left, right) =>
    Number(right.severity === "critical") - Number(left.severity === "critical")
    || ALERT_ORDER.indexOf(left.kind) - ALERT_ORDER.indexOf(right.kind));
}

function worstSeverity(alerts: MachineAlert[]): AlertSeverity | undefined {
  if (alerts.some((alert) => alert.severity === "critical")) return "critical";
  return alerts.length ? "warning" : undefined;
}

export function machineStatuses(batch: FleetBatchResult<unknown>): MachineStatus[] {
  return batch.targets.map(({ target, responses }) => {
    const response = responses.find((item) => item.key === MACHINE_STATUS_KEY);
    const metrics = response?.ok ? response.data as SystemMetrics | undefined : undefined;
    if (metrics && typeof metrics === "object" && metrics.host) {
      if (!metrics.supported) return { target, state: "unsupported", metrics, alerts: [] };
      const alerts = machineAlerts(metrics);
      return { target, state: "online", metrics, alerts, severity: worstSeverity(alerts) };
    }
    if (response?.status === 404) {
      return { target, state: "outdated", error: response.error, alerts: [] };
    }
    return offlineStatus(target, response?.error || target.error || "unavailable");
  });
}

export const LOCAL_MACHINE_TARGET: MachineTarget = {
  id: "local", name: "Local machine", kind: "local", trusted: true, online: true,
};

/**
 * Lists the machines to monitor for a machine scope without contacting them,
 * so each one can be polled on its own and a slow SSH host never holds back
 * the rest. "all" mirrors the fleet API: local plus every trusted SSH host.
 */
export function machineStatusTargets(scope: string, hosts: RemoteHost[]): MachineTarget[] {
  const remote = (host: RemoteHost): MachineTarget => ({
    id: host.id, name: host.name || host.host, kind: "ssh", trusted: Boolean(host.trusted), online: false,
  });
  if (scope === "all") return [LOCAL_MACHINE_TARGET, ...hosts.filter((host) => host.trusted).map(remote)];
  if (scope === "local" || !scope) return [LOCAL_MACHINE_TARGET];
  const host = hosts.find((item) => item.id === scope);
  return [host ? remote(host) : { id: scope, name: scope, kind: "ssh", trusted: false, online: false }];
}

export function loadingStatus(target: MachineTarget): MachineStatus {
  return { target, state: "loading", alerts: [] };
}

export function offlineStatus(target: MachineTarget, error: string): MachineStatus {
  return { target, state: "offline", error, alerts: [] };
}

/** The APFS data volume is what users know as their Mac's disk. */
export function isSystemDisk(mount: string | undefined) {
  return !mount || mount === "/" || mount === "/System/Volumes/Data";
}

/**
 * Some macOS network drivers never advance the interface's received-byte
 * counter (netstat reports the same zero). TCP uploads always receive ACKs,
 * so a download rate pinned at exactly zero while uploads flow means the
 * counter is unavailable, not that nothing arrived.
 */
export function downloadCounterStalled(history: MetricPoint[]) {
  const samples = history.filter((point) => typeof point.rx === "number" && typeof point.tx === "number");
  if (samples.length < 3) return false;
  const averageUpload = samples.reduce((sum, point) => sum + (point.tx ?? 0), 0) / samples.length;
  return samples.every((point) => point.rx === 0) && averageUpload >= 1024;
}

export function summarizeFleet(statuses: MachineStatus[]): FleetStatusSummary {
  return statuses.reduce<FleetStatusSummary>((summary, status) => ({
    total: summary.total + 1,
    reachable: summary.reachable + Number(status.state !== "offline" && status.state !== "loading"),
    attention: summary.attention + Number(Boolean(status.severity)),
    critical: summary.critical + Number(status.severity === "critical"),
    needsInput: summary.needsInput + (status.metrics?.sessions?.needs_input ?? 0),
  }), { total: 0, reachable: 0, attention: 0, critical: 0, needsInput: 0 });
}

const BYTE_UNITS = ["B", "KB", "MB", "GB", "TB", "PB"];

/**
 * Binary magnitudes with three significant digits ("8.20 GB", "243 KB"),
 * matching Activity Monitor and htop.
 */
export function formatBytes(value: number | undefined) {
  if (typeof value !== "number" || !Number.isFinite(value) || value < 0) return "—";
  let unit = 0;
  let scaled = value;
  while (scaled >= 1000 && unit < BYTE_UNITS.length - 1) {
    scaled /= 1024;
    unit += 1;
  }
  const digits = unit === 0 || scaled >= 100 ? 0 : scaled >= 10 ? 1 : 2;
  return `${scaled.toFixed(digits)} ${BYTE_UNITS[unit]}`;
}

export function formatRate(value: number | undefined) {
  if (typeof value !== "number" || !Number.isFinite(value)) return "—";
  return `${formatBytes(value < 1 ? 0 : value)}/s`;
}

export function formatPercent(value: number | undefined) {
  if (typeof value !== "number" || !Number.isFinite(value)) return "—";
  if (value > 0 && value < 10) return `${value.toFixed(1)}%`;
  return `${Math.round(value)}%`;
}

export function formatLoad(value: number | undefined) {
  if (typeof value !== "number" || !Number.isFinite(value)) return "—";
  return value >= 10 ? value.toFixed(1) : value.toFixed(2);
}

export function formatUptime(bootTime: number | undefined, now = Date.now()) {
  if (!bootTime) return null;
  const minutes = Math.max(0, Math.floor((now / 1000 - bootTime) / 60));
  return { days: Math.floor(minutes / 1440), hours: Math.floor((minutes % 1440) / 60), minutes: minutes % 60 };
}

export type SparklineGeometry = { line: string; area: string };

/**
 * Builds SVG path data for a sparkline in a width x height box. Missing
 * values break the line instead of being drawn as zero. max fixes the top of
 * the scale (100 for percentages); otherwise the series' own peak is used.
 */
export function sparklinePaths(
  values: Array<number | undefined>,
  width: number,
  height: number,
  options: { max?: number; slots?: number } = {},
): SparklineGeometry {
  const slots = Math.max(options.slots ?? values.length, values.length, 2);
  const peak = options.max ?? Math.max(0, ...values.filter((value): value is number => typeof value === "number"));
  const scale = peak > 0 ? peak : 1;
  // Right-align the series so a fresh history grows in from the right edge.
  const offset = slots - values.length;
  const x = (index: number) => ((offset + index) / (slots - 1)) * width;
  const y = (value: number) => height - Math.min(1, Math.max(0, value / scale)) * (height - 1) - 0.5;
  const line: string[] = [];
  const area: string[] = [];
  let run: Array<[number, number]> = [];
  const flush = () => {
    if (!run.length) return;
    if (run.length === 1) run = [[run[0][0] - 1, run[0][1]], [run[0][0], run[0][1]]];
    line.push(run.map(([px, py], index) => `${index ? "L" : "M"}${px.toFixed(1)} ${py.toFixed(1)}`).join(" "));
    area.push(`M${run[0][0].toFixed(1)} ${height} ${run.map(([px, py]) => `L${px.toFixed(1)} ${py.toFixed(1)}`).join(" ")} L${run[run.length - 1][0].toFixed(1)} ${height} Z`);
    run = [];
  };
  values.forEach((value, index) => {
    if (typeof value === "number" && Number.isFinite(value)) run.push([x(index), y(value)]);
    else flush();
  });
  flush();
  return { line: line.join(" "), area: area.join(" ") };
}
