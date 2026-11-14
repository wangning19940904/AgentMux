import { describe, expect, it } from "vitest";
import type { Channel } from "../api";
import { summarizeChannelHealth, summarizeFrameworkHealth } from "./overviewHealth";

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
});

describe("summarizeFrameworkHealth", () => {
  it("counts each framework on each machine by its most recent test", () => {
    const at = (minute: number) => `2026-11-14T03:${String(minute).padStart(2, "0")}:00Z`;
    const summary = summarizeFrameworkHealth([
      { kind: "codex", model: "gpt-6", ok: false, latency_ms: 0, checked_at: at(1), error_kind: "model_not_found" },
      { kind: "codex", model: "", ok: true, latency_ms: 900, checked_at: at(2) },
      { kind: "claudecode", ok: false, latency_ms: 0, checked_at: at(3), error_kind: "unauthenticated" },
      { kind: "codex", ok: false, latency_ms: 0, checked_at: at(4), target_id: "h1", target_name: "build box" },
    ]);
    expect(summary).toMatchObject({ total: 3, healthy: 1, issues: 2, level: "warning" });
    expect(summary.issueLabels).toEqual(["claudecode", "codex · build box"]);
    expect(summarizeFrameworkHealth([]).level).toBe("empty");
  });
});
