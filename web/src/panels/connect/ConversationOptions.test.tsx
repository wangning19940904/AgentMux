// @vitest-environment jsdom
import { act } from "react";
import { createRoot } from "react-dom/client";
import { afterEach, expect, it, vi } from "vitest";
import { I18nProvider } from "../../i18n";
import { FeishuChannelOptions } from "./ChannelEditor";

afterEach(() => { vi.unstubAllGlobals(); document.body.replaceChildren(); });

it("lets a channel choose private and group modes independently and edit the queue limit", async () => {
  vi.stubGlobal("IS_REACT_ACT_ENVIRONMENT", true);
  localStorage.setItem("agentmux.lang", "zh");
  const container = document.createElement("div"); document.body.append(container);
  const root = createRoot(container); const updateConfig = vi.fn();
  await act(async () => { root.render(<I18nProvider language="zh"><FeishuChannelOptions draft={{ type: "feishu", config: {} }} updateConfig={updateConfig} codexAgent={false} /></I18nProvider>); });
  const selects = Array.from(container.querySelectorAll("select"));
  const privateMode = selects.find(select => Array.from(select.options).some(option => option.value === "thread"))!;
  const groupMode = selects.find(select => Array.from(select.options).some(option => option.value === "chat-topic"))!;
  expect(privateMode.value).toBe("chat"); expect(groupMode.value).toBe("chat-topic");
  expect(Array.from(privateMode.options).map(option => option.value)).toEqual(["chat", "thread", "group"]);
  await act(async () => { privateMode.value = "group"; privateMode.dispatchEvent(new Event("change", { bubbles: true })); });
  expect(updateConfig).toHaveBeenCalledWith("private_chat_mode", "group");
  await act(async () => { groupMode.value = "new-topic"; groupMode.dispatchEvent(new Event("change", { bubbles: true })); });
  expect(updateConfig).toHaveBeenCalledWith("group_chat_mode", "new-topic");
  expect(container.querySelector('input[type="number"][max="100"]')).not.toBeNull();
  await act(async () => root.unmount());
});
