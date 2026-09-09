// @vitest-environment jsdom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { api, type FleetSyncPreview, type MachineTarget, type Provider } from "../../api";
import { I18nProvider } from "../../i18n";
import { ProviderTransferForm } from "./ProviderTransfer";

const targets: MachineTarget[] = [
  { id: "local", name: "Local", kind: "local", trusted: true, online: true },
  { id: "ssh-a", name: "Machine A", kind: "ssh", trusted: true, online: true },
  { id: "offline", name: "Offline", kind: "ssh", trusted: true, online: false },
];
const provider: Provider = { id: "relay", name: "Relay", base_url: "https://example.test", enabled: false, target_id: "local" };
const preview: FleetSyncPreview = { plan_id: "plan-a", expires_at: "2099-01-01T00:00:00Z", source: targets[0], destinations: [{ target: targets[1], inspection: { resources: [{ type: "provider", key: "relay", name: "Relay", action: "add" }] } }] };
let root: Root;
let mount: HTMLDivElement;
const button = (name: string) => Array.from(mount.querySelectorAll("button")).find((button) => button.textContent === name)!;
const checkbox = () => mount.querySelector<HTMLInputElement>('input[type="checkbox"]')!;

beforeEach(async () => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
  mount = document.createElement("div"); document.body.append(mount); root = createRoot(mount);
  vi.spyOn(api, "previewFleetSync").mockResolvedValue(preview);
  vi.spyOn(api, "applyFleetSync").mockResolvedValue({ plan_id: "plan-a", targets: preview.destinations });
  await act(async () => root.render(<I18nProvider language="zh"><ProviderTransferForm mode="sync" providers={[provider]} targets={targets} fixedProvider={provider} defaultDestinationID="" onApplied={() => {}} /></I18nProvider>));
});
afterEach(async () => { await act(async () => root.unmount()); mount.remove(); vi.restoreAllMocks(); });

describe("provider transfer", () => {
  it("makes preview actionable after selecting a target, then requires a second click to apply", async () => {
    expect(button("预览").disabled).toBe(true);
    expect(mount.querySelectorAll<HTMLInputElement>('input[type="checkbox"]')[1].disabled).toBe(true);
    await act(async () => checkbox().click());
    expect(button("预览").disabled).toBe(false);
    await act(async () => button("预览").click());
    expect(api.previewFleetSync).toHaveBeenCalledWith(expect.objectContaining({ source_target_id: "local", destination_target_ids: ["ssh-a"], provider_ids: ["relay"], include_credentials: false }));
    expect(api.applyFleetSync).not.toHaveBeenCalled();
    expect(button("同步 Provider").disabled).toBe(false);
    await act(async () => button("同步 Provider").click());
    expect(api.applyFleetSync).toHaveBeenCalledWith("plan-a");
    expect(mount.textContent).toContain("已新增");
    expect(mount.textContent).toContain("同步处理结束");
  });
  it("explains why an existing configuration has no changes to apply", async () => {
    const existing = structuredClone(preview);
    existing.destinations[0].inspection.resources[0].action = "exists";
    vi.mocked(api.previewFleetSync).mockResolvedValue(existing);
    await act(async () => checkbox().click());
    await act(async () => button("预览").click());
    expect(button("同步 Provider").disabled).toBe(true);
    expect(mount.textContent).toContain("没有可新增的配置");
    expect(api.applyFleetSync).not.toHaveBeenCalled();
  });
  it("invalidates a completed preview when credentials change", async () => {
    await act(async () => checkbox().click());
    await act(async () => button("预览").click());
    await act(async () => mount.querySelector<HTMLInputElement>(".provider-transfer-credential input")!.click());
    expect(button("同步 Provider")).toBeUndefined();
    await act(async () => button("预览").click());
    expect(api.previewFleetSync).toHaveBeenLastCalledWith(expect.objectContaining({ include_credentials: true }));
    expect(api.applyFleetSync).not.toHaveBeenCalled();
  });
  it("discards an in-flight preview if its destination selection changes", async () => {
    let finish!: (value: FleetSyncPreview) => void;
    vi.mocked(api.previewFleetSync).mockReturnValue(new Promise((resolve) => { finish = resolve; }));
    await act(async () => checkbox().click());
    await act(async () => button("预览").click());
    await act(async () => checkbox().click());
    await act(async () => finish(preview));
    expect(button("同步 Provider")).toBeUndefined();
    expect(button("预览").disabled).toBe(true);
  });
});
