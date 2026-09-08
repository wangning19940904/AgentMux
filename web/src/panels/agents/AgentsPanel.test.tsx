// @vitest-environment jsdom
import { act } from "react";
import { createRoot } from "react-dom/client";
import { afterEach, expect, it, vi } from "vitest";
import { api } from "../../api";
import { currentFleetWarnings, resetFleetWarnings } from "../../api/fleetWarnings";
import { I18nProvider } from "../../i18n";
import { AgentsPanel } from "./AgentsPanel";

afterEach(() => {
  resetFleetWarnings();
  localStorage.clear();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

it("refreshes a partially failed Provider load even when the Agent list succeeded", async () => {
  vi.stubGlobal("IS_REACT_ACT_ENVIRONMENT", true);
  for (const key of ["agentInstances", "agents", "activeRoutes", "channels", "triggers", "mcp", "skills"] as const) {
    vi.spyOn(api, key).mockResolvedValue([]);
  }
  vi.spyOn(api, "tools").mockResolvedValue({ cli: [], bundles: [], frameworks: [], skills: [], mcp: [], marketplace: [], warnings: [] });
  let failing = true;
  let finishRefresh!: () => void;
  const pendingRefresh = new Promise<void>((resolve) => { finishRefresh = resolve; });
  vi.stubGlobal("fetch", vi.fn(async () => {
    if (!failing) await pendingRefresh;
    return new Response(JSON.stringify({
      targets: [
        { target: { id: "local", name: "Local" }, responses: [{ key: "data", ok: true, status: 200, data: [] }] },
        { target: { id: "ssh", name: "aliyun-swas-sg" }, responses: [failing
          ? { key: "data", ok: false, status: 0, error: "EOF" }
          : { key: "data", ok: true, status: 200, data: [] }] },
      ],
    }), { status: 200 });
  }));
  const container = document.createElement("div");
  document.body.append(container);
  const root = createRoot(container);
  try {
    await act(async () => { root.render(<I18nProvider language="zh"><AgentsPanel /></I18nProvider>); });
    expect(currentFleetWarnings()).toEqual([expect.stringContaining("aliyun-swas-sg: EOF · GET /api/v1/providers · ")]);
    const refresh = container.querySelector<HTMLButtonElement>('button[title="刷新"]')!;
    expect(refresh.disabled).toBe(false);
    failing = false;
    await act(async () => { refresh.click(); });
    expect(refresh.disabled).toBe(true);
    // Loading other data must not conceal this failure before it recovers.
    expect(currentFleetWarnings()).toHaveLength(1);
    await act(async () => { finishRefresh(); });
    expect(refresh.disabled).toBe(false);
    expect(currentFleetWarnings()).toEqual([]);
    expect(api.agentInstances).toHaveBeenCalledTimes(2);
  } finally {
    finishRefresh();
    await act(async () => { root.unmount(); });
    container.remove();
  }
});
