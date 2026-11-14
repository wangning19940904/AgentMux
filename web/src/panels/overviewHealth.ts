import type { Channel, FrameworkSmokeResult, TargetMetadata } from "../api";

export type OverviewHealthLevel = "healthy" | "warning" | "empty";

export type OverviewHealthSummary = {
  total: number;
  healthy: number;
  issues: number;
  inactive: number;
  pending: number;
  issueLabels: string[];
  level: OverviewHealthLevel;
};

const HEALTHY_CHANNEL_STATES = new Set(["running", "connected"]);

function healthLabel(name: string, targetName?: string) {
  return targetName ? `${name} · ${targetName}` : name;
}

export function summarizeChannelHealth(channels: Channel[]): OverviewHealthSummary {
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
export function summarizeFrameworkHealth(results: (FrameworkSmokeResult & TargetMetadata)[], displayName: (kind: string) => string = (kind) => kind): OverviewHealthSummary {
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
