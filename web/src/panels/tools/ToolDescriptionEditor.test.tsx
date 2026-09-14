// @vitest-environment jsdom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { I18nProvider } from "../../i18n";
import { ToolDescriptionEditor } from "./ToolDescriptionEditor";

let container: HTMLDivElement;
let root: Root;
const onSave = vi.fn<(description: string) => Promise<string>>();

async function render(description = "原始描述", instance = "local") {
  await act(async () => {
    root.render(<I18nProvider language="zh"><ToolDescriptionEditor key={instance} name="GitHub CLI"
      description={description} kind="cli" disabled={false} onSave={onSave} /></I18nProvider>);
  });
}

async function click(label: string) {
  const button = Array.from(container.querySelectorAll("button")).find((item) => item.textContent === label || item.getAttribute("aria-label") === label);
  if (!button) throw new Error(`missing button ${label}`);
  await act(async () => { button.click(); });
}

async function edit(value: string) {
  const input = container.querySelector("textarea")!;
  await act(async () => {
    Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, "value")!.set!.call(input, value);
    input.dispatchEvent(new Event("input", { bubbles: true }));
  });
}

beforeEach(async () => {
  vi.stubGlobal("IS_REACT_ACT_ENVIRONMENT", true);
  onSave.mockReset().mockImplementation(async (value) => value.trim());
  container = document.createElement("div");
  document.body.append(container);
  root = createRoot(container);
  await render();
});

afterEach(async () => {
  await act(async () => { root.unmount(); });
  container.remove();
  vi.unstubAllGlobals();
});

it("edits multiline text, saves it, and displays the normalized response", async () => {
  await click("编辑「GitHub CLI」的描述");
  expect(document.activeElement).toBe(container.querySelector("textarea"));
  expect(container.textContent).toContain("后续回合生效");
  await edit("  新描述\n第二行  ");
  await click("保存");
  expect(onSave).toHaveBeenCalledWith("  新描述\n第二行  ");
  expect(container.querySelector("textarea")).toBeNull();
  expect(container.textContent).toContain("新描述\n第二行");
});

it("discards cancelled drafts and starts fresh when switching machine instances", async () => {
  await click("编辑「GitHub CLI」的描述");
  await edit("未保存的本机描述");
  await click("取消");
  expect(onSave).not.toHaveBeenCalled();
  expect(container.textContent).toContain("原始描述");
  await click("编辑「GitHub CLI」的描述");
  await edit("另一个未保存的描述");
  await render("远端描述", "ssh-1");
  expect(container.querySelector("textarea")).toBeNull();
  expect(container.textContent).toContain("远端描述");
});

it("retains the draft after failure and allows retrying or clearing the description", async () => {
  onSave.mockRejectedValueOnce(new Error("保存失败"));
  await click("编辑「GitHub CLI」的描述");
  await edit("待重试");
  await click("保存");
  expect(container.querySelector('[role="alert"]')?.textContent).toBe("保存失败");
  expect(container.querySelector("textarea")?.value).toBe("待重试");
  await edit("");
  await click("保存");
  expect(onSave).toHaveBeenLastCalledWith("");
  expect(container.querySelector("textarea")).toBeNull();
  expect(container.textContent).toContain("—");
});

it("prevents duplicate saves and cancellation while saving", async () => {
  let finish!: (value: string) => void;
  onSave.mockImplementation(() => new Promise((resolve) => { finish = resolve; }));
  await click("编辑「GitHub CLI」的描述");
  await click("保存");
  await act(async () => {
    container.querySelector("form")!.dispatchEvent(new Event("submit", { bubbles: true, cancelable: true }));
  });
  expect(onSave).toHaveBeenCalledTimes(1);
  expect([...container.querySelectorAll("button")].every((button) => button.disabled)).toBe(true);
  await act(async () => { finish("保存成功"); });
  expect(container.textContent).toContain("保存成功");
});
