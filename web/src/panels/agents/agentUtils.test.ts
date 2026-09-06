import { describe, expect, it } from "vitest";
import type { AgentInstance, Channel, CLIManagedTool, Provider, ProviderRoute } from "../../api";
import {
  agentRegistryMetrics,
  agentRouteModel,
  approvalModesForRuntime,
  boundChannelsForAgent,
  cliCatalogForAgent,
  composeInjectedPrompt,
  effectiveAgentProvider,
  newAgent,
  routeToolForRuntime,
  runtimeLabel,
  runtimeOptionValues,
  toggleID,
} from "./agentUtils";

describe("agent form model", () => {
  it("creates Agents with safe workspace defaults", () => {
    const agent = newAgent(["codex"]);
    expect(agent.runtime_id).toBe("codex");
    expect(agent.workspace_mode).toBe("shared");
    expect(agent.session_backend).toBe("structured");
  });

  it("composes channel prompts, logs, and CLI notes without duplicate prompt bodies", () => {
    const prompt = composeInjectedPrompt(
      "Base instructions",
      ["/tmp/channel.jsonl"],
      [{ name: "cis-cli", note: "Enterprise operations" }],
      [
        { name: "Feishu", prompt: "Answer concisely." },
        { name: "Slack", prompt: "Answer concisely." },
      ],
      "Channel defaults",
    );
    expect(prompt).toContain("Base instructions");
    expect(prompt).toContain("**Feishu、Slack**");
    expect(prompt.match(/Answer concisely\./g)).toHaveLength(1);
    expect(prompt).toContain("/tmp/channel.jsonl");
    expect(prompt).toContain("cis-cli：Enterprise operations");
  });

  it("shows each CLI once using the edited Agent's machine state", () => {
    const tool = (id: string, targetID: string, installed: boolean): CLIManagedTool => ({
      spec: { id, name: id, bin: id, package: id, uninstall_supported: true },
      installed,
      target_id: targetID,
    });
    const catalog = [
      tool("lark", "local", true),
      tool("opencli", "local", false),
      tool("lark", "ssh-1", false),
      tool("opencli", "ssh-1", true),
    ];

    expect(cliCatalogForAgent(catalog, "local").map(({ spec, installed }) => [spec.id, installed])).toEqual([
      ["lark", true],
      ["opencli", false],
    ]);
    expect(cliCatalogForAgent(catalog).map(({ spec, installed }) => [spec.id, installed])).toEqual([
      ["lark", false],
      ["opencli", false],
    ]);
  });

  it("keeps route and id toggles deterministic", () => {
    expect(routeToolForRuntime("claude-code")).toBe("claudecode");
	expect(routeToolForRuntime("codex-app")).toBe("codex-app");
	expect(runtimeLabel("codex-app")).toBe("Codex Desktop");
	expect(runtimeLabel("cursor")).toBe("Cursor");
	expect(approvalModesForRuntime("codex-app")).toContain("manual");
    expect(toggleID(["a"], "b")).toEqual(["a", "b"]);
    expect(toggleID(["a", "b"], "a")).toEqual(["b"]);
  });

	it("normalizes runtime model options returned by a signed-in CLI", () => {
		expect(runtimeOptionValues([
			{ value: " gpt-5.6-sol " },
			{ value: "gpt-5.6-sol", label: "duplicate" },
			{ value: "composer-2" },
		])).toEqual(["gpt-5.6-sol", "composer-2"]);
	});

	it("distinguishes usage clients without changing registered CLI runtimes", () => {
		expect(runtimeLabel("claude")).toBe("Claude Code CLI");
		expect(runtimeLabel("claudecode")).toBe("Claude Code CLI");
		expect(runtimeLabel("claude-desktop")).toBe("Claude Desktop");
		expect(runtimeLabel("codex")).toBe("Codex CLI");
		expect(runtimeLabel("codex-app")).toBe("Codex Desktop");
		expect(runtimeLabel("codex-vscode")).toBe("Codex IDE");
		expect(runtimeLabel("codex-unknown")).toBe("Codex · Unknown client");
		expect(runtimeLabel("claude-unknown", () => "来源未知")).toBe("Claude Code · 来源未知");
	});

  it("resolves effective Providers on the same machine and ignores local login", () => {
    const routes: ProviderRoute[] = [
      { tool: "codex", provider_id: "shared", model: "route-model", configured: true, target_id: "local" },
      { tool: "codex", provider_id: "shared", model: "remote-model", configured: true, target_id: "ssh-1" },
    ];
    const providers: Provider[] = [
      { id: "shared", name: "Local", base_url: "", model: "provider-model", enabled: true, target_id: "local" },
      { id: "shared", name: "Remote", base_url: "", model: "remote-provider-model", enabled: true, target_id: "ssh-1" },
    ];
    const local = { ...newAgent(["codex"]), id: "local-agent", target_id: "local" };
    const remote = { ...newAgent(["codex"]), id: "remote-agent", target_id: "ssh-1", provider_id: "shared" };
    const login = { ...newAgent(["cursor"]), id: "login-agent", target_id: "local" };

    expect(effectiveAgentProvider(local, routes, providers)?.provider?.name).toBe("Local");
    expect(effectiveAgentProvider(remote, routes, providers)?.provider?.name).toBe("Remote");
    expect(effectiveAgentProvider(login, routes, providers)).toBeNull();
  });

  it("uses agent, route, Provider, then default model precedence", () => {
    const route: ProviderRoute = { tool: "codex", provider_id: "p1", model: "route-model", configured: true };
    const provider: Provider = { id: "p1", name: "Provider", base_url: "", model: "provider-model", enabled: true };
    const agent = { ...newAgent(["codex"]), id: "agent-1" };

    expect(agentRouteModel({ ...agent, default_model: "agent-model" }, [route], [provider])).toEqual({ mode: "provider", model: "agent-model" });
    expect(agentRouteModel(agent, [route], [provider])).toEqual({ mode: "provider", model: "route-model" });
    expect(agentRouteModel(agent, [{ ...route, model: "" }], [provider])).toEqual({ mode: "provider", model: "provider-model" });
    expect(agentRouteModel({ ...agent, runtime_id: "cursor", provider_tool: "cursor" }, [], [])).toEqual({ mode: "login", model: "" });
  });

  it("counts Agents, effective Providers, machines, and only bound channels", () => {
    const agents: AgentInstance[] = [
      { ...newAgent(["codex"]), id: "a1", target_id: "local", provider_id: "p1" },
      { ...newAgent(["codex"]), id: "a2", target_id: "local", provider_id: "p1" },
      { ...newAgent(["codex"]), id: "a3", target_id: "ssh-1", provider_id: "p1" },
      { ...newAgent(["cursor"]), id: "a4", target_id: "ssh-1" },
    ];
    const channels: Channel[] = [
      { id: "c1", name: "Feishu", type: "feishu", enabled: true, agent_id: "a1", target_id: "local" },
      { id: "c1", name: "Remote Feishu", type: "feishu", enabled: true, agent_id: "a3", target_id: "ssh-1" },
      { id: "unbound", name: "Unused", type: "slack", enabled: true, target_id: "local" },
      { id: "wrong-target", name: "Wrong", type: "discord", enabled: true, agent_id: "a1", target_id: "ssh-1" },
    ];

    expect(agentRegistryMetrics(agents, [], [], channels)).toEqual({
      agentCount: 4,
      providerCount: 2,
      machineCount: 2,
      channelCount: 2,
    });
    expect(boundChannelsForAgent(agents[0], channels).map((channel) => channel.id)).toEqual(["c1"]);
  });
});
