// @vitest-environment jsdom
import { act, useState } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import type { AgentInstance } from "../../api";
import { I18nProvider, useI18n } from "../../i18n";
import { AgentConversationOptions } from "./AgentConversationOptions";
import { newAgent } from "./agentUtils";

let root: Root;
let container: HTMLDivElement;
const changes = vi.fn();
function Form({ initial, readOnly = false }: { initial: Partial<AgentInstance>; readOnly?: boolean }) {
  const { t } = useI18n();
  const [draft, setDraft] = useState(initial);
  return <AgentConversationOptions privateMode={draft.private_chat_mode} groupMode={draft.group_chat_mode}
    readOnly={readOnly} t={t}
    onPrivateModeChange={(mode) => { changes("private_chat_mode", mode); setDraft({ ...draft, private_chat_mode: mode }); }}
    onGroupModeChange={(mode) => { changes("group_chat_mode", mode); setDraft({ ...draft, group_chat_mode: mode }); }} />;
}
beforeEach(() => {
  vi.stubGlobal("IS_REACT_ACT_ENVIRONMENT", true);
  container = document.createElement("div"); document.body.append(container);
  root = createRoot(container); changes.mockClear();
});
afterEach(async () => {
  await act(async () => root.unmount()); container.remove(); vi.unstubAllGlobals();
});
it("shows Agent defaults and changes group and private modes independently", async () => {
  await act(async () => root.render(<I18nProvider language="zh"><Form initial={newAgent([])} /></I18nProvider>));
  expect(container.querySelector<HTMLInputElement>('input[value="chat-topic"]')?.checked).toBe(true);
  const select = container.querySelector("select")!;
  expect(select.value).toBe("chat");
  expect(container.textContent).toContain("在话题里发消息：机器人在该话题内回复，各话题使用独立上下文");
  expect(container.textContent).toContain("群内所有消息共用上下文");
  await act(async () => container.querySelector<HTMLInputElement>('input[value="new-topic"]')!.click());
  expect(changes).toHaveBeenLastCalledWith("group_chat_mode", "new-topic");
  expect(select.value).toBe("chat");
  await act(async () => { select.value = "group"; select.dispatchEvent(new Event("change", { bubbles: true })); });
  expect(changes).toHaveBeenLastCalledWith("private_chat_mode", "group");
  expect(container.querySelector<HTMLInputElement>('input[value="new-topic"]')?.checked).toBe(true);
});
it("keeps unset legacy Agent modes until explicitly selected", async () => {
  await act(async () => root.render(<I18nProvider language="zh"><Form initial={{}} /></I18nProvider>));
  expect(container.querySelector<HTMLInputElement>('input[value=""]')?.checked).toBe(true);
  expect(container.querySelector("select")!.value).toBe("");
  expect(changes).not.toHaveBeenCalled();
  await act(async () => container.querySelector<HTMLInputElement>('input[value="chat"]')!.click());
  expect(changes).toHaveBeenLastCalledWith("group_chat_mode", "chat");
  expect(container.querySelector("select")!.value).toBe("");
});
it("disables all conversation controls in read-only mode", async () => {
  await act(async () => root.render(<I18nProvider language="en"><Form initial={newAgent([])} readOnly /></I18nProvider>));
  expect(container.querySelector("fieldset")!.disabled).toBe(true);
  expect(container.querySelector("select")!.disabled).toBe(true);
  expect(container.textContent).toContain("Default regular group mode");
});
