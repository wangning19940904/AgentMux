// @vitest-environment jsdom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { activeRemoteID, api, notifyRemoteHostsChanged, setActiveMachineScope, type RemoteHost } from "../api";
import { I18nProvider } from "../i18n";
import { RemoteHostsPanel } from "./RemoteHostsPanel";

vi.mock("../api", () => ({
  api: {
    remoteHosts: vi.fn(),
    localStatus: vi.fn(),
    statusRemoteHost: vi.fn(),
    deleteRemoteHost: vi.fn(),
    syncRemoteHostsFromSSHConfig: vi.fn(),
  },
  activeRemoteID: vi.fn(),
  setActiveMachineScope: vi.fn(),
  notifyRemoteHostsChanged: vi.fn(),
  RemoteConnectionError: class extends Error {},
}));

const hosts: RemoteHost[] = ["ecs_cn", "lemon_claw"].map((name) => ({
  id: name,
  name,
  host: `${name}.example.test`,
  port: 22,
  user: "tiger",
  remote_addr: "127.0.0.1:8765",
  trusted: true,
}));

function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (cause: Error) => void;
  const promise = new Promise<T>((res, rej) => { resolve = res; reject = rej; });
  return { promise, resolve, reject };
}

let container: HTMLDivElement;
let root: Root;

function button(parent: ParentNode, label: string): HTMLButtonElement {
  const result = Array.from(parent.querySelectorAll("button")).find((item) => item.textContent?.trim() === label);
  if (!result) throw new Error(`Button not found: ${label}`);
  return result;
}

function dialog(): HTMLElement {
  const result = container.querySelector<HTMLElement>('[role="alertdialog"]');
  if (!result) throw new Error("Delete confirmation not shown");
  return result;
}

async function click(target: HTMLElement) {
  await act(async () => { target.click(); });
}

async function openDelete() {
  const row = Array.from(container.querySelectorAll("article")).find((item) => item.textContent?.includes("lemon_claw"))!;
  const trigger = button(row, "删除");
  trigger.focus();
  await click(trigger);
  return trigger;
}

beforeEach(async () => {
  vi.resetAllMocks();
  vi.stubGlobal("IS_REACT_ACT_ENVIRONMENT", true);
  // Reproduce a desktop WebView without a working native confirmation dialog.
  vi.spyOn(window, "confirm").mockImplementation(() => { throw new Error("Native confirm unavailable"); });
  vi.mocked(activeRemoteID).mockReturnValue("ecs_cn");
  vi.mocked(api.remoteHosts).mockResolvedValue(hosts);
  vi.mocked(api.localStatus).mockResolvedValue({ version: "0.1.8" } as Awaited<ReturnType<typeof api.localStatus>>);
  vi.mocked(api.statusRemoteHost).mockResolvedValue({ status: { version: "0.1.8" } } as Awaited<ReturnType<typeof api.statusRemoteHost>>);
  container = document.createElement("div");
  document.body.append(container);
  root = createRoot(container);
  await act(async () => { root.render(<I18nProvider language="zh"><RemoteHostsPanel /></I18nProvider>); });
});

afterEach(async () => {
  await act(async () => { root.unmount(); });
  container.remove();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

describe("SSH machine deletion in the desktop console", () => {
  it("opens an in-app confirmation and cancels without calling the API", async () => {
    const trigger = await openDelete();
    expect(dialog().textContent).toContain("lemon_claw");
    expect(document.activeElement).toBe(button(dialog(), "取消"));
    expect(document.body.style.overflow).toBe("hidden");
    expect(api.deleteRemoteHost).not.toHaveBeenCalled();
    expect(window.confirm).not.toHaveBeenCalled();
    await click(button(dialog(), "取消"));
    expect(container.querySelector('[role="alertdialog"]')).toBeNull();
    expect(api.deleteRemoteHost).not.toHaveBeenCalled();
    expect(document.activeElement).toBe(trigger);
    expect(document.body.style.overflow).toBe("");
  });

  it("contains keyboard focus and supports Escape", async () => {
    await openDelete();
    await act(async () => {
      button(dialog(), "取消").dispatchEvent(new KeyboardEvent("keydown", { key: "Tab", shiftKey: true, bubbles: true }));
    });
    expect(document.activeElement).toBe(button(dialog(), "删除"));
    await act(async () => { dialog().dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true })); });
    expect(container.querySelector('[role="alertdialog"]')).toBeNull();
    expect(api.deleteRemoteHost).not.toHaveBeenCalled();
  });

  it("shows progress, blocks duplicate submissions, and updates the list and selector", async () => {
    const deletion = deferred<{ ok: boolean }>();
    vi.mocked(api.deleteRemoteHost).mockReturnValue(deletion.promise);
    await openDelete();
    const confirm = button(dialog(), "删除");
    await act(async () => { confirm.click(); confirm.click(); });
    expect(api.deleteRemoteHost).toHaveBeenCalledTimes(1);
    expect(api.deleteRemoteHost).toHaveBeenCalledWith("lemon_claw");
    expect(button(dialog(), "删除中…").disabled).toBe(true);
    expect(button(dialog(), "取消").disabled).toBe(true);
    await act(async () => { dialog().dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true })); });
    expect(dialog()).toBeTruthy();
    expect(setActiveMachineScope).not.toHaveBeenCalled();
    await act(async () => { deletion.resolve({ ok: true }); });
    expect(container.querySelector('[role="alertdialog"]')).toBeNull();
    expect(container.textContent).not.toContain("lemon_claw");
    expect(container.textContent).toContain("ecs_cn");
    expect(container.textContent).toContain("SSH 机器已移除。");
    expect(notifyRemoteHostsChanged).toHaveBeenCalledWith([hosts[0]]);
    expect(setActiveMachineScope).not.toHaveBeenCalled();
  });

  it("preserves the selected machine on failure and switches scope only after a successful retry", async () => {
    vi.mocked(activeRemoteID).mockReturnValue("lemon_claw");
    vi.mocked(api.deleteRemoteHost).mockRejectedValueOnce(new Error("Permission denied"));
    await openDelete();
    await click(button(dialog(), "删除"));
    expect(dialog().querySelector('[role="alert"]')?.textContent).toBe("Permission denied");
    expect(button(dialog(), "删除").disabled).toBe(false);
    expect(setActiveMachineScope).not.toHaveBeenCalled();
    expect(notifyRemoteHostsChanged).not.toHaveBeenCalled();
    expect(container.querySelectorAll("article")).toHaveLength(2);
    vi.mocked(api.deleteRemoteHost).mockResolvedValueOnce({ ok: true });
    await click(button(dialog(), "删除"));
    expect(setActiveMachineScope).toHaveBeenCalledTimes(1);
    expect(setActiveMachineScope).toHaveBeenCalledWith("all");
    expect(container.querySelectorAll("article")).toHaveLength(1);
  });

  it("ignores a stale list response that finishes after deletion", async () => {
    const refresh = deferred<RemoteHost[]>();
    vi.mocked(api.remoteHosts).mockReturnValueOnce(refresh.promise);
    vi.mocked(api.syncRemoteHostsFromSSHConfig).mockResolvedValue({ updated: 0, unchanged: 2, unmatched: 0, ambiguous: 0 } as Awaited<ReturnType<typeof api.syncRemoteHostsFromSSHConfig>>);
    await click(button(container, "刷新"));
    expect(api.remoteHosts).toHaveBeenCalledTimes(2);
    vi.mocked(api.deleteRemoteHost).mockResolvedValueOnce({ ok: true });
    await openDelete();
    await click(button(dialog(), "删除"));
    await act(async () => { refresh.resolve(hosts); });
    expect(container.querySelectorAll("article")).toHaveLength(1);
    expect(container.textContent).not.toContain("lemon_claw");
    expect(notifyRemoteHostsChanged).toHaveBeenCalledTimes(1);
    expect(notifyRemoteHostsChanged).toHaveBeenCalledWith([hosts[0]]);
  });
});
