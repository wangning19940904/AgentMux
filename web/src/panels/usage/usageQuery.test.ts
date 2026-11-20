import { describe, expect, it } from "vitest";
import {
  DEFAULT_VIEW,
  bucketKeyFor,
  includesToday,
  rangeBounds,
  rangeDays,
  resolvePeriod,
  timelineKeys,
  toggleFilterValue,
  viewFromHashQuery,
  viewToHashQuery,
  type UsageView,
} from "./usageQuery";

const TODAY = "2026-03-01";

describe("usage range bounds", () => {
  it.each([
    ["today", "2026-03-01", "2026-03-01"],
    ["yesterday", "2026-02-28", "2026-02-28"],
    ["7d", "2026-02-23", "2026-03-01"],
    ["30d", "2026-01-31", "2026-03-01"],
    ["90d", "2025-12-02", "2026-03-01"],
    ["month", "2026-03-01", "2026-03-01"],
    ["lastMonth", "2026-02-01", "2026-02-28"],
    ["all", "", ""],
  ] as const)("resolves %s", (preset, from, to) => {
    expect(rangeBounds({ preset, from: "", to: "" }, TODAY)).toEqual({ from, to });
  });

  it("orders a custom range and falls back to today when a date is missing", () => {
    expect(rangeBounds({ preset: "custom", from: "2026-02-10", to: "2026-01-05" }, TODAY)).toEqual({ from: "2026-01-05", to: "2026-02-10" });
    expect(rangeBounds({ preset: "custom", from: "", to: "" }, TODAY)).toEqual({ from: TODAY, to: TODAY });
    expect(rangeBounds({ preset: "custom", from: "2026-02-30", to: "nonsense" }, TODAY)).toEqual({ from: TODAY, to: TODAY });
  });

  it("counts inclusive days across a daylight-saving change", () => {
    expect(rangeDays({ from: "2026-03-07", to: "2026-03-09" })).toBe(3);
    expect(rangeDays({ from: "", to: "" })).toBeNull();
  });

  it("knows whether a range can still change", () => {
    expect(includesToday({ from: "2026-02-01", to: "2026-02-28" }, TODAY)).toBe(false);
    expect(includesToday({ from: "2026-02-01", to: TODAY }, TODAY)).toBe(true);
    expect(includesToday({ from: "", to: "" }, TODAY)).toBe(true);
  });
});

describe("usage bucket size", () => {
  it("keeps the bucket count readable in auto mode", () => {
    const period = (from: string, to: string) => resolvePeriod("auto", { from, to });
    expect(period("2026-03-01", "2026-03-01")).toBe("hourly");
    expect(period("2026-02-23", "2026-03-01")).toBe("hourly");
    expect(period("2026-02-22", "2026-03-01")).toBe("daily");
    expect(period("2025-12-02", "2026-03-01")).toBe("daily");
    expect(period("2025-03-01", "2026-03-01")).toBe("weekly");
    expect(period("2022-03-01", "2026-03-01")).toBe("monthly");
    expect(period("", "")).toBe("weekly");
  });

  it("refuses hourly buckets on a range too long for them", () => {
    expect(resolvePeriod("hourly", { from: "2026-02-01", to: "2026-03-01" })).toBe("hourly");
    expect(resolvePeriod("hourly", { from: "2025-12-01", to: "2026-03-01" })).toBe("daily");
    expect(resolvePeriod("hourly", { from: "", to: "" })).toBe("daily");
    expect(resolvePeriod("monthly", { from: TODAY, to: TODAY })).toBe("monthly");
  });
});

describe("usage bucket keys", () => {
  it("labels ISO weeks as the server does, including the year boundary", () => {
    expect(bucketKeyFor(new Date(2026, 0, 1), "weekly")).toBe("2026-W01");
    expect(bucketKeyFor(new Date(2027, 0, 1), "weekly")).toBe("2026-W53");
    expect(bucketKeyFor(new Date(2024, 11, 30), "weekly")).toBe("2025-W01");
    expect(bucketKeyFor(new Date(2026, 10, 19, 13, 59), "hourly")).toBe("2026-11-19 13:00");
    expect(bucketKeyFor(new Date(2026, 10, 19), "monthly")).toBe("2026-11");
  });

  it("lays out every bucket of the range, stopping hourly ranges at the current hour", () => {
    const now = new Date(2026, 2, 1, 9, 30);
    expect(timelineKeys({ from: "2026-02-27", to: "2026-03-01" }, "daily", now)).toEqual(["2026-02-27", "2026-02-28", "2026-03-01"]);
    const hours = timelineKeys({ from: "2026-02-28", to: "2026-03-01" }, "hourly", now);
    expect(hours).toHaveLength(24 + 10);
    expect(hours[0]).toBe("2026-02-28 00:00");
    expect(hours[hours.length - 1]).toBe("2026-03-01 09:00");
    expect(timelineKeys({ from: "2026-02-28", to: "2026-02-28" }, "hourly", now)).toHaveLength(24);
    expect(timelineKeys({ from: "2025-12-29", to: "2026-01-11" }, "weekly", now)).toEqual(["2026-W01", "2026-W02"]);
    expect(timelineKeys({ from: "2025-11-15", to: "2026-01-02" }, "monthly", now)).toEqual(["2025-11", "2025-12", "2026-01"]);
    expect(timelineKeys({ from: "", to: "" }, "daily", now)).toEqual([]);
  });
});

describe("usage view in the hash route", () => {
  it("leaves the default view out of the route", () => {
    expect(viewToHashQuery(DEFAULT_VIEW)).toBe("");
    expect(viewFromHashQuery("")).toEqual(DEFAULT_VIEW);
  });

  it("round-trips a custom range with filters", () => {
    const view: UsageView = {
      preset: "custom", from: "2026-01-05", to: "2026-02-10", granularity: "weekly",
      filters: { machines: ["local", "host-1"], runtimes: ["codex-app"], models: ["vendor/gpt 6", ""], agents: ["agent:a&b"] },
    };
    expect(viewFromHashQuery(viewToHashQuery(view))).toEqual(view);
  });

  it("ignores values it does not understand", () => {
    expect(viewFromHashQuery("range=decade&by=minute")).toEqual(DEFAULT_VIEW);
    expect(viewFromHashQuery("range=custom&from=2026-02-30&to=2026-03-01")).toEqual(DEFAULT_VIEW);
    expect(viewFromHashQuery("range=custom&from=2026-03-02&to=2026-03-01")).toMatchObject({ from: "2026-03-01", to: "2026-03-02" });
  });

  it("toggles one filter value without touching the others", () => {
    const once = toggleFilterValue(DEFAULT_VIEW.filters, "models", "gpt-6");
    expect(once.models).toEqual(["gpt-6"]);
    expect(toggleFilterValue(once, "models", "gpt-6").models).toEqual([]);
    expect(DEFAULT_VIEW.filters.models).toEqual([]);
  });
});
