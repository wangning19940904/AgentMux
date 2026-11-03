// @vitest-environment jsdom
import { act } from "react";
import type { ReactElement } from "react";
import { createRoot } from "react-dom/client";
import type { Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { api } from "../api";
import type { SuperAgent, SuperAgentActivity } from "../api";
import { I18nProvider } from "../i18n";
import { SuperAgentsPanel } from "./SuperAgentsPanel";
import { newSource, newSuperAgent, parseActors, superAgentProblems, withWorkerRuntimes } from "./superAgentModel";

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

function renderPanel(props = {}) {
  mount(<I18nProvider language="en"><SuperAgentsPanel {...props} /></I18nProvider>);
}

beforeEach(() => {
  vi.stubGlobal("IS_REACT_ACT_ENVIRONMENT", true);
  vi.spyOn(api, "superAgents").mockResolvedValue([saved]);
  vi.spyOn(api, "superAgentActivity").mockResolvedValue(activity);
  vi.spyOn(api, "agentInstances").mockResolvedValue([
    { id: "router", name: "Router", runtime_id: "dsh", enabled: true, target_id: "local" } as never,
    { id: "coder", name: "Coder", runtime_id: "claudecode", enabled: true } as never,
    { id: "elsewhere", name: "Elsewhere", runtime_id: "dsh", enabled: true, target_id: "h1" } as never,
  ]);
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
    expect(saveButton().disabled).toBe(true);

    setValue(control(/^Name/), "Triage");
    await until(() => hasOption("Router (dsh)"));
    expect(hasOption("Elsewhere (dsh)")).toBe(false);
    setValue(control(/^Use an existing Agent/), "router");
    click(button(/Add worker/) as HTMLButtonElement);
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
    setValue(control(/^Allowed senders/), "ou_a, ou_b");
    expect(saveButton().disabled).toBe(false);

    click(saveButton());
    await until(() => save.mock.calls.length > 0);
    const body = save.mock.calls[0][0];
    expect(body).toMatchObject({ name: "Triage", router_agent_id: "router", mode: "observe" });
    expect(body.workers).toEqual([{ id: "", agent_id: "gpu", host_id: "h1" }]);
    expect(body.sources[0]).toMatchObject({ kind: "feishu_message", allowed_actors: ["ou_a", "ou_b"], config: { channel_id: "c1" } });
    await until(() => !container.querySelector("[role=dialog]"));
  });

  it("needs only a framework, and sets up triggers from an instruction", async () => {
    const save = vi.spyOn(api, "saveSuperAgent").mockResolvedValue({ ...saved, id: "sa_2" });
    const setup = vi.spyOn(api, "setupSuperAgent").mockImplementation(async (draft) => ({
      conversation_id: "superagent-setup-1", reply: "Added GitHub.", missing_secrets: [{ source_id: "src_1", key: "webhook_secret" }], problems: [],
      // Like the host, a later answer keeps what the form already collected.
      draft: { ...draft, sources: draft.sources.length ? draft.sources : [{ id: "src_1", kind: "github_webhook", enabled: true, allowed_actors: ["alice"], config: { agent_login: "bot" } }] },
    }));
    renderPanel({ createRequested: true });
    await until(() => container.querySelector("[role=dialog]"));
    await until(() => hasOption("Codex"));
    expect(hasOption("Gemini")).toBe(false);
    expect(hasOption("Remote DSH")).toBe(false);
    const draftButton = () => button(/^Draft settings$/) as HTMLButtonElement;
    expect(draftButton().disabled).toBe(true);
    expect(container.querySelector("details")?.open).toBe(false);

    setValue(control(/^Router framework/), "codex");
    expect((button(/^Save$/) as HTMLButtonElement).disabled).toBe(false);
    setValue(control(/^What should it do/), "Watch GitHub for alice");
    await until(() => !draftButton().disabled);
    expect((button(/^Save$/) as HTMLButtonElement).disabled).toBe(true);
    expect(hasText("Click Draft settings to apply what you typed")).toBe(true);
    click(draftButton());
    await until(() => hasText("Added GitHub."));
    expect(setup.mock.calls[0][1]).toBe("Watch GitHub for alice");
    expect(save).not.toHaveBeenCalled();
    expect(hasText("GitHub webhook")).toBe(true);
    expect((control(/^What should it do/) as HTMLTextAreaElement).value).toBe("");

    const secret = [...container.querySelectorAll<HTMLInputElement>(".super-agent-proposal input")][0];
    expect(secret.type).toBe("password");
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
    expect(save.mock.calls[0][0]).toMatchObject({ name: "", router_runtime: "codex", router_agent_id: "", mode: "observe", auto_workers: true, worker_runtimes: ["codex"], workers: [] });
    expect(save.mock.calls[0][0].sources[0].config.webhook_secret).toBe("s3cret");
  });

  it("chooses one router framework and any number of worker frameworks", async () => {
    vi.spyOn(api, "frameworks").mockResolvedValue({ prereqs: { node: true, npm: true }, frameworks: [
      { spec: { kind: "codex", display: "Codex", kind_type: "cli", install_supported: true, install_requires_npm: false, update_supported: true, supported: true }, installed: true, registered: true },
      { spec: { kind: "kimi", display: "Kimi", kind_type: "cli", install_supported: true, install_requires_npm: false, update_supported: true, supported: true }, installed: true, registered: true },
    ] } as never);
    const save = vi.spyOn(api, "saveSuperAgent").mockResolvedValue({ ...saved, id: "sa_2" });
    renderPanel({ createRequested: true });
    await until(() => workerFramework("Kimi"));
    expect(control(/^Router framework/).tagName).toBe("SELECT");
    expect(workerFramework("Codex")?.checked).toBe(false);

    setValue(control(/^Router framework/), "codex");
    expect(workerFramework("Codex")?.checked).toBe(true);
    click(workerFramework("Kimi")!);
    click(workerFramework("Codex")!);
    expect(workerFramework("Kimi")?.checked).toBe(true);
    // Changing the router keeps the workers that were chosen.
    setValue(control(/^Router framework/), "kimi");
    setValue(control(/^Router framework/), "codex");
    expect(workerFramework("Codex")?.checked).toBe(false);

    click(workerFramework("Kimi")!);
    expect(hasText("Choose a worker framework")).toBe(true);
    expect((button(/^Save$/) as HTMLButtonElement).disabled).toBe(true);
    click(workerFramework("Kimi")!);
    click(button(/^Save$/) as HTMLButtonElement);
    await until(() => save.mock.calls.length > 0);
    expect(save.mock.calls[0][0]).toMatchObject({ router_runtime: "codex", auto_workers: true, worker_runtimes: ["kimi"] });
  });

  it("shows each worker framework of a saved Super Agent", async () => {
    vi.spyOn(api, "superAgents").mockResolvedValue([{ ...saved, workers: [], auto_workers: true, worker_runtimes: ["codex", "kimi"] }]);
    renderPanel();
    await until(() => hasText("Fix build"));
    const chips = [...container.querySelectorAll(".super-agent-chip")].map((item) => item.textContent);
    expect(chips).toEqual(["codexAuto workers", "kimiAuto workers"]);
  });

  it("shows an existing Super Agent's triggers without opening advanced settings", async () => {
    renderPanel();
    await until(() => hasText("Fix build"));
    click(button(/^Edit$/));
    const proposal = await until(() => container.querySelector(".super-agent-proposal"));
    expect(proposal.textContent).toContain("GitHub webhook");
    expect(proposal.textContent).toContain("alice");
  });

  it("does not offer a Super Agent's own workers as routers or workers", async () => {
    vi.spyOn(api, "agentInstances").mockResolvedValue([
      { id: "router", name: "Router", runtime_id: "dsh", enabled: true, target_id: "local" } as never,
      { id: "saw_1", name: "Managed", runtime_id: "dsh", enabled: true, target_id: "local", source: "super_agent" } as never,
    ]);
    renderPanel({ createRequested: true });
    await until(() => hasOption("Router (dsh)"));
    expect(hasOption("Managed (dsh)")).toBe(false);
  });

  it("switches cleanly between a framework and an existing Agent", async () => {
    vi.spyOn(api, "saveSuperAgent").mockResolvedValue({ ...saved, id: "sa_2" });
    renderPanel({ createRequested: true });
    await until(() => hasOption("Codex"));
    setValue(control(/^Router framework/), "codex");
    await until(() => hasOption("Router (dsh)"));
    setValue(control(/^Use an existing Agent/), "router");
    expect(control(/^Router framework/).value).toBe("");
    expect(hasText("Using an existing Agent")).toBe(true);
    setValue(control(/^Router framework/), "codex");
    expect(control(/^Use an existing Agent/).value).toBe("");
  });

  it("explains when no framework is installed", async () => {
    vi.spyOn(api, "frameworks").mockResolvedValue({ prereqs: { node: true, npm: true }, frameworks: [] } as never);
    renderPanel({ createRequested: true });
    await until(() => hasText("No framework is installed"));
  });

  it("shows why drafting failed and keeps the instruction", async () => {
    vi.spyOn(api, "setupSuperAgent").mockRejectedValue(new Error("model down"));
    renderPanel({ createRequested: true });
    await until(() => hasOption("Codex"));
    setValue(control(/^Router framework/), "codex");
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
    await until(() => hasOption("Router (dsh)"));
    setValue(control(/^Use an existing Agent/), "router");
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
