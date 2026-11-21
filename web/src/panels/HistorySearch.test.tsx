// @vitest-environment jsdom
import { act } from "react";
import { createRoot } from "react-dom/client";
import { afterEach, expect, it, vi } from "vitest";
import { api } from "../api";
import { I18nProvider } from "../i18n";
import { HistorySearch, historyRequest } from "./HistorySearch";

afterEach(() => {
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

it("sends one wording per line and a time hint only for a full range", () => {
  expect(historyRequest("", " 迁移 \n\nmigration\n", "", "")).toEqual({ queries: ["迁移", "migration"] });
  const ranged = historyRequest("a1", "迁移", "2026-11-10", "2026-11-12");
  expect(ranged.agent_id).toBe("a1");
  expect(new Date(ranged.around!.from).getDate()).toBe(10);
  expect(new Date(ranged.around!.to).getDate()).toBe(12);
  expect(historyRequest("", "迁移", "2026-11-12", "2026-11-10").around).toBeUndefined();
  expect(historyRequest("", "迁移", "2026-11-12", "").around).toBeUndefined();
});

it("shows both lists with their snippets", async () => {
  vi.stubGlobal("IS_REACT_ACT_ENVIRONMENT", true);
  vi.spyOn(api, "agentInstances").mockResolvedValue([{ id: "a1", name: "Ops" } as never]);
  const searchHistory = vi.spyOn(api, "searchHistory").mockResolvedValue({
    memory: [{ id: "mem:1", kind: "memory", memory_scope: "agent:a1", time: "2026-11-18T08:00:00Z", snippet: "李雷负责数据库迁移", score: 0.03, matched_queries: [0, 1] }],
    originals: [{ id: "turn:t:answer", kind: "turn_answer", agent_id: "a1", user_name: "Alice", time: "2026-11-12T08:00:00Z", snippet: "迁移改到周二", score: 0.02, matched_queries: [0], in_time_range: true }],
    notes: ["originals cover turns since 2026-10-21; older turns have expired"],
  });
  const container = document.createElement("div");
  const root = createRoot(container);
  try {
    await act(async () => {
      root.render(<I18nProvider language="en"><HistorySearch /></I18nProvider>);
    });
    const textarea = container.querySelector("textarea")!;
    await act(async () => {
      const setter = Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, "value")!.set!;
      setter.call(textarea, "数据库迁移\ndatabase migration");
      textarea.dispatchEvent(new Event("input", { bubbles: true }));
    });
    await act(async () => {
      container.querySelector("form")!.dispatchEvent(new Event("submit", { bubbles: true, cancelable: true }));
    });
    expect(searchHistory).toHaveBeenCalledWith({ queries: ["数据库迁移", "database migration"] });
    const text = container.textContent ?? "";
    expect(text).toContain("李雷负责数据库迁移");
    expect(text).toContain("迁移改到周二");
    expect(text).toContain("Ops");
    expect(text).toContain("in the time asked about");
    expect(text).toContain("found by 2 of the wordings");
    expect(text).toContain("older turns have expired");
  } finally {
    act(() => root.unmount());
  }
});
