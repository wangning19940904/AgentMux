// @vitest-environment jsdom
import { act } from "react";
import type { ReactElement } from "react";
import { createRoot } from "react-dom/client";
import type { Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { api } from "../api";
import type { SuperAgent, SuperAgentActivity, SuperAgentRouterList } from "../api";
import { I18nProvider } from "../i18n";
import { SuperAgentsPanel } from "./SuperAgentsPanel";
import { newRouter, newSource, newSuperAgent, parseActors, superAgentProblems, withWorkerRuntimes } from "./superAgentModel";

let root: Root | null = null;
let container: HTMLElement;

async function settle() {
  await act(async () => { await new Promise((resolve) => setTimeout(resolve, 0)); });
}

async function until<T>(find: () => T | null | undefined | false): Promise<T> {
  for (let attempt = 0; attempt < 50; attempt++) {
    const found = find();
    if (found) return found;
    await settle();
  }
  throw new Error(`not found in: ${container.textContent}`);
}

function mount(element: ReactElement) {
  container = document.createElement("div");
  document.body.append(container);
  root = createRoot(container);
  act(() => root!.render(element));
}

const hasText = (text: string | RegExp) => {
  const body = container.textContent ?? "";
  return typeof text === "string" ? body.includes(text) : text.test(body);
};
const button = (name: RegExp) => [...container.querySelectorAll("button")].find((item) => name.test((item.textContent ?? "").trim()));
const hasOption = (name: string) => [...container.querySelectorAll("option")].some((item) => item.textContent === name);
const option = (name: string) => [...container.querySelectorAll("option")].find((item) => item.textContent === name);
const workerFramework = (name: string) => [...container.querySelectorAll(".super-agent-toggle")].find((item) => item.textContent === name)?.querySelector("input");

function control(label: RegExp): HTMLInputElement | HTMLSelectElement | HTMLTextAreaElement {
  const byAria = [...container.querySelectorAll<HTMLSelectElement>("[aria-label]")].find((item) => label.test(item.getAttribute("aria-label") ?? ""));
  if (byAria) return byAria;
  const field = [...container.querySelectorAll("label.field")].find((item) => label.test(item.querySelector("span")?.textContent ?? ""));
  const found = field?.querySelector<HTMLInputElement>("input, select, textarea");
  if (!found) throw new Error(`no control for ${label}`);
  return found;
}

function setValue(element: HTMLInputElement | HTMLSelectElement | HTMLTextAreaElement, value: string) {
  const prototype = Object.getPrototypeOf(element) as object;
  Object.getOwnPropertyDescriptor(prototype, "value")!.set!.call(element, value);
  act(() => { element.dispatchEvent(new Event(element instanceof HTMLSelectElement ? "change" : "input", { bubbles: true })); });
}

function click(element: Element | undefined) {
  if (!element) throw new Error("nothing to click");
  act(() => { element.dispatchEvent(new MouseEvent("click", { bubbles: true })); });
}

function press(element: Element, key: string) {
  act(() => { element.dispatchEvent(new KeyboardEvent("keydown", { key, bubbles: true })); });
}

function focus(element: HTMLElement) {
  act(() => { element.focus(); });
}

const chips = (label: RegExp) => [...(control(label).closest(".super-agent-picker")?.querySelectorAll(".super-agent-token") ?? [])].map((item) => item.textContent);

const saved: SuperAgent = {
  id: "sa_1", name: "Repo keeper", router_agent_id: "router", mode: "act", max_concurrency: 2, enabled: true,
  workers: [
    { id: "w1", agent_id: "coder", label: "Laptop" },
    { id: "w2", agent_id: "gpu", host_id: "h1", label: "GPU box" },
  ],
  sources: [{ id: "src_1", kind: "github_webhook", enabled: true, allowed_actors: ["alice"], config: { agent_login: "bot", webhook_secret: "<redacted>" } }],
};

const activity: SuperAgentActivity = {
  workers: [{ worker_id: "w1", available: true }, { worker_id: "w2", available: false, detail: "unreachable" }],
  sources: { src_1: { last_error: "bad signature" } },
  tickets: [
    { id: "t1", title: "Fix build", status: "running", worker: saved.workers[1], updated_at: "2026-11-03T00:00:00Z" },
    { id: "t2", title: "Broken thing", status: "failed", worker: saved.workers[0], error: "tests failed", updated_at: "2026-11-03T00:00:00Z" },
  ],
  triggers: [{ id: "e1", source_kind: "github_webhook", text: "hello", actor: "mallory", disposition: "filtered", reason: "not allowed", received_at: "2026-11-03T00:00:00Z" }],
};

const routerList: SuperAgentRouterList = {
  routers: [{ id: "router", name: "Router", runtime_id: "codex", mcp_servers: [], used_by: [{ id: "sa_1", name: "Repo keeper" }] }],
  frameworks: [
    { runtime_id: "codex", can_route: true, mcp_servers: true },
    { runtime_id: "kimi", can_route: false, mcp_servers: false, reason: "no read-only mode" },
  ],
  mcp_servers: ["oncall"],
};
const noRouters: SuperAgentRouterList = { ...routerList, routers: [] };

function renderPanel(props = {}) {
  mount(<I18nProvider language="en"><SuperAgentsPanel {...props} /></I18nProvider>);
}

beforeEach(() => {
  vi.stubGlobal("IS_REACT_ACT_ENVIRONMENT", true);
  vi.spyOn(api, "superAgents").mockResolvedValue([saved]);
  vi.spyOn(api, "superAgentActivity").mockResolvedValue(activity);
  vi.spyOn(api, "agentInstances").mockResolvedValue([
    { id: "router", name: "Router", runtime_id: "codex", enabled: true, target_id: "local", source: "super_agent_router" } as never,
    { id: "coder", name: "Coder", runtime_id: "claudecode", enabled: true } as never,
    { id: "elsewhere", name: "Elsewhere", runtime_id: "dsh", enabled: true, target_id: "h1" } as never,
  ]);
  vi.spyOn(api, "superAgentRouters").mockResolvedValue(routerList);
  vi.spyOn(api, "saveSuperAgentRouter").mockImplementation(async (router) => ({ ...router, id: router.id || "sar_new", used_by: [] }));
  vi.spyOn(api, "frameworks").mockResolvedValue({ prereqs: { node: true, npm: true }, frameworks: [
    { spec: { kind: "codex", display: "Codex", kind_type: "cli", install_supported: true, install_requires_npm: false, update_supported: true, supported: true }, installed: true, registered: true },
    { spec: { kind: "gemini", display: "Gemini", kind_type: "cli", install_supported: true, install_requires_npm: false, update_supported: true, supported: true }, installed: false, registered: true },
    { spec: { kind: "dsh", display: "Remote DSH", kind_type: "cli", install_supported: true, install_requires_npm: false, update_supported: true, supported: true }, installed: true, registered: true, target_id: "h1" },
  ] } as never);
  vi.spyOn(api, "remoteHosts").mockResolvedValue([{ id: "h1", name: "GPU box" } as never]);
  vi.spyOn(api, "channels").mockResolvedValue([
    { id: "c1", name: "Team bot", type: "feishu" } as never,
    { id: "c2", name: "Slack bot", type: "slack" } as never,
  ]);
  vi.spyOn(api, "remoteAgentInstances").mockResolvedValue([{ id: "gpu", name: "GPU coder", runtime_id: "dsh", enabled: true } as never]);
  vi.spyOn(api, "feishuChats").mockResolvedValue({ items: [{ chat_id: "oc_team", name: "Team" }, { chat_id: "oc_ops", name: "Ops", external: true }] });
  vi.spyOn(api, "feishuMembers").mockResolvedValue({ items: [{ open_id: "ou_a", name: "Alice", chat_ids: ["oc_team"] }, { open_id: "ou_c", name: "Carol", chat_ids: ["oc_ops"] }] });
  vi.spyOn(api, "feishuUsers").mockResolvedValue([]);
});

afterEach(() => {
  act(() => root?.unmount());
  root = null;
  container.remove();
  vi.restoreAllMocks();
});

describe("SuperAgentsPanel", () => {
  it("shows workers by location, ticket controls and why an event was filtered", async () => {
    renderPanel();
    await until(() => hasText("Fix build"));
    expect(hasText("Repo keeper")).toBe(true);
    const remote = [...container.querySelectorAll(".super-agent-chip")].find((item) => item.textContent?.includes("GPU box"));
    expect(remote?.getAttribute("title")).toBe("unreachable");
    expect(remote?.textContent).toContain("remote");
    expect(remote?.querySelector("i")?.className).toBe("offline");
    for (const text of ["tests failed", "not allowed", "bad signature", "/hook/super-agent/github/src_1"]) expect(hasText(text)).toBe(true);
  });

  it("stops a running ticket and retries a failed one", async () => {
    const stop = vi.spyOn(api, "stopSuperAgentTicket").mockResolvedValue({ ok: true });
    const retry = vi.spyOn(api, "retrySuperAgentTicket").mockResolvedValue({ ok: true });
    renderPanel();
    await until(() => hasText("Fix build"));
    click(button(/^Stop$/));
    await until(() => stop.mock.calls.length > 0);
    expect(stop).toHaveBeenCalledWith("t1");
    await until(() => !(button(/^Retry$/) as HTMLButtonElement).disabled);
    click(button(/^Retry$/));
    await until(() => retry.mock.calls.length > 0);
    expect(retry).toHaveBeenCalledWith("t2");
  });

  it("opens the create form on request, once, and defaults to observing", async () => {
    const handled = vi.fn();
    renderPanel({ createRequested: true, onCreateRequestHandled: handled });
    await until(() => container.querySelector("[role=dialog]"));
    expect(handled).toHaveBeenCalledTimes(1);
    expect(hasText("Records what the router would do")).toBe(true);
  });

  it("creates a Super Agent with a remote worker and a Feishu trigger", async () => {
    const save = vi.spyOn(api, "saveSuperAgent").mockResolvedValue({ ...saved, id: "sa_2" });
    renderPanel({ createRequested: true });
    await until(() => container.querySelector("[role=dialog]"));
    const saveButton = () => button(/^Save$/) as HTMLButtonElement;
    // The first router agent is chosen, and workers start on its framework.
    await until(() => control(/^Router agent/).value === "router");
    expect(workerFramework("Codex")?.checked).toBe(true);
    expect(saveButton().disabled).toBe(false);

    setValue(control(/^Name/), "Triage");
    click(button(/Add worker/) as HTMLButtonElement);
    await until(() => hasOption("Coder (claudecode)"));
    expect(hasOption("Elsewhere (dsh)")).toBe(false);
    expect(hasOption("Router (codex)")).toBe(false);
    expect(saveButton().disabled).toBe(true);
    await until(() => hasOption("GPU box"));
    setValue(control(/^Machine/), "h1");
    await until(() => hasOption("GPU coder (dsh)"));
    expect(api.remoteAgentInstances).toHaveBeenCalledWith("h1");
    setValue(control(/^Agent$/), "gpu");
    setValue(control(/^Add trigger/), "feishu_message");

    await until(() => hasOption("Team bot"));
    expect(hasOption("Slack bot")).toBe(false);
    expect(saveButton().disabled).toBe(true);
    setValue(control(/^Feishu channel/), "c1");
    await until(() => hasText("The bot is in 2 chats."));
    expect(api.feishuChats).toHaveBeenCalledWith("c1");
    expect(api.feishuMembers).not.toHaveBeenCalled();

    const groups = control(/^Only these chats/) as HTMLInputElement;
    focus(groups);
    setValue(groups, "tea");
    press(groups, "Enter");
    expect(chips(/^Only these chats/)).toEqual(["Team"]);

    vi.mocked(api.feishuUsers).mockResolvedValue([{ open_id: "ou_b", name: "Bob", email: "bob@acme.com" }]);
    const senders = control(/^Allowed senders/) as HTMLInputElement;
    focus(senders);
    await until(() => hasText("2 people in the chosen chats."));
    expect(api.feishuMembers).toHaveBeenCalledWith("c1", ["oc_team"]);
    setValue(senders, "ali");
    await until(() => [...container.querySelectorAll("[role=option]")].some((item) => item.textContent?.includes("Alice")));
    press(senders, "Enter");
    setValue(senders, "bob@acme.com, nobody@acme.com");
    press(senders, "Enter");
    await until(() => chips(/^Allowed senders/).length === 2);
    expect(api.feishuUsers).toHaveBeenCalledWith("c1", { emails: ["bob@acme.com", "nobody@acme.com"] });
    expect(chips(/^Allowed senders/)).toEqual(["Alice", "Bob"]);
    expect(hasText("No one found for nobody@acme.com")).toBe(true);
    expect(saveButton().disabled).toBe(false);

    click(saveButton());
    await until(() => save.mock.calls.length > 0);
    const body = save.mock.calls[0][0];
    expect(body).toMatchObject({ name: "Triage", router_agent_id: "router", router_runtime: "", mode: "observe", worker_runtimes: ["codex"] });
    expect(body.workers).toEqual([{ id: "", agent_id: "gpu", host_id: "h1" }]);
    expect(api.saveSuperAgentRouter).not.toHaveBeenCalled();
    expect(body.sources[0]).toMatchObject({ kind: "feishu_message", allowed_actors: ["ou_a", "ou_b"], config: { channel_id: "c1", chat_ids: "oc_team" } });
    await until(() => !container.querySelector("[role=dialog]"));
  });

  it("shows saved Feishu senders and groups by name, and keeps pasted ids", async () => {
    const feishu: SuperAgent = { ...saved, sources: [{ id: "src_f", kind: "feishu_message", enabled: true, allowed_actors: ["ou_a", "ou_z", "*"], config: { channel_id: "c1", chat_ids: "oc_ops, oc_gone" } }] };
    vi.spyOn(api, "superAgents").mockResolvedValue([feishu]);
    vi.mocked(api.feishuUsers).mockResolvedValue([{ open_id: "ou_a", name: "Alice" }]);
    renderPanel();
    await until(() => button(/^Edit$/));
    click(button(/^Edit$/));
    await until(() => chips(/^Allowed senders/).includes("Alice"));
    expect(api.feishuUsers).toHaveBeenCalledWith("c1", { open_ids: ["ou_a", "ou_z"] });
    expect(chips(/^Allowed senders/)).toEqual(["Alice", "ou_z", "Everyone"]);
    await until(() => chips(/^Only these chats/).includes("Ops"));
    expect(chips(/^Only these chats/)).toEqual(["Ops", "oc_gone"]);

    const senders = control(/^Allowed senders/) as HTMLInputElement;
    focus(senders);
    setValue(senders, "ou_new");
    act(() => { senders.blur(); });
    await until(() => chips(/^Allowed senders/).includes("ou_new"));
    focus(senders);
    setValue(senders, "car");
    act(() => { senders.blur(); });
    expect(chips(/^Allowed senders/)).not.toContain("car");
  });

  it("shows why Feishu refused to list the bot's chats", async () => {
    vi.mocked(api.feishuChats).mockRejectedValue(new Error("99991672 Access denied. One of the following scopes is required: [im:chat:readonly]"));
    renderPanel({ createRequested: true });
    await until(() => container.querySelector("[role=dialog]"));
    setValue(control(/^Add trigger/), "feishu_message");
    await until(() => hasOption("Team bot"));
    setValue(control(/^Feishu channel/), "c1");
    await until(() => hasText("Couldn't read the bot's chats: 99991672 Access denied"));
    expect(hasText("im:chat:readonly")).toBe(true);
  });

  it("creates a router agent and sets up triggers from an instruction", async () => {
    vi.spyOn(api, "superAgentRouters").mockResolvedValue(noRouters);
    const save = vi.spyOn(api, "saveSuperAgent").mockResolvedValue({ ...saved, id: "sa_2" });
    const setup = vi.spyOn(api, "setupSuperAgent").mockImplementation(async (draft) => ({
      conversation_id: "superagent-setup-1", reply: "Added GitHub.", missing_secrets: [{ source_id: "src_1", key: "webhook_secret" }], problems: [],
      // Like the host, a later answer keeps what the form already collected.
      draft: { ...draft, sources: draft.sources.length ? draft.sources : [{ id: "src_1", kind: "github_webhook", enabled: true, allowed_actors: ["alice"], config: { agent_login: "bot" } }] },
    }));
    renderPanel({ createRequested: true });
    await until(() => container.querySelector("[role=dialog]"));
    // With no router agent yet, the form configures a new one.
    await until(() => control(/^Router agent/).value === "__new_router__");
    expect(control(/^Framework/).value).toBe("codex");
    expect(hasOption("Gemini")).toBe(false);
    expect(hasOption("Remote DSH")).toBe(false);
    expect(hasText("Read-only built-in tools, always")).toBe(true);
    expect(container.querySelector("details")?.open).toBe(false);
    const draftButton = () => button(/^Draft settings$/) as HTMLButtonElement;
    expect(draftButton().disabled).toBe(true);
    expect((button(/^Save$/) as HTMLButtonElement).disabled).toBe(false);

    setValue(control(/^What should it do/), "Watch GitHub for alice");
    await until(() => !draftButton().disabled);
    expect((button(/^Save$/) as HTMLButtonElement).disabled).toBe(true);
    expect(hasText("Click Draft settings to apply what you typed")).toBe(true);
    click(draftButton());
    await until(() => hasText("Added GitHub."));
    // Drafting runs on the router agent, so it is created first, once.
    expect(api.saveSuperAgentRouter).toHaveBeenCalledTimes(1);
    expect(vi.mocked(api.saveSuperAgentRouter).mock.calls[0][0]).toMatchObject({ id: "", runtime_id: "codex", mcp_servers: [] });
    expect(setup.mock.calls[0][0]).toMatchObject({ router_agent_id: "sar_new", router_runtime: "" });
    expect(setup.mock.calls[0][1]).toBe("Watch GitHub for alice");
    expect(save).not.toHaveBeenCalled();
    expect((control(/^What should it do/) as HTMLTextAreaElement).value).toBe("");
    expect(hasText("Enter the secrets marked *")).toBe(true);

    // The drafted trigger is on the form itself, with its secret field.
    const secret = control(/^Webhook secret/) as HTMLInputElement;
    expect(secret.type).toBe("password");
    expect(secret.closest("details")).toBeNull();
    expect((button(/^Save$/) as HTMLButtonElement).disabled).toBe(true);
    setValue(secret, "s3cret");
    await until(() => !(button(/^Save$/) as HTMLButtonElement).disabled);

    setValue(control(/^What should it do/), "Only on weekdays");
    await until(() => !draftButton().disabled);
    click(draftButton());
    await until(() => setup.mock.calls.length === 2);
    expect(setup.mock.calls[1][2]).toBe("superagent-setup-1");
    await until(() => !(button(/^Save$/) as HTMLButtonElement).disabled);

    click(button(/^Save$/) as HTMLButtonElement);
    await until(() => save.mock.calls.length > 0);
    expect(api.saveSuperAgentRouter).toHaveBeenCalledTimes(1);
    expect(save.mock.calls[0][0]).toMatchObject({ name: "", router_runtime: "", router_agent_id: "sar_new", mode: "observe", auto_workers: true, worker_runtimes: ["codex"], workers: [] });
    expect(save.mock.calls[0][0].sources[0].config.webhook_secret).toBe("s3cret");
  });

  it("creates a router on a framework that can route, with any number of worker frameworks", async () => {
    vi.spyOn(api, "frameworks").mockResolvedValue({ prereqs: { node: true, npm: true }, frameworks: [
      { spec: { kind: "kimi", display: "Kimi", kind_type: "cli", install_supported: true, install_requires_npm: false, update_supported: true, supported: true }, installed: true, registered: true },
      { spec: { kind: "codex", display: "Codex", kind_type: "cli", install_supported: true, install_requires_npm: false, update_supported: true, supported: true }, installed: true, registered: true },
    ] } as never);
    vi.spyOn(api, "superAgentRouters").mockResolvedValue(noRouters);
    const save = vi.spyOn(api, "saveSuperAgent").mockResolvedValue({ ...saved, id: "sa_2" });
    renderPanel({ createRequested: true });
    await until(() => workerFramework("Kimi"));
    await until(() => control(/^Framework/).value === "codex");
    expect(option("Kimi (no read-only mode)")?.disabled).toBe(true);
    expect(workerFramework("Codex")?.checked).toBe(true);
    // MCP servers are off until allowed.
    const oncall = workerFramework("oncall")!;
    expect(oncall.checked).toBe(false);
    click(oncall);
    setValue(control(/^Model/), "gpt-5-mini");

    click(workerFramework("Kimi")!);
    click(workerFramework("Codex")!);
    expect(workerFramework("Kimi")?.checked).toBe(true);
    click(workerFramework("Kimi")!);
    expect(hasText("Choose a worker framework")).toBe(true);
    expect((button(/^Save$/) as HTMLButtonElement).disabled).toBe(true);
    click(workerFramework("Kimi")!);
    click(button(/^Save$/) as HTMLButtonElement);
    await until(() => save.mock.calls.length > 0);
    expect(vi.mocked(api.saveSuperAgentRouter).mock.calls[0][0]).toMatchObject({ runtime_id: "codex", model: "gpt-5-mini", mcp_servers: ["oncall"] });
    expect(save.mock.calls[0][0]).toMatchObject({ router_agent_id: "sar_new", auto_workers: true, worker_runtimes: ["kimi"] });
  });

  it("shows each worker framework of a saved Super Agent", async () => {
    vi.spyOn(api, "superAgents").mockResolvedValue([{ ...saved, workers: [], auto_workers: true, worker_runtimes: ["codex", "kimi"] }]);
    renderPanel();
    await until(() => hasText("Fix build"));
    const chips = [...container.querySelectorAll(".super-agent-chip")].map((item) => item.textContent);
    expect(chips).toEqual(["codexAuto workers", "kimiAuto workers"]);
  });

  it("edits an existing Super Agent's router, workers and triggers outside advanced settings", async () => {
    renderPanel();
    await until(() => hasText("Fix build"));
    click(button(/^Edit$/));
    await until(() => control(/^Router agent/).value === "router");
    expect(hasText("Codex · framework's default model · read-only tools")).toBe(true);
    for (const label of [/^Router agent/, /^Routing rules/, /^Agent$/, /^Agent's GitHub login/, /^Add trigger/]) {
      expect(control(label).closest("details")).toBeNull();
    }
    expect(control(/^Mode/).closest("details")).toBeTruthy();
    // A router used by this Super Agent cannot be deleted from here.
    expect(button(/Delete router/)).toBeUndefined();
  });

  it("edits a router agent with the Super Agent, and deletes an unused one", async () => {
    vi.spyOn(api, "superAgentRouters").mockResolvedValue({ ...routerList, routers: [
      ...routerList.routers,
      { id: "spare", name: "Spare", runtime_id: "codex", mcp_servers: [], used_by: [] },
    ] });
    const remove = vi.spyOn(api, "deleteSuperAgentRouter").mockResolvedValue({ ok: true });
    vi.spyOn(window, "confirm").mockReturnValue(true);
    const save = vi.spyOn(api, "saveSuperAgent").mockResolvedValue(saved);
    renderPanel();
    await until(() => hasText("Fix build"));
    click(button(/^Edit$/));
    await until(() => control(/^Router agent/).value === "router");

    setValue(control(/^Router agent/), "spare");
    click(button(/Delete router/));
    await until(() => remove.mock.calls.length > 0);
    expect(remove).toHaveBeenCalledWith("spare");

    setValue(control(/^Router agent/), "router");
    click(button(/^Edit router$/));
    expect(hasText("A router agent's framework can't change")).toBe(true);
    setValue(control(/^Router name/), "Ops router");
    click(workerFramework("oncall")!);
    click(button(/^Save$/) as HTMLButtonElement);
    await until(() => save.mock.calls.length > 0);
    expect(vi.mocked(api.saveSuperAgentRouter).mock.calls[0][0]).toMatchObject({ id: "router", name: "Ops router", runtime_id: "codex", mcp_servers: ["oncall"] });
    expect(save.mock.calls[0][0]).toMatchObject({ id: "sa_1", router_agent_id: "router" });
  });

  it("offers only router agents as routers, and a Super Agent's own agents as neither", async () => {
    vi.spyOn(api, "agentInstances").mockResolvedValue([
      { id: "router", name: "Router", runtime_id: "codex", enabled: true, target_id: "local", source: "super_agent_router" } as never,
      { id: "saw_1", name: "Managed", runtime_id: "dsh", enabled: true, target_id: "local", source: "super_agent" } as never,
      { id: "coder", name: "Coder", runtime_id: "claudecode", enabled: true } as never,
    ]);
    renderPanel({ createRequested: true });
    await until(() => control(/^Router agent/).value === "router");
    const routerOptions = [...(control(/^Router agent/) as HTMLSelectElement).options].map((item) => item.textContent);
    expect(routerOptions).toEqual(["Router (Codex)", "New router agent…"]);
    click(button(/Add worker/) as HTMLButtonElement);
    await until(() => hasOption("Coder (claudecode)"));
    expect(hasOption("Managed (dsh)")).toBe(false);
    expect(hasOption("Router (codex)")).toBe(false);
  });

  it("keeps an ordinary Agent a Super Agent already routes with, until it switches", async () => {
    vi.spyOn(api, "superAgents").mockResolvedValue([{ ...saved, router_agent_id: "coder" }]);
    renderPanel();
    await until(() => hasText("Fix build"));
    click(button(/^Edit$/));
    await until(() => hasOption("Coder (an ordinary Agent, kept)"));
    expect(control(/^Router agent/).value).toBe("coder");
    expect(hasText("chosen before router agents existed")).toBe(true);
    setValue(control(/^Router agent/), "router");
    expect(hasOption("Coder (an ordinary Agent, kept)")).toBe(false);
  });

  it("says why Draft settings cannot run, and fills fields from templates", async () => {
    const confirm = vi.spyOn(window, "confirm").mockReturnValue(false);
    renderPanel({ createRequested: true });
    await until(() => control(/^Router agent/).value === "router");
    const draftButton = () => button(/^Draft settings$/) as HTMLButtonElement;
    const instruction = () => control(/^What should it do/) as HTMLTextAreaElement;
    expect(draftButton().disabled).toBe(true);
    expect(hasText("Describe what it should do above, or start from an example.")).toBe(true);

    click(button(/^GitHub issues and PRs$/));
    expect(instruction().value).toMatch(/^Handle GitHub issues and pull requests in <owner\/repo>/);
    expect(draftButton().disabled).toBe(false);
    expect(hasText("Describe what it should do above")).toBe(false);
    // What was typed is only replaced when the operator agrees.
    setValue(instruction(), "my own words");
    click(button(/^Feishu @mention$/));
    expect(instruction().value).toBe("my own words");
    confirm.mockReturnValue(true);
    click(button(/^Feishu @mention$/));
    expect(instruction().value).toMatch(/^When someone @mentions the bot/);

    const rules = () => control(/^Routing rules/) as HTMLTextAreaElement;
    confirm.mockClear();
    click(button(/^Use template$/));
    expect(confirm).not.toHaveBeenCalled();
    expect(rules().value).toMatch(/^Take on: requests about <project>/);
    expect(rules().value.split("\n")).toHaveLength(4);
    setValue(rules(), "only widgets");
    confirm.mockReturnValue(false);
    click(button(/^Use template$/));
    expect(rules().value).toBe("only widgets");
  });

  it("says when Draft settings needs a router agent first", async () => {
    vi.spyOn(api, "frameworks").mockResolvedValue({ prereqs: { node: true, npm: true }, frameworks: [] } as never);
    vi.spyOn(api, "superAgentRouters").mockResolvedValue(noRouters);
    renderPanel({ createRequested: true });
    await until(() => hasText("Choose or create a router agent below first."));
    click(button(/^Email requests$/));
    expect((button(/^Draft settings$/) as HTMLButtonElement).disabled).toBe(true);
  });

  it("explains when no framework is installed", async () => {
    vi.spyOn(api, "frameworks").mockResolvedValue({ prereqs: { node: true, npm: true }, frameworks: [] } as never);
    vi.spyOn(api, "superAgentRouters").mockResolvedValue(noRouters);
    renderPanel({ createRequested: true });
    await until(() => hasText("No framework is installed"));
  });

  it("shows why drafting failed and keeps the instruction", async () => {
    vi.spyOn(api, "setupSuperAgent").mockRejectedValue(new Error("model down"));
    renderPanel({ createRequested: true });
    await until(() => control(/^Router agent/).value === "router");
    setValue(control(/^What should it do/), "Watch GitHub");
    await until(() => !(button(/^Draft settings$/) as HTMLButtonElement).disabled);
    click(button(/^Draft settings$/) as HTMLButtonElement);
    await until(() => hasText("model down"));
    expect((control(/^What should it do/) as HTMLTextAreaElement).value).toBe("Watch GitHub");
  });

  it("keeps the form open and shows the server's reason when saving fails", async () => {
    vi.spyOn(api, "saveSuperAgent").mockRejectedValue(new Error("the router cannot use yolo"));
    vi.spyOn(api, "superAgents").mockResolvedValue([]);
    renderPanel();
    await until(() => button(/Create Super Agent/));
    click(button(/Create Super Agent/));
    await until(() => container.querySelector("[role=dialog]"));
    setValue(control(/^Name/), "x");
    await until(() => control(/^Router agent/).value === "router");
    click(button(/^Save$/));
    await until(() => hasText("the router cannot use yolo"));
    expect(container.querySelector("[role=dialog]")).toBeTruthy();
  });

  it("sends a Console request", async () => {
    const trigger = vi.spyOn(api, "triggerSuperAgent").mockResolvedValue({} as never);
    renderPanel();
    await until(() => hasText("Fix build"));
    setValue(control(/^Send a request/), "check prod");
    click(button(/^Send$/));
    await until(() => trigger.mock.calls.length > 0);
    expect(trigger).toHaveBeenCalledWith("sa_1", "check prod");
  });
});

describe("superAgentModel", () => {
  it("requires what the backend requires", () => {
    let agent = newSuperAgent();
    expect(superAgentProblems(agent)).toEqual(["superAgents.problem.router", "superAgents.problem.noWorkers"]);
    agent.router_agent_id = "r";
    agent = withWorkerRuntimes(agent, ["codex"]);
    expect(agent.auto_workers).toBe(true);
    expect(superAgentProblems(agent)).toEqual([]);
    agent.router_agent_id = "";
    agent.router_runtime = "codex";
    expect(superAgentProblems(agent)).toEqual([]);
    agent.router_runtime = "";
    expect(superAgentProblems(agent, newRouter())).toEqual(["superAgents.problem.routerFramework"]);
    expect(superAgentProblems(agent, newRouter("codex"))).toEqual([]);
    agent.router_runtime = "codex";
    agent = withWorkerRuntimes(agent, []);
    expect(agent.auto_workers).toBe(false);
    expect(superAgentProblems(agent)).toEqual(["superAgents.problem.noWorkers"]);
    agent.workers = [{ id: "", agent_id: "" }];
    expect(superAgentProblems(agent)).toEqual(["superAgents.problem.worker"]);
    agent.workers = [{ id: "", agent_id: "w" }];
    expect(superAgentProblems(agent)).toEqual([]);
    const github = newSource("github_webhook");
    agent.sources = [github];
    expect(superAgentProblems(agent)).toEqual(["superAgents.problem.actors", "superAgents.problem.sourceField"]);
    github.allowed_actors = ["*"];
    github.config = { agent_login: "b", webhook_secret: "s" };
    expect(superAgentProblems(agent)).toEqual(["superAgents.problem.wildcard"]);
    agent.sources = [{ ...newSource("feishu_message"), allowed_actors: ["*"], config: { channel_id: "c" } }];
    expect(superAgentProblems(agent)).toEqual([]);
  });

  it("parses sender lists", () => {
    expect(parseActors(" a@b.c, d\ne  f ")).toEqual(["a@b.c", "d", "e", "f"]);
  });
});
