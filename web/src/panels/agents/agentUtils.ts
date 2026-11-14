import { api, type AgentInstance, type Channel, type CLIManagedTool, type RuntimeOption, type SuperAgent, type Trigger } from "../../api";

export type DrawerMode = "create" | "edit";

export const EMPTY_AGENT: AgentInstance = {
  id: "",
  name: "",
  runtime_id: "",
  desktop_thread_id: "",
  work_dir: "",
  workspace_mode: "shared",
  worktree_base_ref: "",
  session_backend: "structured",
  system_prompt: "",
  default_model: "",
  default_reasoning_effort: "",
  default_service_tier: "",
  default_approval_mode: "",
  private_chat_mode: "chat",
  group_chat_mode: "chat-topic",
  memory_scope: "",
  channel_bindings: [],
  schedules: [],
  mcp_servers: [],
  skills: [],
  clis: [],
  enabled: true,
  source: "manual",
};

// Fleet tool discovery returns one catalog entry per machine. Agent editing is
// target-scoped, so only that machine's catalog is relevant. A target-less
// draft represents a fleet-wide create; collapse those entries and consider a
// CLI available only when it is installed everywhere it was reported.
export function cliCatalogForAgent(catalog: CLIManagedTool[], targetID?: string): CLIManagedTool[] {
  const scoped = targetID ? catalog.filter((item) => item.target_id === targetID) : catalog;
  const byID = new Map<string, CLIManagedTool>();
  for (const item of scoped) {
    const current = byID.get(item.spec.id);
    byID.set(item.spec.id, current
      ? { ...current, installed: current.installed && item.installed }
      : item);
  }
  return [...byID.values()];
}

export function composeInjectedPrompt(
  base: string,
  logPaths: string[],
  clis: { name: string; note: string }[],
  channelPrompts: { name: string; prompt: string }[] = [],
  channelPromptHeading = "渠道消息默认注入（每条入站消息）"
): string {
  const sections: string[] = [];
  const trimmedBase = base.replace(/\n+$/, "");

  const groupedChannelPrompts = new Map<string, string[]>();
  channelPrompts.forEach(({ name, prompt }) => {
    const trimmedPrompt = prompt.trim();
    if (!trimmedPrompt) return;
    const names = groupedChannelPrompts.get(trimmedPrompt) ?? [];
    if (name.trim() && !names.includes(name.trim())) names.push(name.trim());
    groupedChannelPrompts.set(trimmedPrompt, names);
  });
  groupedChannelPrompts.forEach((names, prompt) => {
    const source = names.length > 0 ? `\n\n**${names.join("、")}**` : "";
    sections.push(`### ${channelPromptHeading}${source}\n\n${prompt}`);
  });

  if (logPaths.length > 0) {
    sections.push(["绑定的事件回调日志路径为：", ...logPaths.map((path) => `- ${path}`)].join("\n"));
  }
  if (clis.length > 0) {
    const lines = clis
      .filter((cli) => cli.name.trim())
      .map((cli) => (cli.note.trim() ? `- ${cli.name}：${cli.note}` : `- ${cli.name}`));
    sections.push(["已启用以下 CLI 工具：", ...lines].join("\n"));
  }

  return [trimmedBase, ...sections].filter((part) => part !== "").join("\n\n");
}

export async function syncAgentConnectBindings(
  agentID: string,
  selectedChannelIDs: string[],
  selectedTriggerIDs: string[],
  channels: Channel[],
  triggers: Trigger[]
) {
  const selectedChannels = new Set(selectedChannelIDs);
  const selectedTriggers = new Set(selectedTriggerIDs);
  const channelUpdates = channels
    .map((channel) => {
      const currentAgentID = channel.agent_id ?? "";
      const nextAgentID = selectedChannels.has(channel.id) ? agentID : currentAgentID === agentID ? "" : currentAgentID;
      return nextAgentID === currentAgentID ? null : api.upsertChannel({ ...channel, agent_id: nextAgentID, target_id: channel.target_id });
    })
    .filter((update): update is Promise<Channel> => Boolean(update));
  const triggerUpdates = triggers
    .map((trigger) => {
      const currentAgentID = trigger.agent_id ?? "";
      const nextAgentID = selectedTriggers.has(trigger.id) ? agentID : currentAgentID === agentID ? "" : currentAgentID;
      return nextAgentID === currentAgentID ? null : api.upsertTrigger({ ...trigger, agent_id: nextAgentID, target_id: trigger.target_id });
    })
    .filter((update): update is Promise<Trigger> => Boolean(update));
  await Promise.all([...channelUpdates, ...triggerUpdates]);
}

export function toggleID(items: string[], id: string): string[] {
  return items.includes(id) ? items.filter((item) => item !== id) : [...items, id];
}

export function newAgent(runtimeOptions: string[]): AgentInstance {
  const runtime = runtimeOptions[0] ?? "";
  return copyAgent({
    ...EMPTY_AGENT,
    runtime_id: runtime,
    memory_scope: "agent:new",
    source: "manual",
  });
}

export function runtimeLabel(runtime: string, t?: (key: string) => string): string {
  switch (runtime) {
    case "claude":
    case "claudecode":
      return "Claude Code CLI";
    case "codex":
      return "Codex CLI";
    case "codex-agentmux":
      return `Codex · ${t?.("usage.agentmuxInternal") ?? "AgentMux internal"}`;
    case "claude-unknown":
      return `Claude Code · ${t?.("usage.unknownClient") ?? "Unknown client"}`;
    case "codex-unknown":
      return `Codex · ${t?.("usage.unknownClient") ?? "Unknown client"}`;
    case "codex-vscode":
      return "Codex IDE";
    case "cursor":
      return "Cursor";
    case "gemini":
      return "Gemini CLI";
    case "dsh":
      return "DeepSeek Harness";
    case "iflow":
      return "iFlow CLI";
    case "kimi":
      return "Kimi CLI";
    case "opencode":
      return "OpenCode CLI";
    case "qoder":
      return "Qoder CLI";
    case "traecli":
      return "TRAE CLI";
    case "claude-desktop":
      return "Claude Desktop";
    case "codex-app":
      return "Codex Desktop";
    default:
      return runtime;
  }
}

export function resourceTargetID(resource: { target_id?: string }): string {
  return resource.target_id?.trim() || "local";
}

export function boundChannelsForAgent(agent: AgentInstance, channels: Channel[]): Channel[] {
  const targetID = resourceTargetID(agent);
  return channels.filter(
    (channel) => resourceTargetID(channel) === targetID && channel.agent_id === agent.id,
  );
}

export function agentRegistryMetrics(
  agents: AgentInstance[],
  channels: Channel[],
): { agentCount: number; machineCount: number; channelCount: number } {
  const agentKeys = new Set(agents.map((agent) => `${resourceTargetID(agent)}::${agent.id}`));
  const machineIDs = new Set(agents.map((agent) => resourceTargetID(agent)));
  const channelKeys = new Set<string>();
  channels.forEach((channel) => {
    if (!channel.agent_id) return;
    const targetID = resourceTargetID(channel);
    if (agentKeys.has(`${targetID}::${channel.agent_id}`)) channelKeys.add(`${targetID}::${channel.id}`);
  });

  return {
    agentCount: agents.length,
    machineCount: machineIDs.size,
    channelCount: channelKeys.size,
  };
}

export function runtimeOptionValues(options: RuntimeOption[] | undefined): string[] {
	if (!Array.isArray(options)) return [];
	const seen = new Set<string>();
	const values: string[] = [];
	options.forEach((option) => {
		const value = option?.value?.trim();
		if (!value || seen.has(value)) return;
		seen.add(value);
		values.push(value);
	});
	return values;
}

export function serviceTierLabel(value: string): string {
	if (value === "priority" || value === "fast") return `${value} (${value === "priority" ? "快速" : "fast"})`;
	if (value === "default" || value === "normal" || value === "standard") return `${value} (普通)`;
	return value;
}

export function workDirErrorMessage(err: unknown, t: (key: string) => string): string {
  const message = err instanceof Error ? err.message : String(err);
  if (message.includes("desktop directory picker unavailable")) return t("agents.workDirPickerUnavailable");
  if (message.includes("directory path is required")) return t("agents.workDirRequired");
  if (message.includes("path is not a directory")) return t("agents.workDirNotDirectory");
  return message;
}

export function isConfigManaged(agent: AgentInstance): boolean {
  return agent.source === "config.toml" || agent.id.startsWith("config:");
}

// Router agents are configured under Super Agents, which keep them read-only
// and without tools; routers made per framework before that were tagged like
// auto workers.
export function isRouterAgent(agent: AgentInstance): boolean {
  return agent.source === "super_agent_router" || (agent.source === "super_agent" && agent.id.startsWith("sar_"));
}

// isAgentPart reports whether an Agent instance belongs to an agent rather
// than being one: a router agent or an auto worker. The Agents page shows them
// through the agent they belong to.
export function isAgentPart(agent: AgentInstance): boolean {
  return agent.source === "super_agent" || agent.source === "super_agent_router";
}

// unifiedAgentFor finds the agent whose brain an Agent instance on this
// machine is. Agents on other machines are managed there.
export function unifiedAgentFor(agent: AgentInstance, agents: SuperAgent[]): SuperAgent | undefined {
  if (agent.target_id && agent.target_id !== "local") return undefined;
  return agents.find((item) => item.id === agent.id && item.router_agent_id === agent.id)
    ?? agents.find((item) => item.router_agent_id === agent.id);
}

// agentsWithoutRow are the agents whose brain is not an Agent row of its own:
// those that answer with a router agent. The Agents page lists them as rows too.
export function agentsWithoutRow(agents: SuperAgent[], instances: AgentInstance[]): SuperAgent[] {
  const rows = new Set(instances.filter((item) => !isAgentPart(item) && (!item.target_id || item.target_id === "local")).map((item) => item.id));
  return agents.filter((agent) => !rows.has(agent.router_agent_id));
}

// workPolicy is the short label of what an agent does besides chatting.
export function workPolicy(agent: SuperAgent | undefined): { key: string; params?: Record<string, string | number> } {
  if (!agent || (!agent.auto_workers && agent.workers.length === 0)) return { key: "agents.work.policyChat" };
  if (agent.auto_workers && (agent.worker_runtimes ?? []).length > 0) {
    return { key: "agents.work.policyAuto", params: { frameworks: (agent.worker_runtimes ?? []).join(", ") } };
  }
  return { key: "agents.work.policyRoster", params: { count: agent.workers.length } };
}

export function isReadOnlyAgent(agent: AgentInstance): boolean {
  return isConfigManaged(agent) || isRouterAgent(agent);
}

export function readOnlyNoticeKey(agent: AgentInstance): string {
  return isRouterAgent(agent) ? "agents.routerReadOnlyNotice" : "agents.readOnlyNotice";
}

export function agentSourceLabelKey(agent: AgentInstance): string {
  if (isRouterAgent(agent)) return "agents.sourceRouter";
  if (isConfigManaged(agent)) return "agents.sourceConfig";
  if (agent.source === "manual") return "agents.sourceManual";
  if (agent.source === "console" || !agent.source) return "agents.sourceConsole";
  return "agents.sourceUnknown";
}

export function agentSourceDetailKey(agent: AgentInstance): string {
  if (isRouterAgent(agent)) return "agents.routerManaged";
  if (isConfigManaged(agent)) return "agents.configManaged";
  if (agent.source === "manual") return "agents.manualManagedDetail";
  return "agents.consoleManagedDetail";
}

export function agentSourceClass(agent: AgentInstance): string {
  if (isReadOnlyAgent(agent)) return "config";
  if (agent.source === "manual") return "manual";
  return "console";
}

export function copyAgent(agent: AgentInstance): AgentInstance {
  return {
    ...agent,
    env: { ...(agent.env ?? {}) },
    channel_bindings: (agent.channel_bindings ?? []).map((channel) => ({ ...channel, config: { ...(channel.config ?? {}) } })),
    schedules: (agent.schedules ?? []).map((schedule) => ({ ...schedule })),
    mcp_servers: [...(agent.mcp_servers ?? [])],
    skills: [...(agent.skills ?? [])],
  };
}

export function approvalModesForRuntime(runtimeID: string) {
  switch (runtimeID) {
    case "claude":
    case "claudecode":
    case "qoder":
    case "codex":
    case "codex-app":
    case "traecli":
      return ["manual", "auto_edit", "auto", "plan", "yolo"];
    case "gemini":
    case "opencode":
    case "iflow":
      return ["manual", "auto_edit", "plan", "yolo"];
    case "cursor":
      return ["manual", "auto", "plan", "yolo"];
    case "dsh":
      return ["auto_edit", "plan", "yolo"];
    case "kimi":
      return ["auto"];
    default:
      return [];
  }
}

export function approvalModeLabelKey(mode: string) {
  return `approval.${mode}`;
}
