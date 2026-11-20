import { beforeEach, describe, expect, it } from "vitest";
import type { UsageBucket, UsageReport } from "../../api";
import { emptyTotals } from "./format";
import {
  OTHER_SLOT, assignSlots, bucketStartDate, buildTrendSeries, niceCeiling, resetSeriesSlots, stackTotals, timelineFor,
  type TrendLabels,
} from "./usageTrend";

const labels: TrendLabels = {
  total: "Total", other: "Other", unknownModel: "Unknown model", unattributed: "No breakdown",
  runtime: (id) => `fw:${id}`, machine: (id, name) => (id === "local" ? "This machine" : name),
};

function bucket(key: string, tokens: number, models: Record<string, number> = {}, runtimes: Record<string, number> = {}): UsageBucket {
  return {
    key,
    totals: { ...emptyTotals(), input_tokens: tokens },
    by_model: Object.entries(models).map(([model, value]) => ({ model, tokens: value, cost_usd: 0 })),
    by_runtime: Object.entries(runtimes).map(([runtime, value]) => ({ runtime, tokens: value, cost_usd: 0 })),
  };
}

function report(buckets: UsageBucket[], extra: Partial<UsageReport> = {}): UsageReport {
  return { period: "daily", totals: emptyTotals(), buckets, by_model: [], by_source: [], ...extra };
}

beforeEach(() => resetSeriesSlots());

describe("usage trend series", () => {
  const keys = ["2026-03-01", "2026-03-02", "2026-03-03"];

  it("fills buckets without usage with zero", () => {
    const series = buildTrendSeries("total", report([bucket("2026-03-01", 10), bucket("2026-03-03", 5)]), keys, "daily", labels);
    expect(series).toEqual([{ id: "total", label: "Total", tokens: 15, values: [10, 0, 5], slot: 0 }]);
    expect(buildTrendSeries("total", report([]), keys, "daily", labels)).toEqual([]);
  });

  it("splits by model, names the unknown model and folds the tail into Other", () => {
    const models = { a: 60, b: 50, c: 40, d: 30, e: 20, f: 6, "": 4 };
    const series = buildTrendSeries("model", report([bucket("2026-03-02", 210, models)]), keys, "daily", labels);
    expect(series.map((item) => [item.label, item.tokens])).toEqual([["a", 60], ["b", 50], ["c", 40], ["d", 30], ["e", 20], ["Other", 10]]);
    expect(series[series.length - 1].slot).toBe(OTHER_SLOT);
    expect(stackTotals(series, keys.length)).toEqual([0, 210, 0]);
    const unknown = buildTrendSeries("model", report([bucket("2026-03-02", 4, { "": 4 })]), keys, "daily", labels);
    expect(unknown[0].label).toBe("Unknown model");
  });

  it("keeps usage without a breakdown visible instead of dropping it", () => {
    const series = buildTrendSeries("framework", report([bucket("2026-03-01", 100, {}, { codex: 70 })]), keys, "daily", labels);
    expect(series.map((item) => [item.label, item.values[0]])).toEqual([["fw:codex", 70], ["No breakdown", 30]]);
  });

  it("draws one series per machine and spreads a whole-day answer over its hours", () => {
    const hours = ["2026-03-01 00:00", "2026-03-01 01:00"];
    const usage = report([bucket("2026-03-01 00:00", 10)], {
      by_machine: [
        { target_id: "local", target_name: "Local machine", totals: emptyTotals(), buckets: [bucket("2026-03-01 00:00", 10)] },
        { target_id: "old", target_name: "Old build", totals: emptyTotals(), buckets: [bucket("2026-03-01", 48)] },
      ],
    });
    const series = buildTrendSeries("machine", usage, hours, "hourly", labels);
    expect(series.map((item) => [item.label, item.values])).toEqual([["This machine", [10, 0]], ["Old build", [2, 2]]]);
  });

  it("keeps a series' colour when the ranking changes", () => {
    expect(assignSlots("model", ["a", "b", "c"])).toEqual([0, 1, 2]);
    expect(assignSlots("model", ["c", "a"])).toEqual([2, 0]);
    expect(assignSlots("model", ["d", "c"])).toEqual([0, 2]);
    // Another dimension has its own colours.
    expect(assignSlots("framework", ["c"])).toEqual([0]);
  });
});

describe("usage trend axis", () => {
  it("spans all time from the first bucket with usage", () => {
    const now = new Date(2026, 2, 20, 12);
    expect(timelineFor(report([bucket("2026-W10", 1)]), { from: "", to: "" }, "weekly", now)).toEqual(["2026-W10", "2026-W11", "2026-W12"]);
    expect(timelineFor(report([]), { from: "", to: "" }, "weekly", now)).toEqual([]);
    expect(timelineFor(null, { from: "2026-03-19", to: "2026-03-20" }, "daily", now)).toEqual(["2026-03-19", "2026-03-20"]);
  });

  it("finds the first date of a bucket", () => {
    expect(bucketStartDate("2026-W01")).toBe("2025-12-29");
    expect(bucketStartDate("2026-W10")).toBe("2026-03-02");
    expect(bucketStartDate("2026-03")).toBe("2026-03-01");
    expect(bucketStartDate("2026-03-05 13:00")).toBe("2026-03-05");
    expect(bucketStartDate("session-1")).toBeNull();
  });

  it("rounds the axis maximum up to a clean step", () => {
    expect(niceCeiling(0)).toBe(1);
    expect(niceCeiling(7)).toBe(8);
    expect(niceCeiling(1_200_000)).toBe(1_200_000);
    expect(niceCeiling(551_000_000)).toBe(600_000_000);
    expect(niceCeiling(2_300_000)).toBe(2_500_000);
    expect(niceCeiling(500)).toBe(500);
  });
});
