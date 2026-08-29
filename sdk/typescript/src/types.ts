/**
 * Wire types aligned with contract/schemas (the golden JSON schemas generated
 * from the AgentMux Go types). Unknown extra fields from newer servers are
 * preserved at runtime; these interfaces describe the contracted subset.
 */

/** Contract major this SDK speaks; servers outside it report `incompatible`. */
export const SUPPORTED_CONTRACT_MAJOR = 1;

/** Unified 5-state health machine (see contract/CONTRACT.md). */
export type HealthState =
  | "ready"
  | "unauthorized"
  | "incompatible"
  | "unreachable"
  | "missing";

export interface ModuleState {
  registered: boolean;
  active: boolean;
}

/** Response of GET /api/v1/capabilities. */
export interface Capabilities {
  ok: boolean;
  product: string;
  version: string;
  contract_version: string;
  features: string[];
  modules?: Record<string, ModuleState>;
  agents?: { count: number; runtimes?: string[] };
  channels?: { count: number };
  projects?: number;
  auth?: {
    bridge_enabled: boolean;
    /**
     * "admin" when the credential sees the whole instance, "tenant" when it is
     * confined to one application. Servers older than contract 1.1 omit it,
     * which means there is no tenancy and the scope is effectively admin.
     */
    scope?: "admin" | "tenant";
    tenant?: string;
    tenant_id?: string;
  };
}

export interface HealthReport {
  state: HealthState;
  message: string;
  version?: string;
  contractVersion?: string;
  capabilities?: Capabilities;
}

/** Invocation attachment; `kind` is "image" or "file". */
export interface Attachment {
  kind: "image" | "file";
  name?: string;
  mime_type?: string;
  path?: string;
  url?: string;
}

export interface InvocationRequest {
  agent_id?: string;
  project?: string;
  conversation_id?: string;
  input: string;
  attachments?: Attachment[];
  output_schema?: Record<string, unknown>;
}

export interface InvocationResult {
  id: string;
  agent_id?: string;
  project?: string;
  conversation_id: string;
  session_id?: string;
  answer: string;
  duration_ms: number;
  usage?: Record<string, unknown>;
}

export type InvocationEventType =
  | "started"
  | "thinking"
  | "tool_use"
  | "output"
  | "final"
  | "permission"
  | "model_request"
  | "model_response"
  | "compaction"
  | "completed"
  | "error"
  | "event";

/**
 * One SSE event from POST /api/v1/invocations/stream.
 *
 * The `text` of `output`/`thinking` events is a **full snapshot**: replace
 * what you previously displayed instead of appending.
 */
export interface InvocationEvent {
  type: InvocationEventType | (string & {});
  invocation_id?: string;
  conversation_id?: string;
  session_id?: string;
  event_id?: string;
  turn_id?: string;
  item_id?: string;
  text?: string;
  status?: string;
  final?: boolean;
  duration_ms?: number;
  tool_name?: string;
  tool_call_id?: string;
  tool_input?: string;
  tool_result?: string;
  interaction?: Record<string, unknown>;
  usage?: Record<string, unknown>;
  metadata?: Record<string, string>;
  error?: string;
  result?: InvocationResult;
}

/** Console-managed Agent (contract/schemas/agent_instance.json subset). */
export interface AgentInstance {
  id: string;
  name: string;
  runtime_id: string;
  enabled: boolean;
  work_dir?: string;
  system_prompt?: string;
  provider_tool?: string;
  default_model?: string;
  default_reasoning_effort?: string;
  default_service_tier?: string;
  default_approval_mode?: string;
  source?: string;
  owner_tenant_id?: string;
  owner_tenant_name?: string;
  visibility?: ResourceVisibility;
  [extra: string]: unknown;
}

/** IM channel (contract/schemas/channel.json). */
export interface Channel {
  id: string;
  name: string;
  type: string;
  enabled: boolean;
  agent_id?: string;
  config?: Record<string, string>;
  owner_tenant_id?: string;
  owner_tenant_name?: string;
  visibility?: ResourceVisibility;
  [extra: string]: unknown;
}

/**
 * Tenancy (contract 1.1). One AgentMux instance is shared by several host
 * applications; each is a tenant that sees only its own resources, the ones
 * marked public, and the ones an administrator granted it.
 */
export type ResourceVisibility = "private" | "public";

/** A registered host application (contract/schemas/tenant.json). */
export interface Tenant {
  id: string;
  name: string;
  status: "active" | "disabled" | (string & {});
  kind?: "app" | "web" | "service" | (string & {});
  note?: string;
  [extra: string]: unknown;
}

/**
 * Result of registering a tenant. `token` is returned once and cannot be
 * recovered, so the host backend must persist it before discarding this object.
 */
export interface TenantRegistration {
  tenant: Tenant;
  token: string;
  prefix?: string;
}

/** Response of GET /api/v1/tenancy/self. */
export interface TenancySelf {
  admin: boolean;
  tenant?: string;
  tenant_id?: string;
  kind?: string;
  status?: string;
}

/** Automation trigger (contract/schemas/trigger.json subset). */
export interface Trigger {
  id: string;
  name: string;
  kind: string;
  enabled: boolean;
  agent_id?: string;
  channel_id?: string;
  cron_expr?: string;
  prompt?: string;
  event?: string;
  last_status?: string;
  last_error?: string;
  [extra: string]: unknown;
}

export type OrchestrationStatus =
  | "queued"
  | "running"
  | "succeeded"
  | "failed"
  | "cancelled";

export interface OrchestrationTask {
  id: string;
  input: string;
  agent_id?: string;
  project?: string;
  depends_on?: string[];
  status?: OrchestrationStatus;
  output?: string;
  error?: string;
  invocation_id?: string;
  conversation_id?: string;
}

export interface Orchestration {
  id: string;
  name?: string;
  status: OrchestrationStatus;
  max_concurrency: number;
  error?: string;
  tasks?: OrchestrationTask[];
}

/**
 * Tenant-scoped host application view composed from public AgentMux APIs by
 * `client.integration.snapshot()`.
 */
export interface IntegrationSnapshot {
  capabilities: Capabilities;
  identity: TenancySelf;
  runtimes: string[];
  platforms: string[];
  agents: AgentInstance[];
  channels: Channel[];
  triggers: Trigger[];
  orchestrations: Orchestration[];
}

/** Response of POST /api/v1/console/sessions. */
export interface ConsoleSession {
  enter_url: string;
  expires_at: string;
  session_ttl_seconds: number;
}

const VERSION_PATTERN = /v?(\d+)\.(\d+)(?:\.(\d+))?/;

/** Parse "v1.2.3" / "1.2" into a comparable [major, minor, patch] tuple. */
export function versionKey(version: string | undefined | null): [number, number, number] | null {
  const match = VERSION_PATTERN.exec(version ?? "");
  if (!match) return null;
  return [Number(match[1]), Number(match[2]), Number(match[3] ?? 0)];
}

export function compareVersions(a: string, b: string): number {
  const keyA = versionKey(a);
  const keyB = versionKey(b);
  if (!keyA || !keyB) return 0;
  for (let i = 0; i < 3; i++) {
    const delta = (keyA[i] ?? 0) - (keyB[i] ?? 0);
    if (delta !== 0) return Math.sign(delta);
  }
  return 0;
}

export function contractMajor(contractVersion: string | undefined): number | null {
  const key = versionKey(contractVersion);
  return key ? key[0] : null;
}
