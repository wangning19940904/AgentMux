// @vitest-environment jsdom
import { act } from "react";
import { createRoot } from "react-dom/client";
import { afterEach, expect, it, vi } from "vitest";
import { api, APIError } from "../api";
import { I18nProvider } from "../i18n";
import { loadPhoneAppLink, PhoneAppLink } from "./PhoneAppLink";

afterEach(() => {
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

const link = {
  hub_url: "https://101.200.234.220",
  fingerprint: "a".repeat(64),
  link: `agentmux://hub?fp=${"a".repeat(64)}&url=https%3A%2F%2F101.200.234.220`,
  reach: "public" as const,
};

async function render() {
  vi.stubGlobal("IS_REACT_ACT_ENVIRONMENT", true);
  const container = document.createElement("div");
  const root = createRoot(container);
  await act(async () => {
    root.render(<I18nProvider language="en"><PhoneAppLink /></I18nProvider>);
  });
  return { container, unmount: () => act(() => root.unmount()) };
}

it("shows the hub link as a QR code with the address and steps", async () => {
  vi.spyOn(api, "hubClientLink").mockResolvedValue(link);
  const { container, unmount } = await render();
  try {
    expect(container.querySelector('[data-testid="phone-app-qr"] svg')).not.toBeNull();
    expect(container.textContent).toContain("https://101.200.234.220");
    expect(container.textContent).toContain("Scan QR code");
    expect(container.textContent).not.toContain("Works only on the hub's network");
  } finally {
    unmount();
  }
});

it("says when only a phone on the hub's network can connect", async () => {
  vi.spyOn(api, "hubClientLink").mockResolvedValue({ ...link, hub_url: "https://192.168.1.20:8766", reach: "private" });
  const { container, unmount } = await render();
  try {
    expect(container.querySelector('[data-testid="phone-app-qr"]')).not.toBeNull();
    expect(container.textContent).toContain("Works only on the hub's network");
  } finally {
    unmount();
  }
});

it("explains a refusal with the config to change instead of a code", async () => {
  vi.spyOn(api, "hubClientLink").mockRejectedValue(
    new APIError("phones cannot reach this hub yet", 409, { error: "phones cannot reach this hub yet", reason: "disabled" }),
  );
  const { container, unmount } = await render();
  try {
    expect(container.querySelector('[data-testid="phone-app-qr"]')).toBeNull();
    expect(container.textContent).toContain("Phones cannot reach this hub yet");
    expect(container.querySelector(".phone-app-link-config")?.textContent).toContain("client_api = true");
  } finally {
    unmount();
  }
});

it("maps refusal reasons and other failures", async () => {
  vi.spyOn(api, "hubClientLink")
    .mockRejectedValueOnce(new APIError("loopback", 409, { reason: "loopback" }))
    .mockRejectedValueOnce(new APIError("odd", 409, { reason: "something-new" }))
    .mockRejectedValueOnce(new Error("network down"));
  expect(await loadPhoneAppLink()).toMatchObject({ phase: "refused", reason: "loopback" });
  expect(await loadPhoneAppLink()).toMatchObject({ phase: "refused", reason: "disabled" });
  expect(await loadPhoneAppLink()).toEqual({ phase: "error", message: "network down" });
});
