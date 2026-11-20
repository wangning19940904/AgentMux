// @vitest-environment jsdom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { UsageReport } from "../api";
import { I18nProvider } from "../i18n";
import { clearResourceCache } from "../resourceCache";
import { clearUsageAnswers } from "./usage/usageData";
import { resetSeriesSlots } from "./usage/usageTrend";

vi.mock("../api", async () => {
  const actual = await vi.importActual<typeof import("../api")>("../api");
  return {
    ...actual,
    // One machine: the page asks it directly.
    activeMachineScope: () => "local",
    api: {
      usage: vi.fn(),
      usageTargets: vi.fn(),
      usageFromTarget: vi.fn(),
      usageSources: vi.fn(),
      menubarSettings: vi.fn(),
      saveMenubarSettings: vi.fn(),
      agentInstances: vi.fn(),
      superAgents: vi.fn(),
      channels: vi.fn(),
      cursorUsageAction: vi.fn(),
    },
  };
});

const { api } = await import("../api");
const { UsagePanel } = await import("./UsagePanel");

function report(tokens: number, extra: Partial<UsageReport> = {}): UsageReport {
  return {
    period: "hourly",
    totals: {
      input_tokens: tokens, output_tokens: 0, cache_read_tokens: 0, cache_write_tokens: 0,
      cost_usd: tokens / 100, records: 4, sessions: 1, estimated_tokens: 0, estimated_records: 0,
    },
    buckets: [], by_model: [], by_source: [], by_runtime: [], by_agent: [],
    facets: { models: [], runtimes: [], agents: [] },
    ...extra,
  };
}

let container: HTMLDivElement;
let root: Root;

beforeEach(() => {
  vi.resetAllMocks();
  vi.useFakeTimers();
  vi.setSystemTime(new Date("2026-10-14T15:00:00"));
  vi.stubGlobal("IS_REACT_ACT_ENVIRONMENT", true);
  vi.spyOn(document, "hidden", "get").mockReturnValue(false);
  clearUsageAnswers();
  clearResourceCache();
  resetSeriesSlots();
  window.location.hash = "#usage";
  vi.mocked(api.usage).mockResolvedValue(report(100));
  vi.mocked(api.menubarSettings).mockResolvedValue({ cny_rate: 7, currency: "cny" } as Awaited<ReturnType<typeof api.menubarSettings>>);
  vi.mocked(api.usageSources).mockResolvedValue([]);
  vi.mocked(api.agentInstances).mockResolvedValue([{ id: "a1", name: "Reviewer" }] as Awaited<ReturnType<typeof api.agentInstances>>);
  vi.mocked(api.superAgents).mockResolvedValue([]);
  vi.mocked(api.channels).mockResolvedValue([]);
  container = document.createElement("div");
  document.body.append(container);
  root = createRoot(container);
});

afterEach(async () => {
  await act(async () => root.unmount());
  container.remove();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
  vi.useRealTimers();
});

async function mount() {
  await act(async () => root.render(<I18nProvider language="zh"><UsagePanel /></I18nProvider>));
  await act(async () => { await vi.advanceTimersByTimeAsync(0); });
}

function updatedAt() {
  return container.querySelector(".overview-updated-at")!.textContent;
}

function button(text: string) {
  const found = [...container.querySelectorAll<HTMLButtonElement>("button")].find((item) => item.textContent?.includes(text));
  if (!found) throw new Error(`no button containing ${text}`);
  return found;
}

async function click(element: HTMLElement) {
  await act(async () => { element.click(); });
  await act(async () => { await vi.advanceTimersByTimeAsync(0); });
}

const NO_FILTER = { models: [], runtimes: [], agents: [] };

describe("usage dashboard", () => {
  it("includes both cache categories once in the total and shows their counts with the saved rate", async () => {
    const usage = report(100);
    Object.assign(usage.totals, { output_tokens: 20, cache_read_tokens: 3000, cache_write_tokens: 400, cost_usd: 10 });
    vi.mocked(api.usage).mockResolvedValue(usage);
    vi.mocked(api.menubarSettings).mockResolvedValue({ cny_rate: 6.7249, currency: "cny" } as Awaited<ReturnType<typeof api.menubarSettings>>);
    await mount();
    const summary = container.querySelector(".usage-summary-card")!;
    expect(summary.querySelector(".usage-summary-metric > strong")!.textContent).toBe("3.52K");
    expect(summary.textContent).toContain("其中缓存 3.40K Token，已计入总量");
    const breakdown = Array.from(summary.querySelectorAll(".usage-breakdown .stat"), (node) => node.textContent);
    expect(breakdown).toEqual(["非缓存输入100", "输出20", "缓存读取3.00K", "缓存写入400"]);
    expect(summary.textContent).toContain("¥67.25");
    expect(summary.textContent).toContain("1 USD = 6.7249 RMB");
  });

  it("opens on today by the hour and refreshes while the range is still changing", async () => {
    await mount();
    const firstUpdate = updatedAt();
    expect(api.usage).toHaveBeenCalledWith("hourly", "2026-10-14", "2026-10-14", NO_FILTER);
    vi.mocked(api.usage).mockResolvedValue(report(250));
    await act(async () => { await vi.advanceTimersByTimeAsync(60_000); });
    expect(api.usage).toHaveBeenCalledTimes(2);
    expect(container.querySelector(".usage-trend-total")!.textContent).toBe("250");
    expect(updatedAt()).not.toBe(firstUpdate);
  });

  it("keeps the last figures and their time when a refresh fails", async () => {
    await mount();
    const successfulUpdate = updatedAt();
    vi.mocked(api.usage).mockRejectedValue(new Error("usage unavailable"));
    await act(async () => { await vi.advanceTimersByTimeAsync(60_000); });
    expect(container.querySelector('[role="alert"]')!.textContent).toContain("usage unavailable");
    expect(container.querySelector(".usage-trend-total")!.textContent).toBe("100");
    expect(updatedAt()).toBe(successfulUpdate);
  });

  it("does not poll a range that ended before today", async () => {
    window.location.hash = "#usage?range=lastMonth";
    await mount();
    expect(api.usage).toHaveBeenCalledWith("daily", "2026-09-01", "2026-09-30", NO_FILTER);
    await act(async () => { await vi.advanceTimersByTimeAsync(180_000); });
    expect(api.usage).toHaveBeenCalledTimes(1);
  });

  it("follows the date across midnight", async () => {
    vi.setSystemTime(new Date("2026-10-14T23:59:30"));
    await mount();
    vi.mocked(api.usage).mockClear();
    await act(async () => { await vi.advanceTimersByTimeAsync(60_000); });
    expect(api.usage).toHaveBeenLastCalledWith("hourly", "2026-10-15", "2026-10-15", NO_FILTER);
  });

  it("picks a preset from the date menu and keeps it in the route", async () => {
    await mount();
    await click(button("日期范围"));
    await click(button("近 30 天"));
    expect(api.usage).toHaveBeenLastCalledWith("daily", "2026-09-15", "2026-10-14", NO_FILTER);
    expect(window.location.hash).toBe("#usage?range=30d");
    // The menu closes after a choice.
    expect(container.querySelector('[role="dialog"]')).toBeNull();
  });

  it("applies any custom range", async () => {
    await mount();
    await click(button("日期范围"));
    const [from, to] = [...container.querySelectorAll<HTMLInputElement>('.usage-filter-custom input[type="date"]')];
    const setValue = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!;
    await act(async () => {
      setValue.call(from, "2026-03-05");
      from.dispatchEvent(new Event("input", { bubbles: true }));
      setValue.call(to, "2026-01-20");
      to.dispatchEvent(new Event("input", { bubbles: true }));
    });
    await click(button("应用"));
    // The dates are put in order, and 45 days are shown by day.
    expect(api.usage).toHaveBeenLastCalledWith("daily", "2026-01-20", "2026-03-05", NO_FILTER);
    expect(window.location.hash).toBe("#usage?range=custom&from=2026-01-20&to=2026-03-05");
  });

  it("restores range, bucket size and filters from the route", async () => {
    window.location.hash = "#usage?range=7d&by=daily&model=gpt-6&framework=codex-app&agent=agent%3Aa1";
    await mount();
    expect(api.usage).toHaveBeenCalledWith("daily", "2026-10-08", "2026-10-14", { models: ["gpt-6"], runtimes: ["codex-app"], agents: ["agent:a1"] });
    expect(button("清除筛选").textContent).toContain("3");
  });

  it("offers the values found in the range as filters and narrows the report to a choice", async () => {
    vi.mocked(api.usage).mockResolvedValue(report(100, {
      by_model: [{ model: "gpt-6", tokens: 70, cost_usd: 1 }, { model: "", tokens: 30, cost_usd: 0 }],
      by_agent: [{ agent: "agent:a1", tokens: 100, cost_usd: 1 }],
      facets: {
        models: [{ value: "gpt-6", tokens: 70 }, { value: "", tokens: 30 }],
        runtimes: [{ value: "codex-app", tokens: 100 }],
        agents: [{ value: "agent:a1", tokens: 100 }],
      },
    }));
    await mount();
    await click(button("模型全部"));
    const options = [...container.querySelectorAll('[role="dialog"] [role="option"]')].map((item) => item.textContent);
    expect(options).toEqual(["gpt-670", "未知模型30"]);
    await click(container.querySelector<HTMLElement>('[role="dialog"] [role="option"]')!);
    expect(api.usage).toHaveBeenLastCalledWith("hourly", "2026-10-14", "2026-10-14", { models: ["gpt-6"], runtimes: [], agents: [] });
    expect(window.location.hash).toBe("#usage?model=gpt-6");
    // Agents are listed by name, not by ID.
    expect(container.querySelector('[aria-label="最常用智能体与项目"]')!.textContent).toContain("Reviewer");
  });

  it("filters by a ranking row and clears the filter from the same row", async () => {
    vi.mocked(api.usage).mockResolvedValue(report(100, { by_runtime: [{ runtime: "codex-app", tokens: 100, cost_usd: 1 }] }));
    await mount();
    const row = () => container.querySelector<HTMLButtonElement>('[aria-label="最常用框架"] .framework-rank-row')!;
    await click(row());
    expect(api.usage).toHaveBeenLastCalledWith("hourly", "2026-10-14", "2026-10-14", { models: [], runtimes: ["codex-app"], agents: [] });
    expect(row().getAttribute("aria-pressed")).toBe("true");
    await click(row());
    expect(api.usage).toHaveBeenLastCalledWith("hourly", "2026-10-14", "2026-10-14", NO_FILTER);
  });

  it("draws the trend with every bucket of the range and reads one out on the keyboard", async () => {
    vi.mocked(api.usage).mockResolvedValue(report(300, {
      buckets: [
        { key: "2026-10-14 09:00", totals: { ...report(100).totals }, by_model: [{ model: "a", tokens: 100, cost_usd: 1 }] },
        { key: "2026-10-14 15:00", totals: { ...report(200).totals }, by_model: [{ model: "a", tokens: 150, cost_usd: 1 }, { model: "b", tokens: 50, cost_usd: 1 }] },
      ],
    }));
    await mount();
    // Midnight to the current hour, one column per hour with usage.
    expect(container.querySelectorAll(".usage-chart-bar")).toHaveLength(2);
    await click(button("按模型"));
    expect(container.querySelectorAll(".usage-chart-bar")).toHaveLength(3);
    expect([...container.querySelectorAll(".trend-series-legend strong")].map((item) => item.textContent)).toEqual(["a", "b"]);
    const plot = container.querySelector<HTMLElement>(".usage-chart-plot")!;
    await act(async () => { plot.focus(); });
    const tooltip = () => container.querySelector(".usage-chart-tooltip")!.textContent;
    expect(tooltip()).toContain("2026-10-14 15:00");
    expect(tooltip()).toContain("150");
    expect(tooltip()).toContain("200");
    await act(async () => { plot.dispatchEvent(new KeyboardEvent("keydown", { key: "ArrowLeft", bubbles: true })); });
    expect(tooltip()).toContain("2026-10-14 14:00");
  });

  it("shows the same figures as a table, by period or by dimension", async () => {
    vi.mocked(api.usage).mockResolvedValue(report(300, {
      buckets: [{ key: "2026-10-14 09:00", totals: { ...report(300).totals } }],
      by_model: [{ model: "gpt-6", tokens: 300, cost_usd: 3, estimated_tokens: 10 }],
    }));
    await mount();
    const rows = () => [...container.querySelectorAll(".usage-details-table tbody tr")].map((row) => row.textContent);
    expect(rows()[0]).toContain("2026-10-14 09:00");
    const tabs = container.querySelector('[aria-label="明细维度"]')!;
    await click([...tabs.querySelectorAll("button")].find((item) => item.textContent === "模型")!);
    expect(rows()).toEqual(["gpt-6300100%¥21.00估算"]);
  });
});
