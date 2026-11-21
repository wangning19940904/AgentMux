// @vitest-environment jsdom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { I18nProvider } from "../i18n";
import { DiagnosticsSettings } from "./DiagnosticsSettings";

let container: HTMLDivElement;
let root: Root;

beforeEach(() => {
  vi.stubGlobal("IS_REACT_ACT_ENVIRONMENT", true);
  container = document.createElement("div");
  document.body.append(container);
  root = createRoot(container);
});

afterEach(() => {
  act(() => root.unmount());
  container.remove();
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

describe("DiagnosticsSettings", () => {
  it("downloads the local zip and lists its files", async () => {
    const fetchMock = vi.fn().mockResolvedValue(new Response(new Blob(["zip"]), {
      status: 200,
      headers: {
        "Content-Type": "application/zip",
        "Content-Disposition": 'attachment; filename="agentmux-diagnostics-20261120-120000.zip"',
        "X-AgentMux-Diagnostics-Files": JSON.stringify([{ name: "manifest.json", size: 812 }, { name: "config.json", size: 2048 }]),
      },
    }));
    vi.stubGlobal("fetch", fetchMock);
    const createObjectURL = vi.fn(() => "blob:diagnostics");
    vi.stubGlobal("URL", Object.assign(URL, { createObjectURL, revokeObjectURL: vi.fn() }));
    const click = vi.spyOn(HTMLAnchorElement.prototype, "click").mockImplementation(() => {});

    act(() => root.render(<I18nProvider language="en"><DiagnosticsSettings /></I18nProvider>));
    const toggle = container.querySelector<HTMLInputElement>("input[role=switch]")!;
    act(() => toggle.click());
    const button = [...container.querySelectorAll("button")].find((b) => /diagnos|诊断/i.test(b.textContent ?? ""))!;
    await act(async () => { button.click(); });

    expect(fetchMock).toHaveBeenCalledWith("/api/v1/diagnostics?include_content=1", expect.anything());
    expect(click).toHaveBeenCalledTimes(1);
    expect(createObjectURL).toHaveBeenCalledTimes(1);
    const text = container.textContent ?? "";
    expect(text).toContain("agentmux-diagnostics-20261120-120000.zip");
    expect(text).toContain("manifest.json");
    expect(text).toContain("2.0 KB");
  });

  it("shows why the download failed", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response(JSON.stringify({ error: "this endpoint requires AgentMux administrator credentials" }), { status: 403 })));
    act(() => root.render(<I18nProvider language="en"><DiagnosticsSettings /></I18nProvider>));
    const button = [...container.querySelectorAll("button")].find((b) => /diagnos|诊断/i.test(b.textContent ?? ""))!;
    await act(async () => { button.click(); });
    expect(container.querySelector("[role=alert]")?.textContent).toContain("administrator credentials");
  });
});
