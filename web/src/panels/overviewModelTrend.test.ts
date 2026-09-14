import { describe, expect, it } from "vitest";
import type { UsageBucket } from "../api";
import { buildModelTrendSeries } from "./overviewModelTrend";

const t = (key: string) => key;
function bucket(tokens: number, models?: Array<[string, number]>): UsageBucket {
  return {
    key: "2026-09-14 10:00",
    totals: { input_tokens: tokens, output_tokens: 0, cache_read_tokens: 0, cache_write_tokens: 0, cost_usd: 0, records: 0, sessions: 0, estimated_tokens: 0, estimated_records: 0 },
    by_model: models?.map(([model, tokens]) => ({ model, tokens, cost_usd: 0 })),
  };
}

describe("model usage trend", () => {
  it("uses each model's hourly usage, fills gaps and orders by total tokens", () => {
    const series = buildModelTrendSeries([
      bucket(130, [["b", 20], ["a", 110]]),
      bucket(80, [["b", 80]]),
      bucket(30, [["a", 30]]),
    ], t);
    expect(series).toEqual([
      { id: "model:a", label: "a", tokens: 140, values: [110, 0, 30] },
      { id: "model:b", label: "b", tokens: 100, values: [20, 80, 0] },
    ]);
  });

  it("groups models beyond the top five without losing hourly usage", () => {
    const series = buildModelTrendSeries([
      bucket(280, [["a", 70], ["b", 60], ["c", 50], ["d", 40], ["e", 30], ["f", 20], ["g", 10]]),
      bucket(8, [["f", 5], ["g", 3]]),
    ], t);
    expect(series.map((item) => item.id)).toEqual(["model:a", "model:b", "model:c", "model:d", "model:e", "other-models"]);
    expect(series[5]).toEqual({ id: "other-models", label: "overview.trend.other", tokens: 38, values: [30, 8] });
    expect([0, 1].map((index) => series.reduce((sum, item) => sum + item.values[index], 0))).toEqual([280, 8]);
  });

  it("shows missing breakdowns from older machines separately from an unknown model", () => {
    const series = buildModelTrendSeries([bucket(150, [["a", 100], ["", 10]]), bucket(60)], t);
    expect(series.find((item) => item.id === "unattributed-model")).toEqual({
      id: "unattributed-model", label: "overview.trend.unattributedModel", tokens: 100, values: [40, 60],
    });
    expect(series.find((item) => item.id === "model:")?.label).toBe("overview.unknownModel");
    expect(series.reduce((sum, item) => sum + item.tokens, 0)).toBe(210);
  });

  it("omits empty series and ignores rounding residue from daily buckets", () => {
    expect(buildModelTrendSeries([], t)).toEqual([]);
    expect(buildModelTrendSeries([bucket(0, [["unused", 0]])], t)).toEqual([]);
    const series = buildModelTrendSeries([bucket(0.1 + 0.2, [["a", 0.3]])], t);
    expect(series.map((item) => item.id)).toEqual(["model:a"]);
  });
});
