import { describe, expect, it } from "vitest";
import type { AgentInstance, Channel, CLIManagedTool } from "../../api";
import {
  agentRegistryMetrics,
  approvalModesForRuntime,
  boundChannelsForAgent,
  cliCatalogForAgent,
  composeInjectedPrompt,
  isReadOnlyAgent,
  isRouterAgent,
  newAgent,
  readOnlyNoticeKey,
  runtimeLabel,
  runtimeOptionValues,
  toggleID,
} from "./agentUtils";

describe("agent form model", () => {
  it("keeps Super Agent router agents read-only on the Agents page", () => {
    const router = { ...newAgent(["codex"]), id: "sar_1", source: "super_agent_router" };
    const legacy = { ...newAgent(["codex"]), id: "sar_codex", source: "super_agent" };
    const worker = { ...newAgent(["codex"]), id: "saw_1_codex", source: "super_agent" };
    expect([router, legacy, worker].map(isRouterAgent)).toEqual([true, true, false]);
    expect(isReadOnlyAgent(router)).toBe(true);
    expect(isReadOnlyAgent(worker)).toBe(false);
    expect(readOnlyNoticeKey(router)).toBe("agents.routerReadOnlyNotice");
  });

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

  it("keeps labels and id toggles deterministic", () => {
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
		expect(runtimeLabel("codex-agentmux")).toBe("Codex · AgentMux internal");
		expect(runtimeLabel("codex-vscode")).toBe("Codex IDE");
		expect(runtimeLabel("codex-unknown")).toBe("Codex · Unknown client");
		expect(runtimeLabel("claude-unknown", () => "来源未知")).toBe("Claude Code · 来源未知");
	});

  it("counts Agents, machines, and only bound channels", () => {
    const agents: AgentInstance[] = [
      { ...newAgent(["codex"]), id: "a1", target_id: "local" },
      { ...newAgent(["codex"]), id: "a2", target_id: "local" },
      { ...newAgent(["codex"]), id: "a3", target_id: "ssh-1" },
      { ...newAgent(["cursor"]), id: "a4", target_id: "ssh-1" },
    ];
    const channels: Channel[] = [
      { id: "c1", name: "Feishu", type: "feishu", enabled: true, agent_id: "a1", target_id: "local" },
      { id: "c1", name: "Remote Feishu", type: "feishu", enabled: true, agent_id: "a3", target_id: "ssh-1" },
      { id: "unbound", name: "Unused", type: "slack", enabled: true, target_id: "local" },
      { id: "wrong-target", name: "Wrong", type: "discord", enabled: true, agent_id: "a1", target_id: "ssh-1" },
    ];

    expect(agentRegistryMetrics(agents, channels)).toEqual({
      agentCount: 4,
      machineCount: 2,
      channelCount: 2,
    });
    expect(boundChannelsForAgent(agents[0], channels).map((channel) => channel.id)).toEqual(["c1"]);
  });
});
