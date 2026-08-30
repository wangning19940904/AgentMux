import type { Channel, ProviderMonitorSnapshot } from "../api";

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

export function summarizeModelHealth(
  monitor: ProviderMonitorSnapshot | null | undefined,
  configuredProviders: number,
): OverviewHealthSummary {
  const statuses = monitor?.providers ?? [];
  let healthy = 0;
  let issues = 0;
  let pending = 0;
  const issueLabels: string[] = [];

  for (const provider of statuses) {
    const state = provider.state.trim().toLowerCase();
    if (state === "healthy" && provider.unhealthy_models === 0) {
      healthy += 1;
    } else if (state === "checking") {
      pending += 1;
    } else {
      issues += 1;
      issueLabels.push(healthLabel(provider.provider_name, provider.target_name));
    }
  }

  pending += Math.max(0, configuredProviders - statuses.length);
  const total = Math.max(configuredProviders, statuses.length);
  return {
    total,
    healthy,
    issues,
    inactive: 0,
    pending,
    issueLabels,
    level: total === 0 ? "empty" : issues > 0 || pending > 0 ? "warning" : "healthy",
  };
}
