// @vitest-environment jsdom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { api, type AgentInstance, type FrameworkRuntimeSettings } from "../../api";
import { I18nProvider, useI18n } from "../../i18n";
import { AgentForm } from "./AgentForm";
import { newAgent } from "./agentUtils";

let root: Root;
let container: HTMLDivElement;
const onUpdate = vi.fn();
const listing = { path: "/home/test", parent_path: "/home", entries: [{ name: "project", path: "/home/test/project" }] };

function Form({ draft, canSave = false }: { draft: AgentInstance; canSave?: boolean }) {
  const { t } = useI18n();
  return <AgentForm draft={draft} drawerMode="create" t={t} onUpdate={onUpdate}
    busy="" canSave={canSave} readOnly={false} activeRoutes={[]} channelOptions={[]} cliOptions={[]}
    compatibleProviders={[]} mcpOptions={[]} runtimeOptions={[]} selectedChannelIDs={[]} selectedTriggerIDs={[]}
    skillOptions={[]} triggerOptions={[]} onDelete={vi.fn()} onInstallCLI={vi.fn()} onSave={vi.fn()}
    onToggleChannel={vi.fn()} onToggleTrigger={vi.fn()} />;
}

async function openPicker(targetID?: string) {
  await act(async () => { root.render(<I18nProvider language="zh"><Form draft={{ ...newAgent([]), target_id: targetID }} /></I18nProvider>); });
  await act(async () => { container.querySelector<HTMLButtonElement>('button[aria-label="选择目录"]')!.click(); });
}

function button(label: string): HTMLButtonElement {
  const result = [...document.querySelectorAll("button")].find((item) => item.textContent?.trim() === label);
  if (!result) throw new Error(`Missing button: ${label}`);
  return result;
}

beforeEach(() => {
  vi.stubGlobal("IS_REACT_ACT_ENVIRONMENT", true);
  localStorage.setItem("agentmux:active-remote", "local");
  vi.stubGlobal("fetch", vi.fn(async () => new Response(JSON.stringify(listing), { status: 200 })));
  container = document.createElement("div");
  document.body.append(container);
  root = createRoot(container);
  onUpdate.mockClear();
});

afterEach(async () => {
  await act(async () => { root.unmount(); });
  container.remove();
  localStorage.clear();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

it("opens a browser directory dialog and applies the selected server path", async () => {
  await openPicker();
  expect(document.querySelector('[role="dialog"]')?.textContent).toContain("运行当前 AgentMux 服务的机器");
  expect(fetch).toHaveBeenCalledWith("/api/v1/system/directories?path=", expect.objectContaining({ headers: { "X-AgentMux-Console": "1" } }));
  await act(async () => { button("选择此目录").click(); });
  expect(onUpdate).toHaveBeenCalledWith("work_dir", "/home/test");
  expect(document.querySelector('[role="dialog"]')).toBeNull();
});

it("keeps an explicitly local draft local while the page is scoped to SSH", async () => {
  localStorage.setItem("agentmux:active-remote", "ssh-other");
  await openPicker("local");
  expect(fetch).toHaveBeenCalledWith("/api/v1/system/directories?path=", expect.anything());
  await api.ensureDirectory("/home/test/new", "local");
  expect(fetch).toHaveBeenLastCalledWith("/api/v1/system/directories", expect.objectContaining({ method: "POST", body: JSON.stringify({ path: "/home/test/new" }) }));
});

it("uses the selected SSH host for browsing", async () => {
  await openPicker("ssh-target");
  expect(document.querySelector('[role="dialog"]')?.textContent).toContain("选择 SSH 机器上的目录");
  expect(fetch).toHaveBeenCalledWith("/api/v1/remote/directories?id=ssh-target&path=", expect.anything());
});

it("lets all-machine creation browse a reference host without changing creation scope", async () => {
  localStorage.setItem("agentmux:active-remote", "all");
  vi.spyOn(api, "remoteHosts").mockResolvedValue([{ id: "ssh-target", name: "Test server", trusted: true, host: "test", port: 22, user: "test", remote_addr: "127.0.0.1:8765" }]);
  await openPicker();
  const dialog = document.querySelector('[role="dialog"]')!;
  expect(dialog.textContent).toContain("所选路径将用于全部机器");
  expect(button("选择此目录").disabled).toBe(true);
  expect(fetch).not.toHaveBeenCalled();
  const machine = dialog.querySelector("select")!;
  await act(async () => { machine.value = "ssh-target"; machine.dispatchEvent(new Event("change", { bubbles: true })); });
  expect(fetch).toHaveBeenCalledWith("/api/v1/remote/directories?id=ssh-target&path=", expect.anything());
  await act(async () => { button("选择此目录").click(); });
  expect(onUpdate).toHaveBeenCalledWith("work_dir", "/home/test");
  expect(onUpdate).not.toHaveBeenCalledWith("target_id", expect.anything());
});

it("keeps the native picker in the desktop app", async () => {
  const nativePicker = vi.fn().mockResolvedValue("/Users/test/project");
  vi.stubGlobal("go", { main: { App: { SelectDirectory: nativePicker } } });
  await openPicker();
  expect(nativePicker).toHaveBeenCalledWith("");
  expect(fetch).not.toHaveBeenCalled();
  expect(document.querySelector('[role="dialog"]')).toBeNull();
  expect(onUpdate).toHaveBeenCalledWith("work_dir", "/Users/test/project");
});

it("does not apply an old directory response after switching reference machines", async () => {
  localStorage.setItem("agentmux:active-remote", "all");
  vi.spyOn(api, "remoteHosts").mockResolvedValue([{ id: "ssh-target", name: "Test server", trusted: true, host: "test", port: 22, user: "test", remote_addr: "127.0.0.1:8765" }]);
  let finishLocal!: (value: typeof listing) => void;
  vi.spyOn(api, "directories").mockImplementation((_path, target) => target === "local"
    ? new Promise((resolve) => { finishLocal = resolve; })
    : Promise.resolve({ ...listing, path: "/remote/project" }));
  await openPicker();
  const machine = document.querySelector<HTMLSelectElement>('[role="dialog"] select')!;
  await act(async () => { machine.value = "local"; machine.dispatchEvent(new Event("change", { bubbles: true })); });
  await act(async () => { machine.value = "ssh-target"; machine.dispatchEvent(new Event("change", { bubbles: true })); });
  await act(async () => { finishLocal(listing); });
  await act(async () => { button("选择此目录").click(); });
  expect(onUpdate).toHaveBeenCalledWith("work_dir", "/remote/project");
});

it("shows directory load failures inside the browser dialog", async () => {
  vi.stubGlobal("fetch", vi.fn(async () => new Response(JSON.stringify({ error: "permission denied" }), { status: 403 })));
  await openPicker();
  expect(document.querySelector('[role="dialog"]')?.textContent).toContain("permission denied");
  expect(button("选择此目录").disabled).toBe(true);
});

it("shows the detected local Codex login when all machines contains only local", async () => {
  localStorage.setItem("agentmux:active-remote", "all");
  const auth = vi.spyOn(api, "frameworkAuth").mockResolvedValue({
    kind: "codex",
    state: "authenticated",
    installed: true,
    login_supported: true,
    target_id: "local",
    target_name: "Local machine",
  });
  const settings = vi.spyOn(api, "frameworkRuntimeSettings").mockResolvedValue({
    kind: "codex",
    defaults: {},
    capabilities: {},
  });

  await act(async () => {
    root.render(<I18nProvider language="zh"><Form draft={newAgent(["codex"])} /></I18nProvider>);
  });
  await vi.waitFor(() => {
    expect(container.textContent).toContain("已检测到框架本机登录态");
  });
  expect(container.textContent).not.toContain("请登录或配置 Provider");
  expect(container.textContent).not.toContain("Choose one machine");
  expect(auth).toHaveBeenCalledWith("codex", undefined);
  expect(settings).toHaveBeenCalledWith("codex", "", "local");
});

const options = (...values: string[]) => values.map((value) => ({ value }));
const cursorCatalog: FrameworkRuntimeSettings = {
  kind: "cursor", defaults: { model: "auto", approval_mode: "manual" },
  capabilities: {
    models: options("auto", "grok-4.7", "other-model"),
    reasoning_efforts: options("low", "medium", "high", "xhigh", "max"),
    service_tiers: options("default", "priority"), approval_modes: options("manual", "yolo"),
    model_capabilities: {
      auto: {},
      "grok-4.7": { reasoning_efforts: options("low", "medium", "high", "xhigh"), service_tiers: options("default", "priority") },
      "other-model": { reasoning_efforts: options("high", "max"), service_tiers: options("default", "priority"), variants: [
        { reasoning_effort: "high", service_tier: "priority" }, { reasoning_effort: "max", service_tier: "default" },
      ] },
    },
  },
};

function setting(label: string): HTMLSelectElement {
  const field = [...container.querySelectorAll("label")].find((item) => item.querySelector("span")?.textContent === label);
  if (!field?.querySelector("select")) throw new Error(`Missing setting: ${label}`);
  return field.querySelector("select")!;
}
const selectableValues = (select: HTMLSelectElement) => [...select.options].filter((option) => !option.disabled).map((option) => option.value);
async function renderSettings(draft: AgentInstance) {
  await act(async () => { root.render(<I18nProvider language="zh"><Form draft={draft} canSave /></I18nProvider>); });
}
function mockCatalog() {
  vi.spyOn(api, "frameworkAuth").mockResolvedValue({ kind: "cursor", state: "unknown", installed: true, login_supported: true });
  return vi.spyOn(api, "frameworkRuntimeSettings").mockResolvedValue(cursorCatalog);
}

it("loads real per-model options even when login detection is unknown, and blocks stale max", async () => {
  const load = mockCatalog();
  const draft = { ...newAgent(["cursor"]), default_model: "grok-4.7", default_reasoning_effort: "max" };
  await renderSettings(draft);
  expect(load).toHaveBeenCalled();
  expect(selectableValues(setting("默认思考强度"))).toEqual(["", "low", "medium", "high", "xhigh"]);
  expect(setting("默认思考强度").selectedOptions[0].textContent).toContain("max · 当前选择不支持");
  expect(selectableValues(setting("默认速度模式"))).toEqual(["", "default", "priority"]);
  expect(selectableValues(setting("默认审批模式"))).toEqual(["", "manual", "yolo"]);
  expect(container.querySelector('[role="alert"]')?.textContent).toContain("重新选择后保存");
  expect(button("创建 Agent").disabled).toBe(true);
  expect(onUpdate).not.toHaveBeenCalledWith("default_reasoning_effort", "");

  await renderSettings({ ...draft, default_reasoning_effort: "xhigh" });
  expect(button("创建 Agent").disabled).toBe(false);
  await renderSettings({ ...draft, default_model: "other-model", default_service_tier: "priority" });
  expect(selectableValues(setting("默认思考强度"))).toEqual(["", "high", "max"]);
  expect(selectableValues(setting("默认速度模式"))).toEqual(["", "default"]);
  expect(button("创建 Agent").disabled).toBe(true);

  await renderSettings({ ...draft, default_model: "", default_reasoning_effort: "" });
  expect(setting("默认模型").selectedOptions[0].textContent).toContain("auto");
  expect(setting("默认思考强度").disabled).toBe(true);
  expect(setting("默认速度模式").disabled).toBe(true);
});

it("preserves configured values on discovery failure and lets the user retry", async () => {
  const load = mockCatalog().mockRejectedValueOnce(new Error("catalog unavailable"));
  const draft = { ...newAgent(["cursor"]), default_model: "grok-4.7", default_reasoning_effort: "xhigh" };
  await renderSettings(draft);
  expect(container.textContent).toContain("catalog unavailable");
  expect(setting("默认模型").value).toBe("grok-4.7");
  expect(onUpdate).not.toHaveBeenCalledWith("default_model", "");
  expect(button("创建 Agent").disabled).toBe(true);
  await act(async () => { container.querySelector<HTMLButtonElement>('button[aria-label="刷新模型与运行设置"]')!.click(); });
  expect(load).toHaveBeenCalledTimes(2);
  expect(button("创建 Agent").disabled).toBe(false);
});

it("ignores an old host's catalog response after changing the target", async () => {
  const load = mockCatalog();
  let finishOld!: (value: FrameworkRuntimeSettings) => void;
  load.mockImplementation((_kind, _dir, target) => target === "old-host"
    ? new Promise((resolve) => { finishOld = resolve; }) : Promise.resolve(cursorCatalog));
  const draft = { ...newAgent(["cursor"]), target_id: "old-host", default_model: "grok-4.7" };
  await renderSettings(draft);
  await renderSettings({ ...draft, target_id: "new-host" });
  await act(async () => { finishOld({ kind: "cursor", defaults: {}, capabilities: { models: options("stale-model") } }); });
  expect(selectableValues(setting("默认模型"))).toEqual(["", "auto", "grok-4.7", "other-model"]);
  expect(selectableValues(setting("默认思考强度"))).not.toContain("max");
});
