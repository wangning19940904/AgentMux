// REST data layer. cc-switch used Tauri IPC (invoke); here we talk to the Go
// daemon's HTTP API so the same React UI serves both the WebUI and the Wails
// desktop shell.

const DESKTOP_API_BASE = "http://127.0.0.1:8765";

declare global {
  interface Window {
    go?: {
      main?: {
        App?: {
          SelectDirectory?: (defaultDirectory?: string) => Promise<string>;
        };
      };
    };
  }
}

function apiBase() {
  const host = window.location.hostname;
  const protocol = window.location.protocol;
  const isBrowserDev = (host === "127.0.0.1" || host === "localhost") && window.location.port === "5173";
  const isServerWeb = (host === "127.0.0.1" || host === "localhost") && window.location.port === "8765";
  const isDesktop =
    protocol === "wails:" ||
    host === "wails.localhost" ||
    (!isBrowserDev && !isServerWeb && typeof window.go !== "undefined");
  return isDesktop ? DESKTOP_API_BASE : "";
}

function apiPath(path: string) {
  return `${apiBase()}${path}`;
}

export interface Provider {
  id: string;
  name: string;
  preset?: string;
  category?: string;
  base_url: string;
  api_key_env?: string;
  api_key?: string;
  api_key_available?: boolean;
  api_key_issue?: string;
  model?: string;
  extra?: Record<string, string>;
  settings_config?: Record<string, unknown>;
  meta?: Record<string, unknown>;
  enabled: boolean;
  in_failover_queue?: boolean;
  sort_index?: number;
}

export interface ProxyToolConfig {
  tool: string;
  enabled: boolean;
  auto_failover: boolean;
  max_retries: number;
  failure_threshold: number;
  cooldown_seconds: number;
}

export interface ProxyStatus {
  running: boolean;
  base_url: string;
  tools: ProxyToolConfig[];
}

export interface ProxyTrace {
  id: string;
  timestamp: string;
  tool: string;
  provider_id: string;
  provider_name?: string;
  client_protocol: string;
  upstream_protocol: string;
  client_model?: string;
  upstream_model?: string;
  status_code?: number;
  success: boolean;
  error?: string;
  session_id?: string;
  project_dir?: string;
}

export interface ProviderRoute {
  tool: string;
  provider_id: string;
  provider_name?: string;
  base_url?: string;
  api_key_env?: string;
  api_key_available?: boolean;
  api_key_issue?: string;
  model?: string;
  api_format?: string;
  meta?: Record<string, unknown>;
  configured: boolean;
}

export interface ProviderProbeResult {
  ok: boolean;
  url?: string;
  models: string[];
  count: number;
  message: string;
  api_format?: string;
  codex_wire_api?: string;
  formats?: ProviderProbeCheck[];
  protocols?: ProviderProbeCheck[];
}

export interface ProviderProbeCheck {
  kind: string;
  name: string;
  ok: boolean;
  url?: string;
  status?: number;
  models?: string[];
  message?: string;
}

export interface Claude3PStatus {
  enabled: boolean;
  configured: boolean;
  config_dir: string;
  profile_path?: string;
  active_profile_id?: string;
  active_profile_name?: string;
  base_url?: string;
  auth_scheme?: string;
  model_count: number;
  provider_id?: string;
  provider_name?: string;
  backup_path?: string;
  message?: string;
}

export interface UsageTotals {
  input_tokens: number;
  output_tokens: number;
  cache_read_tokens: number;
  cache_write_tokens: number;
  cost_usd: number;
  records: number;
}

export interface UsageBucket {
  key: string;
  totals: UsageTotals;
}

export interface ModelStat {
  model: string;
  tokens: number;
  cost_usd: number;
}

export interface SourceStat {
  source: string;
  tokens: number;
  cost_usd: number;
}

export interface UsageReport {
  period: string;
  totals: UsageTotals;
  buckets: UsageBucket[];
  by_model: ModelStat[];
  by_source: SourceStat[];
}

export interface Status {
  ok: boolean;
  projects: number;
  version: string;
}

export interface AgentChannelBinding {
  id: string;
  type: string;
  name?: string;
  chat_id?: string;
  status?: string;
  config?: Record<string, string>;
}

export interface AgentSchedule {
  id: string;
  name: string;
  cron: string;
  prompt: string;
  enabled: boolean;
}

export interface AgentInstance {
  id: string;
  name: string;
  runtime_id: string;
  work_dir?: string;
  system_prompt?: string;
  provider_tool?: string;
  provider_id?: string;
  provider_name?: string;
  memory_scope?: string;
  env?: Record<string, string>;
  channel_bindings?: AgentChannelBinding[];
  schedules?: AgentSchedule[];
  mcp_servers?: string[];
  skills?: string[];
  enabled: boolean;
  source?: string;
  created_at?: string;
  updated_at?: string;
}

export interface Channel {
  id: string;
  name: string;
  type: string;
  agent_id?: string;
  agent_name?: string;
  bot_name?: string;
  bot_avatar_url?: string;
  bot_avatar_proxy_url?: string;
  bot_open_id?: string;
  config?: Record<string, string>;
  enabled: boolean;
  state?: string;
  error?: string;
  created_at?: string;
  updated_at?: string;
}

export interface FeishuSetupBeginResponse {
  device_code: string;
  qr_url: string;
  interval: number;
  expires_in: number;
}

export interface FeishuSetupPollResponse {
  status: "pending" | "completed" | "denied" | "expired" | "error";
  base_url?: string;
  app_id?: string;
  app_secret?: string;
  platform?: "feishu" | "lark";
  owner_open_id?: string;
  slow_down?: boolean;
  error?: string;
}

export interface Trigger {
  id: string;
  name: string;
  kind: string;
  agent_id?: string;
  agent_name?: string;
  channel_id?: string;
  channel_name?: string;
  chat_id?: string;
  cron_expr?: string;
  prompt?: string;
  event?: string;
  action_type?: string;
  action_target?: string;
  token?: string;
  session_mode?: string;
  enabled: boolean;
  hook_path?: string;
  last_run?: string;
  last_status?: string;
  last_error?: string;
  created_at?: string;
  updated_at?: string;
}

export interface MemoryEntry {
  id: string;
  scope: string;
  content: string;
  tags?: string[];
  meta?: Record<string, string>;
  created_at: string;
  updated_at: string;
}

export interface Skill {
  name: string;
  path: string;
  description: string;
  tags?: string[];
  enabled: boolean;
  source?: string;
}

export interface MCPServer {
  name: string;
  transport: string;
  command?: string;
  args?: string[];
  url?: string;
  env?: Record<string, string>;
  enabled: boolean;
}

export interface GuardPolicy {
  id: string;
  tool: string;
  action?: string;
  decision: string;
  priority: number;
}

export interface FrameworkSpec {
  kind: string;
  display: string;
  kind_type: "cli" | "sdk";
  language?: string;
  packages?: string[];
  bin?: string;
  env_required?: string[];
  supported: boolean;
  note?: string;
}

export interface Framework {
  spec: FrameworkSpec;
  installed: boolean;
  version?: string;
  detail?: string;
  registered: boolean;
}

export interface FrameworkPrereqs {
  node: boolean;
  node_path?: string;
  npm: boolean;
  npm_path?: string;
}

export interface FrameworksResponse {
  prereqs: FrameworkPrereqs;
  frameworks: Framework[];
}

export interface FrameworkInstallResult {
  kind: string;
  ok: boolean;
  command?: string;
  log?: string;
  version?: string;
  error?: string;
}

export interface AgentSession {
  provider_id: string;
  surface: string;
  session_id: string;
  title?: string;
  summary?: string;
  project_dir?: string;
  created_at?: string;
  last_active_at?: string;
  source_path?: string;
  resume_command?: string;
  file_backed: boolean;
  message_count: number;
  messages_partial?: boolean;
  available: boolean;
  status_message?: string;
}

export interface SessionMessage {
  role: string;
  kind?: string;
  content: string;
  timestamp?: string;
}

async function get<T>(path: string): Promise<T> {
  const res = await fetch(apiPath(path));
  if (!res.ok) throw new Error(`${path}: ${res.status}`);
  return res.json() as Promise<T>;
}

async function post<T>(path: string, body: unknown): Promise<T> {
  const res = await fetch(apiPath(path), {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(body),
  });
  if (!res.ok) throw new Error(`${path}: ${res.status}`);
  return res.json() as Promise<T>;
}

async function postChecked<T>(path: string, body: unknown): Promise<T> {
  const res = await fetch(apiPath(path), {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(body),
  });
  const payload = (await res.json().catch(() => ({}))) as Record<string, unknown>;
  if (!res.ok) {
    const message = typeof payload.error === "string" ? payload.error : `${path}: ${res.status}`;
    throw new Error(message);
  }
  return payload as T;
}

async function del<T>(path: string): Promise<T> {
  const res = await fetch(apiPath(path), { method: "DELETE" });
  if (!res.ok) throw new Error(`${path}: ${res.status}`);
  return res.json() as Promise<T>;
}

async function selectSystemDirectory(defaultDirectory = ""): Promise<{ path: string }> {
  const picker = window.go?.main?.App?.SelectDirectory;
  if (!picker) {
    throw new Error("desktop directory picker unavailable");
  }
  const path = await picker(defaultDirectory);
  return { path };
}

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
  status: () => get<Status>("/api/v1/status"),
  platforms: () => get<string[]>("/api/v1/platforms"),
  agents: () => get<string[]>("/api/v1/agents"),
  agentInstances: () => get<AgentInstance[] | null>("/api/v1/agent-instances"),
  upsertAgentInstance: (agent: Partial<AgentInstance>) =>
    postChecked<AgentInstance>("/api/v1/agent-instances", agent),
  deleteAgentInstance: (id: string) =>
    del<{ ok: boolean }>(`/api/v1/agent-instances?id=${encodeURIComponent(id)}`),
  providers: () => getProviders("/api/v1/providers"),
  activeRoutes: () => get<ProviderRoute[] | null>("/api/v1/providers/active"),
  presets: async () => (await getProviders("/api/v1/providers/presets")) ?? [],
  upsertProvider: (p: Provider) => post<Provider>("/api/v1/providers", p),
  probeProvider: (p: Provider) => postChecked<ProviderProbeResult>("/api/v1/providers/probe", p),
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
  ensureDirectory: (path: string) =>
    postChecked<{ path: string }>("/api/v1/system/directories", { path }),

  // AgentNexus Frameworks: detect & install agent frameworks
  frameworks: () => get<FrameworksResponse>("/api/v1/frameworks"),
  installFramework: (kind: string) =>
    postChecked<FrameworkInstallResult>("/api/v1/frameworks/install", { kind }),
  usage: (period: string) =>
    get<UsageReport>(`/api/v1/usage?period=${encodeURIComponent(period)}`),

  // AgentNexus Connect: channels & triggers
  channels: () => get<Channel[] | null>("/api/v1/channels"),
  upsertChannel: (ch: Partial<Channel>) => postChecked<Channel>("/api/v1/channels", ch),
  deleteChannel: (id: string) => del<{ ok: boolean }>(`/api/v1/channels?id=${encodeURIComponent(id)}`),
  restartChannel: (id: string) =>
    postChecked<{ ok: boolean }>(`/api/v1/channels/restart?id=${encodeURIComponent(id)}`, {}),
  beginFeishuSetup: () => postChecked<FeishuSetupBeginResponse>("/api/v1/setup/feishu/begin", {}),
  pollFeishuSetup: (deviceCode: string, baseUrl = "") =>
    postChecked<FeishuSetupPollResponse>("/api/v1/setup/feishu/poll", { device_code: deviceCode, base_url: baseUrl }),
  triggers: () => get<Trigger[] | null>("/api/v1/triggers"),
  upsertTrigger: (tr: Partial<Trigger>) => postChecked<Trigger>("/api/v1/triggers", tr),
  deleteTrigger: (id: string) => del<{ ok: boolean }>(`/api/v1/triggers?id=${encodeURIComponent(id)}`),
  runTrigger: (id: string) =>
    postChecked<{ ok: boolean }>(`/api/v1/triggers/run?id=${encodeURIComponent(id)}`, {}),

  // AgentNexus Memory
  memory: (scope = "", q = "", limit = 50) =>
    get<MemoryEntry[] | null>(
      `/api/v1/memory?scope=${encodeURIComponent(scope)}&q=${encodeURIComponent(q)}&limit=${limit}`
    ),
  putMemory: (e: Partial<MemoryEntry>) => post<{ id: string }>("/api/v1/memory", e),
  deleteMemory: (id: string) => del<{ ok: boolean }>(`/api/v1/memory?id=${encodeURIComponent(id)}`),

  // AgentNexus Skills
  skills: () => get<Skill[] | null>("/api/v1/skills"),
  toggleSkill: (name: string, enabled: boolean) =>
    post<{ ok: boolean }>("/api/v1/skills/toggle", { name, enabled }),

  // AgentNexus MCP Registry
  mcp: () => get<MCPServer[] | null>("/api/v1/mcp"),
  upsertMCP: (m: MCPServer) => post<MCPServer>("/api/v1/mcp", m),
  deleteMCP: (name: string) => del<{ ok: boolean }>(`/api/v1/mcp?name=${encodeURIComponent(name)}`),

  // AgentNexus Guard
  guardPolicies: () => get<GuardPolicy[] | null>("/api/v1/guard/policies"),
  evaluateGuard: (req: { project?: string; tool: string; action?: string }) =>
    post<{ decision: string }>("/api/v1/guard/evaluate", req),

  // Claude Code / Codex Sessions
  sessions: (provider = "", surface = "") =>
    get<AgentSession[] | null>(
      `/api/v1/sessions?provider=${encodeURIComponent(provider)}&surface=${encodeURIComponent(surface)}`
    ),
  sessionMessages: (session: Pick<AgentSession, "provider_id" | "surface" | "session_id" | "source_path" | "project_dir">) =>
    get<SessionMessage[] | null>(
      `/api/v1/sessions/messages?provider=${encodeURIComponent(session.provider_id)}&surface=${encodeURIComponent(
        session.surface
      )}&session_id=${encodeURIComponent(session.session_id)}&source_path=${encodeURIComponent(
        session.source_path ?? ""
      )}&project_dir=${encodeURIComponent(session.project_dir ?? "")}`
    ),
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
