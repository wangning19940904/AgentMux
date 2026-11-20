import { beforeEach, describe, expect, it, vi } from "vitest";
import type { FleetBatchResult, UsageReport } from "../../api";
import { emptyTotals } from "./format";
import { loadUsage, type UsagePartial, type UsageRequest } from "./usageData";

const scope = vi.hoisted(() => ({ value: "all" }));

vi.mock("../../api", async () => {
  const actual = await vi.importActual<typeof import("../../api")>("../../api");
  return {
    ...actual,
    activeMachineScope: () => scope.value,
    api: { usage: vi.fn(), usageTargets: vi.fn(), usageFromTarget: vi.fn() },
  };
});

const { api } = await import("../../api");

function report(tokens: number): UsageReport {
  return {
    period: "daily",
    totals: { ...emptyTotals(), input_tokens: tokens },
    buckets: [{ key: "2026-03-01", totals: { ...emptyTotals(), input_tokens: tokens } }],
    by_model: [{ model: "m", tokens, cost_usd: 0 }], by_source: [],
    facets: { models: [{ value: "m", tokens }], runtimes: [], agents: [] },
  };
}

function answer(id: string, name: string, data?: UsageReport, error?: string): FleetBatchResult<UsageReport> {
  return {
    targets: [{
      target: { id, name, kind: id === "local" ? "local" : "link", trusted: true, online: true },
      responses: [{ key: "usage", status: data ? 200 : 502, ok: Boolean(data), data, error, duration_ms: 1 }],
    }],
  };
}

function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (error: unknown) => void;
  const promise = new Promise<T>((res, rej) => { resolve = res; reject = rej; });
  return { promise, resolve, reject };
}

const request: UsageRequest = { period: "daily", from: "2026-03-01", to: "2026-03-01", filter: { models: ["m"] }, machines: [] };

beforeEach(() => {
  vi.resetAllMocks();
  scope.value = "all";
});

describe("loading usage machine by machine", () => {
  it("fills the report in as machines answer and does not wait for a slow one to show the rest", async () => {
    vi.mocked(api.usageTargets).mockResolvedValue([{ id: "local", name: "Local machine" }, { id: "slow", name: "Slow" }, { id: "fast", name: "Fast" }]);
    const slow = deferred<FleetBatchResult<UsageReport>>();
    vi.mocked(api.usageFromTarget).mockImplementation((_path, target) => {
      if (target === "slow") return slow.promise;
      return Promise.resolve(answer(target, target === "local" ? "Local machine" : "Fast", report(target === "local" ? 10 : 5)));
    });
    const partials: UsagePartial[] = [];
    const loading = loadUsage(request, (partial) => partials.push(partial));
    await vi.waitFor(() => expect(partials).toHaveLength(2));
    // Two machines are in; the slow one is named as still pending.
    expect(partials[1].report.totals.input_tokens).toBe(15);
    expect(partials[1].pending.map((target) => target.id)).toEqual(["slow"]);
    slow.resolve(answer("slow", "Slow", report(100)));
    const result = await loading;
    expect(result.report.totals.input_tokens).toBe(115);
    // Machines keep the order of the machine list, whoever answered first.
    expect(result.report.by_machine?.map((machine) => machine.target_id)).toEqual(["local", "slow", "fast"]);
    expect(result.targets).toHaveLength(3);
    // Each machine is asked on its own, with the filter in the request.
    expect(vi.mocked(api.usageFromTarget).mock.calls.map(([path, target]) => [path.includes("model=m"), target]))
      .toEqual([[true, "local"], [true, "slow"], [true, "fast"]]);
  });

  it("reports a machine that fails as a warning and keeps the others", async () => {
    vi.mocked(api.usageTargets).mockResolvedValue([{ id: "local", name: "Local machine" }, { id: "down", name: "Down" }, { id: "broken", name: "Broken" }]);
    vi.mocked(api.usageFromTarget).mockImplementation((_path, target) => {
      if (target === "down") return Promise.resolve(answer("down", "Down", undefined, "device is offline"));
      if (target === "broken") return Promise.reject(new Error("Connection lost"));
      return Promise.resolve(answer("local", "Local machine", report(10)));
    });
    const result = await loadUsage(request);
    expect(result.report.totals.input_tokens).toBe(10);
    expect(result.report.warnings).toEqual(["Down: device is offline", "Broken: Connection lost"]);
  });

  it("leaves out a machine whose build cannot filter instead of adding its unfiltered usage", async () => {
    vi.mocked(api.usageTargets).mockResolvedValue([{ id: "local", name: "Local machine" }, { id: "old", name: "Old build" }]);
    vi.mocked(api.usageFromTarget).mockImplementation((_path, target) => Promise.resolve(target === "old"
      // No facets: this build predates filters and has ignored the request's.
      ? answer("old", "Old build", { ...report(1000), facets: undefined })
      : answer("local", "Local machine", { ...report(10), facets: { models: [], runtimes: [], agents: [] } })));
    const filtered = await loadUsage(request);
    expect(filtered.report.totals.input_tokens).toBe(10);
    expect(filtered.report.warnings?.[0]).toContain("Old build: runs an older AgentMux that cannot filter usage");
    // Without a filter its answer is complete and counts.
    const unfiltered = await loadUsage({ ...request, filter: {} });
    expect(unfiltered.report.totals.input_tokens).toBe(1010);
  });

  it("reads only the selected machines", async () => {
    vi.mocked(api.usageTargets).mockResolvedValue([{ id: "local", name: "Local machine" }, { id: "other", name: "Other" }]);
    vi.mocked(api.usageFromTarget).mockResolvedValue(answer("other", "Other", report(7)));
    const result = await loadUsage({ ...request, machines: ["other"] });
    expect(vi.mocked(api.usageFromTarget).mock.calls.map(([, target]) => target)).toEqual(["other"]);
    expect(result.report.totals.input_tokens).toBe(7);
    // The machine list still offers every machine as a filter choice.
    expect(result.targets.map((target) => target.id)).toEqual(["local", "other"]);
  });

  it("fails when no machine answers with data", async () => {
    vi.mocked(api.usageTargets).mockResolvedValue([{ id: "local", name: "Local machine" }]);
    vi.mocked(api.usageFromTarget).mockResolvedValue(answer("local", "Local machine", undefined, "database unavailable"));
    await expect(loadUsage(request)).rejects.toThrow("database unavailable");
  });

  it("falls back to one fleet-wide read when the machine list cannot be read", async () => {
    vi.mocked(api.usageTargets).mockRejectedValue(new Error("403"));
    vi.mocked(api.usage).mockResolvedValue(report(3));
    const result = await loadUsage(request);
    expect(result.report.totals.input_tokens).toBe(3);
    expect(api.usage).toHaveBeenCalledWith("daily", "2026-03-01", "2026-03-01", { models: ["m"] });
  });

  it("asks only this machine outside the fleet view", async () => {
    scope.value = "local";
    vi.mocked(api.usage).mockResolvedValue(report(4));
    const result = await loadUsage(request);
    expect(result.report.totals.input_tokens).toBe(4);
    expect(api.usageTargets).not.toHaveBeenCalled();
  });
});
