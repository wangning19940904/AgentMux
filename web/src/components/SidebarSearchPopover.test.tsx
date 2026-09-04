// @vitest-environment jsdom
import { act, createRef } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { SidebarSearchPopover } from "./SidebarSearchPopover";

let root: Root;
let container: HTMLDivElement;
let anchor: HTMLDivElement;
let rect: DOMRect;
const onClose = vi.fn();
const onSelect = vi.fn();
const anchorRef = createRef<HTMLDivElement>();

beforeEach(() => {
  Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true });
  vi.clearAllMocks();
  container = document.createElement("div");
  container.style.overflow = "hidden";
  container.style.width = "183px";
  anchor = document.createElement("div");
  anchor.append(document.createElement("input"));
  container.append(anchor);
  document.body.append(container);
  Object.assign(anchorRef, { current: anchor });
  rect = { left: 72, top: 60, bottom: 92, width: 167, height: 32 } as DOMRect;
  vi.spyOn(anchor, "getBoundingClientRect").mockImplementation(() => rect);
  vi.stubGlobal("innerWidth", 1228);
  vi.stubGlobal("innerHeight", 768);
  const mount = document.createElement("div");
  container.append(mount);
  root = createRoot(mount);
});
afterEach(async () => {
  await act(async () => root.unmount());
  container.remove();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

async function render(layoutKey = "64:183") {
  await act(async () => root.render(
    <SidebarSearchPopover anchorRef={anchorRef} layoutKey={layoutKey} onClose={onClose} id="results" aria-label="Search results">
      <button role="option" onClick={onSelect}>Usage</button>
    </SidebarSearchPopover>,
  ));
  return document.getElementById("results")!;
}

it("escapes the clipped sidebar and preserves result focus/clicks", async () => {
  const panel = await render();
  expect(panel.parentElement).toBe(document.body);
  expect(container.contains(panel)).toBe(false);
  expect(panel.style.position).toBe("fixed");
  expect(panel.style.width).toBe("260px");
  expect(panel.style.left).toBe("72px");
  expect(panel.style.top).toBe("97px");
  const result = panel.querySelector("button")!;
  await act(async () => {
    anchor.querySelector("input")!.focus();
    result.dispatchEvent(new Event("pointerdown", { bubbles: true }));
    result.focus();
    result.click();
  });
  expect(onClose).not.toHaveBeenCalled();
  expect(onSelect).toHaveBeenCalledOnce();
});

it("repositions on sidebar resizing, viewport resizing and scrolling", async () => {
  const panel = await render();
  rect = { ...rect, left: 88, bottom: 105 };
  await render("80:183");
  expect(panel.style.left).toBe("88px");
  expect(panel.style.top).toBe("110px");
  vi.stubGlobal("innerWidth", 280);
  await act(async () => window.dispatchEvent(new Event("resize")));
  expect(panel.style.width).toBe("256px");
  expect(panel.style.left).toBe("12px");
  rect = { ...rect, top: 700, bottom: 732 };
  await act(async () => window.dispatchEvent(new Event("scroll")));
  expect(panel.style.top).toBe("auto");
  expect(panel.style.bottom).toBe("73px");
  expect(panel.style.maxHeight).toBe("360px");
});

it("closes on outside focus, outside pointer and Escape, and cleans up listeners", async () => {
  await render();
  await act(async () => {
    document.body.dispatchEvent(new Event("focusin", { bubbles: true }));
    document.body.dispatchEvent(new Event("pointerdown", { bubbles: true }));
    document.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape" }));
  });
  expect(onClose).toHaveBeenCalledTimes(3);
  await act(async () => root.render(null));
  document.body.dispatchEvent(new Event("pointerdown", { bubbles: true }));
  expect(onClose).toHaveBeenCalledTimes(3);
  expect(document.getElementById("results")).toBeNull();
});

it("closes when the responsive layout hides its anchor", async () => {
  await render();
  rect = { ...rect, width: 0, height: 0 };
  await act(async () => window.dispatchEvent(new Event("resize")));
  expect(onClose).toHaveBeenCalledOnce();
});
