// REST data layer. cc-switch used Tauri IPC (invoke); here we talk to the Go
// daemon's HTTP API so the same React UI serves both the WebUI and the Wails
// desktop shell.

export * from "./types";
export * from "./client";
export * from "./desktop";
import { activeRemoteID, get, getChecked, post, put, postChecked, del } from "./client";
import { observationGet, observationPost } from "./observability";
import { testRemoteHost, updateRemoteHost, statusRemoteHost, importRemoteHost } from "./remote";
import { selectSystemDirectory } from "./desktop";
import type {
  AgentInstance,
  AgentSession,
  CLIInstallResult,
  CLIUpdateCheck,
  Channel,
  ChannelConversation,
  ChannelInteraction,
  ChannelTask,
  Claude3PStatus,
  DiscoveredRemoteHost,
  FeishuSetupBeginResponse,
  FeishuSetupPollResponse,
  FrameworkAuthStatus,
  FrameworkInstallResult,
  FrameworkLoginResult,
  FrameworkUpdateCheck,
  FrameworksResponse,
  GuardPolicy,
  MCPServer,
  MarketplaceSkill,
  MemoryEntry,
  MenubarSettings,
  ObservationInsight,
  ObservationIntegration,
  ObservationIntegrationActionResult,
  ObservationOverview,
  ObservationSettings,
  ObservationTrace,
  ObservationTraceDetail,
  ObservationTraceFilters,
  Provider,
  ProviderMonitorConfig,
  ProviderMonitorSnapshot,
  ProviderProbeResult,
  ProviderRoute,
  ProxyStatus,
  ProxyToolConfig,
  ProxyTrace,
  RemoteHost,
  SessionMessage,
  Skill,
  Status,
  SystemDirectoryListing,
  ToolsResponse,
  Trigger,
  UsageReport,
  WorkspaceInitResult,
} from "./types";

function normalizeProvider(provider: Partial<Provider> & Record<string, unknown>): Provider {
  const extra =
    provider.extra && typeof provider.extra === "object" && !Array.isArray(provider.extra)
      ? (provider.extra as Record<string, string>)
      : {};
  const meta =
    provider.meta && typeof provider.meta === "object" && !Array.isArray(provider.meta)
      ? (provider.meta as Record<string, unknown>)
      : {};

  return {
    id: typeof provider.id === "string" ? provider.id : "",
    name: typeof provider.name === "string" ? provider.name : "Provider",
    preset: typeof provider.preset === "string" ? provider.preset : undefined,
    category: typeof provider.category === "string" ? provider.category : "custom",
    base_url: typeof provider.base_url === "string" ? provider.base_url : "",
    api_key_env: typeof provider.api_key_env === "string" ? provider.api_key_env : "",
    api_key: typeof provider.api_key === "string" ? provider.api_key : "",
    api_key_available: Boolean(provider.api_key_available),
    api_key_issue: typeof provider.api_key_issue === "string" ? provider.api_key_issue : "",
    model: typeof provider.model === "string" ? provider.model : "",
    extra,
    settings_config:
      provider.settings_config && typeof provider.settings_config === "object" && !Array.isArray(provider.settings_config)
        ? (provider.settings_config as Record<string, unknown>)
        : undefined,
    meta,
    enabled: Boolean(provider.enabled),
    in_failover_queue: Boolean(provider.in_failover_queue),
    sort_index: typeof provider.sort_index === "number" ? provider.sort_index : 0,
  };
}

async function getProviders(path: string): Promise<Provider[] | null> {
  const providers = await get<Provider[] | null>(path);
  if (!Array.isArray(providers)) return providers;
  return providers.map((provider) => normalizeProvider(provider as Partial<Provider> & Record<string, unknown>));
}

export const api = {
  // SSH remote control. These paths intentionally bypass the selected target.
  remoteHosts: () => get<RemoteHost[]>("/api/v1/remote/hosts"),
  discoveredRemoteHosts: () =>
    get<DiscoveredRemoteHost[]>("/api/v1/remote/discovered-hosts"),
  upsertRemoteHost: (host: Partial<RemoteHost>) =>
    postChecked<RemoteHost>("/api/v1/remote/hosts", host),
  importRemoteHost,
  deleteRemoteHost: (id: string) =>
    del<{ ok: boolean }>(`/api/v1/remote/hosts?id=${encodeURIComponent(id)}`),
  testRemoteHost,
  statusRemoteHost,
  updateRemoteHost,

  status: () => get<Status>("/api/v1/status"),
  platforms: () => get<string[]>("/api/v1/platforms"),
  agents: () => get<string[]>("/api/v1/agents"),
  agentInstances: () => get<AgentInstance[] | null>("/api/v1/agent-instances"),
  upsertAgentInstance: (agent: Partial<AgentInstance>) =>
    postChecked<AgentInstance>("/api/v1/agent-instances", agent),
  initializeAgentWorkspace: (payload: { id?: string } | Partial<AgentInstance>) =>
    postChecked<WorkspaceInitResult>("/api/v1/agent-instances/initialize", payload),
  deleteAgentInstance: (id: string) =>
    del<{ ok: boolean }>(`/api/v1/agent-instances?id=${encodeURIComponent(id)}`),
  tools: () => get<ToolsResponse>("/api/v1/tools"),
  installCLI: (id: string, action: "install" | "update") =>
    postChecked<CLIInstallResult>("/api/v1/tools/cli/install", { id, action }),
  checkCLIUpdate: (id: string) => post<CLIUpdateCheck>("/api/v1/tools/cli/check", { id }),
  syncCLISkills: (id: string) =>
    postChecked<CLIInstallResult>("/api/v1/tools/cli/skills/sync", { id }),
  providers: () => getProviders("/api/v1/providers"),
  activeRoutes: () => get<ProviderRoute[] | null>("/api/v1/providers/active"),
  presets: async () => (await getProviders("/api/v1/providers/presets")) ?? [],
  upsertProvider: (p: Provider) => post<Provider>("/api/v1/providers", p),
  probeProvider: (p: Provider) => postChecked<ProviderProbeResult>("/api/v1/providers/probe", p),
  providerMonitor: () => get<ProviderMonitorSnapshot>("/api/v1/providers/monitor"),
  saveProviderMonitor: (config: ProviderMonitorConfig) =>
    put<ProviderMonitorSnapshot>("/api/v1/providers/monitor", config),
  runProviderMonitor: () =>
    postChecked<ProviderMonitorSnapshot>("/api/v1/providers/monitor/run", {}),
  dismissProviderMonitorAlert: (id = "") =>
    del<ProviderMonitorSnapshot>(
      `/api/v1/providers/monitor/alerts${id ? `?id=${encodeURIComponent(id)}` : ""}`
    ),
  deleteProvider: (id: string) => del<{ ok: boolean }>(`/api/v1/providers?id=${encodeURIComponent(id)}`),
  switchProvider: (id: string, tool: string, meta?: Record<string, unknown>, localTakeover?: boolean) =>
    post<{ ok: boolean }>("/api/v1/providers/switch", { id, tool, meta, local_takeover: localTakeover }),
  disableRoute: (tool: string) =>
    del<{ ok: boolean }>(`/api/v1/providers/active?tool=${encodeURIComponent(tool)}`),
  proxyStatus: () => get<ProxyStatus>("/api/v1/proxy/status"),
  proxyTraces: ({ tool = "", sessionID = "", limit = 100 }: { tool?: string; sessionID?: string; limit?: number } = {}) =>
    get<ProxyTrace[] | null>(
      `/api/v1/proxy/traces?tool=${encodeURIComponent(tool)}&session_id=${encodeURIComponent(sessionID)}&limit=${limit}`
    ),
  setTakeover: (tool: string, enabled: boolean) =>
    postChecked<ProxyStatus>("/api/v1/proxy/takeover", { tool, enabled }),
  setProxyToolConfig: (cfg: Partial<ProxyToolConfig> & { tool: string }) =>
    postChecked<ProxyStatus>("/api/v1/proxy/config", cfg),
  setFailoverQueue: (id: string, inQueue: boolean, sortIndex = 0) =>
    postChecked<{ ok: boolean }>("/api/v1/providers/failover", {
      id,
      in_failover_queue: inQueue,
      sort_index: sortIndex,
    }),
  claude3pStatus: () => get<Claude3PStatus>("/api/v1/system/claude-3p"),
  setClaude3p: (enabled: boolean, providerID = "") =>
    postChecked<Claude3PStatus>("/api/v1/system/claude-3p", {
      enabled,
      provider_id: providerID,
    }),
  selectDirectory: (defaultDirectory = "") => selectSystemDirectory(defaultDirectory),
  directories: (path = "") => {
    const remoteID = activeRemoteID();
    return remoteID
      ? getChecked<SystemDirectoryListing>(
          `/api/v1/remote/directories?id=${encodeURIComponent(remoteID)}&path=${encodeURIComponent(path)}`
        )
      : getChecked<SystemDirectoryListing>(`/api/v1/system/directories?path=${encodeURIComponent(path)}`);
  },
  ensureDirectory: (path: string) => {
    const remoteID = activeRemoteID();
    return postChecked<{ path: string }>(
      remoteID
        ? `/api/v1/remote/directories?id=${encodeURIComponent(remoteID)}`
        : "/api/v1/system/directories",
      { path },
    );
  },

  // AgentMux Frameworks: detect & install agent frameworks
  frameworks: () => get<FrameworksResponse>("/api/v1/frameworks"),
  frameworkAuth: (kind: string) =>
    getChecked<FrameworkAuthStatus>(`/api/v1/frameworks/auth?kind=${encodeURIComponent(kind)}`),
  startFrameworkLogin: (kind: string) =>
    postChecked<FrameworkLoginResult>("/api/v1/frameworks/login", { kind }),
  completeFrameworkLogin: (sessionID: string, code: string) =>
    postChecked<{ ok: boolean }>("/api/v1/frameworks/login/complete", { session_id: sessionID, code }),
  installFramework: (kind: string, action: "install" | "update" = "install") =>
    postChecked<FrameworkInstallResult>("/api/v1/frameworks/install", { kind, action }),
  checkFrameworkUpdate: (kind: string) =>
    post<FrameworkUpdateCheck>("/api/v1/frameworks/check", { kind }),
  usage: (period: string, from = "", to = "") => {
    const params = new URLSearchParams({ period });
    if (from) params.set("from", from);
    if (to) params.set("to", to);
    return get<UsageReport>(`/api/v1/usage?${params.toString()}`);
  },
  menubarSettings: () => get<MenubarSettings>("/api/v1/menubar/settings"),
  saveMenubarSettings: (settings: MenubarSettings) =>
    put<MenubarSettings>("/api/v1/menubar/settings", settings),

  // AgentMux Observability
  observationOverview: () => observationGet<ObservationOverview>("/api/v1/observability/overview"),
  observationTraces: ({
    agentID = "",
    runtimeID = "",
    sessionID = "",
    status = "",
    source = "",
    limit = 100,
    offset = 0,
  }: ObservationTraceFilters = {}) =>
    observationGet<ObservationTrace[] | { traces: ObservationTrace[]; total?: number }>(
      `/api/v1/observability/traces?agent_id=${encodeURIComponent(agentID)}&runtime_id=${encodeURIComponent(
        runtimeID
      )}&session_id=${encodeURIComponent(sessionID)}&status=${encodeURIComponent(status)}&source=${encodeURIComponent(
        source
      )}&limit=${limit}&offset=${offset}`
    ),
  observationTrace: (traceID: string) =>
    observationGet<ObservationTraceDetail>(`/api/v1/observability/traces/${encodeURIComponent(traceID)}`),
  observationInsights: ({
    agentID = "",
    status = "",
    ruleID = "",
    limit = 100,
  }: {
    agentID?: string;
    status?: string;
    ruleID?: string;
    limit?: number;
  } = {}) =>
    observationGet<ObservationInsight[] | { insights: ObservationInsight[]; total?: number }>(
      `/api/v1/observability/insights?agent_id=${encodeURIComponent(agentID)}&status=${encodeURIComponent(
        status
      )}&rule_id=${encodeURIComponent(ruleID)}&limit=${limit}`
    ),
  observationSettings: () => observationGet<ObservationSettings>("/api/v1/observability/settings"),
  observationIntegrations: () =>
    observationGet<ObservationIntegration[] | { integrations: ObservationIntegration[] }>("/api/v1/observability/integrations"),
  observationIntegrationAction: (
    host: string,
    action: "preview" | "install" | "repair" | "uninstall" | "doctor",
    body: Record<string, unknown> = {}
  ) =>
    observationPost<ObservationIntegrationActionResult>(
      `/api/v1/observability/integrations/${encodeURIComponent(host)}/${action}`,
      body
    ),

  // AgentMux Connect: channels & triggers
  channels: () => get<Channel[] | null>("/api/v1/channels"),
  upsertChannel: (ch: Partial<Channel>) =>
    postChecked<Channel>("/api/v1/remote/channels/claim", {
      target_id: activeRemoteID(),
      channel: ch,
    }),
  deleteChannel: (id: string) => del<{ ok: boolean }>(`/api/v1/channels?id=${encodeURIComponent(id)}`),
  restartChannel: (id: string) =>
    postChecked<{ ok: boolean }>(`/api/v1/channels/restart?id=${encodeURIComponent(id)}`, {}),
  channelConversations: (channelID: string) =>
    get<ChannelConversation[] | null>(
      `/api/v1/channel-conversations?channel_id=${encodeURIComponent(channelID)}`
    ),
  bindChannelConversation: (channelID: string, conversationID: string, threadID: string) =>
    postChecked<{ ok: boolean; thread_id: string }>("/api/v1/channel-conversations/bind", {
      channel_id: channelID,
      conversation_id: conversationID,
      thread_id: threadID,
    }),
  openCodexThread: (threadID: string) =>
    postChecked<{ ok: boolean; thread_id: string; command?: string; opened?: boolean; status_message?: string }>(
      "/api/v1/channel-conversations/open",
      { thread_id: threadID }
    ),
  channelTasks: (channelID = "", conversationID = "") =>
    get<ChannelTask[] | null>(
      `/api/v1/channel-tasks?channel_id=${encodeURIComponent(channelID)}&conversation_id=${encodeURIComponent(conversationID)}`
    ),
  channelInteractions: (channelID = "", conversationID = "") =>
    get<ChannelInteraction[] | null>(
      `/api/v1/channel-interactions?channel_id=${encodeURIComponent(channelID)}&conversation_id=${encodeURIComponent(conversationID)}`
    ),
  respondChannelInteraction: (
    interactionID: string,
    nonce: string,
    decision: string,
    answers: Record<string, string[]>
  ) =>
    postChecked<{ ok: boolean }>("/api/v1/channel-interactions/respond", {
      interaction_id: interactionID,
      nonce,
      decision,
      answers,
    }),
  beginFeishuSetup: () => postChecked<FeishuSetupBeginResponse>("/api/v1/setup/feishu/begin", {}),
  pollFeishuSetup: (deviceCode: string, baseUrl = "") =>
    postChecked<FeishuSetupPollResponse>("/api/v1/setup/feishu/poll", { device_code: deviceCode, base_url: baseUrl }),
  triggers: () => get<Trigger[] | null>("/api/v1/triggers"),
  upsertTrigger: (tr: Partial<Trigger>) => postChecked<Trigger>("/api/v1/triggers", tr),
  deleteTrigger: (id: string) => del<{ ok: boolean }>(`/api/v1/triggers?id=${encodeURIComponent(id)}`),
  runTrigger: (id: string) =>
    postChecked<{ ok: boolean }>(`/api/v1/triggers/run?id=${encodeURIComponent(id)}`, {}),

  // AgentMux Memory
  memory: (scope = "", q = "", limit = 50) =>
    get<MemoryEntry[] | null>(
      `/api/v1/memory?scope=${encodeURIComponent(scope)}&q=${encodeURIComponent(q)}&limit=${limit}`
    ),
  putMemory: (e: Partial<MemoryEntry>) => post<{ id: string }>("/api/v1/memory", e),
  deleteMemory: (id: string) => del<{ ok: boolean }>(`/api/v1/memory?id=${encodeURIComponent(id)}`),

  // AgentMux Skills
  skills: () => get<Skill[] | null>("/api/v1/skills"),
  skillMarketplace: (q = "", source = "", category = "") =>
    get<MarketplaceSkill[] | null>(
      `/api/v1/skills/marketplace?q=${encodeURIComponent(q)}&source=${encodeURIComponent(source)}&category=${encodeURIComponent(category)}`
    ),
  installSkill: (skill: Pick<MarketplaceSkill, "repo" | "path" | "name">) =>
    postChecked<Skill>("/api/v1/skills/install", skill),
  toggleSkill: (name: string, enabled: boolean) =>
    post<{ ok: boolean }>("/api/v1/skills/toggle", { name, enabled }),

  // AgentMux MCP Registry
  mcp: () => get<MCPServer[] | null>("/api/v1/mcp"),
  upsertMCP: (m: MCPServer) => post<MCPServer>("/api/v1/mcp", m),
  deleteMCP: (name: string) => del<{ ok: boolean }>(`/api/v1/mcp?name=${encodeURIComponent(name)}`),

  // AgentMux Guard
  guardPolicies: () => get<GuardPolicy[] | null>("/api/v1/guard/policies"),
  evaluateGuard: (req: { project?: string; tool: string; action?: string }) =>
    post<{ decision: string }>("/api/v1/guard/evaluate", req),

  // Claude Code / Codex Sessions
  sessions: (provider = "", surface = "") =>
    get<AgentSession[] | null>(
      `/api/v1/sessions?provider=${encodeURIComponent(provider)}&surface=${encodeURIComponent(surface)}`
    ),
  sessionMessages: (session: Pick<AgentSession, "provider_id" | "surface" | "session_id" | "source_path" | "project_dir" | "conversation_id">) =>
    get<SessionMessage[] | null>(
      `/api/v1/sessions/messages?provider=${encodeURIComponent(session.provider_id)}&surface=${encodeURIComponent(
        session.surface
      )}&session_id=${encodeURIComponent(session.session_id)}&source_path=${encodeURIComponent(
        session.source_path ?? ""
      )}&project_dir=${encodeURIComponent(session.project_dir ?? "")}&conversation_id=${encodeURIComponent(
        session.conversation_id ?? ""
      )}`
    ),
  sendSessionMessage: (
    session: Pick<AgentSession, "channel_id" | "conversation_id">,
    text: string
  ) =>
    postChecked<{ ok: boolean; answer: string }>("/api/v1/sessions/messages", {
      channel_id: session.channel_id,
      conversation_id: session.conversation_id,
      text,
    }),
  resumeSession: (session: Pick<AgentSession, "provider_id" | "surface" | "session_id" | "source_path" | "project_dir">, openTerminal = false) =>
    post<{ ok: boolean; command?: string; thread_id?: string; opened?: boolean; status_message?: string }>(
      "/api/v1/sessions/resume",
      {
        provider_id: session.provider_id,
        surface: session.surface,
        session_id: session.session_id,
        source_path: session.source_path,
        project_dir: session.project_dir,
        open_terminal: openTerminal,
      }
    ),
  deleteSession: (session: Pick<AgentSession, "provider_id" | "surface" | "session_id" | "source_path">) =>
    del<{ ok: boolean }>(
      `/api/v1/sessions?provider=${encodeURIComponent(session.provider_id)}&surface=${encodeURIComponent(
        session.surface
      )}&session_id=${encodeURIComponent(session.session_id)}&source_path=${encodeURIComponent(session.source_path ?? "")}`
    ),
};
