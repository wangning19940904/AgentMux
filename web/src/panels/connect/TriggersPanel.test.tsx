// @vitest-environment jsdom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { I18nProvider } from "../../i18n";
import { setMachineOverride } from "../../api";
import { TriggersPanel } from "./TriggersPanel";

vi.mock("../../api", () => ({
  fleetMode: () => true,
  setMachineOverride: vi.fn(),
  api: {
    triggers: vi.fn(async () => [
      { id: "hook", name: "Deploy hook", kind: "webhook", enabled: true, hook_path: "/hooks/deploy" },
      { id: "notify", name: "Notify on done", kind: "event", enabled: true, event: "task.completed", action_type: "shell" },
      { id: "nightly", name: "Nightly", kind: "cron", enabled: true, cron_expr: "0 3 * * *" },
    ]),
    channels: vi.fn(async () => []),
    agentInstances: vi.fn(async () => []),
    superAgents: vi.fn(async () => [
      {
        id: "sa_work",
        name: "Work Agent",
        target_id: "macmini",
        target_name: "bytedance-macmini",
        sources: [
          { id: "src_meego_todo", kind: "callback", enabled: true, allowed_actors: ["meego"], config: {} },
        ],
      },
    ]),
    saveSuperAgent: vi.fn(),
  },
}));
vi.mock("./EventSubscriptionsPanel", () => ({
  EventSubscriptionsSection: () => <section data-testid="subscriptions" />,
}));
vi.mock("../agents/SuperAgentForm", () => ({
  SuperAgentDrawer: ({ draft, machine }: { draft: { name: string }; machine: string }) => (
    <aside data-testid="agent-drawer">{draft.name}@{machine}</aside>
  ),
}));

let root: Root | undefined;
let container: HTMLDivElement;

beforeEach(async () => {
  vi.stubGlobal("IS_REACT_ACT_ENVIRONMENT", true);
  container = document.createElement("div");
  document.body.append(container);
  root = createRoot(container);
  await act(async () => root!.render(<I18nProvider language="en"><TriggersPanel /></I18nProvider>));
});

afterEach(async () => {
  if (root) await act(async () => root?.unmount());
  root = undefined;
  vi.unstubAllGlobals();
  vi.clearAllMocks();
  document.body.replaceChildren();
});

const surface = (title: string) =>
  Array.from(container.querySelectorAll(".surface")).find((item) => item.querySelector("h2")?.textContent === title);

it("lists webhooks and every Super Agent's triggers as inbound, with their machine", () => {
  const inbound = surface("Inbound: start agent work")!;
  expect(inbound.textContent).toContain("Deploy hook");
  expect(inbound.textContent).toContain("src_meego_todo");
  expect(inbound.textContent).toContain("Custom callback → Work Agent");
  expect(inbound.textContent).toContain("bytedance-macmini");
  expect(inbound.textContent).not.toContain("Notify on done");
  expect(container.textContent).not.toContain("Nightly");
});

it("puts event actions and event subscriptions under outbound", () => {
  const outbound = surface("Outbound: event actions")!;
  expect(outbound.textContent).toContain("Notify on done");
  expect(outbound.textContent).not.toContain("Deploy hook");
  expect(container.querySelector('[data-testid="subscriptions"]')).not.toBeNull();
});

it("edits a Super Agent trigger in its agent, on the agent's machine", async () => {
  const edit = Array.from(container.querySelectorAll("button")).find((button) => button.textContent === "Edit in agent")!;
  await act(async () => edit.click());
  expect(setMachineOverride).toHaveBeenLastCalledWith("macmini");
  expect(container.querySelector('[data-testid="agent-drawer"]')?.textContent).toBe("Work Agent@macmini");
});
