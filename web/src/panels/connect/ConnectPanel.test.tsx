// @vitest-environment jsdom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { I18nProvider } from "../../i18n";
import { api } from "../../api";
import { ConnectPanel } from "./ConnectPanel";

vi.mock("../../api", () => ({
  fleetMode: () => false,
  api: {
    channels: vi.fn(async () => [
      { id: "ch", name: "Bot", type: "feishu", enabled: false, config: {} },
    ]),
    triggers: vi.fn(async () => []),
    agentInstances: vi.fn(async () => []),
    platforms: vi.fn(async () => ["feishu"]),
    upsertChannel: vi.fn(),
    restartChannel: vi.fn(),
  },
}));
vi.mock("./ChannelCard", () => ({
  ChannelCard: ({ onToggle, onRestart }: { onToggle: () => void; onRestart: () => void }) => (
    <>
      <button data-testid="toggle" onClick={onToggle} />
      <button data-testid="restart" onClick={onRestart} />
    </>
  ),
}));

let root: Root | undefined;

beforeEach(async () => {
  vi.stubGlobal("IS_REACT_ACT_ENVIRONMENT", true);
  const container = document.createElement("div");
  document.body.append(container);
  root = createRoot(container);
  await act(async () =>
    root!.render(
      <I18nProvider language="en">
        <ConnectPanel />
      </I18nProvider>,
    ),
  );
});

afterEach(async () => {
  if (root) await act(async () => root?.unmount());
  root = undefined;
  vi.unstubAllGlobals();
  document.body.replaceChildren();
});

const click = (testID: string) =>
  act(async () => document.querySelector<HTMLElement>(`[data-testid="${testID}"]`)!.click());

it("styles any rejected action as an error, whatever its wording", async () => {
  vi.mocked(api.upsertChannel).mockRejectedValueOnce(
    new Error("check remote macmini before opening the new connection: dial tcp 127.0.0.1:22022: connect: connection refused"),
  );
  await click("toggle");
  const notice = document.querySelector('[role="alert"].session-notice');
  expect(notice?.textContent).toContain("connection refused");
  expect(notice?.classList.contains("error")).toBe(true);
});

it("does not style a success as an error, and clears a previous failure", async () => {
  vi.mocked(api.upsertChannel).mockRejectedValueOnce(new Error("connection refused"));
  await click("toggle");
  vi.mocked(api.restartChannel).mockResolvedValueOnce({ ok: true });
  await click("restart");
  const notice = document.querySelector('[role="status"].session-notice');
  expect(notice).not.toBeNull();
  expect(notice?.classList.contains("error")).toBe(false);
  expect(document.querySelector('[role="alert"].session-notice')).toBeNull();
});
