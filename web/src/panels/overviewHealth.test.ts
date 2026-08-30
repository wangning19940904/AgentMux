import { describe, expect, it } from "vitest";
import type { Channel, ProviderMonitorSnapshot } from "../api";
import { summarizeChannelHealth, summarizeModelHealth } from "./overviewHealth";

describe("overview health summaries", () => {
  it("separates connected, unhealthy, and disabled channels", () => {
    const channels = [
      { id: "ok", name: "support", type: "feishu", enabled: true, state: "running", connected: true },
      { id: "bad", name: "alerts", bot_name: "Alert Bot", type: "slack", enabled: true, state: "reconnecting", target_name: "edge" },
      { id: "off", name: "archive", type: "discord", enabled: false },
    ] satisfies Channel[];

    expect(summarizeChannelHealth(channels)).toMatchObject({
      total: 3,
      healthy: 1,
      issues: 1,
      inactive: 1,
      issueLabels: ["Alert Bot · edge"],
      level: "warning",
    });
  });

  it("treats an enabled channel with an explicit error as unhealthy", () => {
    const channels = [
      { id: "bad", name: "bot", type: "feishu", enabled: true, state: "running", connected: true, error: "socket closed" },
    ] satisfies Channel[];
    expect(summarizeChannelHealth(channels).issues).toBe(1);
  });

  it("summarizes provider monitor states and unchecked providers", () => {
    const monitor = {
      config: { enabled: true, interval_minutes: 60, probe_models: true, max_models_per_provider: 10 },
      running: false,
      providers: [
        { provider_id: "ok", provider_name: "OpenAI", state: "healthy", catalog_count: 2, checked_models: 2, healthy_models: 2, unhealthy_models: 0, last_checked_at: "2026-08-30T00:00:00Z" },
        { provider_id: "bad", provider_name: "Relay", state: "warning", catalog_count: 2, checked_models: 2, healthy_models: 1, unhealthy_models: 1, last_checked_at: "2026-08-30T00:00:00Z", target_name: "remote" },
      ],
      alerts: [],
    } satisfies ProviderMonitorSnapshot;

    expect(summarizeModelHealth(monitor, 3)).toMatchObject({
      total: 3,
      healthy: 1,
      issues: 1,
      pending: 1,
      issueLabels: ["Relay · remote"],
      level: "warning",
    });
  });

  it("reports a fully healthy model service summary", () => {
    const monitor = {
      config: { enabled: true, interval_minutes: 60, probe_models: true, max_models_per_provider: 10 },
      running: false,
      providers: [
        { provider_id: "ok", provider_name: "OpenAI", state: "healthy", catalog_count: 1, checked_models: 1, healthy_models: 1, unhealthy_models: 0, last_checked_at: "2026-08-30T00:00:00Z" },
      ],
      alerts: [],
    } satisfies ProviderMonitorSnapshot;
    expect(summarizeModelHealth(monitor, 1).level).toBe("healthy");
  });
});
