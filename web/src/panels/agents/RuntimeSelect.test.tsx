// @vitest-environment jsdom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { RuntimeSelect } from "./RuntimeSelect";

let root: Root;
let container: HTMLDivElement;
const onChange = vi.fn();
const defaults = {
  value: "codex", options: ["codex", "cursor", "traecli"], disabled: false,
  label: "Runtime", emptyLabel: "No installed runtime", unavailableLabel: "Not installed", onChange,
};

beforeEach(() => {
  vi.stubGlobal("IS_REACT_ACT_ENVIRONMENT", true);
  container = document.createElement("div");
  document.body.append(container);
  root = createRoot(container);
  onChange.mockClear();
});

afterEach(async () => {
  await act(async () => root.unmount());
  container.remove();
  vi.unstubAllGlobals();
});

async function render(props: Partial<typeof defaults> = {}) {
  await act(async () => root.render(<RuntimeSelect {...defaults} {...props} />));
}

function control() { return container.querySelector<HTMLButtonElement>('[role="combobox"]')!; }
async function key(key: string) {
  await act(async () => control().dispatchEvent(new KeyboardEvent("keydown", { key, bubbles: true })));
}

it("shows official icons for the selected runtime and chooses an option by click", async () => {
  await render();
  expect(control().textContent).toBe("Codex CLI");
  expect(control().querySelector("img")?.getAttribute("src")).toContain("codex.png");
  await act(async () => control().click());
  const options = container.querySelectorAll('[role="option"]');
  expect([...options].every((option) => option.querySelector("img"))).toBe(true);
  await act(async () => (options[1] as HTMLElement).click());
  expect(onChange).toHaveBeenCalledWith("cursor");
  expect(control().getAttribute("aria-expanded")).toBe("false");
});

it("supports keyboard navigation, typeahead, and cancellation without changing the runtime", async () => {
  await render();
  await key("ArrowDown");
  await key("ArrowDown");
  expect(document.getElementById(control().getAttribute("aria-activedescendant")!)?.textContent).toBe("Cursor");
  await key("Escape");
  expect(onChange).not.toHaveBeenCalled();
  await key("End");
  await key("Enter");
  expect(onChange).toHaveBeenLastCalledWith("traecli");
  await key("Home");
  await key(" ");
  expect(onChange).toHaveBeenLastCalledWith("codex");
  await key("t");
  await key("Enter");
  expect(onChange).toHaveBeenLastCalledWith("traecli");
  await act(async () => control().click());
  await act(async () => document.body.dispatchEvent(new Event("pointerdown", { bubbles: true })));
  expect(control().getAttribute("aria-expanded")).toBe("false");
});

it("preserves missing runtimes, skips disabled options, and handles read-only or empty catalogs", async () => {
  await render({ value: "custom-runtime" });
  expect(control().textContent).toContain("custom-runtime(Not installed)");
  expect(control().querySelector("img")).toBeNull();
  await act(async () => control().click());
  expect(container.querySelector('[aria-disabled="true"]')?.textContent).toContain("custom-runtime");
  await key("ArrowDown");
  await key("Enter");
  expect(onChange).toHaveBeenLastCalledWith("cursor");
  onChange.mockClear();
  await render({ disabled: true });
  expect(control().disabled).toBe(true);
  await key("Enter");
  expect(onChange).not.toHaveBeenCalled();
  await render({ value: "", options: [] });
  expect(control().disabled).toBe(true);
  expect(control().textContent).toBe("No installed runtime");
});
