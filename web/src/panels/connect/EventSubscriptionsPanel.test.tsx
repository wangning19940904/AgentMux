// @vitest-environment jsdom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, expect, it, vi } from "vitest";
import { I18nProvider } from "../../i18n";
import {
  EventSubscriptionsPanel,
  splitEventValues,
} from "./EventSubscriptionsPanel";
import { api } from "../../api";

vi.mock("../../api", () => ({
  fleetMode: () => false,
  api: {
    eventSources: vi.fn(async () => [
      {
        ref: "channel:ch",
        name: "Shared bot",
        sources: ["feishu", "agentmux"],
        event_types: [],
        connected: true,
        ingestion: { failures: 0 },
      },
    ]),
    eventSubscriptions: vi.fn(async () => []),
    eventDeliveries: vi.fn(async () => ({ items: [], has_more: false })),
    saveEventSubscription: vi.fn(async () => ({
      subscription: { id: "sub" },
      signing_secret: "shown-once",
    })),
  },
}));
let root: Root | undefined;
afterEach(async () => {
  if (root) await act(async () => root?.unmount());
  root = undefined;
  vi.unstubAllGlobals();
  document.body.replaceChildren();
});

it("shows local-save limitations and only transiently displays the secret", async () => {
  vi.stubGlobal("IS_REACT_ACT_ENVIRONMENT", true);
  const container = document.createElement("div");
  document.body.append(container);
  root = createRoot(container);
  await act(async () =>
    root!.render(
      <I18nProvider language="zh">
        <EventSubscriptionsPanel />
      </I18nProvider>,
    ),
  );
  expect(container.textContent).toContain("配置事件");
  const button = (text: string) =>
    Array.from(container.querySelectorAll("button")).find(
      (item) => item.textContent === text,
    )!;
  await act(async () => button("新建订阅").click());
  const form = container.querySelector("form")!;
  expect(
    form.querySelector('input[type="url"]')?.getAttribute("value"),
  ).toContain("127.0.0.1");
  await act(async () =>
    form.dispatchEvent(
      new Event("submit", { bubbles: true, cancelable: true }),
    ),
  );
  expect(api.saveEventSubscription).toHaveBeenCalled();
  expect(container.textContent).toContain("shown-once");
  expect(container.textContent).toContain("不代表飞书平台权限已验证");
  expect(JSON.stringify(localStorage)).not.toContain("shown-once");
  await act(async () => button("已保存，隐藏密钥").click());
  expect(container.textContent).not.toContain("shown-once");
});
it("deduplicates exact event names without accepting empty entries", () => {
  expect(splitEventValues(" changed,\nchanged\nother, ")).toEqual([
    "changed",
    "other",
  ]);
});
