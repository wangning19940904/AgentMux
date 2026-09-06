// REST data layer. cc-switch used Tauri IPC (invoke); here we talk to the Go
// daemon's HTTP API so the same React UI serves both the WebUI and the Wails
// desktop shell.

export * from "./types";
export * from "./client";
export * from "./desktop";
export * from "./fleet";
import { activeMachineScope, activeRemoteID, fleetAdminQuery, fleetQuery, get, getChecked, getLocal, post, put, putLocal, postChecked, postProgress, del } from "./client";
import { fleetAdminReadArray, fleetCall, fleetGet, fleetMode, fleetReadArray, fleetReadValues, operationFor, singleTargetID, writeTargetIDs } from "./fleet";
import { observationGet, observationPost } from "./observability";
import { testRemoteHost, updateRemoteHost, statusRemoteHost, importRemoteHost } from "./remote";
import { selectSystemDirectory } from "./desktop";
import type {
  AgentInstance,
  AgentSession,
  BundleInstallResult,
  CLIAuthSession,
  CLIAuthStatus,
  CLIInstallResult,
  CLIUpdateCheck,
  Channel,
  ChannelConversation,
  ChannelFeedback,
  ChannelInteraction,
  ChannelTask,
  Claude3PStatus,
  DiscoveredRemoteHost,
  FeishuSetupBeginResponse,
  FeishuSetupPollResponse,
  FeishuAutomationBeginResponse,
  FeishuAutomationPollResponse,
  FeishuAutomationResult,
  FeedbackReport,
  FleetBatchResult,
  FleetSyncApplyResult,
  FleetSyncPathMapping,
  FleetSyncPreview,
  FrameworkAuthStatus,
  FrameworkRuntimeSettings,
  FrameworkInstallResult,
  FrameworkLoginResult,
  FrameworkLoginSessionStatus,
  FrameworkUpdateCheck,
  FrameworksResponse,
  GuardPolicy,
  KeepAwakeStatus,
  MCPServer,
  MarketplaceSkill,
  MachineTarget,
  MemoryEntry,
  MenubarSettings,
  MeetingInvitation,
	MeetingDetail,
  MeetingJoinResult,
  MeetingOverview,
	MeetingResponseMode,
	MeetingTurn,
  ObservationInsight,
  ObservationIntegration,
  ObservationIntegrationActionResult,
  ObservationOverview,
  ObservationSettings,
  ObservationTrace,
  ObservationTraceDetail,
  ObservationTraceFilters,
  Orchestration,
  OrchestrationTask,
  OperationProgress,
  Provider,
  ProviderMonitorConfig,
  ProviderMonitorSnapshot,
  ProviderProbeResult,
  ProviderRoute,
  ProxyStatus,
  ProxyToolConfig,
  ProxyTrace,
  RemoteHost,
  RemoteSSHConfigSyncResult,
  SessionMessage,
  Skill,
  GrantableResourceType,
  ResourceGrant,
  Status,
  SystemDirectoryListing,
  TenancySelf,
  Tenant,
  TenantKind,
  TenantToken,
  TerminalSessionView,
  ToolsResponse,
	TTSCatalogStatus,
	TTSModel,
  Trigger,
  UsageReport,
  UsageTotals,
  CursorUsageActionResult,
  CursorUsageSourceStatus,
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
    target_id: typeof provider.target_id === "string" ? provider.target_id : undefined,
    target_name: typeof provider.target_name === "string" ? provider.target_name : undefined,
  };
}

async function getProviders(path: string): Promise<Provider[] | null> {
  const providers = fleetMode()
    ? await fleetReadArray<Provider>(path)
    : await get<Provider[] | null>(path);
  if (!Array.isArray(providers)) return providers;
  return providers.map((provider) => normalizeProvider(provider as Partial<Provider> & Record<string, unknown>));
}

export function mergeFleetUsage(batch: FleetBatchResult<UsageReport>): UsageReport {
  const reports: Array<{ targetID: string; targetName: string; report: UsageReport }> = [];
  const warnings: string[] = [];
  for (const target of batch.targets) {
    const response = operationFor<UsageReport>(target.responses, "usage");
    if (response?.ok && response.data?.totals) {
      reports.push({ targetID: target.target.id, targetName: target.target.name, report: response.data });
    } else {
      warnings.push(`${target.target.name}: ${response?.error || "unavailable"}`);
    }
  }
  if (reports.length === 0) throw new Error(warnings.join("; ") || "No machine returned usage data.");

  const totals = emptyUsageTotals();
  const buckets = new Map<string, UsageTotals>();
  const bucketRuntimes = new Map<string, Map<string, { tokens: number; cost_usd: number; estimated_tokens: number }>>();
  const models = new Map<string, { tokens: number; cost_usd: number; estimated_tokens: number }>();
  const sources = new Map<string, { tokens: number; cost_usd: number; estimated_tokens: number }>();
  const agents = new Map<string, { tokens: number; cost_usd: number; estimated_tokens: number }>();
  const runtimes = new Map<string, { tokens: number; cost_usd: number; estimated_tokens: number }>();
  for (const item of reports) {
    addUsageTotals(totals, item.report.totals);
    for (const bucket of item.report.buckets ?? []) {
      addUsageTotalsMap(buckets, bucket.key, bucket.totals);
      const runtimesForBucket = bucketRuntimes.get(bucket.key) ?? new Map<string, { tokens: number; cost_usd: number; estimated_tokens: number }>();
      for (const runtime of bucket.by_runtime ?? []) {
        addUsageStat(runtimesForBucket, runtime.runtime, runtime.tokens, runtime.cost_usd, runtime.estimated_tokens);
      }
      bucketRuntimes.set(bucket.key, runtimesForBucket);
    }
    for (const stat of item.report.by_model ?? []) addUsageStat(models, stat.model, stat.tokens, stat.cost_usd, stat.estimated_tokens);
    for (const stat of item.report.by_source ?? []) addUsageStat(sources, stat.source, stat.tokens, stat.cost_usd, stat.estimated_tokens);
    for (const stat of item.report.by_agent ?? []) addUsageStat(agents, `${item.targetName} · ${stat.agent}`, stat.tokens, stat.cost_usd);
    for (const stat of item.report.by_runtime ?? []) addUsageStat(runtimes, stat.runtime, stat.tokens, stat.cost_usd, stat.estimated_tokens);
  }
  const first = reports[0].report;
  const requestedTimezone = Intl.DateTimeFormat().resolvedOptions().timeZone;
  for (const item of reports) {
    if (requestedTimezone && item.report.timezone && item.report.timezone !== requestedTimezone) {
      warnings.push(`${item.targetName}: usage buckets use ${item.report.timezone} instead of ${requestedTimezone}`);
    }
  }
  return {
    period: first.period,
    from: first.from,
    to: first.to,
    timezone: requestedTimezone || first.timezone,
    totals,
    buckets: [...buckets].sort(([left], [right]) => left.localeCompare(right)).map(([key, value]) => ({
      key,
      totals: value,
      by_runtime: usageStats(bucketRuntimes.get(key) ?? new Map(), "runtime"),
    })),
    by_model: usageStats(models, "model"),
    by_source: usageStats(sources, "source"),
    by_agent: usageStats(agents, "agent"),
    by_runtime: usageStats(runtimes, "runtime"),
    by_machine: reports.map((item) => ({
      target_id: item.targetID,
      target_name: item.targetName,
      totals: item.report.totals,
      buckets: item.report.buckets ?? [],
    })),
    warnings,
  };
}

function emptyUsageTotals(): UsageTotals {
  return { input_tokens: 0, output_tokens: 0, cache_read_tokens: 0, cache_write_tokens: 0, cost_usd: 0, records: 0, sessions: 0, estimated_tokens: 0, estimated_records: 0 };
}

function addUsageTotals(target: UsageTotals, value: UsageTotals) {
  target.input_tokens += value.input_tokens || 0;
  target.output_tokens += value.output_tokens || 0;
  target.cache_read_tokens += value.cache_read_tokens || 0;
  target.cache_write_tokens += value.cache_write_tokens || 0;
  target.cost_usd += value.cost_usd || 0;
  target.records += value.records || 0;
  target.sessions += value.sessions || 0;
  target.estimated_tokens += value.estimated_tokens || 0;
  target.estimated_records += value.estimated_records || 0;
}

function addUsageTotalsMap(target: Map<string, UsageTotals>, key: string, value: UsageTotals) {
  const current = target.get(key) ?? emptyUsageTotals();
  addUsageTotals(current, value);
  target.set(key, current);
}

function addUsageStat(target: Map<string, { tokens: number; cost_usd: number; estimated_tokens: number }>, key: string, tokens: number, cost: number, estimated = 0) {
  const current = target.get(key) ?? { tokens: 0, cost_usd: 0, estimated_tokens: 0 };
  current.tokens += tokens || 0;
  current.cost_usd += cost || 0;
  current.estimated_tokens += estimated || 0;
  target.set(key, current);
}

function usageStats(target: Map<string, { tokens: number; cost_usd: number; estimated_tokens: number }>, key: "model" | "source" | "agent" | "runtime") {
  return [...target].map(([name, value]) => ({ [key]: name, ...value })).sort((left, right) => right.cost_usd - left.cost_usd) as never;
}

export function mergeFleetObservationOverview(batch: FleetBatchResult<ObservationOverview>): ObservationOverview {
  const merged: ObservationOverview = {
    traces: 0, spans: 0, events: 0, model_requests: 0, tool_calls: 0,
    failed_traces: 0, partial_traces: 0, active_agents: 0, usage: {}, coverage: [], recent_traces: [],
  };
  let success = 0;
  const warnings: string[] = [];
  for (const target of batch.targets) {
    const response = operationFor<ObservationOverview>(target.responses, "overview");
    if (!response?.ok || !response.data) {
      warnings.push(`${target.target.name}: ${response?.error || "unavailable"}`);
      continue;
    }
    success += 1;
    const value = response.data;
    for (const key of ["traces", "spans", "events", "model_requests", "tool_calls", "failed_traces", "partial_traces", "active_agents"] as const) {
      merged[key] = (merged[key] ?? 0) + (value[key] ?? 0);
    }
    for (const key of ["input_tokens", "output_tokens", "cache_read_tokens", "cache_write_tokens", "reasoning_tokens", "tool_tokens", "total_tokens", "cost_usd"] as const) {
      merged.usage![key] = (merged.usage?.[key] ?? 0) + (value.usage?.[key] ?? 0);
    }
    merged.coverage!.push(...(value.coverage ?? []).map((coverage) => ({
      ...coverage,
      source: `${target.target.name} · ${coverage.source}`,
    })));
    merged.recent_traces!.push(...(value.recent_traces ?? []).map((trace) => ({
      ...trace, target_id: target.target.id, target_name: target.target.name,
    })));
  }
  if (!success) throw new Error(warnings.join("; ") || "No machine returned observability data.");
  merged.error_rate = (merged.traces ?? 0) > 0 ? (merged.failed_traces ?? 0) / (merged.traces ?? 1) : 0;
  merged.recent_traces = (merged.recent_traces ?? [])
    .sort((left, right) => Date.parse(right.started_at) - Date.parse(left.started_at))
    .slice(0, 20);
  (merged as ObservationOverview & { warnings?: string[] }).warnings = warnings;
  return merged;
}

function mergeFleetFrameworks(batch: FleetBatchResult<FrameworksResponse>): FrameworksResponse {
  const result: FrameworksResponse = { prereqs: { node: true, npm: true }, frameworks: [], warnings: [] };
  let success = 0;
  for (const target of batch.targets) {
    const response = operationFor<FrameworksResponse>(target.responses, "frameworks");
    if (!response?.ok || !response.data) {
      result.warnings!.push(`${target.target.name}: ${response?.error || "unavailable"}`);
      continue;
    }
    success += 1;
    result.prereqs.node = result.prereqs.node && response.data.prereqs.node;
    result.prereqs.npm = result.prereqs.npm && response.data.prereqs.npm;
    result.frameworks.push(...(response.data.frameworks ?? []).map((item) => ({ ...item, target_id: target.target.id, target_name: target.target.name })));
  }
  if (!success) throw new Error(result.warnings?.join("; ") || "No machine returned framework data.");
  return result;
}

function mergeFleetTools(batch: FleetBatchResult<ToolsResponse>): ToolsResponse {
  const result: ToolsResponse = { cli: [], bundles: [], frameworks: [], skills: [], mcp: [], marketplace: [], warnings: [] };
  const marketplace = new Map<string, MarketplaceSkill>();
  let success = 0;
  for (const target of batch.targets) {
    const response = operationFor<ToolsResponse>(target.responses, "tools");
    if (!response?.ok || !response.data) {
      result.warnings!.push(`${target.target.name}: ${response?.error || "unavailable"}`);
      continue;
    }
    success += 1;
    result.cli.push(...(response.data.cli ?? []).map((item) => ({ ...item, target_id: target.target.id, target_name: target.target.name })));
    result.bundles.push(...(response.data.bundles ?? []).map((item) => ({ ...item, target_id: target.target.id, target_name: target.target.name })));
    result.frameworks.push(...(response.data.frameworks ?? []).map((item) => ({ ...item, target_id: target.target.id, target_name: target.target.name })));
    result.skills.push(...(response.data.skills ?? []).map((item) => ({ ...item, target_id: target.target.id, target_name: target.target.name })));
    result.mcp.push(...(response.data.mcp ?? []).map((item) => ({ ...item, target_id: target.target.id, target_name: target.target.name })));
    for (const item of response.data.marketplace ?? []) marketplace.set(`${item.repo}:${item.path}`, item);
  }
  result.marketplace = [...marketplace.values()];
  if (!success) throw new Error(result.warnings?.join("; ") || "No machine returned tool data.");
  return result;
}

function mergeFleetProviderMonitor(batch: FleetBatchResult<ProviderMonitorSnapshot>): ProviderMonitorSnapshot {
  let config: ProviderMonitorConfig | undefined;
  const result: ProviderMonitorSnapshot = {
    config: { enabled: false, interval_minutes: 60, probe_models: false, max_models_per_provider: 10 },
    running: false, providers: [], alerts: [], warnings: [],
  };
  let success = 0;
  for (const target of batch.targets) {
    const response = operationFor<ProviderMonitorSnapshot>(target.responses, "monitor");
    if (!response?.ok || !response.data) {
      result.warnings!.push(`${target.target.name}: ${response?.error || "unavailable"}`);
      continue;
    }
    success += 1;
    config ??= response.data.config;
    result.running = result.running || response.data.running;
    if (!result.last_run_at || (response.data.last_run_at && response.data.last_run_at > result.last_run_at)) result.last_run_at = response.data.last_run_at;
    result.providers.push(...(response.data.providers ?? []).map((item) => ({ ...item, target_id: target.target.id, target_name: target.target.name })));
    result.alerts.push(...(response.data.alerts ?? []).map((item) => ({ ...item, target_id: target.target.id, target_name: target.target.name })));
  }
  if (!success) throw new Error(result.warnings?.join("; ") || "No machine returned provider monitor data.");
  if (config) result.config = config;
  return result;
}

function mergeFleetProxyStatus(batch: FleetBatchResult<ProxyStatus>): ProxyStatus {
  const result: ProxyStatus = { running: false, base_url: "", tools: [], warnings: [] };
  let success = 0;
  for (const target of batch.targets) {
    const response = operationFor<ProxyStatus>(target.responses, "proxy");
    if (!response?.ok || !response.data) {
      result.warnings!.push(`${target.target.name}: ${response?.error || "unavailable"}`);
      continue;
    }
    success += 1;
    result.running = result.running || response.data.running;
    if (!result.base_url) result.base_url = response.data.base_url;
    result.tools.push(...(response.data.tools ?? []).map((item) => ({ ...item, target_id: target.target.id, target_name: target.target.name })));
  }
  if (!success) throw new Error(result.warnings?.join("; ") || "No machine returned proxy status.");
  return result;
}

export const api = {
  // SSH remote control. These paths intentionally bypass the selected target.
  remoteHosts: () => get<RemoteHost[]>("/api/v1/remote/hosts"),
  fleetTargets: () => getLocal<MachineTarget[]>("/api/v1/remote/fleet/targets"),
  discoveredRemoteHosts: () =>
    get<DiscoveredRemoteHost[]>("/api/v1/remote/discovered-hosts"),
  upsertRemoteHost: (host: Partial<RemoteHost>) =>
    postChecked<RemoteHost>("/api/v1/remote/hosts", host),
  syncRemoteHostsFromSSHConfig: () =>
    postChecked<RemoteSSHConfigSyncResult>("/api/v1/remote/hosts/sync-ssh-config", {}),
  importRemoteHost,
  deleteRemoteHost: (id: string) =>
    del<{ ok: boolean }>(`/api/v1/remote/hosts?id=${encodeURIComponent(id)}`),
  testRemoteHost,
  statusRemoteHost,
  updateRemoteHost,
  previewFleetSync: (payload: {
    source_target_id: string;
    destination_target_ids: string[];
    categories?: string[];
    provider_ids?: string[];
    include_credentials?: boolean;
    preserve_activation?: boolean;
    path_mappings?: Record<string, FleetSyncPathMapping[]>;
  }) => postChecked<FleetSyncPreview>("/api/v1/remote/sync/preview", payload),
  applyFleetSync: (planID: string) =>
    postChecked<FleetSyncApplyResult>("/api/v1/remote/sync/apply", { plan_id: planID }),

  status: () => get<Status>("/api/v1/status"),
  localStatus: () => getLocal<Status>("/api/v1/status"),
  keepAwakeStatus: () => get<KeepAwakeStatus>("/api/v1/system/keep-awake"),
  setKeepAwake: (durationMinutes: number) =>
    put<KeepAwakeStatus>("/api/v1/system/keep-awake", { duration_minutes: durationMinutes }),
  platforms: () => fleetMode() ? fleetReadValues<string>("/api/v1/platforms") : get<string[]>("/api/v1/platforms"),
  agents: () => fleetMode() ? fleetReadValues<string>("/api/v1/agents") : get<string[]>("/api/v1/agents"),
  agentInstances: () => fleetMode() ? fleetReadArray<AgentInstance>("/api/v1/agent-instances") : get<AgentInstance[] | null>("/api/v1/agent-instances"),
  upsertAgentInstance: async (agent: Partial<AgentInstance>, targetIDs?: string[]) => {
    const targets = targetIDs ?? writeTargetIDs(agent.target_id);
    if (targets.length === 1 && targets[0] === activeMachineScope() && activeMachineScope() !== "all") {
      return postChecked<AgentInstance>("/api/v1/agent-instances", agent);
    }
    const payload = { ...agent };
    delete payload.target_id;
    delete payload.target_name;
    if (!payload.id && targets[0] === "all") payload.id = `agent-${crypto.randomUUID().replace(/-/g, "").slice(0, 12)}`;
    return (await fleetCall<AgentInstance>({ key: "agent", method: "POST", path: "/api/v1/agent-instances", body: payload }, targets)).first;
  },
  initializeAgentWorkspace: (payload: { id?: string } | Partial<AgentInstance>) =>
    postChecked<WorkspaceInitResult>("/api/v1/agent-instances/initialize", payload),
  deleteAgentInstance: async (id: string, targetID?: string) => {
    const targets = writeTargetIDs(targetID);
    const path = `/api/v1/agent-instances?id=${encodeURIComponent(id)}`;
    if (targets.length === 1 && targets[0] === activeMachineScope() && activeMachineScope() !== "all") return del<{ ok: boolean }>(path);
    return (await fleetCall<{ ok: boolean }>({ key: "agent", method: "DELETE", path }, targets)).first;
  },

  // Tenancy. Everything except tenancySelf is administrator-only and returns
  // 403 when the Console runs inside a tenant-scoped session.
  tenancySelf: () => get<TenancySelf>("/api/v1/tenancy/self"),
  registerTenant: (payload: { name: string; kind?: TenantKind; target_id?: string }) => {
    const body = { ...payload }; delete body.target_id;
    return fleetCall<{ tenant: Tenant; token: string; prefix: string }>({ key: "tenant", method: "POST", path: "/api/v1/tenancy/register", body }, writeTargetIDs(payload.target_id)).then((result) => result.first);
  },
  tenants: () => fleetMode() ? fleetReadArray<Tenant>("/api/v1/tenancy/tenants") : get<Tenant[] | null>("/api/v1/tenancy/tenants"),
	allTenants: () => fleetAdminReadArray<Tenant>("/api/v1/tenancy/tenants"),
  upsertTenant: async (tenant: Partial<Tenant>) => {
    const body = { ...tenant }; delete body.target_id; delete body.target_name;
    return (await fleetCall<Tenant>({ key: "tenant", method: "POST", path: "/api/v1/tenancy/tenants", body }, writeTargetIDs(tenant.target_id))).first;
  },
  deleteTenant: async (id: string, targetID?: string) =>
    (await fleetCall<{ ok: boolean }>({ key: "tenant", method: "DELETE", path: `/api/v1/tenancy/tenants?id=${encodeURIComponent(id)}` }, writeTargetIDs(targetID))).first,
  tenantTokens: (tenantID: string, targetID?: string) => targetID
    ? fleetGet<TenantToken[] | null>(`/api/v1/tenancy/tokens?tenant_id=${encodeURIComponent(tenantID)}`, targetID)
    : get<TenantToken[] | null>(`/api/v1/tenancy/tokens?tenant_id=${encodeURIComponent(tenantID)}`),
  createTenantToken: (payload: { tenant_id: string; name?: string; expires_in_hours?: number; target_id?: string }) => {
    const body = { ...payload }; delete body.target_id;
    return fleetCall<TenantToken>({ key: "token", method: "POST", path: "/api/v1/tenancy/tokens", body }, writeTargetIDs(payload.target_id)).then((result) => result.first);
  },
  revokeTenantToken: (id: string) =>
    del<{ ok: boolean }>(`/api/v1/tenancy/tokens?id=${encodeURIComponent(id)}`),
  resourceGrants: (tenantID?: string) => fleetMode()
    ? fleetReadArray<ResourceGrant>(tenantID
        ? `/api/v1/tenancy/grants?tenant_id=${encodeURIComponent(tenantID)}`
        : "/api/v1/tenancy/grants")
    : get<ResourceGrant[] | null>(
      tenantID
        ? `/api/v1/tenancy/grants?tenant_id=${encodeURIComponent(tenantID)}`
        : "/api/v1/tenancy/grants",
    ),
  tenantAdminDetails: async (tenantID: string, targetID?: string) => {
    const resolvedTargetID = targetID || singleTargetID();
    const batch = await fleetAdminQuery<unknown>([
      { key: "grants", path: `/api/v1/tenancy/grants?tenant_id=${encodeURIComponent(tenantID)}` },
      { key: "agents", path: "/api/v1/agent-instances" },
      { key: "channels", path: "/api/v1/channels" },
      { key: "triggers", path: "/api/v1/triggers" },
      { key: "providers", path: "/api/v1/providers" },
    ], [resolvedTargetID]);
    const target = batch.targets[0];
    if (!target) throw new Error(`Unknown machine: ${resolvedTargetID}`);
    const readArray = <T extends object>(key: string): Array<T & { target_id: string; target_name: string }> => {
      const response = operationFor<T[]>(target.responses, key);
      if (!response?.ok) throw new Error(`${target.target.name}: ${response?.error || `${key} unavailable`}`);
      if (response.data == null) return [];
      if (!Array.isArray(response.data)) throw new Error(`${target.target.name}: invalid ${key} response`);
      return response.data.map((item) => ({
        ...item,
        target_id: target.target.id,
        target_name: target.target.name,
      }));
    };
    return {
      grants: readArray<ResourceGrant>("grants"),
      agents: readArray<AgentInstance>("agents"),
      channels: readArray<Channel>("channels"),
      triggers: readArray<Trigger>("triggers"),
      providers: readArray<Provider>("providers").map((provider) =>
        normalizeProvider(provider as Provider & Record<string, unknown>),
      ),
    };
  },
  upsertResourceGrant: (grant: ResourceGrant) => {
    const body = { ...grant }; delete body.target_id; delete body.target_name;
    return fleetCall<ResourceGrant>({ key: "grant", method: "POST", path: "/api/v1/tenancy/grants", body }, writeTargetIDs(grant.target_id)).then((result) => result.first);
  },
  deleteResourceGrant: (grant: Pick<ResourceGrant, "tenant_id" | "resource_type" | "resource_id" | "target_id">) =>
    fleetCall<{ ok: boolean }>({ key: "grant", method: "DELETE", path:
      `/api/v1/tenancy/grants?tenant_id=${encodeURIComponent(grant.tenant_id)}` +
        `&resource_type=${encodeURIComponent(grant.resource_type)}` +
        `&resource_id=${encodeURIComponent(grant.resource_id)}` }, writeTargetIDs(grant.target_id)).then((result) => result.first),
  assignResourceOwner: (payload: {
    resource_type: GrantableResourceType;
    resource_id: string;
    tenant_id: string;
    target_id?: string;
  }) => {
    const body = { ...payload }; delete body.target_id;
    return fleetCall<{ ok: boolean }>({ key: "owner", method: "POST", path: "/api/v1/tenancy/ownership", body }, writeTargetIDs(payload.target_id)).then((result) => result.first);
  },

  tools: () => activeMachineScope() === "all"
    ? fleetQuery<ToolsResponse>([{ key: "tools", path: "/api/v1/tools" }]).then(mergeFleetTools)
    : get<ToolsResponse>("/api/v1/tools"),
	ttsModels: () => getChecked<TTSCatalogStatus>("/api/v1/tts/models"),
	downloadTTSModel: (id: string, onProgress: (progress: OperationProgress) => void) =>
		postProgress<TTSModel>("/api/v1/tts/models/download/stream", { id }, onProgress),
	deleteTTSModel: (id: string) =>
		del<TTSCatalogStatus>(`/api/v1/tts/models?id=${encodeURIComponent(id)}`),
  installCLI: (
    id: string,
    action: "install" | "update" | "uninstall",
    onProgress?: (progress: OperationProgress) => void,
    acknowledgeInternal = false,
    targetIDs?: string[],
  ) => {
    if (targetIDs || fleetMode()) {
      const targets = targetIDs ?? ["all"];
      onProgress?.({ phase: "fleet", detail: "Applying to selected machines", percent: 10 });
      return fleetCall<CLIInstallResult>({
        key: "install", method: "POST", path: "/api/v1/tools/cli/install",
        body: { id, action, acknowledge_internal: acknowledgeInternal },
      }, targets, { confirm: targetIDs ? false : undefined }).then((result) => {
        onProgress?.({ phase: "complete", percent: 100 });
        return result;
      });
    }
    const request = onProgress
      ? postProgress<CLIInstallResult>("/api/v1/tools/cli/install/stream", { id, action, acknowledge_internal: acknowledgeInternal }, onProgress)
      : postChecked<CLIInstallResult>("/api/v1/tools/cli/install", { id, action, acknowledge_internal: acknowledgeInternal });
    return request.then((result) => ({ first: result, successes: [result], errors: [] as string[] }));
  },
  installBundle: (id: string, onProgress?: (progress: OperationProgress) => void, acknowledgeInternal = false) =>
    fleetMode()
      ? (onProgress?.({ phase: "fleet", detail: "Applying to all available machines", percent: 10 }), fleetCall<BundleInstallResult>({ key: "install", method: "POST", path: "/api/v1/tools/bundles/install", body: { id, acknowledge_internal: acknowledgeInternal } }, ["all"]).then((result) => { onProgress?.({ phase: "complete", percent: 100 }); return result.first; }))
      : onProgress
      ? postProgress<BundleInstallResult>("/api/v1/tools/bundles/install/stream", { id, acknowledge_internal: acknowledgeInternal }, onProgress)
      : postChecked<BundleInstallResult>("/api/v1/tools/bundles/install", { id, acknowledge_internal: acknowledgeInternal }),
  checkCLIUpdate: (id: string, targetID?: string) => targetID
    ? fleetCall<CLIUpdateCheck>({ key: "check", method: "POST", path: "/api/v1/tools/cli/check", body: { id } }, [targetID], { confirm: false }).then((result) => result.first)
    : post<CLIUpdateCheck>("/api/v1/tools/cli/check", { id }),
  cliAuth: (id: string, targetID?: string) => targetID
    ? fleetGet<CLIAuthStatus>(`/api/v1/tools/cli/auth?id=${encodeURIComponent(id)}`, targetID)
    : getChecked<CLIAuthStatus>(`/api/v1/tools/cli/auth?id=${encodeURIComponent(id)}`),
  startCLIAuth: (id: string, force = false, targetID?: string) => targetID
    ? fleetCall<CLIAuthSession>({ key: "login", method: "POST", path: "/api/v1/tools/cli/auth/login", body: { id, force } }, [targetID], { confirm: false }).then((result) => result.first)
    : postChecked<CLIAuthSession>("/api/v1/tools/cli/auth/login", { id, force }),
  cliAuthSession: (sessionID: string, targetID?: string) => targetID
    ? fleetGet<CLIAuthSession>(`/api/v1/tools/cli/auth/login?session_id=${encodeURIComponent(sessionID)}`, targetID)
    : getChecked<CLIAuthSession>(`/api/v1/tools/cli/auth/login?session_id=${encodeURIComponent(sessionID)}`),
  cancelCLIAuth: (sessionID: string, targetID?: string) => targetID
    ? fleetCall<{ ok: boolean }>({ key: "login", method: "POST", path: "/api/v1/tools/cli/auth/login/cancel", body: { session_id: sessionID } }, [targetID], { confirm: false }).then((result) => result.first)
    : postChecked<{ ok: boolean }>("/api/v1/tools/cli/auth/login/cancel", { session_id: sessionID }),
  syncCLISkills: (id: string, onProgress?: (progress: OperationProgress) => void) =>
    fleetMode()
      ? (onProgress?.({ phase: "fleet", percent: 10 }), fleetCall<CLIInstallResult>({ key: "sync", method: "POST", path: "/api/v1/tools/cli/skills/sync", body: { id } }, ["all"]).then((result) => { onProgress?.({ phase: "complete", percent: 100 }); return result.first; }))
      : onProgress
      ? postProgress<CLIInstallResult>("/api/v1/tools/cli/skills/sync/stream", { id }, onProgress)
      : postChecked<CLIInstallResult>("/api/v1/tools/cli/skills/sync", { id }),
  providers: () => getProviders("/api/v1/providers"),
  allProviders: async () => (await fleetAdminReadArray<Provider>("/api/v1/providers"))
    .map((provider) => normalizeProvider(provider as Provider & Record<string, unknown>)),
  activeRoutes: () => fleetMode() ? fleetReadArray<ProviderRoute>("/api/v1/providers/active") : get<ProviderRoute[] | null>("/api/v1/providers/active"),
  presets: async () => (await getProviders("/api/v1/providers/presets")) ?? [],
  upsertProvider: async (p: Provider, targetIDs?: string[]) => {
    const targets = targetIDs ?? writeTargetIDs(p.target_id);
    const payload = { ...p }; delete payload.target_id; delete payload.target_name;
    if (!payload.id && targets[0] === "all") payload.id = `provider-${crypto.randomUUID().replace(/-/g, "").slice(0, 12)}`;
    return (await fleetCall<Provider>({ key: "provider", method: "POST", path: "/api/v1/providers", body: payload }, targets)).first;
  },
  probeProvider: (p: Provider) => postChecked<ProviderProbeResult>("/api/v1/providers/probe", p),
  providerMonitor: () => fleetMode()
    ? fleetQuery<ProviderMonitorSnapshot>([{ key: "monitor", path: "/api/v1/providers/monitor" }]).then(mergeFleetProviderMonitor)
    : get<ProviderMonitorSnapshot>("/api/v1/providers/monitor"),
  saveProviderMonitor: (config: ProviderMonitorConfig) => fleetMode()
    ? fleetCall<ProviderMonitorSnapshot>({ key: "monitor", method: "PUT", path: "/api/v1/providers/monitor", body: config }, ["all"], { confirm: false }).then((result) => result.first)
    : put<ProviderMonitorSnapshot>("/api/v1/providers/monitor", config),
  refreshProviderModels: () => fleetMode()
    ? fleetCall<ProviderMonitorSnapshot>({ key: "monitor", method: "POST", path: "/api/v1/providers/monitor/run?catalog_only=true", body: {} }, ["all"], { confirm: false }).then((result) => result.first)
    : postChecked<ProviderMonitorSnapshot>("/api/v1/providers/monitor/run?catalog_only=true", {}),
  runProviderMonitor: () => fleetMode()
    ? fleetCall<ProviderMonitorSnapshot>({ key: "monitor", method: "POST", path: "/api/v1/providers/monitor/run", body: {} }, ["all"], { confirm: false }).then((result) => result.first)
    : postChecked<ProviderMonitorSnapshot>("/api/v1/providers/monitor/run", {}),
  dismissProviderMonitorAlert: (id = "", targetID?: string) => fleetMode()
    ? fleetCall<ProviderMonitorSnapshot>({ key: "monitor", method: "DELETE", path:
        `/api/v1/providers/monitor/alerts${id ? `?id=${encodeURIComponent(id)}` : ""}` }, writeTargetIDs(targetID)).then((result) => result.first)
    : del<ProviderMonitorSnapshot>(
      `/api/v1/providers/monitor/alerts${id ? `?id=${encodeURIComponent(id)}` : ""}`
    ),
  deleteProvider: async (id: string, targetID?: string) =>
    (await fleetCall<{ ok: boolean }>({ key: "provider", method: "DELETE", path: `/api/v1/providers?id=${encodeURIComponent(id)}` }, writeTargetIDs(targetID))).first,
  switchProvider: (id: string, tool: string, meta?: Record<string, unknown>, localTakeover?: boolean, targetID?: string) =>
    fleetCall<{ ok: boolean }>({ key: "route", method: "POST", path: "/api/v1/providers/switch", body: { id, tool, meta, local_takeover: localTakeover } }, writeTargetIDs(targetID)).then((result) => result.first),
  disableRoute: (tool: string, targetID?: string) =>
    fleetCall<{ ok: boolean }>({ key: "route", method: "DELETE", path: `/api/v1/providers/active?tool=${encodeURIComponent(tool)}` }, writeTargetIDs(targetID)).then((result) => result.first),
  proxyStatus: () => fleetMode()
    ? fleetQuery<ProxyStatus>([{ key: "proxy", path: "/api/v1/proxy/status" }]).then(mergeFleetProxyStatus)
    : get<ProxyStatus>("/api/v1/proxy/status"),
  proxyTraces: ({ tool = "", sessionID = "", limit = 100 }: { tool?: string; sessionID?: string; limit?: number } = {}, targetID?: string) => {
    const path = `/api/v1/proxy/traces?tool=${encodeURIComponent(tool)}&session_id=${encodeURIComponent(sessionID)}&limit=${limit}`;
    return targetID ? fleetGet<ProxyTrace[] | null>(path, targetID) : get<ProxyTrace[] | null>(path);
  },
  setTakeover: (tool: string, enabled: boolean, targetID?: string) =>
    fleetCall<ProxyStatus>({ key: "proxy", method: "POST", path: "/api/v1/proxy/takeover", body: { tool, enabled } }, writeTargetIDs(targetID)).then((result) => result.first),
  setProxyToolConfig: (cfg: Partial<ProxyToolConfig> & { tool: string }, targetID?: string) => {
    const body = { ...cfg }; delete body.target_id; delete body.target_name;
    return fleetCall<ProxyStatus>({ key: "proxy", method: "POST", path: "/api/v1/proxy/config", body }, writeTargetIDs(targetID)).then((result) => result.first);
  },
  setFailoverQueue: (id: string, inQueue: boolean, sortIndex = 0, targetID?: string) =>
    fleetCall<{ ok: boolean }>({ key: "provider", method: "POST", path: "/api/v1/providers/failover", body: {
      id,
      in_failover_queue: inQueue,
      sort_index: sortIndex,
    } }, writeTargetIDs(targetID)).then((result) => result.first),
  claude3pStatus: () => get<Claude3PStatus>("/api/v1/system/claude-3p"),
  setClaude3p: (enabled: boolean, providerID = "", targetID?: string) =>
    fleetCall<Claude3PStatus>({ key: "claude3p", method: "POST", path: "/api/v1/system/claude-3p", body: {
      enabled,
      provider_id: providerID,
    } }, writeTargetIDs(targetID)).then((result) => result.first),
  selectDirectory: (defaultDirectory = "") => selectSystemDirectory(defaultDirectory),
  directories: (path = "", targetID?: string) => {
    const remoteID = targetID && targetID !== "local" ? targetID : activeRemoteID();
    return remoteID
      ? getChecked<SystemDirectoryListing>(
          `/api/v1/remote/directories?id=${encodeURIComponent(remoteID)}&path=${encodeURIComponent(path)}`
        )
      : getChecked<SystemDirectoryListing>(`/api/v1/system/directories?path=${encodeURIComponent(path)}`);
  },
  ensureDirectory: (path: string, targetID?: string) => {
    const remoteID = targetID && targetID !== "local" ? targetID : activeRemoteID();
    return postChecked<{ path: string }>(
      remoteID
        ? `/api/v1/remote/directories?id=${encodeURIComponent(remoteID)}`
        : "/api/v1/system/directories",
      { path },
    );
  },

  // AgentMux Frameworks: detect & install agent frameworks
  frameworks: () => activeMachineScope() === "all"
    ? fleetQuery<FrameworksResponse>([{ key: "frameworks", path: "/api/v1/frameworks" }]).then(mergeFleetFrameworks)
    : get<FrameworksResponse>("/api/v1/frameworks"),
  frameworkRuntimeSettings: (kind: string, workDir = "", targetID?: string) => {
    const path =
      `/api/v1/frameworks/runtime-settings?kind=${encodeURIComponent(kind)}` +
        (workDir ? `&work_dir=${encodeURIComponent(workDir)}` : "");
    return fleetGet<FrameworkRuntimeSettings>(path, singleTargetID(targetID));
  },
  frameworkAuth: (kind: string, targetID?: string) =>
    fleetGet<FrameworkAuthStatus>(`/api/v1/frameworks/auth?kind=${encodeURIComponent(kind)}`, singleTargetID(targetID)),
  logoutFramework: (kind: string, targetID?: string) =>
    fleetCall<FrameworkAuthStatus>({
      key: "logout", method: "POST", path: "/api/v1/frameworks/auth/logout", body: { kind },
    }, [singleTargetID(targetID)], { confirm: false }).then((result) => result.first),
  startFrameworkLogin: (kind: string, targetID?: string) =>
    fleetCall<FrameworkLoginResult>({ key: "login", method: "POST", path: "/api/v1/frameworks/login", body: { kind } }, [singleTargetID(targetID)], { confirm: false }).then((result) => result.first),
  frameworkLoginSession: (sessionID: string, targetID?: string) =>
    fleetGet<FrameworkLoginSessionStatus>(`/api/v1/frameworks/login?session_id=${encodeURIComponent(sessionID)}`, singleTargetID(targetID)),
  cancelFrameworkLogin: (sessionID: string, targetID?: string) =>
    fleetCall<{ ok: boolean }>({ key: "login", method: "POST", path: "/api/v1/frameworks/login/cancel", body: { session_id: sessionID } }, [singleTargetID(targetID)], { confirm: false }).then((result) => result.first),
  completeFrameworkLogin: (sessionID: string, code: string, targetID?: string) =>
    fleetCall<{ ok: boolean }>({ key: "login", method: "POST", path: "/api/v1/frameworks/login/complete", body: { session_id: sessionID, code } }, [singleTargetID(targetID)], { confirm: false }).then((result) => result.first),
  installFramework: (
    kind: string,
    action: "install" | "update" | "uninstall" = "install",
    onProgress?: (progress: OperationProgress) => void,
    acknowledgeInternal = false,
    targetID?: string,
  ) => targetID || fleetMode()
    ? (onProgress?.({ phase: "fleet", detail: "Applying to selected machine", percent: 10 }), fleetCall<FrameworkInstallResult>({ key: "install", method: "POST", path: "/api/v1/frameworks/install", body: { kind, action, acknowledge_internal: acknowledgeInternal } }, targetID ? [targetID] : ["all"], { confirm: Boolean(targetID) ? false : undefined }).then((result) => { onProgress?.({ phase: "complete", percent: 100 }); return result.first; }))
    : onProgress
    ? postProgress<FrameworkInstallResult>("/api/v1/frameworks/install/stream", { kind, action, acknowledge_internal: acknowledgeInternal }, onProgress)
    : postChecked<FrameworkInstallResult>("/api/v1/frameworks/install", { kind, action, acknowledge_internal: acknowledgeInternal }),
  checkFrameworkUpdate: (kind: string, targetID?: string) => targetID
    ? fleetCall<FrameworkUpdateCheck>({ key: "check", method: "POST", path: "/api/v1/frameworks/check", body: { kind } }, [targetID], { confirm: false }).then((result) => result.first)
    : post<FrameworkUpdateCheck>("/api/v1/frameworks/check", { kind }),
  usage: (period: string, from = "", to = "") => {
    const params = new URLSearchParams({ period });
    if (from) params.set("from", from);
    if (to) params.set("to", to);
    const timezone = Intl.DateTimeFormat().resolvedOptions().timeZone;
    if (timezone) params.set("timezone", timezone);
    const path = `/api/v1/usage?${params.toString()}`;
    if (activeMachineScope() !== "all") return get<UsageReport>(path);
    return fleetQuery<UsageReport>([{ key: "usage", path }]).then((batch) => mergeFleetUsage(batch));
  },
  usageSources: () => fleetMode()
    ? fleetReadArray<CursorUsageSourceStatus>("/api/v1/usage/sources", "usage_sources")
    : get<CursorUsageSourceStatus[]>("/api/v1/usage/sources"),
  cursorUsageAction: async (action: "preview" | "connect" | "sync" | "repair" | "disconnect", targetID?: string) => {
    const path = `/api/v1/usage/sources/cursor/${encodeURIComponent(action)}`;
    if (targetID || activeMachineScope() === "all") {
      return (await fleetCall<CursorUsageActionResult>({ key: "cursor_usage", method: "POST", path }, writeTargetIDs(targetID))).first;
    }
    return postChecked<CursorUsageActionResult>(path, {});
  },
  menubarSettings: () => getLocal<MenubarSettings>("/api/v1/menubar/settings"),
  saveMenubarSettings: (settings: MenubarSettings) =>
    putLocal<MenubarSettings>("/api/v1/menubar/settings", settings),

  // AgentMux Observability
  observationOverview: () => activeMachineScope() === "all"
    ? fleetQuery<ObservationOverview>([{ key: "overview", path: "/api/v1/observability/overview" }]).then(mergeFleetObservationOverview)
    : observationGet<ObservationOverview>("/api/v1/observability/overview"),
  observationTraces: ({
    agentID = "",
    runtimeID = "",
    sessionID = "",
    status = "",
    source = "",
    limit = 100,
    offset = 0,
  }: ObservationTraceFilters = {}) =>
    (activeMachineScope() === "all" ? fleetReadArray<ObservationTrace>(
      `/api/v1/observability/traces?agent_id=${encodeURIComponent(agentID)}&runtime_id=${encodeURIComponent(
        runtimeID
      )}&session_id=${encodeURIComponent(sessionID)}&status=${encodeURIComponent(status)}&source=${encodeURIComponent(
        source
      )}&limit=${limit}&offset=${offset}`
    ) : observationGet<ObservationTrace[] | { traces: ObservationTrace[]; total?: number }>(
      `/api/v1/observability/traces?agent_id=${encodeURIComponent(agentID)}&runtime_id=${encodeURIComponent(
        runtimeID
      )}&session_id=${encodeURIComponent(sessionID)}&status=${encodeURIComponent(status)}&source=${encodeURIComponent(
        source
      )}&limit=${limit}&offset=${offset}`
    )),
  observationTrace: (traceID: string, targetID?: string) => targetID
    ? fleetGet<ObservationTraceDetail>(`/api/v1/observability/traces/${encodeURIComponent(traceID)}`, targetID)
    : observationGet<ObservationTraceDetail>(`/api/v1/observability/traces/${encodeURIComponent(traceID)}`),
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
    (activeMachineScope() === "all" ? fleetReadArray<ObservationInsight>(
      `/api/v1/observability/insights?agent_id=${encodeURIComponent(agentID)}&status=${encodeURIComponent(
        status
      )}&rule_id=${encodeURIComponent(ruleID)}&limit=${limit}`
    ) : observationGet<ObservationInsight[] | { insights: ObservationInsight[]; total?: number }>(
      `/api/v1/observability/insights?agent_id=${encodeURIComponent(agentID)}&status=${encodeURIComponent(
        status
      )}&rule_id=${encodeURIComponent(ruleID)}&limit=${limit}`
    )),
  observationSettings: () => observationGet<ObservationSettings>("/api/v1/observability/settings"),
  observationIntegrations: () => activeMachineScope() === "all"
    ? fleetReadArray<ObservationIntegration>("/api/v1/observability/integrations")
    : observationGet<ObservationIntegration[] | { integrations: ObservationIntegration[] }>("/api/v1/observability/integrations"),
  observationIntegrationAction: (
    host: string,
    action: "preview" | "install" | "repair" | "uninstall" | "doctor",
    body: Record<string, unknown> = {},
    targetID?: string,
  ) => targetID
    ? fleetCall<ObservationIntegrationActionResult>({
        key: "integration", method: "POST",
        path: `/api/v1/observability/integrations/${encodeURIComponent(host)}/${action}`,
        body,
      }, [targetID]).then((result) => result.first)
    : observationPost<ObservationIntegrationActionResult>(
        `/api/v1/observability/integrations/${encodeURIComponent(host)}/${action}`,
        body,
      ),

  // AgentMux Connect: channels & triggers
  channels: () => fleetMode() ? fleetReadArray<Channel>("/api/v1/channels") : get<Channel[] | null>("/api/v1/channels"),
  channelHealth: () => fleetMode()
    ? fleetReadArray<Channel>("/api/v1/channels?view=health")
    : get<Channel[] | null>("/api/v1/channels?view=health"),
  upsertChannel: async (ch: Partial<Channel>) => {
    const targetID = ch.target_id || activeRemoteID();
    const channel = { ...ch };
    delete channel.target_id;
    delete channel.target_name;
    if (activeMachineScope() === "all" && !ch.target_id) {
      const chinese = document.documentElement.lang.toLowerCase().startsWith("zh");
      if (!window.confirm(chinese ? "确定在所有可用机器上保存这个 Channel 吗？" : "Save this channel on every available machine?")) {
        throw new Error(chinese ? "已取消多机操作。" : "Fleet operation cancelled.");
      }
      if (!channel.id) channel.id = `channel-${crypto.randomUUID().replace(/-/g, "").slice(0, 12)}`;
      const targets = (await getLocal<MachineTarget[]>("/api/v1/remote/fleet/targets")).filter((target) => target.trusted);
      const results = await Promise.allSettled(targets.map((target) => postChecked<Channel>("/api/v1/remote/channels/claim", {
        target_id: target.id === "local" ? "" : target.id,
        channel,
      }).then((saved) => ({ ...saved, target_id: target.id, target_name: target.name }))));
      const success = results.find((result) => result.status === "fulfilled");
      if (!success || success.status !== "fulfilled") throw new Error(results.map((result) => result.status === "rejected" ? String(result.reason) : "").filter(Boolean).join("; "));
      return success.value;
    }
    return postChecked<Channel>("/api/v1/remote/channels/claim", {
      target_id: targetID === "local" ? "" : targetID,
      channel,
    });
  },
  deleteChannel: async (id: string, targetID?: string) =>
    (await fleetCall<{ ok: boolean }>({ key: "channel", method: "DELETE", path: `/api/v1/channels?id=${encodeURIComponent(id)}` }, writeTargetIDs(targetID))).first,
  restartChannel: async (id: string, targetID?: string) =>
    (await fleetCall<{ ok: boolean }>({ key: "channel", method: "POST", path: `/api/v1/channels/restart?id=${encodeURIComponent(id)}`, body: {} }, writeTargetIDs(targetID))).first,
  channelConversations: (channelID: string, targetID?: string) => {
    const path = `/api/v1/channel-conversations?channel_id=${encodeURIComponent(channelID)}`;
    return targetID ? fleetGet<ChannelConversation[] | null>(path, targetID) : get<ChannelConversation[] | null>(path);
  },
  bindChannelConversation: (channelID: string, conversationID: string, threadID: string, targetID?: string) =>
    fleetCall<{ ok: boolean; thread_id: string }>({ key: "bind", method: "POST", path: "/api/v1/channel-conversations/bind", body: {
      channel_id: channelID,
      conversation_id: conversationID,
      thread_id: threadID,
    } }, writeTargetIDs(targetID)).then((result) => result.first),
  openCodexThread: (threadID: string, targetID?: string) =>
    fleetCall<{ ok: boolean; thread_id: string; command?: string; opened?: boolean; status_message?: string }>({
      key: "open", method: "POST", path: "/api/v1/channel-conversations/open", body: { thread_id: threadID },
    }, writeTargetIDs(targetID)).then((result) => result.first),
  channelTasks: (channelID = "", conversationID = "", targetID?: string) => {
    const path = `/api/v1/channel-tasks?channel_id=${encodeURIComponent(channelID)}&conversation_id=${encodeURIComponent(conversationID)}`;
    return targetID ? fleetGet<ChannelTask[] | null>(path, targetID) : get<ChannelTask[] | null>(path);
  },
  channelInteractions: (channelID = "", conversationID = "", targetID?: string) => {
    const path = `/api/v1/channel-interactions?channel_id=${encodeURIComponent(channelID)}&conversation_id=${encodeURIComponent(conversationID)}`;
    return targetID ? fleetGet<ChannelInteraction[] | null>(path, targetID) : get<ChannelInteraction[] | null>(path);
  },
  respondChannelInteraction: (
    interactionID: string,
    nonce: string,
    decision: string,
    answers: Record<string, string[]>,
    targetID?: string,
  ) => fleetCall<{ ok: boolean }>({ key: "interaction", method: "POST", path: "/api/v1/channel-interactions/respond", body: {
      interaction_id: interactionID,
      nonce,
      decision,
      answers,
    } }, writeTargetIDs(targetID)).then((result) => result.first),
  meetingOverview: () => getChecked<MeetingOverview>(`/api/v1/remote/meetings?target_id=${encodeURIComponent(activeMachineScope())}`),
  respondMeetingInvitation: (invitation: MeetingInvitation, decision: "join" | "reject") =>
    postChecked<MeetingInvitation>("/api/v1/remote/meetings/invitations/respond", {
      target_id: invitation.target_id ?? "",
      channel_id: invitation.channel_id,
      invitation_id: invitation.id,
      nonce: invitation.nonce,
      decision,
    }),
  joinMeeting: (targetID: string, channelID: string, meetingNumber: string) =>
    postChecked<MeetingJoinResult>("/api/v1/remote/meetings/join", {
      target_id: targetID,
      channel_id: channelID,
      meeting_number: meetingNumber,
    }),
  meetingActivity: async (targetID: string, channelID: string, meetingID: string) => {
    const detail = await getChecked<MeetingDetail>(`/api/v1/remote/meetings/activity?target_id=${encodeURIComponent(targetID)}&channel_id=${encodeURIComponent(channelID)}&meeting_id=${encodeURIComponent(meetingID)}`);
    return {
      ...detail,
      // Older remote AgentMux instances may still encode empty Go slices as
      // null. Keep the UI-facing contract stable while mixed versions run.
      items: Array.isArray(detail.items) ? detail.items : [],
      turns: Array.isArray(detail.turns) ? detail.turns : [],
    };
  },
  sendMeetingMessage: (targetID: string, channelID: string, meetingID: string, text: string) =>
    postChecked<{ sent: boolean }>("/api/v1/remote/meetings/messages", {
      target_id: targetID, channel_id: channelID, meeting_id: meetingID, text,
    }),
  askMeeting: (targetID: string, channelID: string, meetingID: string, question: string) =>
    postChecked<MeetingTurn>("/api/v1/remote/meetings/questions", {
      target_id: targetID, channel_id: channelID, meeting_id: meetingID, question,
    }),
  setMeetingResponseMode: (targetID: string, channelID: string, mode: MeetingResponseMode) =>
    postChecked<{ channel_id: string; response_mode: MeetingResponseMode }>("/api/v1/remote/meetings/response-mode", {
      target_id: targetID, channel_id: channelID, mode,
    }),
  beginFeishuSetup: () => postChecked<FeishuSetupBeginResponse>("/api/v1/setup/feishu/begin", {}),
  pollFeishuSetup: (deviceCode: string, baseUrl = "") =>
    postChecked<FeishuSetupPollResponse>("/api/v1/setup/feishu/poll", { device_code: deviceCode, base_url: baseUrl }),
  beginFeishuAutomation: () =>
    postChecked<FeishuAutomationBeginResponse>("/api/v1/setup/feishu/automation/begin", {}),
  pollFeishuAutomation: (sessionID: string) =>
    postChecked<FeishuAutomationPollResponse>("/api/v1/setup/feishu/automation/poll", { session_id: sessionID }),
  configureFeishuAutomation: (sessionID: string, appID: string, visibility: "owner" | "all") =>
    postChecked<FeishuAutomationResult>("/api/v1/setup/feishu/automation/configure", {
      session_id: sessionID,
      app_id: appID,
      publish: true,
      visibility,
    }),
  triggers: () => fleetMode() ? fleetReadArray<Trigger>("/api/v1/triggers") : get<Trigger[] | null>("/api/v1/triggers"),
  upsertTrigger: async (tr: Partial<Trigger>) => {
    const payload = { ...tr }; delete payload.target_id; delete payload.target_name;
    return (await fleetCall<Trigger>({ key: "trigger", method: "POST", path: "/api/v1/triggers", body: payload }, writeTargetIDs(tr.target_id))).first;
  },
  deleteTrigger: async (id: string, targetID?: string) =>
    (await fleetCall<{ ok: boolean }>({ key: "trigger", method: "DELETE", path: `/api/v1/triggers?id=${encodeURIComponent(id)}` }, writeTargetIDs(targetID))).first,
  runTrigger: async (id: string, targetID?: string) =>
    (await fleetCall<{ ok: boolean }>({ key: "trigger", method: "POST", path: `/api/v1/triggers/run?id=${encodeURIComponent(id)}`, body: {} }, writeTargetIDs(targetID))).first,

  // AgentMux Memory
  memory: (scope = "", q = "", limit = 50) =>
    (fleetMode() ? fleetReadArray<MemoryEntry>(
      `/api/v1/memory?scope=${encodeURIComponent(scope)}&q=${encodeURIComponent(q)}&limit=${limit}`
    ) : get<MemoryEntry[] | null>(
      `/api/v1/memory?scope=${encodeURIComponent(scope)}&q=${encodeURIComponent(q)}&limit=${limit}`
    )),
  putMemory: async (e: Partial<MemoryEntry>, targetIDs?: string[]) => {
    const targets = targetIDs ?? writeTargetIDs(e.target_id);
    const payload = { ...e }; delete payload.target_id; delete payload.target_name;
    if (!payload.id && targets[0] === "all") payload.id = crypto.randomUUID().replace(/-/g, "").slice(0, 16);
    return (await fleetCall<{ id: string }>({ key: "memory", method: "POST", path: "/api/v1/memory", body: payload }, targets)).first;
  },
  deleteMemory: async (id: string, targetID?: string) =>
    (await fleetCall<{ ok: boolean }>({ key: "memory", method: "DELETE", path: `/api/v1/memory?id=${encodeURIComponent(id)}` }, writeTargetIDs(targetID))).first,

  // AgentMux Skills
  skills: () => fleetMode() ? fleetReadArray<Skill>("/api/v1/skills") : get<Skill[] | null>("/api/v1/skills"),
  skillMarketplace: (q = "", source = "", category = "") =>
    get<MarketplaceSkill[] | null>(
      `/api/v1/skills/marketplace?q=${encodeURIComponent(q)}&source=${encodeURIComponent(source)}&category=${encodeURIComponent(category)}`
    ),
  installSkill: (skill: Pick<MarketplaceSkill, "repo" | "path" | "name">, targetIDs?: string[]) => {
    if (targetIDs || fleetMode()) {
      return fleetCall<Skill>({ key: "skill", method: "POST", path: "/api/v1/skills/install", body: skill }, targetIDs ?? ["all"], { confirm: targetIDs ? false : undefined });
    }
    return postChecked<Skill>("/api/v1/skills/install", skill)
      .then((result) => ({ first: result, successes: [result], errors: [] as string[] }));
  },
  uninstallSkill: (name: string, targetID?: string) => {
    const path = `/api/v1/skills?name=${encodeURIComponent(name)}`;
    if (targetID) {
      return fleetCall<{ ok: boolean }>({ key: "skill", method: "DELETE", path }, [targetID], { confirm: false }).then((result) => result.first);
    }
    return del<{ ok: boolean }>(path);
  },
  toggleSkill: async (name: string, enabled: boolean, targetID?: string) =>
    (await fleetCall<{ ok: boolean }>({ key: "skill", method: "POST", path: "/api/v1/skills/toggle", body: { name, enabled } }, writeTargetIDs(targetID))).first,

  // AgentMux MCP Registry
  mcp: () => fleetMode() ? fleetReadArray<MCPServer>("/api/v1/mcp") : get<MCPServer[] | null>("/api/v1/mcp"),
  upsertMCP: async (m: MCPServer, targetIDs?: string[]) => {
    const targets = targetIDs ?? writeTargetIDs(m.target_id);
    const payload = { ...m }; delete payload.target_id; delete payload.target_name;
    return (await fleetCall<MCPServer>({ key: "mcp", method: "POST", path: "/api/v1/mcp", body: payload }, targets)).first;
  },
  deleteMCP: async (name: string, targetID?: string) =>
    (await fleetCall<{ ok: boolean }>({ key: "mcp", method: "DELETE", path: `/api/v1/mcp?name=${encodeURIComponent(name)}` }, writeTargetIDs(targetID))).first,

  // AgentMux Guard
  guardPolicies: () => fleetMode() ? fleetReadArray<GuardPolicy>("/api/v1/guard/policies") : get<GuardPolicy[] | null>("/api/v1/guard/policies"),
	evaluateGuard: async (req: { agent_id?: string; runtime_id?: string; tool: string; action?: string }, targetIDs?: string[]) =>
    (await fleetCall<{ decision: string }>({ key: "guard", method: "POST", path: "/api/v1/guard/evaluate", body: req }, targetIDs ?? writeTargetIDs())).first,

  // Claude Code / Codex Sessions
  sessions: (provider = "", surface = "") =>
    (fleetMode() ? fleetReadArray<AgentSession>(
      `/api/v1/sessions?provider=${encodeURIComponent(provider)}&surface=${encodeURIComponent(surface)}`
    ) : get<AgentSession[] | null>(
      `/api/v1/sessions?provider=${encodeURIComponent(provider)}&surface=${encodeURIComponent(surface)}`
    )),
  codexDesktopThreads: (targetID?: string) => fleetGet<AgentSession[] | null>("/api/v1/codex/desktop-threads", singleTargetID(targetID)),
  sessionMessages: (session: Pick<AgentSession, "provider_id" | "surface" | "session_id" | "source_path" | "project_dir" | "conversation_id" | "target_id">) => {
    const path = `/api/v1/sessions/messages?provider=${encodeURIComponent(session.provider_id)}&surface=${encodeURIComponent(
        session.surface
      )}&session_id=${encodeURIComponent(session.session_id)}&source_path=${encodeURIComponent(
        session.source_path ?? ""
      )}&project_dir=${encodeURIComponent(session.project_dir ?? "")}&conversation_id=${encodeURIComponent(
        session.conversation_id ?? ""
      )}`;
    return session.target_id ? fleetGet<SessionMessage[] | null>(path, session.target_id) : get<SessionMessage[] | null>(path);
  },
  sendSessionMessage: (
    session: Pick<AgentSession, "channel_id" | "conversation_id" | "target_id">,
    text: string
  ) => fleetCall<{ ok: boolean; answer: string }>({ key: "message", method: "POST", path: "/api/v1/sessions/messages", body: {
      channel_id: session.channel_id,
      conversation_id: session.conversation_id,
      text,
    } }, writeTargetIDs(session.target_id)).then((result) => result.first),
  resumeSession: (session: Pick<AgentSession, "provider_id" | "surface" | "session_id" | "source_path" | "project_dir" | "target_id">, openTerminal = false) =>
    fleetCall<{ ok: boolean; command?: string; thread_id?: string; opened?: boolean; status_message?: string }>({
      key: "resume", method: "POST", path: "/api/v1/sessions/resume", body: {
        provider_id: session.provider_id,
        surface: session.surface,
        session_id: session.session_id,
        source_path: session.source_path,
        project_dir: session.project_dir,
        open_terminal: openTerminal,
		} }, writeTargetIDs(session.target_id)).then((result) => result.first),
	stopSession: (session: Pick<AgentSession, "channel_id" | "conversation_id" | "active_task_id" | "target_id">) =>
		fleetCall<{ ok: boolean; status: string; can_stop: boolean; task_id?: string }>({ key: "stop", method: "POST", path: "/api/v1/sessions/stop", body: {
			channel_id: session.channel_id,
			conversation_id: session.conversation_id,
			active_task_id: session.active_task_id,
		} }, writeTargetIDs(session.target_id)).then((result) => result.first),
  terminalSession: (session: Pick<AgentSession, "channel_id" | "conversation_id" | "target_id">) => {
    const path = `/api/v1/sessions/terminal?channel_id=${encodeURIComponent(session.channel_id ?? "")}&conversation_id=${encodeURIComponent(session.conversation_id ?? "")}`;
    return session.target_id ? fleetGet<TerminalSessionView>(path, session.target_id) : get<TerminalSessionView>(path);
  },
  writeTerminal: (session: Pick<AgentSession, "channel_id" | "conversation_id" | "target_id">, text: string, submit = true) =>
    fleetCall<{ ok: boolean }>({ key: "terminal", method: "POST", path: "/api/v1/sessions/terminal/input", body: {
      channel_id: session.channel_id,
      conversation_id: session.conversation_id,
      text,
      submit,
    } }, writeTargetIDs(session.target_id)).then((result) => result.first),
  resizeTerminal: (session: Pick<AgentSession, "channel_id" | "conversation_id" | "target_id">, columns: number, rows: number) =>
    fleetCall<{ ok: boolean }>({ key: "terminal", method: "POST", path: "/api/v1/sessions/terminal/resize", body: {
      channel_id: session.channel_id,
      conversation_id: session.conversation_id,
      columns,
      rows,
    } }, writeTargetIDs(session.target_id)).then((result) => result.first),
  feedback: (filters: { channelID?: string; taskID?: string; limit?: number } = {}) => {
    const path = `/api/v1/feedback?channel_id=${encodeURIComponent(filters.channelID ?? "")}&task_id=${encodeURIComponent(filters.taskID ?? "")}&limit=${filters.limit ?? 200}`;
    if (!fleetMode()) return get<FeedbackReport>(path);
    return fleetQuery<FeedbackReport>([{ key: "feedback", path }]).then((batch) => {
      const report: FeedbackReport = { items: [], counts: { positive: 0, progress: 0, negative: 0 }, total: 0 };
      for (const target of batch.targets) {
        const response = operationFor<FeedbackReport>(target.responses, "feedback");
        if (!response?.ok || !response.data) continue;
        report.items.push(...response.data.items.map((item) => ({ ...item, target_id: target.target.id, target_name: target.target.name })));
        for (const semantic of ["positive", "progress", "negative"] as const) report.counts[semantic] += response.data.counts[semantic] || 0;
        report.total += response.data.total || 0;
      }
      report.items.sort((left, right) => Date.parse(right.updated_at) - Date.parse(left.updated_at));
      return report;
    });
  },
  updateFeedbackDetail: (feedback: Pick<ChannelFeedback, "id" | "target_id"> & { reason?: string; comment?: string }) => {
    const body = { ...feedback }; delete body.target_id;
    return fleetCall<{ ok: boolean }>({ key: "feedback", method: "POST", path: "/api/v1/feedback/detail", body }, writeTargetIDs(feedback.target_id)).then((result) => result.first);
  },
  orchestrations: (active = false) =>
    fleetMode() ? fleetReadArray<Orchestration>(`/api/v1/orchestrations?active=${active}`) : get<Orchestration[] | null>(`/api/v1/orchestrations?active=${active}`),
  orchestration: (id: string, targetID?: string) => targetID
    ? fleetGet<Orchestration>(`/api/v1/orchestrations?id=${encodeURIComponent(id)}`, targetID)
    : get<Orchestration>(`/api/v1/orchestrations?id=${encodeURIComponent(id)}`),
	createOrchestration: (payload: { name?: string; max_concurrency?: number; tasks: Array<Pick<OrchestrationTask, "id" | "agent_id" | "input" | "depends_on">>; target_id?: string }) => {
    const body = { ...payload }; delete body.target_id;
    return fleetCall<Orchestration>({ key: "orchestration", method: "POST", path: "/api/v1/orchestrations", body }, writeTargetIDs(payload.target_id)).then((result) => result.first);
  },
  cancelOrchestration: (id: string, targetID?: string) =>
    fleetCall<{ ok: boolean }>({ key: "orchestration", method: "POST", path: "/api/v1/orchestrations/cancel", body: { id } }, writeTargetIDs(targetID)).then((result) => result.first),
  deleteSession: (session: Pick<AgentSession, "provider_id" | "surface" | "session_id" | "source_path" | "target_id">) => {
    const path = `/api/v1/sessions?provider=${encodeURIComponent(session.provider_id)}&surface=${encodeURIComponent(
        session.surface
      )}&session_id=${encodeURIComponent(session.session_id)}&source_path=${encodeURIComponent(session.source_path ?? "")}`;
    return fleetCall<{ ok: boolean }>({ key: "session", method: "DELETE", path }, writeTargetIDs(session.target_id)).then((result) => result.first);
  },
};
