// @vitest-environment jsdom
import { act } from "react";
import { createRoot } from "react-dom/client";
import { expect, it, vi } from "vitest";
import { I18nProvider } from "../../i18n";
import { ProviderModelBadges } from "./ProviderModelBadges";
import { providerModelHealthRows } from "./providerModelHealth";
import { draftToProvider, providerSupportedModels, providerToDraft } from "./providerUtils";

it("preserves blocked models through editing, excludes them from options, and exposes restoration", async () => {
  const provider = { id: "relay", name: "Relay", base_url: "", enabled: false, model: "blocked", meta: { supported_models: ["ready", "blocked"], blocked_models: ["blocked", "removed"] } };
  const saved = draftToProvider(providerToDraft(provider), [provider]);
  expect(saved.meta?.blocked_models).toEqual(["blocked", "removed"]);
  expect(providerSupportedModels(saved)).toEqual(["ready"]);
  const rows = providerModelHealthRows(providerSupportedModels(saved), [], saved.meta?.blocked_models as string[]);
  expect(rows.map((r) => [r.model, Boolean(r.blocked)])).toEqual([["ready", false], ["blocked", true], ["removed", true]]);
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
  const host = document.createElement("div"); const root = createRoot(host); const toggle = vi.fn();
  await act(async () => root.render(<I18nProvider language="zh"><ProviderModelBadges rows={rows} onToggleBlocked={toggle} /></I18nProvider>));
  await act(async () => host.querySelector<HTMLButtonElement>('[aria-label="恢复 blocked"]')!.click());
  expect(toggle).toHaveBeenCalledWith("blocked", false);
  await act(async () => root.unmount());
});
