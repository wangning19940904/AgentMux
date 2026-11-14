// @vitest-environment jsdom
import { act } from "react";
import type { ReactElement } from "react";
import { createRoot } from "react-dom/client";
import type { Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { APIError, api } from "../api";
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

// valueOf reads a control that may not be rendered yet.
function valueOf(label: RegExp): string | undefined {
  try {
    return control(label).value;
  } catch {
    return undefined;
  }
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
  routers: [{ id: "router", name: "Router", runtime_id: "codex", approval_mode: "plan", mcp_servers: [], used_by: [{ id: "sa_1", name: "Repo keeper" }] }],
  frameworks: [
    { runtime_id: "codex", can_route: true, mcp_servers: true, approval_modes: ["auto_edit", "auto", "plan", "yolo"], default_approval_mode: "yolo" },
    { runtime_id: "kimi", can_route: false, mcp_servers: false, reason: "no read-only mode" },
  ],
  mcp_servers: ["oncall"],
};
const noRouters: SuperAgentRouterList = { ...routerList, routers: [] };
// The same router before any Super Agent routes with it.
const freeRouters: SuperAgentRouterList = { ...routerList, routers: [{ ...routerList.routers[0], used_by: [] }] };

const efforts = (...values: string[]) => values.map((value) => ({ value }));
const codexSettings = {
  kind: "codex",
  defaults: { model: "gpt-6", reasoning_effort: "medium", service_tier: "default" },
  capabilities: {
    models: [{ value: "gpt-6", label: "GPT-6 Astra" }, { value: "gpt-6-mini" }],
    model_capabilities: {
      "gpt-6": { reasoning_efforts: efforts("low", "medium", "high", "xhigh"), service_tiers: efforts("default", "priority") },
      "gpt-6-mini": { reasoning_efforts: efforts("low", "medium"), service_tiers: efforts("default") },
    },
  },
};
const picker = () => container.querySelector<HTMLButtonElement>(".model-picker-trigger")!;
const fastSwitch = () => container.querySelector<HTMLInputElement>(".model-picker input[role=switch]");
const efforts_ = () => [...container.querySelectorAll(".model-picker-segments button")].map((item) => item.textContent);
const models = () => [...container.querySelectorAll(".model-picker-option")].map((item) => item.textContent);

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
  vi.spyOn(api, "frameworkRuntimeSettings").mockResolvedValue(codexSettings as never);
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
  vi.spyOn(api, "feishuUserAuth").mockResolvedValue({ user: null });
  vi.spyOn(api, "superAgentGitHubSetup").mockResolvedValue({ local_url: "http://127.0.0.1:8765", api_protected: false });
  // No GitHub CLI here unless a test says so: the guide offers pasting a token.
  vi.spyOn(api, "githubOperator").mockResolvedValue({ installed: false, repos: [] });
});

afterEach(() => {
  act(() => root?.unmount());
  root = null;
  container.remove();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
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

  it("lists the agent's follow-ups and cancels one that is still set", async () => {
    vi.spyOn(api, "superAgentActivity").mockResolvedValue({
      ...activity,
      timers: [
        { id: "stm_1", kind: "follow_up", status: "active", thread_key: "github:o/r#1", ticket_id: "t1", note: "Check whether Li approved.", due_at: "2026-11-04T09:00:00Z" },
        { id: "stm_2", kind: "follow_up", status: "done", thread_key: "github:o/r#2", note: "Ask about the release.", due_at: "2026-11-02T09:00:00Z", fired_at: "2026-11-02T09:00:01Z" },
      ],
      timer_warning: "2 more are due and wait.",
    });
    const cancel = vi.spyOn(api, "cancelSuperAgentTimer").mockResolvedValue({ ok: true });
    renderPanel();
    await until(() => hasText("Check whether Li approved."));
    for (const text of ["Follow-ups", "github:o/r#1 · Fix build", "Ask about the release.", "Fired", "2 more are due and wait."]) expect(hasText(text)).toBe(true);
    // Only the follow-up that is still set can be cancelled.
    expect([...container.querySelectorAll("button")].filter((item) => /^Cancel$/.test((item.textContent ?? "").trim()))).toHaveLength(1);
    click(button(/^Cancel$/));
    await until(() => cancel.mock.calls.length > 0);
    expect(cancel).toHaveBeenCalledWith("stm_1");
  });

  it("shows no follow-ups section for an agent that set none", async () => {
    renderPanel();
    await until(() => hasText("Fix build"));
    expect(hasText("Follow-ups")).toBe(false);
  });

  it("opens the create form on request, once, and defaults to observing", async () => {
    const handled = vi.fn();
    renderPanel({ createRequested: true, onCreateRequestHandled: handled });
    await until(() => container.querySelector("[role=dialog]"));
    expect(handled).toHaveBeenCalledTimes(1);
    expect(hasText("Records what the router would do")).toBe(true);
  });

  it("creates an agent with a router of its own, a remote working directory and a Feishu trigger", async () => {
    vi.spyOn(api, "superAgentRouters").mockResolvedValue(freeRouters);
    const save = vi.spyOn(api, "saveSuperAgent").mockResolvedValue({ ...saved, id: "sa_2" });
    renderPanel({ createRequested: true });
    await until(() => container.querySelector("[role=dialog]"));
    const saveButton = () => button(/^Save$/) as HTMLButtonElement;
    // Every agent gets a new router, even when an unused one exists, and its
    // workers start on the router's framework.
    await until(() => valueOf(/^Framework/) === "codex");
    expect(workerFramework("Codex")?.checked).toBe(true);
    expect(hasOption("Router (Codex)")).toBe(false);
    // The name is required.
    expect(saveButton().disabled).toBe(true);
    setValue(control(/^Agent name/), "Triage");
    expect(saveButton().disabled).toBe(false);

    // A remote path allows workers on that machine.
    await until(() => !(button(/^Add remote path$/) as HTMLButtonElement).disabled);
    click(button(/^Add remote path$/));
    expect(workerFramework("GPU box")?.checked).toBe(true);
    setValue(control(/^Directory on that machine/), "~/models");
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
    expect(vi.mocked(api.saveSuperAgentRouter).mock.calls[0][0]).toMatchObject({ id: "", name: "Triage router", runtime_id: "codex" });
    const body = save.mock.calls[0][0];
    expect(body).toMatchObject({ name: "Triage", router_agent_id: "sar_new", router_runtime: "", mode: "observe", worker_runtimes: ["codex"], workers: [] });
    expect(body.worker_machines).toEqual([{ host_id: "h1", dir: "~/models" }]);
    expect(body.sources[0]).toMatchObject({ kind: "feishu_message", allowed_actors: ["ou_a", "ou_b"], config: { channel_id: "c1", chat_ids: "oc_team" } });
    await until(() => !container.querySelector("[role=dialog]"));
  });

  it("works in a folder it creates by default, or in a local path chosen for it", async () => {
    vi.spyOn(api, "superAgentRouters").mockResolvedValue(noRouters);
    const save = vi.spyOn(api, "saveSuperAgent").mockResolvedValue({ ...saved, id: "sa_2" });
    renderPanel({ createRequested: true });
    await until(() => valueOf(/^Framework/) === "codex");
    expect(hasText("Default directory (created automatically)")).toBe(true);
    click(button(/Choose a local path$/));
    setValue(control(/^Local path/), "~/code/app");
    click(button(/^Use default$/));
    expect(() => control(/^Local path/)).toThrow();
    click(button(/Choose a local path$/));
    setValue(control(/^Local path/), "~/code/web");
    setValue(control(/^Agent name/), "Web");
    click(button(/^Save$/) as HTMLButtonElement);
    await until(() => save.mock.calls.length > 0);
    expect(save.mock.calls[0][0]).toMatchObject({ auto_worker_dir: "~/code/web" });
  });

  it("guides a GitHub trigger: the token fills in the login, and a local install gets a forward command", async () => {
    vi.spyOn(api, "superAgentRouters").mockResolvedValue(freeRouters);
    const check = vi.spyOn(api, "checkGitHubAccount").mockResolvedValue({ login: "my-agent-bot", scopes: ["read:org"], classic: true });
    renderPanel({ createRequested: true });
    await until(() => container.querySelector("[role=dialog]"));
    setValue(control(/^Add trigger/), "github_webhook");
    const links = () => [...container.querySelectorAll<HTMLAnchorElement>("a.setup-link")].map((link) => link.href);
    // Step 1 links to signing up, by link or QR code; without the GitHub CLI,
    // step 2 offers a token instead.
    expect(links()).toContain("https://github.com/signup");
    expect(container.querySelector(".setup-qr-link svg")).toBeTruthy();
    expect(links()).toContain("https://github.com/settings/tokens/new?scopes=repo&description=AgentMux+Super+Agent");
    await until(() => hasText("Install the GitHub CLI to sign the agent in here"));

    setValue(control(/^Agent's token/), "ghp_secret");
    click(button(/^Check token$/));
    await until(() => control(/^Agent's GitHub login/).value === "my-agent-bot");
    expect(check).toHaveBeenCalledWith({ token: "ghp_secret", api_base: "", super_agent_id: "", source_id: expect.stringMatching(/^src_/) });
    expect(hasText("Token belongs to @my-agent-bot")).toBe(true);
    // A classic token without the repo scope cannot reply.
    expect(hasText("This token can't comment")).toBe(true);

    // The test page is on localhost, which GitHub cannot reach, so the trigger
    // starts out reading notifications: nothing to set up on GitHub.
    expect(button(/^Check notifications$/)?.getAttribute("aria-pressed")).toBe("true");
    expect(hasText("checks the agent account's GitHub notifications")).toBe(true);
    expect(hasText("can only be read with a classic token")).toBe(false);
    click(button(/^Webhook$/));
    expect(hasText("GitHub can't reach this computer directly")).toBe(true);
    expect(hasText("Enter the repository as owner/name")).toBe(true);
    setValue(control(/^Only this repository/), "acme/app");
    const forward = [...container.querySelectorAll(".setup-command")].find((item) => item.querySelector("code")?.textContent?.startsWith("gh webhook forward"))!;
    // The secret is masked on screen; Copy hands over the real one.
    expect(forward.querySelector("code")?.textContent).toMatch(/^gh webhook forward --repo=acme\/app --events=issues,issue_comment,pull_request,pull_request_review_comment --url=http:\/\/127\.0\.0\.1:8765\/hook\/super-agent\/github\/src_[0-9a-f-]{36} --secret=•{8}$/);
    const writeText = vi.fn().mockResolvedValue(undefined);
    vi.stubGlobal("navigator", { ...navigator, clipboard: { writeText } });
    click(forward.querySelector("button")!);
    await until(() => writeText.mock.calls.length > 0);
    expect(writeText.mock.calls[0][0]).toMatch(/ --secret=[0-9a-f]{64}$/);

    // A public address switches the guide to an ordinary webhook, and warns
    // that the Console would be reachable there too.
    setValue(control(/^AgentMux public address/), "https://agent.example.com/");
    await until(() => hasText("Open webhook settings for acme/app"));
    expect(hasText(/https:\/\/agent\.example\.com\/hook\/super-agent\/github\/src_/)).toBe(true);
    expect([...container.querySelectorAll<HTMLAnchorElement>("a.setup-link")].some((link) => link.href === "https://github.com/acme/app/settings/hooks/new")).toBe(true);
    expect(hasText("The Console API has no password")).toBe(true);
  });

  it("checks a pasted token by itself and offers the repositories it reaches", async () => {
    vi.spyOn(api, "superAgentRouters").mockResolvedValue(freeRouters);
    const check = vi.spyOn(api, "checkGitHubAccount").mockResolvedValue({ login: "my-agent-bot", scopes: ["repo"], classic: true, repos: [
      { full_name: "acme/app", private: true, can_push: true },
      { full_name: "acme/docs", private: false, can_push: false },
    ] });
    renderPanel({ createRequested: true });
    await until(() => container.querySelector("[role=dialog]"));
    setValue(control(/^Add trigger/), "github_webhook");
    setValue(control(/^Agent's token/), "ghp_" + "a".repeat(36));
    // No click: the check runs once typing pauses.
    expect(check).not.toHaveBeenCalled();
    await act(async () => { await new Promise((resolve) => setTimeout(resolve, 700)); });
    await until(() => control(/^Agent's GitHub login/).value === "my-agent-bot");
    expect(check).toHaveBeenCalledTimes(1);

    const repos = control(/^Only this repository/) as HTMLSelectElement;
    expect(repos.tagName).toBe("SELECT");
    expect([...repos.options].map((item) => item.textContent)).toEqual(["Any repository the account can reach", "acme/app · private", "acme/docs · read only", "Other…"]);
    setValue(repos, "acme/docs");
    expect(hasText("can read this repository but not push to it")).toBe(true);
    // A repository the list does not hold can still be typed.
    setValue(control(/^Only this repository/), "__other__");
    expect(control(/^Only this repository/).tagName).toBe("INPUT");
    expect(control(/^Only this repository/).value).toBe("acme/docs");
  });

  it("signs the agent in with a one-time code, invites it to the repository, and suggests the operator as sender", async () => {
    vi.spyOn(api, "superAgentRouters").mockResolvedValue(freeRouters);
    vi.mocked(api.githubOperator).mockResolvedValue({ installed: true, login: "me", repos: ["acme/app"] });
    const token = "gho_" + "b".repeat(36);
    vi.spyOn(api, "startGitHubAgentLogin").mockResolvedValue({ id: "", session_id: "s1", phase: "login", state: "waiting", login_url: "https://github.com/login/device", verification_code: "ABCD-1234" });
    vi.spyOn(api, "pollGitHubAgentLogin").mockResolvedValue({ id: "", session_id: "s1", phase: "login", state: "succeeded", token });
    const check = vi.spyOn(api, "checkGitHubAccount")
      .mockResolvedValueOnce({ login: "agent-bot", scopes: ["repo", "notifications"], classic: true, repos: [] })
      .mockResolvedValue({ login: "agent-bot", scopes: ["repo", "notifications"], classic: true, repos: [{ full_name: "acme/app", private: true, can_push: true }] });
    const invite = vi.spyOn(api, "inviteGitHubAgent").mockResolvedValue({ invited: true, accepted: true });
    renderPanel({ createRequested: true });
    await until(() => container.querySelector("[role=dialog]"));
    setValue(control(/^Add trigger/), "github_webhook");
    await until(() => button(/^Sign in as the agent$/));
    expect(container.querySelector<HTMLDetailsElement>(".setup-details")?.open).toBe(false);

    click(button(/^Sign in as the agent$/));
    await until(() => hasText("ABCD-1234"));
    expect(api.startGitHubAgentLogin).toHaveBeenCalledWith("");
    expect(hasText("not as yourself")).toBe(true);
    expect(container.querySelectorAll(".qr-frame svg").length).toBe(2);
    // The token arrives with the finished sign-in and is checked like a pasted one.
    await act(async () => { await new Promise((resolve) => setTimeout(resolve, 2300)); });
    await until(() => control(/^Agent's GitHub login/).value === "agent-bot");
    expect(check.mock.calls[0][0]).toMatchObject({ token });
    expect(button(/^Sign in again$/)).toBeTruthy();

    // The operator administers acme/app, so the agent can be invited in one click.
    setValue(control(/^Only this repository/), "acme/app");
    expect(option("acme/app · needs access")).toBeTruthy();
    click(button(/^Invite @agent-bot and accept$/));
    await until(() => hasText("@agent-bot can push to acme/app."));
    expect(invite).toHaveBeenCalledWith(expect.objectContaining({ repo: "acme/app", agent_login: "agent-bot", invite: true, token }));

    click(button(/^Add yourself \(@me\)$/));
    expect((control(/^Allowed senders/) as HTMLTextAreaElement).value).toBe("me");
    expect(button(/^Add yourself/)).toBeUndefined();
  }, 10_000);

  it("warns when the agent is signed in as the operator's own account", async () => {
    vi.spyOn(api, "superAgentRouters").mockResolvedValue(freeRouters);
    vi.mocked(api.githubOperator).mockResolvedValue({ installed: true, login: "me", repos: [] });
    vi.spyOn(api, "checkGitHubAccount").mockResolvedValue({ login: "Me", scopes: ["repo"], classic: true, repos: [] });
    renderPanel({ createRequested: true });
    await until(() => container.querySelector("[role=dialog]"));
    setValue(control(/^Add trigger/), "github_webhook");
    setValue(control(/^Agent's token/), "ghp_short");
    click(button(/^Check token$/));
    await until(() => hasText("is your own GitHub account"));
    expect(hasText("Token belongs to")).toBe(false);
    expect(button(/^Add yourself/)).toBeUndefined();
  });

  it("checks a saved trigger's token when the form opens", async () => {
    const check = vi.spyOn(api, "checkGitHubAccount").mockResolvedValue({ login: "bot", scopes: ["repo"], classic: true, repos: [{ full_name: "acme/app", private: false, can_push: true }] });
    vi.spyOn(api, "superAgents").mockResolvedValue([{ ...saved, sources: [{ id: "src_1", kind: "github_webhook", enabled: true, allowed_actors: ["alice"],
      config: { agent_login: "bot", delivery: "poll", github_token: "<redacted>", repo: "ACME/app" } }] }]);
    renderPanel();
    await until(() => hasText("Fix build"));
    expect(hasText("Checks the agent's GitHub notifications")).toBe(true);
    click(button(/^Edit$/));
    await until(() => check.mock.calls.length > 0);
    expect(check).toHaveBeenCalledWith({ token: "<redacted>", api_base: "", super_agent_id: "sa_1", source_id: "src_1" });
    await until(() => control(/^Only this repository/).tagName === "SELECT");
    expect(control(/^Only this repository/).value).toBe("acme/app");
  });

  it("creates a Feishu bot by scanning, selects it, and lets the scanner ask", async () => {
    vi.spyOn(api, "superAgentRouters").mockResolvedValue(freeRouters);
    const created = { id: "c9", name: "Feishu Bot", type: "feishu", config: { app_id: "cli_new" } } as never;
    vi.mocked(api.channels).mockResolvedValueOnce([]).mockResolvedValue([created]);
    vi.spyOn(api, "beginFeishuSetup").mockResolvedValue({ device_code: "dev", qr_url: "https://accounts.feishu.cn/qr", interval: 1, expires_in: 600 });
    vi.spyOn(api, "pollFeishuSetup").mockResolvedValue({ status: "completed", app_id: "cli_new", app_secret: "shh", platform: "feishu", owner_open_id: "ou_me" });
    const upsert = vi.spyOn(api, "upsertChannel").mockResolvedValue(created);
    const save = vi.spyOn(api, "saveSuperAgent").mockResolvedValue({ ...saved, id: "sa_2" });
    renderPanel({ createRequested: true });
    await until(() => container.querySelector("[role=dialog]"));
    setValue(control(/^Agent name/), "Bot");
    setValue(control(/^Add trigger/), "feishu_message");
    expect(hasText("there is no App ID or secret to copy")).toBe(true);

    click(button(/^Scan to create a bot$/));
    await until(() => hasText("Bot created and selected."));
    expect(upsert.mock.calls[0][0]).toMatchObject({ type: "feishu", agent_id: "", enabled: true, config: { app_id: "cli_new", app_secret: "shh" } });
    await until(() => control(/^Feishu channel/).value === "c9");
    // The next step turns on its events with a second scan.
    expect(button(/^Scan to set up$/)).toBeTruthy();

    const saveButton = () => button(/^Save$/) as HTMLButtonElement;
    await until(() => !saveButton().disabled);
    click(saveButton());
    await until(() => save.mock.calls.length > 0);
    expect(save.mock.calls[0][0].sources[0]).toMatchObject({ kind: "feishu_message", allowed_actors: ["ou_me"], config: { channel_id: "c9" } });
  });

  it("fills in mail servers from the address and points to the provider's app passwords", async () => {
    vi.spyOn(api, "superAgentRouters").mockResolvedValue(freeRouters);
    renderPanel({ createRequested: true });
    await until(() => container.querySelector("[role=dialog]"));
    setValue(control(/^Add trigger/), "email");
    setValue(control(/^Email address/), "agent@gmail.com");
    expect(control(/^Mail provider/).value).toBe("gmail");
    expect(control(/^IMAP host/).value).toBe("imap.gmail.com");
    expect(control(/^SMTP host/).value).toBe("smtp.gmail.com");
    expect(hasText("Turn on 2-Step Verification")).toBe(true);
    expect([...container.querySelectorAll<HTMLAnchorElement>("a.setup-link")].map((link) => link.href)).toContain("https://myaccount.google.com/apppasswords");
    // An address it does not recognise keeps the provider chosen by hand.
    setValue(control(/^Mail provider/), "feishu");
    setValue(control(/^Email address/), "agent@gmail.com.cn");
    expect(control(/^IMAP host/).value).toBe("imap.feishu.cn");
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
    expect(container.querySelector(".super-agent-scope-apply")).toBeNull();
  });

  it("links to the Feishu console when the app is missing a permission", async () => {
    const applyURL = "https://open.feishu.cn/app/cli_a/auth?q=im:chat:readonly,im:chat.members:read&op_from=openapi&token_type=tenant";
    const message = "99991672 Access denied. One of the following scopes is required: [im:chat:readonly, im:chat.members:read]";
    vi.mocked(api.feishuMembers).mockRejectedValue(new APIError(message, 502, { error: message, scopes: ["im:chat:readonly", "im:chat.members:read"], apply_url: applyURL }));
    renderPanel({ createRequested: true });
    await until(() => container.querySelector("[role=dialog]"));
    setValue(control(/^Add trigger/), "feishu_message");
    await until(() => hasOption("Team bot"));
    setValue(control(/^Feishu channel/), "c1");
    await until(() => hasText("The bot is in 2 chats."));
    const senders = control(/^Allowed senders/) as HTMLInputElement;
    focus(senders);
    await until(() => hasText("Couldn't read chat members: the Feishu app is missing a permission."));
    // Feishu's own words repeat the scopes; they are kept for hovering only.
    expect(hasText("Access denied")).toBe(false);
    const scopes = () => [...container.querySelectorAll(".super-agent-scope-list code")].map((item) => item.textContent);
    expect(scopes()).toEqual(["im:chat:readonly", "im:chat.members:read"]);
    const apply = container.querySelector<HTMLAnchorElement>(".super-agent-scope-apply");
    expect(apply?.textContent).toBe("Request permission");
    expect(apply?.href).toBe(applyURL);
    expect(apply?.target).toBe("_blank");

    const lookupMessage = "99991672 Access denied. One of the following scopes is required: [contact:user.id:readonly]";
    vi.mocked(api.feishuUsers).mockRejectedValue(new APIError(lookupMessage, 502, { error: lookupMessage, scopes: ["contact:user.id:readonly"], apply_url: "https://open.feishu.cn/app/cli_a/auth?q=contact:user.id:readonly&op_from=openapi&token_type=tenant" }));
    setValue(senders, "dan@acme.com");
    press(senders, "Enter");
    await until(() => hasText("Couldn't look this up: the Feishu app is missing a permission."));
    expect(scopes()).toContain("contact:user.id:readonly");
    expect(container.querySelectorAll(".super-agent-scope-apply")).toHaveLength(2);
  });

  it("configures everything itself, and creates the new router agent when it saves", async () => {
    vi.spyOn(api, "superAgentRouters").mockResolvedValue(noRouters);
    const save = vi.spyOn(api, "saveSuperAgent").mockResolvedValue({ ...saved, id: "sa_2" });
    renderPanel({ createRequested: true });
    await until(() => container.querySelector("[role=dialog]"));
    await until(() => valueOf(/^Framework/) === "codex");
    expect(hasOption("Gemini")).toBe(false);
    expect(hasOption("Remote DSH")).toBe(false);
    // There is no Provider to choose.
    expect(hasText("Provider override")).toBe(false);
    // A new router acts on its own unless the operator chooses otherwise; its
    // permissions are folded away.
    expect(control(/^Permissions/).value).toBe("yolo");
    expect(control(/^Permissions/).closest("details")?.open).toBe(false);
    expect(hasText("With YOLO it answers from files")).toBe(true);
    expect(button(/Draft settings/)).toBeUndefined();

    setValue(control(/^Agent name/), "Ops");
    setValue(control(/^Add trigger/), "github_webhook");
    setValue(control(/^Agent's GitHub login/), "bot");
    setValue(control(/^Allowed senders/), "alice");
    // GitHub cannot reach a page on localhost, so the trigger reads the agent's
    // notifications, which needs its token.
    expect((button(/^Save$/) as HTMLButtonElement).disabled).toBe(true);
    setValue(control(/^Agent's token \*/), "ghp_x");
    await until(() => !(button(/^Save$/) as HTMLButtonElement).disabled);
    expect(api.saveSuperAgentRouter).not.toHaveBeenCalled();

    click(button(/^Save$/) as HTMLButtonElement);
    await until(() => save.mock.calls.length > 0);
    expect(api.saveSuperAgentRouter).toHaveBeenCalledTimes(1);
    expect(vi.mocked(api.saveSuperAgentRouter).mock.calls[0][0]).toMatchObject({ id: "", name: "Ops router", runtime_id: "codex", model: "", reasoning_effort: "", service_tier: "", mcp_servers: [] });
    expect(save.mock.calls[0][0]).toMatchObject({ name: "Ops", router_runtime: "", router_agent_id: "sar_new", mode: "observe", auto_workers: true, worker_runtimes: ["codex"], workers: [] });
    const github = save.mock.calls[0][0].sources[0];
    expect(github).toMatchObject({ kind: "github_webhook", allowed_actors: ["alice"], config: { agent_login: "bot", delivery: "poll", github_token: "ghp_x" } });
    expect(github.id).toMatch(/^src_[0-9a-f-]{36}$/);
    expect(github.config.webhook_secret).toMatch(/^[0-9a-f]{64}$/);
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
    await until(() => valueOf(/^Framework/) === "codex");
    expect(option("Kimi (can't run unattended)")?.disabled).toBe(true);
    setValue(control(/^Agent name/), "Kimi helper");
    expect(workerFramework("Codex")?.checked).toBe(true);
    // MCP servers are off until allowed.
    const oncall = workerFramework("oncall")!;
    expect(oncall.checked).toBe(false);
    click(oncall);
    await until(() => !picker().disabled);
    click(picker());
    await until(() => models().includes("gpt-6-mini"));
    click(button(/^gpt-6-mini$/));

    click(workerFramework("Kimi")!);
    click(workerFramework("Codex")!);
    expect(workerFramework("Kimi")?.checked).toBe(true);
    click(workerFramework("Kimi")!);
    expect(hasText("Choose a worker framework")).toBe(true);
    expect((button(/^Save$/) as HTMLButtonElement).disabled).toBe(true);
    click(workerFramework("Kimi")!);
    click(button(/^Save$/) as HTMLButtonElement);
    await until(() => save.mock.calls.length > 0);
    expect(vi.mocked(api.saveSuperAgentRouter).mock.calls[0][0]).toMatchObject({ runtime_id: "codex", model: "gpt-6-mini", mcp_servers: ["oncall"] });
    expect(save.mock.calls[0][0]).toMatchObject({ router_agent_id: "sar_new", auto_workers: true, worker_runtimes: ["kimi"] });
  });

  it("shows each worker framework of a saved Super Agent", async () => {
    vi.spyOn(api, "superAgents").mockResolvedValue([{ ...saved, workers: [], auto_workers: true, worker_runtimes: ["codex", "kimi"] }]);
    renderPanel();
    await until(() => hasText("Fix build"));
    const chips = [...container.querySelectorAll(".super-agent-chip")].map((item) => item.textContent);
    expect(chips).toEqual(["codexAuto workers", "kimiAuto workers"]);
  });

  it("lets an agent launch workers on another machine, and names the machine on its tickets", async () => {
    vi.spyOn(api, "superAgents").mockResolvedValue([{ ...saved, auto_workers: true, worker_runtimes: ["codex"], worker_machines: [{ host_id: "h1", dir: "~/code" }] }]);
    vi.spyOn(api, "superAgentActivity").mockResolvedValue({
      ...activity, machines: [{ host_id: "h1", name: "GPU box", available: false, detail: "device is offline" }],
      tickets: [{ id: "t3", title: "Train it", status: "running", worker: { id: "auto:codex@h1", agent_id: "sawr_sa_1_codex", host_id: "h1", label: "Repo keeper worker (codex)" }, updated_at: "2026-11-03T00:00:00Z" }],
    });
    const save = vi.spyOn(api, "saveSuperAgent").mockResolvedValue(saved);
    renderPanel();
    await until(() => hasText("Train it"));
    const chip = [...container.querySelectorAll(".super-agent-chip")].find((item) => item.textContent === "GPU boxlaunches workers");
    expect(chip?.getAttribute("title")).toBe("device is offline");
    expect(chip?.querySelector("i")?.className).toBe("offline");
    expect(hasText("Repo keeper worker (codex) · GPU box")).toBe(true);

    click(button(/^Edit$/));
    await until(() => control(/^Directory on that machine/).value === "~/code");
    // The only machine is already listed, so there is none left to add.
    await until(() => hasOption("GPU box"));
    expect((button(/^Add remote path$/) as HTMLButtonElement).disabled).toBe(true);
    expect(workerFramework("GPU box")?.checked).toBe(true);
    setValue(control(/^What this machine is for/), "Has the GPU");
    click(button(/^Save$/) as HTMLButtonElement);
    await until(() => save.mock.calls.length > 0);
    expect(save.mock.calls[0][0]).toMatchObject({ auto_workers: true, worker_machines: [{ host_id: "h1", dir: "~/code", capabilities: "Has the GPU" }] });

    // Unchecking the machine removes it and its directory.
    vi.mocked(api.saveSuperAgent).mockClear();
    click(button(/^Edit$/));
    await until(() => workerFramework("GPU box")?.checked);
    click(workerFramework("GPU box")!);
    expect(() => control(/^Directory on that machine/)).toThrow();
  });

  it("edits an existing agent's own router, directories, workers and triggers outside advanced settings", async () => {
    renderPanel();
    await until(() => hasText("Fix build"));
    click(button(/^Edit$/));
    await until(() => hasText("A router's framework cannot change once it is created."));
    expect(() => control(/^Framework/)).toThrow();
    for (const label of [/^Agent name/, /^Router prompt/, /^Worker approval mode/, /^Agent's GitHub login/, /^Add trigger/]) {
      expect(control(label).closest("details")).toBeNull();
    }
    expect(control(/^Mode/).closest("details")).toBeTruthy();
    // Workers from an older roster are listed, to be removed.
    expect(hasText("Existing Agent workers (legacy)")).toBe(true);
    expect(hasText("Laptop")).toBe(true);
  });

  it("edits the agent's router with it", async () => {
    const save = vi.spyOn(api, "saveSuperAgent").mockResolvedValue(saved);
    renderPanel();
    await until(() => hasText("Fix build"));
    click(button(/^Edit$/));
    await until(() => valueOf(/^Permissions/) === "plan");
    setValue(control(/^Permissions/), "yolo");
    click(workerFramework("oncall")!);
    click(button(/^Save$/) as HTMLButtonElement);
    await until(() => save.mock.calls.length > 0);
    expect(vi.mocked(api.saveSuperAgentRouter).mock.calls[0][0]).toMatchObject({ id: "router", name: "Router", runtime_id: "codex", approval_mode: "yolo", mcp_servers: ["oncall"] });
    expect(save.mock.calls[0][0]).toMatchObject({ id: "sa_1", router_agent_id: "router" });
  });




  it("keeps an ordinary Agent an older agent routes with, read-only", async () => {
    vi.spyOn(api, "superAgents").mockResolvedValue([{ ...saved, router_agent_id: "coder" }]);
    const save = vi.spyOn(api, "saveSuperAgent").mockResolvedValue(saved);
    renderPanel();
    await until(() => hasText("Fix build"));
    click(button(/^Edit$/));
    await until(() => hasText("This agent routes with an older Agent"));
    expect(hasText("Coder · claudecode")).toBe(true);
    expect(() => control(/^Permissions/)).toThrow();
    click(workerFramework("Codex")!);
    click(button(/^Save$/) as HTMLButtonElement);
    await until(() => save.mock.calls.length > 0);
    expect(api.saveSuperAgentRouter).not.toHaveBeenCalled();
    expect(save.mock.calls[0][0]).toMatchObject({ router_agent_id: "coder" });
  });

  it("fills the routing rules from a template, replacing them only when asked", async () => {
    const confirm = vi.spyOn(window, "confirm").mockReturnValue(false);
    vi.spyOn(api, "superAgentRouters").mockResolvedValue(freeRouters);
    renderPanel({ createRequested: true });
    await until(() => valueOf(/^Framework/) === "codex");
    const rules = () => control(/^Router prompt/) as HTMLTextAreaElement;
    click(button(/^Use template$/));
    await until(() => /^Take on:/.test(rules().value));
    expect(confirm).not.toHaveBeenCalled();
    expect(rules().value).toMatch(/^Take on: requests about <project>/);
    expect(rules().value.split("\n")).toHaveLength(4);
    setValue(rules(), "only widgets");
    click(button(/^Use template$/));
    await settle();
    expect(confirm).toHaveBeenCalledTimes(1);
    expect(rules().value).toBe("only widgets");
    confirm.mockReturnValue(true);
    click(button(/^Use template$/));
    await until(() => /^Take on:/.test(rules().value));
  });

  it("chooses the router's model, reasoning effort and speed in one picker, and names it after the Super Agent", async () => {
    vi.spyOn(api, "superAgentRouters").mockResolvedValue(noRouters);
    vi.spyOn(api, "saveSuperAgent").mockResolvedValue({ ...saved, id: "sa_2" });
    renderPanel({ createRequested: true });
    await until(() => valueOf(/^Framework/) === "codex");
    setValue(control(/^Agent name/), "Triage");
    expect(picker().textContent).toBe("Default model");
    click(picker());
    await until(() => models().length > 0);
    expect(api.frameworkRuntimeSettings).toHaveBeenCalledWith("codex", "", "local");
    expect(models()).toEqual(["Default (GPT-6 Astra)", "GPT-6 Astra", "gpt-6-mini"]);
    expect(efforts_()).toEqual(["Default (Medium)", "Low", "Medium", "High", "Extra high"]);
    expect(hasText("The framework lists these models for its current setup (its own sign-in, a gateway or a router).")).toBe(true);
    click(button(/^Extra high$/));
    click(fastSwitch()!);
    expect(picker().textContent).toBe("Default model · Extra high · Fast");

    // A model that lacks the chosen effort or fast mode falls back to its defaults.
    click(button(/^gpt-6-mini$/));
    expect(picker().textContent).toBe("gpt-6-mini");
    expect(efforts_()).toEqual(["Default (Medium)", "Low", "Medium"]);
    expect(fastSwitch()).toBeNull();

    click(button(/^GPT-6 Astra$/));
    click(button(/^High$/));
    click(fastSwitch()!);
    expect(picker().textContent).toBe("GPT-6 Astra · High · Fast");
    act(() => { document.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape" })); });
    expect(container.querySelector(".model-picker-popover")).toBeNull();

    click(button(/^Save$/) as HTMLButtonElement);
    await until(() => vi.mocked(api.saveSuperAgentRouter).mock.calls.length > 0);
    expect(vi.mocked(api.saveSuperAgentRouter).mock.calls[0][0]).toMatchObject({
      name: "Triage router", runtime_id: "codex", model: "gpt-6", reasoning_effort: "high", service_tier: "priority",
    });
  });

  it("offers the framework's efforts and speed when its default model is unknown", async () => {
    vi.spyOn(api, "superAgentRouters").mockResolvedValue(noRouters);
    vi.spyOn(api, "frameworkRuntimeSettings").mockResolvedValue({
      ...codexSettings, defaults: {},
      capabilities: { ...codexSettings.capabilities, reasoning_efforts: efforts("low", "ultra"), service_tiers: efforts("default", "priority") },
    } as never);
    renderPanel({ createRequested: true });
    await until(() => valueOf(/^Framework/) === "codex");
    click(picker());
    await until(() => models().length > 0);
    expect(models()[0]).toBe("Default model");
    expect(efforts_()).toEqual(["Default", "Low", "Ultra"]);
    expect(fastSwitch()).not.toBeNull();
  });

  it("lets the router use a model ID the framework does not list, such as a router's group", async () => {
    vi.spyOn(api, "superAgentRouters").mockResolvedValue(noRouters);
    renderPanel({ createRequested: true });
    await until(() => valueOf(/^Framework/) === "codex");
    click(picker());
    await until(() => models().length > 0);
    // Models always come from the framework, never from AgentMux providers.
    expect(api.frameworkRuntimeSettings).toHaveBeenCalledWith("codex", "", "local");
    const input = container.querySelector<HTMLInputElement>(".model-picker-typed input")!;
    setValue(input, "group/opus-anywhere");
    click(button(/^Use$/));
    expect(picker().textContent).toBe("group/opus-anywhere");
    expect(models()).toContain("group/opus-anywhere");
  });

  it("lets the router's model be typed when the framework's models cannot be read", async () => {
    vi.spyOn(api, "superAgentRouters").mockResolvedValue(noRouters);
    vi.spyOn(api, "frameworkRuntimeSettings").mockRejectedValue(new Error("not signed in"));
    renderPanel({ createRequested: true });
    await until(() => valueOf(/^Framework/) === "codex");
    click(picker());
    await until(() => hasText("Couldn't read the models: not signed in."));
    const input = container.querySelector<HTMLInputElement>(".model-picker-popover input")!;
    setValue(input, "gpt-6");
    expect(picker().textContent).toBe("gpt-6");
    expect(fastSwitch()).toBeNull();
  });

  it("explains when no framework is installed", async () => {
    vi.spyOn(api, "frameworks").mockResolvedValue({ prereqs: { node: true, npm: true }, frameworks: [] } as never);
    vi.spyOn(api, "superAgentRouters").mockResolvedValue(noRouters);
    renderPanel({ createRequested: true });
    await until(() => hasText("No framework is installed"));
  });

  it("keeps the form open and shows the server's reason when saving fails", async () => {
    vi.spyOn(api, "saveSuperAgent").mockRejectedValue(new Error("the router cannot use yolo"));
    vi.spyOn(api, "superAgents").mockResolvedValue([]);
    vi.spyOn(api, "superAgentRouters").mockResolvedValue(freeRouters);
    renderPanel();
    await until(() => button(/Create Super Agent/));
    click(button(/Create Super Agent/));
    await until(() => container.querySelector("[role=dialog]"));
    setValue(control(/^Agent name/), "x");
    await until(() => valueOf(/^Framework/) === "codex");
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
    // Reading notifications needs the token instead of a webhook secret.
    const polling = { ...newSource("github_webhook"), allowed_actors: ["alice"], config: { delivery: "poll", agent_login: "b" } as Record<string, string> };
    agent.sources = [polling];
    expect(superAgentProblems(agent)).toEqual(["superAgents.problem.sourceField"]);
    polling.config = { ...polling.config, github_token: "t" };
    expect(superAgentProblems(agent)).toEqual([]);
  });

  it("shows one agent with its own brain: its bound channels are listed, not edited, and saving needs no workers", async () => {
    const own: SuperAgent = {
      id: "coder", name: "Coder", router_agent_id: "coder", mode: "act", max_concurrency: 2, enabled: true, workers: [],
      sources: [{ id: "channel:c1", kind: "channel", name: "Team bot", enabled: true, allowed_actors: [], config: { channel_id: "c1" } }],
    };
    vi.mocked(api.superAgents).mockResolvedValue([saved, own]);
    const save = vi.spyOn(api, "saveSuperAgent").mockImplementation(async (agent) => agent);
    renderPanel({ agentID: "coder" });
    await until(() => hasText("Chats only"));
    expect(hasText("Repo keeper")).toBe(false);
    expect(hasText("Bound channel · Team bot")).toBe(true);
    expect(button(/^Delete$/)).toBeUndefined();
    click(button(/Edit workers and triggers/));
    await until(() => hasText("This agent answers with its pre-migration Agent settings"));
    expect(() => control(/^Framework/)).toThrow();
    expect([...container.querySelectorAll("[role=dialog] .super-agent-chip")].some((item) => item.textContent?.includes("Team bot"))).toBe(true);
    click(await until(() => workerFramework("Codex")));
    click(button(/Save/));
    await until(() => save.mock.calls.length > 0);
    expect(save.mock.calls[0][0]).toMatchObject({ id: "coder", router_agent_id: "coder", auto_workers: true, worker_runtimes: ["codex"] });
  });

  it("parses sender lists", () => {
    expect(parseActors(" a@b.c, d\ne  f ")).toEqual(["a@b.c", "d", "e", "f"]);
  });
});
