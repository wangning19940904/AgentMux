import { api, type AgentInstance, type Channel, type Provider, type ProviderRoute, type Trigger } from "../../api";

export type DrawerMode = "create" | "edit";

export const EMPTY_AGENT: AgentInstance = {
  id: "",
  name: "",
  runtime_id: "",
  work_dir: "",
  workspace_mode: "shared",
  worktree_base_ref: "",
  session_backend: "structured",
  system_prompt: "",
  provider_tool: "",
  provider_id: "",
  default_model: "",
  default_reasoning_effort: "",
  default_service_tier: "",
  default_approval_mode: "",
  memory_scope: "",
  channel_bindings: [],
  schedules: [],
  mcp_servers: [],
  skills: [],
  clis: [],
  enabled: true,
  source: "manual",
};

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
      return nextAgentID === currentAgentID ? null : api.upsertChannel({ ...channel, agent_id: nextAgentID });
    })
    .filter((update): update is Promise<Channel> => Boolean(update));
  const triggerUpdates = triggers
    .map((trigger) => {
      const currentAgentID = trigger.agent_id ?? "";
      const nextAgentID = selectedTriggers.has(trigger.id) ? agentID : currentAgentID === agentID ? "" : currentAgentID;
      return nextAgentID === currentAgentID ? null : api.upsertTrigger({ ...trigger, agent_id: nextAgentID });
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
    provider_tool: routeToolForRuntime(runtime),
    memory_scope: "agent:new",
    source: "manual",
  });
}

export function routeToolForRuntime(runtime: string): string {
  return normalizeTool(runtime);
}

export function routeToolOptionsForRuntime(runtime: string): string[] {
  const tool = routeToolForRuntime(runtime);
  return tool ? [tool] : [];
}

export function runtimeLabel(runtime: string): string {
  switch (runtime) {
    case "claudecode":
      return "Claude Code CLI";
    case "codex":
      return "Codex CLI";
    case "cursor":
      return "Cursor Agent CLI";
    case "gemini":
      return "Gemini CLI";
    case "iflow":
      return "iFlow CLI";
    case "kimi":
      return "Kimi CLI";
    case "opencode":
      return "OpenCode CLI";
    case "qoder":
      return "Qoder CLI";
    case "claude-desktop":
      return "Claude Desktop";
    case "codex-app":
      return "Codex Desktop";
    default:
      return runtime;
  }
}

export function activeRouteForTool(routes: ProviderRoute[], tool: string): ProviderRoute | undefined {
  const normalized = normalizeTool(tool);
  return routes.find((route) => route.tool === tool || normalizeTool(route.tool) === normalized);
}

export function agentProviderSummary(agent: AgentInstance, activeRoutes: ProviderRoute[], t: (key: string) => string): string {
  if (agent.provider_name || agent.provider_id) return `${t("agents.providerOverrideShort")}: ${agent.provider_name || agent.provider_id}`;
  const route = activeRouteForTool(activeRoutes, agent.provider_tool || agent.runtime_id);
  const provider = route?.configured ? route.provider_name || route.provider_id : "";
  return provider ? `${t("agents.followRouteShort")}: ${provider}` : t("agents.followRouteShort");
}

export function normalizeTool(tool: string): string {
  switch (tool.trim()) {
    case "claude":
    case "claudecode":
    case "claudecode-cli":
    case "claude-code":
    case "claude-code-cli":
      return "claudecode";
    case "claude-desktop":
    case "claudecode-desktop":
    case "claude-code-desktop":
      return "claude-desktop";
    case "codex":
    case "codex-cli":
    case "codex-app":
    case "codex-desktop":
    case "codex-app-server":
      return "codex";
    default:
      return tool.trim();
  }
}

export function providerModelOptions(provider: Provider | undefined): string[] {
  const seen = new Set<string>();
  const out: string[] = [];
  const add = (model: unknown) => {
    if (typeof model !== "string") return;
    const trimmed = model.trim();
    if (!trimmed || seen.has(trimmed)) return;
    seen.add(trimmed);
    out.push(trimmed);
  };
  add(provider?.model);
  const supported = provider?.meta?.supported_models;
  if (Array.isArray(supported)) supported.forEach(add);
  return out;
}

export function runtimeProviderOptions(provider: Provider | undefined, key: "supported_reasoning_efforts" | "supported_service_tiers"): string[] {
	const values = provider?.meta?.[key];
	if (!Array.isArray(values)) return [];
	return values.filter((value): value is string => typeof value === "string" && value.trim().length > 0).map((value) => value.trim());
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

export function agentSourceLabelKey(agent: AgentInstance): string {
  if (isConfigManaged(agent)) return "agents.sourceConfig";
  if (agent.source === "manual") return "agents.sourceManual";
  if (agent.source === "console" || !agent.source) return "agents.sourceConsole";
  return "agents.sourceUnknown";
}

export function agentSourceDetailKey(agent: AgentInstance): string {
  if (isConfigManaged(agent)) return "agents.configManaged";
  if (agent.source === "manual") return "agents.manualManagedDetail";
  return "agents.consoleManagedDetail";
}

export function agentSourceClass(agent: AgentInstance): string {
  if (isConfigManaged(agent)) return "config";
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
      return ["manual", "auto_edit", "auto", "plan", "yolo"];
    case "gemini":
    case "opencode":
    case "iflow":
      return ["manual", "auto_edit", "plan", "yolo"];
    case "cursor":
      return ["manual", "auto", "plan", "yolo"];
    case "kimi":
      return ["auto"];
    default:
      return [];
  }
}

export function approvalModeLabelKey(mode: string) {
  return `approval.${mode}`;
}
