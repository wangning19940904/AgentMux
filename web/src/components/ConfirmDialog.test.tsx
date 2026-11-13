// @vitest-environment jsdom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { I18nProvider } from "../i18n";
import { confirmAction, ConfirmHost } from "./ConfirmDialog";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

let container: HTMLDivElement;
let root: Root;

beforeEach(() => {
  // The desktop WebView answers window.confirm with false without showing it.
  vi.spyOn(window, "confirm").mockReturnValue(false);
  container = document.createElement("div");
  document.body.appendChild(container);
  root = createRoot(container);
  act(() => root.render(<I18nProvider language="en"><ConfirmHost /></I18nProvider>));
});

afterEach(() => {
  act(() => root.unmount());
  container.remove();
  vi.restoreAllMocks();
});

function button(label: string) {
  const found = Array.from(container.querySelectorAll("button")).find((item) => item.textContent === label);
  if (!found) throw new Error(`button ${label} not shown`);
  return found;
}

describe("confirmAction", () => {
  it("asks in the app instead of the native dialog and resolves true on confirm", async () => {
    let answer: Promise<boolean> = Promise.resolve(false);
    act(() => { answer = confirmAction("Delete test?", { danger: true, confirmLabel: "Delete" }); });
    expect(container.textContent).toContain("Delete test?");
    act(() => button("Delete").click());
    await expect(answer).resolves.toBe(true);
    expect(window.confirm).not.toHaveBeenCalled();
    expect(container.querySelector("[role=alertdialog]")).toBeNull();
  });

  it("resolves false on cancel and on Escape, one request at a time", async () => {
    let first: Promise<boolean> = Promise.resolve(true);
    let second: Promise<boolean> = Promise.resolve(true);
    act(() => {
      first = confirmAction("First?");
      second = confirmAction("Second?");
    });
    expect(container.textContent).toContain("First?");
    expect(container.textContent).not.toContain("Second?");
    act(() => button("Cancel").click());
    await expect(first).resolves.toBe(false);
    expect(container.textContent).toContain("Second?");
    act(() => { window.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape" })); });
    await expect(second).resolves.toBe(false);
  });

  it("cancels pending requests when the host unmounts", async () => {
    let answer: Promise<boolean> = Promise.resolve(true);
    act(() => { answer = confirmAction("Pending?"); });
    act(() => root.render(<I18nProvider language="en"><span /></I18nProvider>));
    await expect(answer).resolves.toBe(false);
  });
});
