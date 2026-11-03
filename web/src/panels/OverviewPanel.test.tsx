// @vitest-environment jsdom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { api, type UsageReport } from "../api";
import { I18nProvider } from "../i18n";
import { OverviewPanel } from "./OverviewPanel";

vi.mock("../api", () => ({
  api: {
    usage: vi.fn(),
    menubarSettings: vi.fn(),
    providers: vi.fn(),
    channelHealth: vi.fn(),
    providerMonitor: vi.fn(),
  },
}));

function report(tokens: number): UsageReport {
  return {
    period: "hourly",
    totals: {
      input_tokens: tokens, output_tokens: 0, cache_read_tokens: 0, cache_write_tokens: 0,
      cost_usd: tokens / 100, records: 1, sessions: 1, estimated_tokens: 0, estimated_records: 0,
    },
    buckets: [], by_model: [], by_source: [], by_runtime: [],
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
  vi.mocked(api.usage).mockResolvedValue(report(100));
  vi.mocked(api.menubarSettings).mockResolvedValue({ cny_rate: 7 } as Awaited<ReturnType<typeof api.menubarSettings>>);
  vi.mocked(api.providers).mockResolvedValue([]);
  vi.mocked(api.channelHealth).mockImplementation(async () => []);
  vi.mocked(api.providerMonitor).mockImplementation(async () => ({}) as Awaited<ReturnType<typeof api.providerMonitor>>);
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
  await act(async () => root.render(<I18nProvider language="zh"><OverviewPanel /></I18nProvider>));
}

function updatedAt() {
  return container.querySelector(".overview-updated-at")!.textContent;
}

describe("overview usage refresh", () => {
  it("includes both cache categories once in the total and shows their counts with the saved rate", async () => {
    const usage = report(100);
    Object.assign(usage.totals, { output_tokens: 20, cache_read_tokens: 3000, cache_write_tokens: 400, cost_usd: 10 });
    vi.mocked(api.usage).mockResolvedValue(usage);
    vi.mocked(api.menubarSettings).mockResolvedValue({ cny_rate: 6.7249 } as Awaited<ReturnType<typeof api.menubarSettings>>);
    await mount();
    const summary = container.querySelector(".usage-summary-card")!;
    expect(summary.querySelector(".usage-summary-metric > strong")!.textContent).toBe("3.52K");
    expect(summary.textContent).toContain("其中缓存 3.40K Token，已计入总量");
    const breakdown = Array.from(summary.querySelectorAll(".usage-breakdown .stat"), (node) => node.textContent);
    expect(breakdown).toEqual(["非缓存输入100", "输出20", "缓存读取3.00K", "缓存写入400"]);
    expect(summary.textContent).toContain("¥67.25");
    expect(summary.textContent).toContain("1 USD = 6.7249 RMB");
  });

  it("refreshes usage and its timestamp on the polling interval", async () => {
    await mount();
    const firstUpdate = updatedAt();
    expect(api.usage).toHaveBeenCalledWith("hourly", "2026-10-14", "2026-10-14");
    vi.mocked(api.usage).mockResolvedValue(report(250));
    await act(async () => { await vi.advanceTimersByTimeAsync(30_000); });
    expect(api.usage).toHaveBeenCalledTimes(2);
    expect(container.querySelector(".usage-trend-total")!.textContent).toBe("250");
    expect(updatedAt()).not.toBe(firstUpdate);
    expect(updatedAt()).toContain(new Date().toLocaleTimeString());
  });

  it("only shows a timestamp after successful usage data and keeps it on failure", async () => {
    let finish!: (value: UsageReport) => void;
    vi.mocked(api.usage).mockReturnValueOnce(new Promise((resolve) => { finish = resolve; }));
    await mount();
    expect(updatedAt()).toContain("—");
    vi.setSystemTime(Date.now() + 1_000);
    await act(async () => { finish(report(100)); });
    const successfulUpdate = updatedAt();
    expect(successfulUpdate).toContain(new Date().toLocaleTimeString());
    vi.mocked(api.usage).mockRejectedValue(new Error("usage unavailable"));
    await act(async () => { await vi.advanceTimersByTimeAsync(30_000); });
    expect(api.channelHealth).toHaveBeenCalledTimes(2);
    expect(container.textContent).toContain("usage unavailable");
    expect(container.querySelector(".usage-trend-total")!.textContent).toBe("100");
    expect(updatedAt()).toBe(successfulUpdate);
  });

  it.each([
    { buttonIndex: 0, from: "2026-10-15" },
    { buttonIndex: 1, from: "2026-10-09" },
    { buttonIndex: 2, from: "2026-09-16" },
  ])("moves range $buttonIndex across midnight without querying yesterday again", async ({ buttonIndex, from }) => {
    vi.setSystemTime(new Date("2026-10-14T23:59:50"));
    await mount();
    await act(async () => { container.querySelectorAll<HTMLButtonElement>(".statistics-range button")[buttonIndex].click(); });
    vi.mocked(api.usage).mockClear();
    await act(async () => { await vi.advanceTimersByTimeAsync(30_000); });
    expect(api.usage).toHaveBeenCalledTimes(1);
    expect(api.usage).toHaveBeenLastCalledWith("hourly", from, "2026-10-15");
  });

  it("catches up to the current date when the app regains focus after being hidden", async () => {
    await mount();
    vi.spyOn(document, "hidden", "get").mockReturnValue(true);
    await act(async () => { await vi.advanceTimersByTimeAsync(30_000); });
    expect(api.usage).toHaveBeenCalledTimes(1);
    vi.setSystemTime(new Date("2026-10-16T09:00:00"));
    vi.spyOn(document, "hidden", "get").mockReturnValue(false);
    await act(async () => { window.dispatchEvent(new Event("focus")); });
    expect(api.usage).toHaveBeenCalledTimes(2);
    expect(api.usage).toHaveBeenLastCalledWith("hourly", "2026-10-16", "2026-10-16");
  });
});
