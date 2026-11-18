// @vitest-environment jsdom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { api, type PhoneDevice } from "../api";
import { I18nProvider } from "../i18n";
import { PhoneDevices } from "./PhoneDevices";

vi.mock("../api", () => ({
  api: { phones: vi.fn(), signOutPhone: vi.fn() },
}));
vi.mock("../components/ConfirmDialog", () => ({ confirmAction: vi.fn(async () => true) }));

const now = new Date().toISOString();
const phones: PhoneDevice[] = [
  { id: "sess_a", platform: "android", name: "Xiaomi 23127PN0CC", os: "Android 15", app_version: "0.2.0",
    account_id: "acct_1", account_name: "王宁", signed_in_at: now, last_seen_at: now, active: true },
  { id: "sess_b", platform: "ios", account_id: "acct_1", account_name: "王宁",
    signed_in_at: "2026-01-01T00:00:00Z", last_seen_at: "2026-01-02T00:00:00Z", active: false },
];

let container: HTMLDivElement;
let root: Root;

beforeEach(() => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
  container = document.createElement("div");
  document.body.appendChild(container);
  root = createRoot(container);
});

afterEach(() => {
  act(() => root.unmount());
  container.remove();
  vi.clearAllMocks();
});

async function render() {
  await act(async () => {
    root.render(<I18nProvider language="en"><PhoneDevices /></I18nProvider>);
  });
}

describe("PhoneDevices", () => {
  it("lists signed-in phones with their account and state", async () => {
    vi.mocked(api.phones).mockResolvedValue(phones);
    await render();
    const text = container.textContent ?? "";
    expect(text).toContain("Xiaomi 23127PN0CC");
    expect(text).toContain("Android 15");
    expect(text).toContain("v0.2.0");
    expect(container.querySelectorAll("article")).toHaveLength(2);
    // A phone without a model is named by its platform.
    expect(text).toMatch(/iPhone/);
  });

  it("signs a phone out and reloads", async () => {
    vi.mocked(api.phones).mockResolvedValueOnce(phones).mockResolvedValueOnce(phones.slice(1));
    vi.mocked(api.signOutPhone).mockResolvedValue({ ok: true });
    await render();
    const signOut = container.querySelectorAll("article button")[0] as HTMLButtonElement;
    await act(async () => signOut.click());
    expect(api.signOutPhone).toHaveBeenCalledWith("sess_a");
    expect(container.querySelectorAll("article")).toHaveLength(1);
  });

  it("says how to connect when no phone is signed in", async () => {
    vi.mocked(api.phones).mockResolvedValue([]);
    await render();
    expect(container.querySelector(".empty-state")).not.toBeNull();
  });
});
