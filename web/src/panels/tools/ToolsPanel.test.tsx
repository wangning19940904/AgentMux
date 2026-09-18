// @vitest-environment jsdom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { api, type CLIManagedTool } from "../../api";
import { I18nProvider } from "../../i18n";
import { ToolsPanel } from "../ToolsPanel";

let container: HTMLDivElement;
let root: Root;

function tool(id: string, targetID: string, targetName: string): CLIManagedTool {
  return {
    spec: { id, name: id === "lark" ? "Lark CLI" : "OpenCLI", bin: id, package: id, uninstall_supported: true },
    installed: true, version: targetID.startsWith("old") ? "1.0.92" : "1.0.95",
    target_id: targetID, target_name: targetName,
  };
}

beforeEach(() => {
  vi.stubGlobal("IS_REACT_ACT_ENVIRONMENT", true);
  localStorage.clear();
  const cli = [
    tool("lark", "old-a", "A machine"), tool("lark", "old-b", "B machine"),
    tool("lark", "latest", "C machine"), tool("lark", "local", "Local"),
    tool("opencli", "old-a", "A machine"),
  ];
  vi.spyOn(api, "tools").mockResolvedValue({ cli, bundles: [], frameworks: [], skills: [], mcp: [], marketplace: [] });
  vi.spyOn(api, "skillMarketplace").mockResolvedValue([]);
  vi.spyOn(api, "fleetTargets").mockResolvedValue(["old-a", "old-b", "latest", "local"].map((id) => ({
    id, name: id, kind: id === "local" ? "local" : "ssh", online: true, trusted: true,
  })));
  vi.spyOn(api, "checkCLIUpdate").mockImplementation(async (id, targetID) => ({
    id, installed: true, update_available: Boolean(targetID?.startsWith("old")),
  }));
  container = document.createElement("div");
  document.body.append(container);
  root = createRoot(container);
});

afterEach(async () => {
  await act(async () => { root.unmount(); });
  container.remove();
  localStorage.clear();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

async function render() {
  await act(async () => { root.render(<I18nProvider language="zh"><ToolsPanel /></I18nProvider>); });
}

function larkRow() {
  return [...container.querySelectorAll("tr.unified-tool-row")].find((row) => row.querySelector("strong")?.textContent === "Lark CLI")!;
}

function button(text: string) {
  const match = [...larkRow().querySelectorAll("button")].find((item) => item.textContent?.startsWith(text));
  if (!match) throw new Error(`Missing button: ${text}`);
  return match;
}

it("updates this tool across outdated machines, skips current versions, and continues after a failure", async () => {
  let failFirst!: (reason: Error) => void;
  const install = vi.spyOn(api, "installCLI")
    .mockImplementationOnce(() => new Promise((_, reject) => { failFirst = reject; }))
    .mockResolvedValue({ first: { id: "lark", action: "update", ok: true }, successes: [], errors: [] });
  await render();
  // The selected machine is current; the group action still covers other machines.
  await act(async () => { button("本机").click(); });
  expect(button("检查更新").disabled).toBe(false);
  const update = button("更新全部机器");
  expect(update.textContent).toBe("更新全部机器2");
  expect(update.disabled).toBe(false);
  await act(async () => { update.click(); update.click(); });
  expect(install).toHaveBeenCalledTimes(1);
  expect(update.disabled).toBe(true);
  await act(async () => { failFirst(new Error("SSH unavailable")); });
  expect(install.mock.calls.map(([id, action, , , targets]) => ({ id, action, targets }))).toEqual([
    { id: "lark", action: "update", targets: ["old-a"] },
    { id: "lark", action: "update", targets: ["old-b"] },
  ]);
  expect(container.textContent).toContain("成功 1 项，失败 1 项");
  expect(container.textContent).toContain("Lark CLI · A machine");
  expect(container.textContent).toContain("SSH unavailable");
  expect(container.textContent).toContain("Lark CLI · B machine");
});

it("preserves the existing selected-machine update action", async () => {
  const install = vi.spyOn(api, "installCLI").mockResolvedValue({
    first: { id: "lark", action: "update", ok: true }, successes: [], errors: [],
  });
  await render();
  await act(async () => { button("B machine").click(); });
  const update = [...larkRow().querySelectorAll("button")].find((item) => item.textContent === "更新")!;
  await act(async () => { update.click(); });
  expect(install).toHaveBeenCalledTimes(1);
  expect(install).toHaveBeenCalledWith("lark", "update", expect.any(Function), false, ["old-b"]);
});

it("does not offer an all-machines action in a single-machine scope", async () => {
  localStorage.setItem("agentmux:active-remote", "old-a");
  await render();
  expect(container.textContent).not.toContain("更新全部机器");
});
