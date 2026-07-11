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
  default_model?: string;
  default_reasoning_effort?: string;
  default_service_tier?: string;
  memory_scope?: string;
  env?: Record<string, string>;
  channel_bindings?: AgentChannelBinding[];
  schedules?: AgentSchedule[];
  mcp_servers?: string[];
  skills?: string[];
  clis?: string[];
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

export interface CLIManagedTool {
  spec: {
    id: string;
    name: string;
    bin: string;
    package: string;
    registry?: string;
    note?: string;
  };
  installed: boolean;
  path?: string;
  version?: string;
  detail?: string;
}

export interface CLIInstallResult {
  id: string;
  action: "install" | "update";
  ok: boolean;
  command?: string;
  log?: string;
  version?: string;
  error?: string;
}

export interface CLIUpdateCheck {
  id: string;
  installed: boolean;
  current_version?: string;
  latest_version?: string;
  update_available: boolean;
  checked_at?: string;
  error?: string;
}

export interface MarketplaceSkill {
  name: string;
  description: string;
  category?: string;
  source: string;
  repo: string;
  path: string;
  url?: string;
  trusted: boolean;
  installed: boolean;
}

export interface WorkspaceInitResult {
  work_dir: string;
  created?: string[];
  updated?: string[];
  warnings?: string[];
  runtime_id?: string;
  agent_id?: string;
}

export interface ToolsResponse {
  cli: CLIManagedTool[];
  frameworks: Framework[];
  skills: Skill[];
  mcp: MCPServer[];
  marketplace: MarketplaceSkill[];
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

export interface ObservationError {
  code?: string;
  message?: string;
  retryable?: boolean;
}

export interface ObservationModel {
  provider?: string;
  requested?: string;
  resolved?: string;
  protocol?: string;
  request_id?: string;
  attempt?: number;
  reasoning_effort?: string;
  service_tier?: string;
  finish_reason?: string;
  ttft_ms?: number;
  duration_ms?: number;
}

export interface ObservationTool {
  name?: string;
  call_id?: string;
  category?: string;
  duration_ms?: number;
  input_bytes?: number;
  output_bytes?: number;
}

export interface ObservationUsage {
  input_tokens?: number;
  output_tokens?: number;
  cache_read_tokens?: number;
  cache_write_tokens?: number;
  reasoning_tokens?: number;
  tool_tokens?: number;
  total_tokens?: number;
  cost_usd?: number;
  cumulative?: boolean;
}

export interface ObservationPayloadRef {
  id: string;
  content_type?: string;
  key_id?: string;
  original_bytes?: number;
  stored_bytes?: number;
  redacted?: boolean;
  expires_at?: string;
}

export interface ObservationTrace {
  trace_id: string;
  root_span_id?: string;
  name?: string;
  started_at: string;
  ended_at?: string;
  agent_id?: string;
  agent_name?: string;
  runtime_id?: string;
  conversation_id?: string;
  session_id?: string;
  turn_id?: string;
  source?: string;
  provenance?: string[];
  quality?: string;
  status?: string;
  error?: ObservationError;
  model?: ObservationModel;
  usage?: ObservationUsage;
  span_count?: number;
  event_count?: number;
  attributes?: Record<string, unknown>;
  created_at?: string;
  updated_at?: string;
}

export interface ObservationSpan {
  span_id: string;
  trace_id: string;
  parent_span_id?: string;
  kind: string;
  name?: string;
  sequence?: number;
  started_at: string;
  ended_at?: string;
  duration_ms?: number;
  agent_id?: string;
  runtime_id?: string;
  conversation_id?: string;
  session_id?: string;
  turn_id?: string;
  source?: string;
  provenance?: string[];
  quality?: string;
  status?: string;
  error?: ObservationError;
  model?: ObservationModel;
  tool?: ObservationTool;
  payload_id?: string;
  payload_ref?: ObservationPayloadRef;
  usage?: ObservationUsage;
  attributes?: Record<string, unknown>;
  content?: unknown;
  tool_input?: unknown;
  tool_output?: unknown;
}

export interface ObservationEvent {
  version?: string;
  event_id: string;
  sequence?: number;
  time: string;
  trace_id: string;
  span_id: string;
  parent_span_id?: string;
  kind: string;
  name?: string;
  lifecycle?: string;
  source?: string;
  provenance?: string[];
  quality?: string;
  status?: string;
  error?: ObservationError;
  model?: ObservationModel;
  tool?: ObservationTool;
  usage?: ObservationUsage;
  payload_ref?: ObservationPayloadRef;
  attributes?: Record<string, unknown>;
  content?: unknown;
}

export interface ObservationTraceDetail {
  trace: ObservationTrace;
  spans: ObservationSpan[];
  events: ObservationEvent[];
}

export interface ObservationCoverage {
  source: string;
  quality?: string;
  status?: string;
  events?: number;
  traces?: number;
  last_seen_at?: string;
  detail?: string;
}

export interface ObservationOverview {
  traces?: number;
  spans?: number;
  events?: number;
  model_requests?: number;
  tool_calls?: number;
  failed_traces?: number;
  partial_traces?: number;
  active_agents?: number;
  error_rate?: number;
  usage?: ObservationUsage;
  coverage?: ObservationCoverage[];
  recent_traces?: ObservationTrace[];
}

export interface ObservationInsight {
  id: string;
  rule_id: string;
  agent_id?: string;
  trace_id?: string;
  severity?: string;
  status: string;
  title: string;
  summary?: string;
  suggestion?: string;
  sample_size?: number;
  confidence?: number;
  estimated_token_savings?: number;
  estimated_cost_savings_usd?: number;
  related_trace_ids?: string[];
  only_suggestion?: boolean;
  created_at?: string;
  updated_at?: string;
}

export interface ObservationExporterSettings {
  name?: string;
  type?: string;
  endpoint?: string;
  enabled?: boolean;
  include_content?: boolean;
  status?: string;
  pending?: number;
  last_error?: string;
}

export interface ObservationSettings {
  enabled?: boolean;
  capture_content?: string;
  content_retention_days?: number;
  detail_retention_days?: number;
  backfill_days?: number;
  metadata_only?: boolean;
  key_status?: string;
  exporters?: ObservationExporterSettings[];
}

export interface ObservationIntegrationCoverage {
  enabled?: boolean;
  available?: boolean;
  status?: string;
  quality?: string;
  detail?: string;
  last_seen_at?: string;
}

export interface ObservationIntegration {
  host: string;
  name?: string;
  status?: string;
  installed?: boolean;
  version?: string;
  trust?: string;
  pending_trust?: boolean;
  plugin?: ObservationIntegrationCoverage;
  otel?: ObservationIntegrationCoverage;
  transcript?: ObservationIntegrationCoverage;
  proxy?: ObservationIntegrationCoverage;
  owners?: string[];
  owner?: string;
  drift?: boolean;
  conflicts?: string[];
  warnings?: string[];
  target_paths?: string[];
  install_id?: string;
  coverage?: Record<string, string>;
  findings?: ObservationIntegrationFinding[];
  updated_at?: string;
  metadata?: Record<string, unknown>;
}

export interface ObservationIntegrationFinding {
  code?: string;
  severity?: string;
  message: string;
  owner?: string;
  path?: string;
  blocking?: boolean;
}

export interface ObservationIntegrationAction {
  kind?: string;
  target?: string;
  command?: string[];
  reason?: string;
}

export interface ObservationIntegrationActionResult {
  ok?: boolean;
  host?: string;
  action?: string;
  status?: string;
  message?: string;
  pending_trust?: boolean;
  warnings?: string[];
  conflicts?: string[];
  changes?: string[];
  integration?: ObservationIntegration;
  preview?: unknown;
  changed?: boolean;
  blocked?: boolean;
  actions?: ObservationIntegrationAction[];
  findings?: ObservationIntegrationFinding[];
  preserved?: string[];
  record?: Record<string, unknown>;
}

export interface ObservationTraceFilters {
  agentID?: string;
  runtimeID?: string;
  sessionID?: string;
  status?: string;
  source?: string;
  limit?: number;
  offset?: number;
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

let observationSessionPromise: Promise<void> | null = null;
let observationBearer = "";

async function ensureObservationSession(): Promise<void> {
  if (observationSessionPromise) return observationSessionPromise;
  observationSessionPromise = (async () => {
    const nonceResponse = await fetch(apiPath("/api/v1/observability/session/nonce"), {
      credentials: "include",
    });
    if (!nonceResponse.ok) throw new Error(`observability nonce: ${nonceResponse.status}`);
    const noncePayload = (await nonceResponse.json()) as { nonce?: string };
    if (!noncePayload.nonce) throw new Error("observability nonce missing");
    const desktop = apiBase() !== "";
    const sessionResponse = await fetch(apiPath("/api/v1/observability/session"), {
      method: "POST",
      credentials: "include",
      headers: {
        "Content-Type": "application/json",
        ...(desktop ? { "X-AgentNexus-Desktop": "1" } : {}),
      },
      body: JSON.stringify({ nonce: noncePayload.nonce }),
    });
    const sessionPayload = (await sessionResponse.json().catch(() => ({}))) as {
      error?: string;
      session_token?: string;
    };
    if (!sessionResponse.ok) {
      throw new Error(sessionPayload.error || `observability session: ${sessionResponse.status}`);
    }
    observationBearer = sessionPayload.session_token || "";
  })().catch((error) => {
    observationSessionPromise = null;
    throw error;
  });
  return observationSessionPromise;
}

async function observationFetch(path: string, init: RequestInit = {}, retry = true): Promise<Response> {
  await ensureObservationSession();
  const headers = new Headers(init.headers);
  if (observationBearer) headers.set("Authorization", `Bearer ${observationBearer}`);
  const response = await fetch(apiPath(path), { ...init, headers, credentials: "include" });
  if (response.status === 401 && retry) {
    observationBearer = "";
    observationSessionPromise = null;
    return observationFetch(path, init, false);
  }
  return response;
}

async function observationGet<T>(path: string): Promise<T> {
  const response = await observationFetch(path);
  if (!response.ok) throw new Error(`${path}: ${response.status}`);
  return response.json() as Promise<T>;
}

async function observationPost<T>(path: string, body: unknown): Promise<T> {
  const response = await observationFetch(path, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(body),
  });
  const payload = (await response.json().catch(() => ({}))) as Record<string, unknown>;
  if (!response.ok) {
    const message = typeof payload.error === "string" ? payload.error : `${path}: ${response.status}`;
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
  initializeAgentWorkspace: (payload: { id?: string } | Partial<AgentInstance>) =>
    postChecked<WorkspaceInitResult>("/api/v1/agent-instances/initialize", payload),
  deleteAgentInstance: (id: string) =>
    del<{ ok: boolean }>(`/api/v1/agent-instances?id=${encodeURIComponent(id)}`),
  tools: () => get<ToolsResponse>("/api/v1/tools"),
  installCLI: (id: string, action: "install" | "update") =>
    postChecked<CLIInstallResult>("/api/v1/tools/cli/install", { id, action }),
  checkCLIUpdate: (id: string) => post<CLIUpdateCheck>("/api/v1/tools/cli/check", { id }),
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

  // AgentNexus Observability
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
  skillMarketplace: (q = "", source = "", category = "") =>
    get<MarketplaceSkill[] | null>(
      `/api/v1/skills/marketplace?q=${encodeURIComponent(q)}&source=${encodeURIComponent(source)}&category=${encodeURIComponent(category)}`
    ),
  installSkill: (skill: Pick<MarketplaceSkill, "repo" | "path" | "name">) =>
    postChecked<Skill>("/api/v1/skills/install", skill),
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
