// @vitest-environment jsdom
import { act } from "react";
import { createRoot } from "react-dom/client";
import { afterEach, expect, it, vi } from "vitest";
import { I18nProvider } from "../../i18n";
import { FeishuChannelOptions } from "./ChannelEditor";

afterEach(() => { vi.unstubAllGlobals(); document.body.replaceChildren(); });

it("directs conversation mode changes to the Agent and keeps the channel queue limit", async () => {
  vi.stubGlobal("IS_REACT_ACT_ENVIRONMENT", true);
  localStorage.setItem("agentmux.lang", "zh");
  const container = document.createElement("div"); document.body.append(container);
  const root = createRoot(container); const updateConfig = vi.fn();
  await act(async () => { root.render(<I18nProvider language="zh"><FeishuChannelOptions draft={{ type: "feishu", config: {} }} updateConfig={updateConfig} codexAgent={false} /></I18nProvider>); });
  const selects = Array.from(container.querySelectorAll("select"));
  expect(selects.some(select => Array.from(select.options).some(option => option.value === "thread" || option.value === "chat-topic"))).toBe(false);
  expect(container.textContent).toContain("Agent → 编辑 → 会话模式");
  expect(container.querySelector('input[type="number"][max="100"]')).not.toBeNull();
  await act(async () => root.unmount());
});
