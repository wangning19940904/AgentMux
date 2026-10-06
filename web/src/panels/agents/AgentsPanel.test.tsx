// @vitest-environment jsdom
import { clearResourceCache } from "../../resourceCache";
import { act } from "react";
import { createRoot } from "react-dom/client";
import { afterEach, expect, it, vi } from "vitest";
import { api } from "../../api";
import { currentFleetWarnings, resetFleetWarnings } from "../../api/fleetWarnings";
import { I18nProvider } from "../../i18n";
import { AgentsPanel } from "./AgentsPanel";
import { newAgent } from "./agentUtils";

vi.mock("./AgentForm", () => ({
  AgentForm: ({ selectedChannelIDs, onSave, canSave }: { selectedChannelIDs: string[]; onSave: () => void; canSave: boolean }) =>
    <><output data-selection>{selectedChannelIDs.join(",")}</output><button data-save disabled={!canSave} onClick={onSave}>save draft</button></>,
}));

afterEach(() => {
  clearResourceCache();
  resetFleetWarnings();
  localStorage.clear();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

it("waits for binding data before editing and preserves a late-arriving binding on save", async () => {
  vi.stubGlobal("IS_REACT_ACT_ENVIRONMENT", true);
  localStorage.setItem("agentmux:active-remote", "local");
  const agent = { ...newAgent(["cursor"]), id: "ning", name: "宁宝", default_model: "grok-4.7", default_reasoning_effort: "xhigh", default_service_tier: "priority" };
  vi.spyOn(api, "agentInstances").mockResolvedValue([agent]);
  vi.spyOn(api, "agents").mockResolvedValue(["cursor"]);
  for (const key of ["providers", "activeRoutes", "triggers", "mcp", "skills"] as const) vi.spyOn(api, key).mockResolvedValue([]);
  vi.spyOn(api, "tools").mockResolvedValue({ cli: [], bundles: [], frameworks: [], skills: [], mcp: [], marketplace: [], warnings: [] });
  const channel = { id: "ning-bot", name: "宁宝渠道", type: "feishu", enabled: true, agent_id: "ning" };
  let finishChannels!: (value: typeof channel[]) => void;
  vi.spyOn(api, "channels").mockImplementationOnce(() => new Promise((resolve) => { finishChannels = resolve; })).mockResolvedValue([channel]);
  vi.spyOn(api, "upsertAgentInstance").mockResolvedValue(agent);
  const saveChannel = vi.spyOn(api, "upsertChannel");
  const container = document.createElement("div");
  const root = createRoot(container);
  try {
    await act(async () => root.render(<I18nProvider language="zh"><AgentsPanel /></I18nProvider>));
    const edit = () => [...container.querySelectorAll<HTMLButtonElement>("button")].find((button) => button.textContent === "编辑")!;
    expect(container.textContent).toContain("grok-4.7 · xhigh · 快速");
    expect(container.textContent).toContain("渠道加载中");
    expect(container.textContent).not.toContain("未绑定渠道");
    expect(edit().disabled).toBe(true);
    await act(async () => container.querySelector("article")!.dispatchEvent(new MouseEvent("dblclick", { bubbles: true })));
    expect(container.querySelector('[role="dialog"]')).toBeNull();
    await act(async () => finishChannels([channel]));
    expect(edit().disabled).toBe(false);
    await act(async () => edit().click());
    expect(container.querySelector("[data-selection]")!.textContent).toBe("ning-bot");
    await act(async () => container.querySelector<HTMLButtonElement>("[data-save]")!.click());
    expect(api.upsertAgentInstance).toHaveBeenCalled();
    expect(saveChannel).not.toHaveBeenCalled();
    expect(container.textContent).toContain("宁宝渠道");
    vi.mocked(api.channels).mockRejectedValueOnce(new Error("offline"));
    await act(async () => container.querySelector<HTMLButtonElement>('button[title="刷新"]')!.click());
    expect(container.textContent).toContain("宁宝渠道");
    expect(container.textContent).toContain("渠道或触发器加载失败");
    expect(edit().disabled).toBe(true);
  } finally { await act(async () => root.unmount()); }
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
