// @vitest-environment jsdom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { I18nProvider } from "../i18n";
import { clearResourceCache } from "../resourceCache";

vi.mock("../api", async () => {
  const actual = await vi.importActual<typeof import("../api")>("../api");
  return {
    ...actual,
    api: { channelHealth: vi.fn(), frameworkHealthOverview: vi.fn(), fleet: vi.fn(), usageSources: vi.fn() },
  };
});

const { api } = await import("../api");
const { HealthPanel } = await import("./HealthPanel");

type Resolved<T extends (...args: never[]) => unknown> = Awaited<ReturnType<T>>;

let container: HTMLDivElement;
let root: Root;

beforeEach(() => {
  vi.resetAllMocks();
  vi.useFakeTimers();
  vi.setSystemTime(new Date("2026-10-14T15:00:00"));
  vi.stubGlobal("IS_REACT_ACT_ENVIRONMENT", true);
  vi.spyOn(document, "hidden", "get").mockReturnValue(false);
  clearResourceCache();
  vi.mocked(api.channelHealth).mockResolvedValue([]);
  vi.mocked(api.frameworkHealthOverview).mockResolvedValue([]);
  vi.mocked(api.fleet).mockResolvedValue({ source: "local", fetched_at: "2026-10-14T07:00:00Z", machines: [] });
  vi.mocked(api.usageSources).mockResolvedValue([]);
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
  await act(async () => root.render(<I18nProvider language="en"><HealthPanel /></I18nProvider>));
  await act(async () => { await vi.advanceTimersByTimeAsync(0); });
}

function rows(sectionID: string) {
  return [...container.querySelectorAll(`#${sectionID} .health-row`)].map((row) => row.textContent ?? "");
}

function overall() {
  return container.querySelector('.statistics-header-actions [role="status"]')!.textContent;
}

describe("health dashboard", () => {
  it("says nothing is configured on a fresh machine instead of claiming health", async () => {
    await mount();
    expect(overall()).toBe("Not configured");
    expect(container.querySelector("#health-machines")).toBeNull();
    expect(container.querySelector("#health-channels")!.textContent).toContain("No channels configured");
  });

  it("counts every problem and lists it before what works", async () => {
    vi.mocked(api.channelHealth).mockResolvedValue([
      { id: "ok", name: "support", type: "feishu", enabled: true, state: "running", connected: true, target_id: "local", target_name: "Local" },
      { id: "off", name: "archive", type: "slack", enabled: false },
      { id: "bad", name: "alerts", bot_name: "Alert bot", type: "feishu", enabled: true, state: "error", connected: false, error: "token expired" },
    ] as Resolved<typeof api.channelHealth>);
    vi.mocked(api.frameworkHealthOverview).mockResolvedValue([
      { kind: "codex", ok: true, latency_ms: 2100, model: "gpt-6", checked_at: "2026-10-14T06:00:00Z", target_id: "local", target_name: "Local" },
      // An older pass must not hide the latest failure.
      { kind: "claude", ok: true, latency_ms: 900, checked_at: "2026-10-13T06:00:00Z", target_id: "local", target_name: "Local" },
      { kind: "claude", ok: false, latency_ms: 0, error_detail: "not signed in", checked_at: "2026-10-14T06:30:00Z", target_id: "local", target_name: "Local" },
    ] as Resolved<typeof api.frameworkHealthOverview>);
    vi.mocked(api.fleet).mockResolvedValue({
      source: "local", fetched_at: "2026-10-14T07:00:00Z",
      machines: [
        { id: "m1", name: "mini", online: true, hub_role: "hub", version: "0.1.1" },
        { id: "m2", name: "laptop", online: false, last_seen_at: "2026-10-14T05:00:00Z" },
      ],
    });
    vi.mocked(api.usageSources).mockResolvedValue([
      { source: "cursor", connected: true, cloud_status: "error", local_status: "ready", hook: { status: "healthy" }, last_error: "Cursor access token is not a JWT", target_id: "local", target_name: "Local" },
      { source: "cursor", connected: false, cloud_status: "not_connected", local_status: "not_found", hook: { status: "not_installed" }, target_id: "m1", target_name: "mini" },
    ] as Resolved<typeof api.usageSources>);
    await mount();

    expect(overall()).toBe("4 need attention");
    const channels = rows("health-channels");
    expect(channels.map((row) => row.slice(0, 12))).toEqual(["Disconnected", "Connectedsup", "Disabledarch"]);
    expect(channels[0]).toContain("Alert bot");
    expect(channels[0]).toContain("token expired");

    const frameworks = rows("health-frameworks");
    expect(frameworks).toHaveLength(2);
    expect(frameworks[0]).toContain("Failed");
    expect(frameworks[0]).toContain("not signed in");
    expect(frameworks[1]).toContain("Passed");
    expect(frameworks[1]).toContain("gpt-6 · 2.1s");

    const machines = rows("health-machines");
    expect(machines[0]).toContain("Offline");
    expect(machines[0]).toContain("laptop");
    expect(machines[1]).toContain("Hub");

    // A source that stopped syncing is a problem; one never connected is not.
    const sources = rows("health-sources");
    expect(sources[0]).toContain("Not collecting");
    expect(sources[0]).toContain("Cursor access token is not a JWT");
    expect(sources[1]).toContain("Not connected");
  });

  it("reports everything healthy only when it is, and keeps checking", async () => {
    vi.mocked(api.channelHealth).mockResolvedValue([
      { id: "ok", name: "support", type: "feishu", enabled: true, state: "running", connected: true },
    ] as Resolved<typeof api.channelHealth>);
    await mount();
    expect(overall()).toBe("Everything is healthy");
    vi.mocked(api.channelHealth).mockResolvedValue([
      { id: "ok", name: "support", type: "feishu", enabled: true, state: "stopped", connected: false },
    ] as Resolved<typeof api.channelHealth>);
    await act(async () => { await vi.advanceTimersByTimeAsync(30_000); });
    expect(overall()).toBe("1 need attention");
  });

  it("counts a list that cannot be loaded as needing attention", async () => {
    vi.mocked(api.frameworkHealthOverview).mockRejectedValue(new Error("hub unreachable"));
    await mount();
    await act(async () => { await vi.advanceTimersByTimeAsync(10_000); });
    expect(overall()).toBe("1 need attention");
    expect(container.querySelector("#health-frameworks [role='alert']")!.textContent).toContain("hub unreachable");
  });
});
