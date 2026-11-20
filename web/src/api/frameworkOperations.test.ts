import { afterEach, describe, expect, it, vi } from "vitest";

const fleetExecute = vi.fn();
const fleetMonitorQuery = vi.fn();
vi.mock("./client", () => ({
  fleetExecute: (...args: unknown[]) => fleetExecute(...args),
  fleetMonitorQuery: (...args: unknown[]) => fleetMonitorQuery(...args),
}));

const { FrameworkOperationsUnsupported, followFrameworkOperationJob, startFrameworkOperationJob } = await import("./frameworkOperations");

const target = { id: "home", name: "home-macmini" };
const batch = (response: Record<string, unknown>) => ({ targets: [{ target, responses: [{ key: "operation", duration_ms: 1, ...response }] }] });
const job = (state: string, extra: Record<string, unknown> = {}) => ({
  id: "fwop-1", kind: "codex", action: "update", state,
  progress: { phase: "updating", detail: "line", percent: 30 },
  started_at: "2026-11-19T12:00:00Z", updated_at: "2026-11-19T12:00:01Z", ...extra,
});

afterEach(() => {
  vi.useRealTimers();
  fleetExecute.mockReset();
  fleetMonitorQuery.mockReset();
});

describe("framework operation jobs", () => {
  it("treats a machine without the route as unsupported so callers can fall back", async () => {
    fleetExecute.mockResolvedValueOnce(batch({ status: 404, ok: false, error: "404 page not found" }));
    await expect(startFrameworkOperationJob({ kind: "codex", action: "update" }, "home")).rejects.toBeInstanceOf(FrameworkOperationsUnsupported);
    fleetExecute.mockResolvedValueOnce(batch({ status: 502, ok: false, error: "target returned an empty or invalid JSON response" }));
    await expect(startFrameworkOperationJob({ kind: "codex", action: "update" }, "home")).rejects.toBeInstanceOf(FrameworkOperationsUnsupported);
    fleetExecute.mockResolvedValueOnce(batch({ status: 409, ok: false, error: "codex already has an uninstall running" }));
    await expect(startFrameworkOperationJob({ kind: "codex", action: "update" }, "home")).rejects.toThrow("home-macmini: codex already has an uninstall running");
  });

  it("polls until the operation settles, reporting output and riding out a missed poll", async () => {
    vi.useFakeTimers();
    fleetMonitorQuery
      .mockResolvedValueOnce(batch({ status: 200, ok: true, data: job("running", { log: "==> Fetching\n" }) }))
      .mockResolvedValueOnce(batch({ status: 504, ok: false, error: "relay timeout" }))
      .mockResolvedValueOnce(batch({ status: 200, ok: true, data: job("succeeded", { result: { kind: "codex", action: "update", ok: true, warning: "brew exited 1" } }) }));
    const seen: Array<{ detail?: string; log?: string }> = [];
    const result = followFrameworkOperationJob(job("running") as never, "home", (progress) => seen.push(progress));
    await vi.runAllTimersAsync();
    await expect(result).resolves.toMatchObject({ ok: true, warning: "brew exited 1" });
    expect(fleetMonitorQuery.mock.calls[0][0][0].path).toBe("/api/v1/frameworks/operations?id=fwop-1");
    expect(seen.some((progress) => progress.log === "==> Fetching\n")).toBe(true);
    expect(seen.some((progress) => progress.detail?.includes("relay timeout"))).toBe(true);
  });

  it("explains an operation the machine forgot", async () => {
    vi.useFakeTimers();
    fleetMonitorQuery.mockResolvedValueOnce(batch({ status: 404, ok: false, error: "framework operation not found" }));
    const result = followFrameworkOperationJob(job("running") as never, "home");
    const settled = expect(result).rejects.toThrow("AgentMux restarted");
    await vi.runAllTimersAsync();
    await settled;
  });
});
