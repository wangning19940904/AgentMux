// @vitest-environment jsdom
import { act } from "react";
import { createRoot } from "react-dom/client";
import { afterEach, expect, it, vi } from "vitest";
import { api } from "./api";
import type { KeepAwakeStatus } from "./api";
import { I18nProvider } from "./i18n";
import { KeepAwakeControl, keepAwakeRequest, loadKeepAwakePrefs } from "./KeepAwakeControl";

afterEach(() => {
  localStorage.clear();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

const idle: KeepAwakeStatus = {
  supported: true,
  enabled: false,
  mode: "display",
  simulate_activity: false,
  duration_minutes: 60,
  remaining_seconds: 0,
};

it("loads stored preferences defensively", () => {
  expect(loadKeepAwakePrefs({ getItem: () => null })).toEqual({ duration: 60, mode: "display", simulate: false });
  expect(loadKeepAwakePrefs({ getItem: () => "{bad json" })).toEqual({ duration: 60, mode: "display", simulate: false });
  expect(loadKeepAwakePrefs({ getItem: () => JSON.stringify({ duration: 0, mode: "system", simulate: true }) }))
    .toEqual({ duration: 0, mode: "system", simulate: true });
  expect(loadKeepAwakePrefs({ getItem: () => JSON.stringify({ duration: 99999, mode: "nope" }) }))
    .toEqual({ duration: 60, mode: "display", simulate: false });
});

it("only simulates activity in display mode", () => {
  expect(keepAwakeRequest(true, { duration: 0, mode: "system", simulate: true }))
    .toEqual({ enabled: true, duration_minutes: 0, mode: "system", simulate_activity: false });
  expect(keepAwakeRequest(true, { duration: 30, mode: "display", simulate: true }))
    .toEqual({ enabled: true, duration_minutes: 30, mode: "display", simulate_activity: true });
  expect(keepAwakeRequest(false, { duration: 30, mode: "display", simulate: true })).toEqual({ enabled: false });
});

it("toggles keep-awake and re-applies preference changes while it is on", async () => {
  vi.stubGlobal("IS_REACT_ACT_ENVIRONMENT", true);
  vi.spyOn(api, "keepAwakeStatus").mockResolvedValue(idle);
  const setKeepAwake = vi.spyOn(api, "setKeepAwake").mockImplementation(async (request) => ({
    ...idle,
    enabled: request.enabled,
    mode: request.mode ?? "display",
    simulate_activity: request.simulate_activity ?? false,
    duration_minutes: request.duration_minutes ?? 0,
    remaining_seconds: (request.duration_minutes ?? 0) * 60,
    activity_state: request.simulate_activity ? "blocked" : undefined,
  }));
  const container = document.createElement("div");
  const root = createRoot(container);
  try {
    await act(async () => {
      root.render(<I18nProvider language="en"><KeepAwakeControl onError={() => undefined} /></I18nProvider>);
    });
    const toggle = container.querySelector<HTMLInputElement>('input[role="switch"]')!;
    expect(toggle.checked).toBe(false);

    await act(async () => { toggle.click(); });
    expect(setKeepAwake).toHaveBeenLastCalledWith({ enabled: true, duration_minutes: 60, mode: "display", simulate_activity: false });
    expect(container.textContent).toContain("about 1 h left");

    const select = container.querySelector<HTMLSelectElement>("select")!;
    await act(async () => {
      select.value = "0";
      select.dispatchEvent(new Event("change", { bubbles: true }));
    });
    expect(setKeepAwake).toHaveBeenLastCalledWith({ enabled: true, duration_minutes: 0, mode: "display", simulate_activity: false });
    expect(container.textContent).toContain("until you turn it off");

    const simulate = container.querySelector<HTMLInputElement>(".preference-keep-awake-simulate input")!;
    await act(async () => { simulate.click(); });
    expect(setKeepAwake).toHaveBeenLastCalledWith({ enabled: true, duration_minutes: 0, mode: "display", simulate_activity: true });
    expect(container.querySelector('[role="alert"] button')?.textContent).toBe("Open Accessibility settings");

    const sleepOnly = Array.from(container.querySelectorAll<HTMLButtonElement>('[role="radio"]')).find((button) => button.textContent === "Sleep only")!;
    await act(async () => { sleepOnly.click(); });
    expect(setKeepAwake).toHaveBeenLastCalledWith({ enabled: true, duration_minutes: 0, mode: "system", simulate_activity: false });
    expect(container.querySelector(".preference-keep-awake-simulate")).toBeNull();

    await act(async () => { container.querySelector<HTMLInputElement>('input[role="switch"]')!.click(); });
    expect(setKeepAwake).toHaveBeenLastCalledWith({ enabled: false });
    expect(JSON.parse(localStorage.getItem("agentmux.keepAwake.prefs") || "{}")).toEqual({ duration: 0, mode: "system", simulate: true });
  } finally {
    await act(async () => { root.unmount(); });
  }
});

it("applies a custom hours and minutes duration only on Confirm", async () => {
  vi.stubGlobal("IS_REACT_ACT_ENVIRONMENT", true);
  vi.spyOn(api, "keepAwakeStatus").mockResolvedValue(idle);
  const setKeepAwake = vi.spyOn(api, "setKeepAwake").mockImplementation(async (request) => ({
    ...idle,
    enabled: request.enabled,
    duration_minutes: request.duration_minutes ?? 0,
    remaining_seconds: (request.duration_minutes ?? 0) * 60,
  }));
  const container = document.createElement("div");
  const root = createRoot(container);
  const input = (name: string) => container.querySelector<HTMLInputElement>(`input[aria-label="${name}"]`)!;
  const type = async (name: string, value: string) => {
    await act(async () => {
      const field = input(name);
      Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(field, value);
      field.dispatchEvent(new Event("input", { bubbles: true }));
    });
  };
  const confirm = () => Array.from(container.querySelectorAll<HTMLButtonElement>("button")).find((b) => b.textContent === "Confirm")!;
  const stored = () => JSON.parse(localStorage.getItem("agentmux.keepAwake.prefs") || "{}");
  try {
    await act(async () => {
      root.render(<I18nProvider language="en"><KeepAwakeControl onError={() => undefined} /></I18nProvider>);
    });
    const select = container.querySelector<HTMLSelectElement>("select")!;
    await act(async () => {
      select.value = "custom";
      select.dispatchEvent(new Event("change", { bubbles: true }));
    });
    // The picker starts from the current duration, so there is nothing to confirm yet.
    expect(input("Hours").value).toBe("1");
    expect(input("Minutes").value).toBe("00");
    expect(confirm().disabled).toBe(true);

    // The picker has only text fields; typing sets each part.
    expect(container.querySelectorAll(".keep-awake-picker button").length).toBe(1);
    await type("Hours", "1");
    await type("Minutes", "55");
    expect(input("Hours").value).toBe("1");
    expect(input("Minutes").value).toBe("55");
    expect(stored().duration).toBeUndefined();

    // While off, Confirm saves the duration for the next time it is turned on.
    await act(async () => { confirm().click(); });
    expect(setKeepAwake).not.toHaveBeenCalled();
    expect(stored().duration).toBe(115);
    expect(confirm().disabled).toBe(true);

    await act(async () => { container.querySelector<HTMLInputElement>('input[role="switch"]')!.click(); });
    expect(setKeepAwake).toHaveBeenLastCalledWith({ enabled: true, duration_minutes: 115, mode: "display", simulate_activity: false });

    // While on, Confirm re-applies the new duration immediately.
    await type("Hours", "0");
    expect(setKeepAwake).toHaveBeenCalledTimes(1);
    await act(async () => { confirm().click(); });
    expect(setKeepAwake).toHaveBeenLastCalledWith({ enabled: true, duration_minutes: 55, mode: "display", simulate_activity: false });

    // Zero is not a valid custom duration.
    await type("Minutes", "0");
    expect(input("Minutes").value).toBe("00");
    expect(confirm().disabled).toBe(true);
    expect(container.textContent).toContain("Pick at least 1 minute.");
  } finally {
    await act(async () => { root.unmount(); });
  }
});

it("renders nothing where keep-awake is unsupported", async () => {
  vi.stubGlobal("IS_REACT_ACT_ENVIRONMENT", true);
  vi.spyOn(api, "keepAwakeStatus").mockResolvedValue({ ...idle, supported: false });
  const container = document.createElement("div");
  const root = createRoot(container);
  try {
    await act(async () => {
      root.render(<I18nProvider language="en"><KeepAwakeControl onError={() => undefined} /></I18nProvider>);
    });
    expect(container.innerHTML).toBe("");
  } finally {
    await act(async () => { root.unmount(); });
  }
});
