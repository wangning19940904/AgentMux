// @vitest-environment jsdom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { activeMachineScope, activeRemoteID, api, type Framework, type FrameworkInstallResult, type OperationProgress } from "../api";
import { I18nProvider } from "../i18n";
import { clearResourceCache } from "../resourceCache";
import { FrameworksPanel } from "./FrameworksPanel";
import { rememberExpandedFrameworkGroups } from "./frameworks/frameworkGrouping";
import { resetFrameworkPageCache } from "./frameworks/frameworkPageCache";

vi.mock("../api", () => ({
  api: {
    frameworks: vi.fn(),
    remoteHosts: vi.fn(),
    checkFrameworkUpdate: vi.fn(),
    frameworkAuth: vi.fn(),
    frameworkHealth: vi.fn(),
    saveFrameworkHealthSettings: vi.fn(),
    installFramework: vi.fn(),
    frameworkInstallSettings: vi.fn(),
    saveFrameworkInstallSettings: vi.fn(),
  },
  activeMachineScope: vi.fn(),
  activeRemoteID: vi.fn(),
}));

const dsh: Framework = {
  spec: {
    kind: "dsh", display: "DeepSeek Harness", company: "DeepSeek AI", kind_type: "cli",
    install_supported: true, install_requires_npm: true, update_supported: true, supported: true,
  },
  installed: false,
  registered: true,
};

function frameworksResponse(installed: boolean) {
  return {
    prereqs: { node: true, npm: true },
    frameworks: [installed ? { ...dsh, installed: true, version: "0.2.1-rc.5" } : dsh],
  };
}

function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((res) => { resolve = res; });
  return { promise, resolve };
}

let container: HTMLDivElement;
let root: Root;

function button(parent: ParentNode, label: string): HTMLButtonElement {
  const result = Array.from(parent.querySelectorAll("button")).find((item) => item.textContent?.trim() === label);
  if (!result) throw new Error(`Button not found: ${label}`);
  return result;
}

async function click(target: HTMLElement) {
  await act(async () => { target.click(); });
}

async function typeInto(input: HTMLInputElement, value: string) {
  const setValue = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!;
  await act(async () => {
    setValue.call(input, value);
    input.dispatchEvent(new Event("input", { bubbles: true }));
  });
}

beforeEach(async () => {
  vi.resetAllMocks();
  clearResourceCache();
  resetFrameworkPageCache();
  localStorage.clear();
  // Groups start collapsed; most tests look at the rows inside DeepSeek Harness.
  rememberExpandedFrameworkGroups(new Set(["framework:dsh"]));
  vi.stubGlobal("IS_REACT_ACT_ENVIRONMENT", true);
  vi.mocked(activeMachineScope).mockReturnValue("local" as ReturnType<typeof activeMachineScope>);
  vi.mocked(activeRemoteID).mockReturnValue("");
  vi.mocked(api.frameworks).mockResolvedValue(frameworksResponse(false));
  vi.mocked(api.remoteHosts).mockResolvedValue([]);
  vi.mocked(api.frameworkHealth).mockResolvedValue({ results: [] });
  vi.mocked(api.checkFrameworkUpdate).mockResolvedValue({ kind: "dsh", installed: true, update_available: false });
  vi.mocked(api.frameworkAuth).mockResolvedValue({ kind: "dsh", state: "unknown" } as Awaited<ReturnType<typeof api.frameworkAuth>>);
  container = document.createElement("div");
  document.body.append(container);
  await mountPanel();
});

async function mountPanel() {
  root = createRoot(container);
  await act(async () => { root.render(<I18nProvider language="zh"><FrameworksPanel /></I18nProvider>); });
}

// Mounts the page again with freshly mocked data instead of the cached list.
async function remount() {
  await act(async () => { root.unmount(); });
  clearResourceCache();
  await mountPanel();
}

afterEach(async () => {
  await act(async () => { root.unmount(); });
  container.remove();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

describe("framework install", () => {
  it("closes the dialog on install and tracks progress in the catalog", async () => {
    const install = deferred<FrameworkInstallResult>();
    let report: ((update: OperationProgress) => void) | undefined;
    vi.mocked(api.installFramework).mockImplementation((_kind, _action, onProgress) => {
      report = onProgress;
      return install.promise;
    });

    await click(button(container, "安装框架"));
    const dialog = container.querySelector<HTMLElement>('[role="dialog"]')!;
    await click(button(dialog, "安装"));

    expect(api.installFramework).toHaveBeenCalledWith("dsh", "install", expect.any(Function), false, undefined, undefined);
    expect(container.querySelector('[role="dialog"]')).toBeNull();
    const table = container.querySelector<HTMLElement>(".framework-table")!;
    expect(table.querySelector(".framework-installing-row")?.textContent).toContain("DeepSeek Harness");
    expect(table.textContent).not.toContain("当前还没有安装任何框架");

    await act(async () => { report?.({ phase: "installing", percent: 30, detail: "npm install -g @deepseek-ai/dsh@latest" }); });
    const bar = table.querySelector<HTMLElement>('[role="progressbar"]')!;
    expect(bar.getAttribute("aria-valuenow")).toBe("30");
    expect(table.textContent).toContain("正在下载并安装");

    vi.mocked(api.frameworks).mockResolvedValue(frameworksResponse(true));
    await act(async () => { install.resolve({ kind: "dsh", action: "install", ok: true, version: "0.2.1-rc.5" }); });

    expect(table.querySelector(".framework-installing-row")).toBeNull();
    expect(table.querySelector('[role="progressbar"]')).toBeNull();
    expect(table.querySelector(".framework-installed-row")?.textContent).toContain("v0.2.1-rc.5");
    expect(container.querySelector(".session-notice")?.textContent).toBe("已安装");
  });

  it("keeps install progress and its outcome across leaving and returning to the page", async () => {
    const install = deferred<FrameworkInstallResult>();
    let report: ((update: OperationProgress) => void) | undefined;
    vi.mocked(api.installFramework).mockImplementation((_kind, _action, onProgress) => {
      report = onProgress;
      return install.promise;
    });
    await click(button(container, "安装框架"));
    await click(button(container.querySelector<HTMLElement>('[role="dialog"]')!, "安装"));
    await act(async () => { report?.({ phase: "installing", percent: 30 }); });

    // Navigate away, progress arrives meanwhile, then come back.
    await act(async () => { root.unmount(); });
    await act(async () => { report?.({ phase: "installing", percent: 55 }); });
    await mountPanel();
    const bar = container.querySelector<HTMLElement>('.framework-installing-row + .catalog-progress-row [role="progressbar"]');
    expect(bar?.getAttribute("aria-valuenow")).toBe("55");

    // Leave again and let the install finish while the page is not mounted.
    await act(async () => { root.unmount(); });
    vi.mocked(api.frameworks).mockResolvedValue(frameworksResponse(true));
    await act(async () => { install.resolve({ kind: "dsh", action: "install", ok: true, version: "0.2.1-rc.5", log: "added 618 packages" }); });
    await mountPanel();

    expect(container.querySelector(".framework-installing-row")).toBeNull();
    expect(container.querySelector(".framework-installed-row")?.textContent).toContain("v0.2.1-rc.5");
    expect(container.querySelector(".session-notice")?.textContent).toBe("已安装");
    expect(container.querySelector(".framework-log")?.textContent).toContain("added 618 packages");
    expect(api.installFramework).toHaveBeenCalledTimes(1);
  });

  it("drops the progress row and reports the error when an install fails", async () => {
    vi.mocked(api.installFramework).mockResolvedValue({
      kind: "dsh", action: "install", ok: false, error: "install failed: exit status 1",
      log: "npm error notarget No matching version found", command: "npm install -g @deepseek-ai/dsh@latest",
    });

    await click(button(container, "安装框架"));
    await click(button(container.querySelector<HTMLElement>('[role="dialog"]')!, "安装"));

    expect(container.querySelector('[role="dialog"]')).toBeNull();
    expect(container.querySelector(".framework-installing-row")).toBeNull();
    expect(container.querySelector(".session-notice.error")?.textContent).toContain("install failed: exit status 1");
    expect(container.querySelector(".framework-log")?.textContent).toContain("notarget");
  });
});

describe("install settings", () => {
  it("asks before running a custom install command and sends the confirmed text", async () => {
    const command = "pnpm add -g @deepseek-ai/dsh";
    vi.mocked(api.frameworks).mockResolvedValue({
      prereqs: { node: true, npm: true },
      frameworks: [{ ...dsh, commands: { install: { command, custom: true } } }],
    });
    vi.mocked(api.installFramework).mockResolvedValue({ kind: "dsh", action: "install", ok: false, error: "stop here" });
    await remount();

    await click(button(container, "安装框架"));
    await click(button(container.querySelector<HTMLElement>('[role="dialog"]')!, "安装"));
    let confirm = container.querySelector<HTMLElement>('[role="alertdialog"]')!;
    expect(confirm.textContent).toContain(command);
    await click(button(confirm, "取消"));
    expect(api.installFramework).not.toHaveBeenCalled();

    await click(button(container, "安装框架"));
    await click(button(container.querySelector<HTMLElement>('[role="dialog"]')!, "安装"));
    confirm = container.querySelector<HTMLElement>('[role="alertdialog"]')!;
    await click(button(confirm, "运行"));
    expect(api.installFramework).toHaveBeenCalledWith("dsh", "install", expect.any(Function), false, undefined, command);
  });

  it("saves registries and custom commands for this machine", async () => {
    vi.mocked(api.frameworkInstallSettings).mockResolvedValue({
      settings: {},
      frameworks: [{ kind: "dsh", display: "DeepSeek Harness", install_command: "npm install -g @deepseek-ai/dsh@latest", update_command: "npm install -g @deepseek-ai/dsh@latest", registry_applies: true }],
    });
    vi.mocked(api.saveFrameworkInstallSettings).mockResolvedValue({ settings: {}, frameworks: [] });

    await click(button(container, "安装设置"));
    const dialog = container.querySelector<HTMLElement>('[role="dialog"]')!;
    const inputs = Array.from(dialog.querySelectorAll<HTMLInputElement>("input"));
    expect(inputs[2].placeholder).toBe("npm install -g @deepseek-ai/dsh@latest");
    await typeInto(inputs[0], "https://bnpm.byted.org/");
    await typeInto(inputs[1], "https://registry.npmjs.org/");
    await typeInto(inputs[3], "npm install -g @deepseek-ai/dsh@next");
    const reloads = vi.mocked(api.frameworks).mock.calls.length;
    await click(button(dialog, "保存"));

    expect(api.saveFrameworkInstallSettings).toHaveBeenCalledWith({
      default_registry: "https://bnpm.byted.org/",
      frameworks: { dsh: { registry: "https://registry.npmjs.org/", update_command: "npm install -g @deepseek-ai/dsh@next" } },
    });
    expect(container.querySelector('[role="dialog"]')).toBeNull();
    expect(container.querySelector(".session-notice")?.textContent).toBe("安装设置已保存");
    expect(vi.mocked(api.frameworks).mock.calls.length).toBeGreaterThan(reloads);
  });

  it("only edits install settings for this machine", async () => {
    vi.mocked(activeMachineScope).mockReturnValue("ecs_cn" as ReturnType<typeof activeMachineScope>);
    vi.mocked(activeRemoteID).mockReturnValue("ecs_cn");
    await remount();
    expect(button(container, "安装设置").disabled).toBe(true);
  });

  it("lists every machine's version and updates only the outdated ones of a framework", async () => {
    vi.mocked(activeMachineScope).mockReturnValue("all" as ReturnType<typeof activeMachineScope>);
    vi.mocked(api.frameworks).mockResolvedValue({
      prereqs: { node: true, npm: true },
      frameworks: [
        { ...dsh, installed: true, version: "0.2.0", target_id: "home", target_name: "home-macmini" },
        { ...dsh, installed: true, version: "0.2.1", target_id: "local", target_name: "this-mac" },
      ],
    });
    vi.mocked(api.checkFrameworkUpdate).mockImplementation(async (_kind, targetID) => (
      targetID === "home"
        ? { kind: "dsh", installed: true, update_available: true, current_version: "0.2.0", latest_version: "0.2.1" }
        : { kind: "dsh", installed: true, update_available: false, latest_version: "0.2.1" }
    ));
    vi.mocked(api.installFramework).mockResolvedValue({ kind: "dsh", action: "update", ok: true });
    await remount();

    const rows = Array.from(container.querySelectorAll<HTMLElement>(".framework-instance-row"));
    expect(rows.map((row) => row.querySelector(".framework-machine-name")?.textContent)).toEqual(["本机", "home-macmini"]);
    expect(rows[0].textContent).toContain("v0.2.1");
    expect(rows[1].textContent).toContain("v0.2.0");
    expect(rows[1].className).toContain("outdated");
    const header = container.querySelector<HTMLElement>(".framework-group-row")!;
    expect(header.textContent).toContain("DeepSeek Harness");
    expect(header.textContent).toContain("2 台机器");

    await click(button(header, "更新 1 台"));
    expect(api.installFramework).toHaveBeenCalledTimes(1);
    expect(vi.mocked(api.installFramework).mock.calls[0][4]).toBe("home");
  });

  it("shows each framework's health record and turns periodic checks on for this machine", async () => {
    vi.mocked(api.frameworks).mockResolvedValue(frameworksResponse(true));
    vi.mocked(api.frameworkHealth).mockResolvedValue({
      results: [],
      records: [{
        kind: "dsh", state: "failing", error_kind: "unauthenticated", source: "run",
        reason: "not signed in: Not logged in · Please run /login", checked_at: "2026-11-15T08:00:00Z",
      }],
      checks: { enabled: false, interval_minutes: 30 },
    });
    vi.mocked(api.saveFrameworkHealthSettings).mockImplementation(async (settings) => settings);
    await remount();
    await act(async () => { await new Promise((resolve) => setTimeout(resolve, 0)); });

    expect(container.textContent).toContain("异常");
    expect(container.textContent).toContain("not signed in: Not logged in");
    expect(container.textContent).toContain("Agent 运行");

    const toggle = container.querySelector<HTMLInputElement>(".framework-health-checks input[type=checkbox]");
    expect(toggle?.checked).toBe(false);
    await click(toggle!);
    expect(api.saveFrameworkHealthSettings).toHaveBeenCalledWith({ enabled: true, interval_minutes: 30 }, undefined);
    expect(container.querySelector(".framework-health-checks select")).not.toBeNull();
  });

  it("starts with groups collapsed and groups by machine on request", async () => {
    rememberExpandedFrameworkGroups(new Set());
    vi.mocked(activeMachineScope).mockReturnValue("all" as ReturnType<typeof activeMachineScope>);
    const codex: Framework = { ...dsh, spec: { ...dsh.spec, kind: "codex", display: "Codex", company: "OpenAI" } };
    vi.mocked(api.frameworks).mockResolvedValue({
      prereqs: { node: true, npm: true },
      frameworks: [
        { ...dsh, installed: true, version: "0.2.0", target_id: "home", target_name: "home-macmini" },
        { ...dsh, installed: true, version: "0.2.1", target_id: "local", target_name: "this-mac" },
        { ...codex, installed: true, version: "0.170.0", target_id: "home", target_name: "home-macmini" },
      ],
    });
    vi.mocked(api.checkFrameworkUpdate).mockImplementation(async (kind, targetID) => (
      kind === "dsh" && targetID === "home"
        ? { kind, installed: true, update_available: true, current_version: "0.2.0", latest_version: "0.2.1" }
        : { kind, installed: true, update_available: false }
    ));
    await remount();

    let headers = Array.from(container.querySelectorAll<HTMLElement>(".framework-group-row"));
    expect(headers.map((row) => row.querySelector("strong")?.textContent)).toEqual(["Codex", "DeepSeek Harness"]);
    expect(container.querySelectorAll(".framework-instance-row")).toHaveLength(0);
    expect(headers[1].textContent).toContain("1 台可更新");

    await click(headers[1].querySelector<HTMLButtonElement>(".framework-group-toggle")!);
    expect(headers[1].querySelector(".framework-group-toggle")?.getAttribute("aria-expanded")).toBe("true");
    expect(container.querySelectorAll(".framework-instance-row")).toHaveLength(2);

    await click(button(container, "按机器"));
    headers = Array.from(container.querySelectorAll<HTMLElement>(".framework-group-row"));
    expect(headers.map((row) => row.querySelector("strong")?.textContent)).toEqual(["本机", "home-macmini"]);
    expect(headers[1].textContent).toContain("2 个框架");
    expect(headers[1].textContent).toContain("1 个可更新");
    expect(container.querySelectorAll(".framework-instance-row")).toHaveLength(0);

    await click(button(container, "全部展开"));
    const rows = Array.from(container.querySelectorAll<HTMLElement>(".framework-instance-row"));
    expect(rows.map((row) => row.querySelector(".framework-machine-name")?.textContent)).toEqual(["DeepSeek Harness", "Codex", "DeepSeek Harness"]);
    expect(localStorage.getItem("agentmux.frameworks.groupBy")).toBe("machine");
  });

  it("shows the last known list and update checks at once when the page opens again", async () => {
    vi.mocked(api.frameworks).mockResolvedValue(frameworksResponse(true));
    await remount();
    expect(api.checkFrameworkUpdate).toHaveBeenCalledTimes(1);
    await act(async () => { root.unmount(); });

    const pending = deferred<ReturnType<typeof frameworksResponse>>();
    vi.mocked(api.frameworks).mockReturnValue(pending.promise);
    await mountPanel();
    expect(container.querySelector(".framework-installed-row")?.textContent).toContain("v0.2.1-rc.5");
    expect(container.textContent).not.toContain("加载中");
    expect(api.checkFrameworkUpdate).toHaveBeenCalledTimes(1);
    await act(async () => { pending.resolve(frameworksResponse(true)); });
  });

  it("hides the periodic check setting when every machine is in view", async () => {
    vi.mocked(activeMachineScope).mockReturnValue("all" as ReturnType<typeof activeMachineScope>);
    await remount();
    expect(container.querySelector(".framework-health-checks")).toBeNull();
  });
});
