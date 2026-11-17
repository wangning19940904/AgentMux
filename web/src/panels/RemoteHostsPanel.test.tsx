// @vitest-environment jsdom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { activeRemoteID, api, notifyRemoteHostsChanged, RemoteConnectionError, setActiveMachineScope, type RemoteHost } from "../api";
import { I18nProvider } from "../i18n";
import { RemoteHostsPanel } from "./RemoteHostsPanel";

vi.mock("../api", () => ({
  api: {
    remoteHosts: vi.fn(),
    localStatus: vi.fn(),
    hubReplication: vi.fn(),
    fleet: vi.fn(),
    statusRemoteHost: vi.fn(),
    deleteRemoteHost: vi.fn(),
    syncRemoteHostsFromSSHConfig: vi.fn(),
    discoveredRemoteHosts: vi.fn(),
    importRemoteHost: vi.fn(),
    createDevicePairingCode: vi.fn(),
    addPassiveDevice: vi.fn(),
    authSession: vi.fn(),
    pairedDevices: vi.fn(),
    accounts: vi.fn(),
    assignDeviceOwner: vi.fn(),
    hubClientLink: vi.fn(),
  },
  APIError: class extends Error {},
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
  vi.mocked(api.hubReplication).mockResolvedValue({ enabled: false });
  vi.mocked(api.fleet).mockRejectedValue(new Error("no fleet"));
  vi.mocked(api.statusRemoteHost).mockResolvedValue({ status: { version: "0.1.8" } } as Awaited<ReturnType<typeof api.statusRemoteHost>>);
  container = document.createElement("div");
  document.body.append(container);
  root = createRoot(container);
  await act(async () => { root.render(<I18nProvider language="zh"><RemoteHostsPanel /></I18nProvider>); });
});

describe("SSH proxy imports", () => {
  it.each([
    { proxy_jump: "bastion" },
    { proxy_command: true },
  ])("allows import and retains fingerprint confirmation for %j", async (proxy) => {
    const discovered = {
      name: "private-box", ssh_alias: "private-box", host: "127.0.0.1", port: 22022,
      user: "tester", source: "~/.ssh/config", ...proxy,
    };
    vi.mocked(api.discoveredRemoteHosts).mockResolvedValue([discovered]);
    const untrusted = Object.assign(new Error("Confirm host key"), {
      code: "host_key_untrusted", fingerprint: "SHA256:target-key",
    });
    Object.setPrototypeOf(untrusted, RemoteConnectionError.prototype);
    vi.mocked(api.importRemoteHost).mockRejectedValueOnce(untrusted);
    await click(button(container, "连接远程服务器"));
    const row = container.querySelector<HTMLElement>(".remote-discovered-card")!;
    expect(row.textContent).toContain("使用 SSH 代理");
    expect(button(row, "导入").disabled).toBe(false);
    await click(button(row, "导入"));
    expect(api.importRemoteHost).toHaveBeenCalledWith(discovered, false, {});
    expect(row.textContent).toContain("SHA256:target-key");
    expect(api.importRemoteHost).toHaveBeenCalledTimes(1);
    vi.mocked(api.importRemoteHost).mockResolvedValueOnce({ installed: false } as Awaited<ReturnType<typeof api.importRemoteHost>>);
    await click(button(row, "信任并继续"));
    expect(api.importRemoteHost).toHaveBeenLastCalledWith(discovered, true, {});
    expect(row.textContent).not.toContain("SHA256:target-key");
  });

  it("keeps the existing profile and AgentMux address when changing its SSH alias", async () => {
    const existing = { ...hosts[0], remote_addr: "127.0.0.1:12101" };
    const discovered = {
      name: "home-box", ssh_alias: "home-box", host: "box-auto", port: 22,
      user: existing.user, source: "~/.ssh/config", proxy_command: true,
    };
    vi.mocked(api.remoteHosts).mockResolvedValueOnce([existing]);
    vi.mocked(api.syncRemoteHostsFromSSHConfig).mockResolvedValueOnce({
      hosts: [existing], updated: 0, unchanged: 1, unmatched: 0, ambiguous: 0,
    });
    await click(button(container, "刷新"));
    vi.mocked(api.discoveredRemoteHosts).mockResolvedValueOnce([discovered]);
    const updated = { ...existing, name: discovered.name, ssh_alias: discovered.ssh_alias };
    vi.mocked(api.importRemoteHost).mockResolvedValueOnce({
      ok: true, installed: false, host: updated, name: updated.name, latency_ms: 1, host_key_fingerprint: "test",
    });
    vi.mocked(api.remoteHosts).mockResolvedValueOnce([updated]);
    await click(button(container, "编辑"));
    await click(button(container, "更换 SSH Config 别名"));
    const importDialog = container.querySelector<HTMLElement>('[role="dialog"]')!;
    expect(importDialog.querySelector("input")?.value).toBe(existing.remote_addr);
    await click(button(importDialog, "验证并更换"));
    expect(api.importRemoteHost).toHaveBeenCalledWith(discovered, false, {
      existingID: existing.id, remoteAddr: existing.remote_addr,
    });
    expect(container.querySelector('[role="dialog"]')).toBeNull();
    expect(notifyRemoteHostsChanged).toHaveBeenLastCalledWith([updated]);
  });
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
    expect(container.querySelectorAll("article:not(.local-machine-card)")).toHaveLength(2);
    vi.mocked(api.deleteRemoteHost).mockResolvedValueOnce({ ok: true });
    await click(button(dialog(), "删除"));
    expect(setActiveMachineScope).toHaveBeenCalledTimes(1);
    expect(setActiveMachineScope).toHaveBeenCalledWith("all");
    expect(container.querySelectorAll("article:not(.local-machine-card)")).toHaveLength(1);
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
    expect(container.querySelectorAll("article:not(.local-machine-card)")).toHaveLength(1);
    expect(container.textContent).not.toContain("lemon_claw");
    expect(notifyRemoteHostsChanged).toHaveBeenCalledTimes(1);
    expect(notifyRemoteHostsChanged).toHaveBeenCalledWith([hosts[0]]);
  });
});

describe("machines that connect to the hub themselves", () => {
  it("shows on a standby what the primary hub sees, not its own failed probes", async () => {
    const linked = (id: string, name: string): RemoteHost => ({ id, name, host: "", port: 0, user: "", remote_addr: "", trusted: true, kind: "link", online: false });
    vi.mocked(api.remoteHosts).mockResolvedValue([linked("dev_up", "up-box"), linked("dev_down", "down-box")]);
    vi.mocked(api.statusRemoteHost).mockRejectedValue(new Error("device is offline"));
    vi.mocked(api.fleet).mockResolvedValue({
      source: "hub",
      fetched_at: "2026-11-15T01:00:00Z",
      machines: [
        { id: "dev_up", name: "up-box", online: true },
        { id: "dev_down", name: "down-box", online: false, last_seen_at: "2026-11-14T01:00:00Z" },
        { id: "dev_node", name: "other-node", online: true, version: "0.1.0" },
      ],
    });
    await act(async () => { root.render(<I18nProvider language="zh"><RemoteHostsPanel key="standby" /></I18nProvider>); });
    const row = (name: string) => Array.from(container.querySelectorAll("article")).find((item) => item.textContent?.includes(name))!;
    expect(row("up-box").textContent).toContain("健康");
    expect(row("up-box").textContent).toContain("连接到 Hub");
    expect(row("down-box").textContent).toContain("离线");
    expect(row("down-box").textContent).not.toContain("异常");
    expect(row("other-node").textContent).toContain("经由 Hub 获知");
    expect(row("other-node").querySelectorAll("button")).toHaveLength(0);
  });

  it("shows a linked machine without SSH details or SSH-only actions", async () => {
    const linked: RemoteHost = { id: "dev_1", name: "home-box", host: "", port: 0, user: "", remote_addr: "", trusted: true, kind: "link", online: true };
    vi.mocked(api.remoteHosts).mockResolvedValue([hosts[0], linked]);
    // An older version would offer an SSH update on an SSH machine.
    vi.mocked(api.statusRemoteHost).mockResolvedValue({ status: { version: "0.1.7" } } as Awaited<ReturnType<typeof api.statusRemoteHost>>);
    await act(async () => { root.render(<I18nProvider language="zh"><RemoteHostsPanel key="reload" /></I18nProvider>); });
    const rows = Array.from(container.querySelectorAll("article"));
    const ssh = rows.find((row) => row.textContent?.includes("ecs_cn"))!;
    const row = rows.find((row) => row.textContent?.includes("home-box"))!;
    expect(row.textContent).toContain("主动连接到本机");
    expect(row.textContent).not.toContain("@");
    expect(Array.from(row.querySelectorAll("button")).map((item) => item.textContent?.trim())).toEqual(["检查更新", "删除"]);
    expect(Array.from(ssh.querySelectorAll("button")).map((item) => item.textContent?.trim())).toEqual(["更新", "编辑", "删除"]);
  });

  it("labels the standby hub and this machine's hub role", async () => {
    const standby: RemoteHost = { id: "dev_standby", name: "standby-mac", host: "", port: 0, user: "", remote_addr: "", trusted: true, kind: "link", online: true, hub_role: "standby" };
    vi.mocked(api.remoteHosts).mockResolvedValue([hosts[0], standby]);
    vi.mocked(api.hubReplication).mockResolvedValue({ enabled: true, hub: true, name: "this-mac", status: { role: "primary" } });
    await act(async () => { root.render(<I18nProvider language="zh"><RemoteHostsPanel key="hub" /></I18nProvider>); });
    // This machine is listed first, as the local hub, with nothing to manage.
    const local = container.querySelector("article.local-machine-card")!;
    expect(container.querySelector("article")).toBe(local);
    expect(local.textContent).toContain("this-mac");
    expect(local.textContent).toContain("本机 Hub");
    expect(local.querySelectorAll("button")).toHaveLength(0);
    expect(container.querySelector(".remote-machines-heading .pill")?.textContent).toBe("3");
    const row = Array.from(container.querySelectorAll("article")).find((item) => item.textContent?.includes("standby-mac"))!;
    expect(row.textContent).toContain("备用 Hub");
    // It is still a machine like any other: checked and updatable.
    expect(Array.from(row.querySelectorAll("button")).map((item) => item.textContent?.trim())).toEqual(["检查更新", "删除"]);
    expect(vi.mocked(api.statusRemoteHost).mock.calls.some(([id]) => id === "dev_standby")).toBe(true);
    // Deleting it first explains what that breaks.
    await click(button(row, "删除"));
    expect(container.querySelector('[role="alertdialog"]')?.textContent).toContain("无法接管");
    expect(api.deleteRemoteHost).not.toHaveBeenCalled();
  });

  it("creates a one-time join command", async () => {
    vi.mocked(api.createDevicePairingCode).mockResolvedValue({
      code: "ABCD2345", expires_at: "2026-11-12T12:30:00Z", hub_url: "https://10.0.0.2:8766",
      join_token: "amxj_x", command: "amux join amxj_x", warning: "Use a trusted network.",
    });
    await click(button(container, "配对一台机器"));
    await click(button(container, "生成连接命令"));
    expect(api.createDevicePairingCode).toHaveBeenCalledWith("");
    const card = container.querySelector<HTMLElement>(".remote-pairing")!;
    expect(card.querySelector("code")?.textContent).toBe("amux join amxj_x");
    expect(card.textContent).toContain("https://10.0.0.2:8766");
    expect(card.textContent).toContain("Use a trusted network.");
    // Closing refreshes the list: the machine may have joined meanwhile.
    const before = vi.mocked(api.remoteHosts).mock.calls.length;
    await click(button(card, "关闭"));
    expect(container.querySelector(".remote-pairing")).toBeNull();
    expect(vi.mocked(api.remoteHosts).mock.calls.length).toBe(before + 1);
  });
});

describe("device owners", () => {
  it("lets an administrator hand a paired machine to an account", async () => {
    const linked: RemoteHost = { id: "dev_mini", name: "mini", host: "", port: 0, user: "", remote_addr: "", kind: "link", trusted: true };
    vi.mocked(api.remoteHosts).mockResolvedValue([...hosts, linked]);
    vi.mocked(api.authSession).mockResolvedValue({ admin: true, accounts_enabled: true } as Awaited<ReturnType<typeof api.authSession>>);
    vi.mocked(api.pairedDevices).mockResolvedValue([{ id: "dev_mini", name: "mini", created_at: "", online: true }]);
    vi.mocked(api.accounts).mockResolvedValue([
      { id: "acc_1", login: "ning", display_name: "Ning", role: "member", status: "active" },
    ] as Awaited<ReturnType<typeof api.accounts>>);
    vi.mocked(api.assignDeviceOwner).mockResolvedValue({ ok: true });
    await act(async () => { root.render(<I18nProvider language="zh"><RemoteHostsPanel key="owners" /></I18nProvider>); });

    const select = container.querySelector<HTMLSelectElement>('select[aria-label="mini 的所有者"]')!;
    expect(select.value).toBe("");
    // SSH machines are not paired devices and have no owner.
    expect(container.querySelectorAll("select.remote-host-owner")).toHaveLength(1);
    await act(async () => {
      select.value = "acc_1";
      select.dispatchEvent(new Event("change", { bubbles: true }));
    });
    expect(api.assignDeviceOwner).toHaveBeenCalledWith("dev_mini", "acc_1");
  });
});

describe("connecting a phone", () => {
  it("opens the phone app's link from the Devices page", async () => {
    vi.mocked(api.hubClientLink).mockResolvedValue({
      link: "agentmux://hub?url=https%3A%2F%2F10.0.0.2%3A8766", hub_url: "https://10.0.0.2:8766", reach: "public",
    } as Awaited<ReturnType<typeof api.hubClientLink>>);
    await click(button(container, "连接手机"));
    expect(container.querySelector(".phone-app-link.inline")?.textContent).toContain("https://10.0.0.2:8766");
    expect(container.querySelector(".phone-app-link.inline")).not.toBeNull();
    await click(button(container.querySelector<HTMLElement>(".phone-app-link.inline")!, "关闭"));
    expect(container.querySelector(".phone-app-link.inline")).toBeNull();
  });
});

describe("machines this hub dials", () => {
  beforeEach(async () => {
    await act(async () => { root.render(<I18nProvider language="en"><RemoteHostsPanel key="passive" /></I18nProvider>); });
  });

  it("adds a machine that listens from its invite", async () => {
    vi.mocked(api.addPassiveDevice).mockResolvedValue({
      device: { id: "dev_p", name: "build server" }, replaced: false, online: true,
      passive: { addrs: ["192.0.2.5:8767"], fingerprint: "ab".repeat(32), state: "connected" },
    });
    await click(button(container, "Pair a machine"));
    const card = container.querySelector<HTMLElement>(".remote-pairing")!;
    const add = button(card, "Add and dial");
    // Only an invite can be added.
    expect(add.disabled).toBe(true);
    const input = card.querySelector<HTMLInputElement>('input[aria-label="Invite (amxi_…)"]')!;
    await act(async () => {
      const setter = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!;
      setter.call(input, " amxi_invite ");
      input.dispatchEvent(new Event("input", { bubbles: true }));
    });
    expect(add.disabled).toBe(false);
    await click(add);
    expect(api.addPassiveDevice).toHaveBeenCalledWith("amxi_invite", "");
    expect(container.querySelector(".remote-pairing")).toBeNull();
    expect(container.textContent).toContain("build server was added; this hub dials it.");
  });

  it("shows a machine reached only this way as one the hub dials", async () => {
    vi.mocked(api.remoteHosts).mockResolvedValue([{ ...hosts[0], id: "dev_p", name: "listener", kind: "link", online: true, link_mode: "passive" }]);
    await act(async () => { root.render(<I18nProvider language="en"><RemoteHostsPanel key="passive-list" /></I18nProvider>); });
    const row = Array.from(container.querySelectorAll("article")).find((item) => item.textContent?.includes("listener"))!;
    expect(row.textContent).toContain("Listens; this hub dials it");
  });
});

describe("machines reached more than one way", () => {
  const merged: RemoteHost = {
    ...hosts[1],
    machine_uid: "0f8fad5b-d9cb-469f-a165-70867728950e",
    paths: [
      { via: "ssh", host_id: "lemon_claw", name: "lemon_claw", ok: true, rtt_ms: 12.5, active: false },
      { via: "link", host_id: "dev_lemon", name: "lemon (link)", ok: true, rtt_ms: 3.1, active: true },
      { via: "relay", host_id: "dev_lemon", name: "lemon (link)", ok: false, active: false, error: "offline at the hub" },
    ],
  };

  beforeEach(async () => {
    vi.mocked(api.remoteHosts).mockResolvedValue([hosts[0], merged]);
    await act(async () => { root.render(<I18nProvider language="en"><RemoteHostsPanel key="merged" /></I18nProvider>); });
  });

  it("lists the machine once with every path, its round trip and the one in use", () => {
    const rows = Array.from(container.querySelectorAll("article")).filter((item) => item.textContent?.includes("lemon"));
    expect(rows).toHaveLength(1);
    const paths = Array.from(rows[0].querySelectorAll(".remote-host-paths li"));
    expect(paths.map((item) => item.textContent?.trim())).toEqual([
      "SSH · 12.5 ms", "its link · 3.1 ms · in use", "through the hub",
    ]);
    expect(paths[1].className).toContain("active");
    expect(paths[2].className).toContain("danger");
    expect(paths[2].getAttribute("title")).toBe("offline at the hub");
  });

  it("removes one path and keeps the machine", async () => {
    vi.mocked(api.deleteRemoteHost).mockResolvedValue({ ok: true });
    const row = Array.from(container.querySelectorAll("article")).find((item) => item.textContent?.includes("lemon"))!;
    const remove = row.querySelector<HTMLButtonElement>('button[aria-label="Remove the its link path"]')!;
    // The entry the machine is listed as, and the hub's relay, are not removed here.
    expect(row.querySelectorAll(".remote-host-paths button")).toHaveLength(1);
    await click(remove);
    vi.mocked(api.remoteHosts).mockResolvedValue([hosts[0], { ...hosts[1], machine_uid: merged.machine_uid }]);
    await click(button(dialog(), "Delete"));
    expect(api.deleteRemoteHost).toHaveBeenCalledWith("dev_lemon");
    const rows = Array.from(container.querySelectorAll("article")).filter((item) => item.textContent?.includes("lemon"));
    expect(rows).toHaveLength(1);
    expect(rows[0].querySelector(".remote-host-paths")).toBeNull();
  });
});
