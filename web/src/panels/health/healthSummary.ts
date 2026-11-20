import type { Channel, CursorUsageSourceStatus, FleetMachine, FrameworkSmokeResult, TargetMetadata } from "../../api";

export type HealthLevel = "healthy" | "warning" | "empty";

export type HealthSummary = {
  total: number;
  healthy: number;
  issues: number;
  inactive: number;
  pending: number;
  issueLabels: string[];
  level: HealthLevel;
};

const HEALTHY_CHANNEL_STATES = new Set(["running", "connected"]);

function healthLabel(name: string, targetName?: string) {
  return targetName ? `${name} · ${targetName}` : name;
}

export function summarizeChannelHealth(channels: Channel[]): HealthSummary {
  let healthy = 0;
  let issues = 0;
  let inactive = 0;
  const issueLabels: string[] = [];

  for (const channel of channels) {
    if (!channel.enabled) {
      inactive += 1;
      continue;
    }
    const state = (channel.state ?? "").trim().toLowerCase();
    const connected = channel.connected === true || HEALTHY_CHANNEL_STATES.has(state);
    if (connected && !channel.error) {
      healthy += 1;
      continue;
    }
    issues += 1;
    issueLabels.push(healthLabel(channel.bot_name || channel.name, channel.target_name));
  }

  return {
    total: channels.length,
    healthy,
    issues,
    inactive,
    pending: 0,
    issueLabels,
    level: channels.length === 0 ? "empty" : issues > 0 ? "warning" : "healthy",
  };
}

// summarizeFrameworkHealth counts each framework by its most recent test, on
// each machine: whatever model it was tested with.
export function summarizeFrameworkHealth(results: (FrameworkSmokeResult & TargetMetadata)[], displayName: (kind: string) => string = (kind) => kind): HealthSummary {
  const latest = new Map<string, FrameworkSmokeResult & TargetMetadata>();
  for (const result of results) {
    const key = `${result.target_id || "local"}::${result.kind}`;
    const current = latest.get(key);
    if (!current || Date.parse(result.checked_at) > Date.parse(current.checked_at)) latest.set(key, result);
  }
  let healthy = 0;
  const issueLabels: string[] = [];
  for (const result of latest.values()) {
    if (result.ok) healthy += 1;
    else issueLabels.push(healthLabel(displayName(result.kind), result.target_name));
  }
  return {
    total: latest.size,
    healthy,
    issues: issueLabels.length,
    inactive: 0,
    pending: 0,
    issueLabels,
    level: latest.size === 0 ? "empty" : issueLabels.length > 0 ? "warning" : "healthy",
  };
}

const BROKEN_SOURCE_STATES = new Set(["error", "conflict", "drift", "unavailable"]);

// usageSourceIssue is why a connected usage source is not collecting, or ""
// when it is. A source the operator never connected is not a fault.
export function usageSourceIssue(source: CursorUsageSourceStatus): string {
  if (!source.connected) return "";
  if (source.last_error) return source.last_error;
  for (const state of [source.cloud_status, source.local_status, source.hook?.status]) {
    if (BROKEN_SOURCE_STATES.has((state ?? "").trim().toLowerCase())) return state;
  }
  return "";
}

export function summarizeUsageSources(sources: (CursorUsageSourceStatus & TargetMetadata)[]): HealthSummary {
  const connected = sources.filter((source) => source.connected);
  const issueLabels = connected.filter((source) => usageSourceIssue(source)).map((source) => healthLabel("Cursor", source.target_name));
  return {
    total: connected.length,
    healthy: connected.length - issueLabels.length,
    issues: issueLabels.length,
    inactive: sources.length - connected.length,
    pending: 0,
    issueLabels,
    level: connected.length === 0 ? "empty" : issueLabels.length > 0 ? "warning" : "healthy",
  };
}

export function summarizeMachines(machines: FleetMachine[]): HealthSummary {
  const issueLabels = machines.filter((machine) => !machine.online).map((machine) => machine.name || machine.id);
  return {
    total: machines.length,
    healthy: machines.length - issueLabels.length,
    issues: issueLabels.length,
    inactive: 0,
    pending: 0,
    issueLabels,
    level: machines.length === 0 ? "empty" : issueLabels.length > 0 ? "warning" : "healthy",
  };
}

// latestFrameworkResults keeps each framework's most recent test per machine,
// failures first.
export function latestFrameworkResults<T extends FrameworkSmokeResult & TargetMetadata>(results: T[]): T[] {
  const latest = new Map<string, T>();
  for (const result of results) {
    const key = `${result.target_id || "local"}::${result.kind}`;
    const current = latest.get(key);
    if (!current || Date.parse(result.checked_at) > Date.parse(current.checked_at)) latest.set(key, result);
  }
  return [...latest.values()].sort((left, right) => Number(left.ok) - Number(right.ok) || left.kind.localeCompare(right.kind));
}

export function channelIsHealthy(channel: Channel): boolean {
  const state = (channel.state ?? "").trim().toLowerCase();
  return (channel.connected === true || HEALTHY_CHANNEL_STATES.has(state)) && !channel.error;
}
