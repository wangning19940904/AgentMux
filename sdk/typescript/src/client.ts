/**
 * Fetch-based AgentMux client for browsers and Node (>=18).
 *
 * In browsers, do not embed the bridge token: either proxy through your own
 * backend (BFF) or rely on a Console session cookie minted by your backend
 * via `client.console.createSession()`.
 */

import {
  AgentMuxUnreachableError,
  errorForStatus,
} from "./errors.js";
import { iterSSEPayloads } from "./sse.js";
import {
  SUPPORTED_CONTRACT_MAJOR,
  compareVersions,
  contractMajor,
  versionKey,
  type AgentInstance,
  type Capabilities,
  type Channel,
  type ConsoleSession,
  type TenantRegistration,
  type HealthReport,
  type InvocationEvent,
  type InvocationRequest,
  type InvocationResult,
  type IntegrationSnapshot,
  type Orchestration,
  type OrchestrationTask,
  type TenancySelf,
  type Tenant,
  type Trigger,
} from "./types.js";

export const DEFAULT_BASE_URL = "http://127.0.0.1:8765";

export interface AgentMuxClientOptions {
  baseUrl?: string;
  /** Bridge bearer token. Never embed this in browser code. */
  token?: string;
  /** Optional consumer version floor checked by health(). */
  minVersion?: string;
  /** Custom fetch implementation (tests, polyfills). */
  fetch?: typeof fetch;
  /** Extra headers on every request. */
  headers?: Record<string, string>;
}

export interface InvokeOptions {
  agentId?: string;
  project?: string;
  conversationId?: string;
  input: string;
  attachments?: InvocationRequest["attachments"];
  outputSchema?: Record<string, unknown>;
  signal?: AbortSignal;
}

interface RequestOptions {
  method?: string;
  body?: unknown;
  query?: Record<string, string>;
  signal?: AbortSignal;
  accept?: string;
}

async function errorMessage(response: Response): Promise<string> {
  try {
    const payload: unknown = await response.clone().json();
    if (payload && typeof payload === "object" && "error" in payload) {
      const message = (payload as { error?: unknown }).error;
      if (typeof message === "string") return message;
    }
  } catch {
    // fall through to text
  }
  const text = (await response.text().catch(() => "")).trim();
  return text || response.statusText;
}

function buildInvocationPayload(options: InvokeOptions): InvocationRequest {
  if (!options.agentId === !options.project) {
    throw new Error("exactly one of agentId and project is required");
  }
  const payload: InvocationRequest = { input: options.input };
  if (options.agentId) payload.agent_id = options.agentId;
  if (options.project) payload.project = options.project;
  if (options.conversationId) payload.conversation_id = options.conversationId;
  if (options.attachments?.length) payload.attachments = options.attachments;
  if (options.outputSchema) payload.output_schema = options.outputSchema;
  return payload;
}

export class AgentMuxClient {
  readonly baseUrl: string;
  private readonly token?: string;
  private readonly minVersion?: string;
  private readonly fetchImpl: typeof fetch;
  private readonly extraHeaders: Record<string, string>;

  readonly agents: AgentsResource;
  readonly channels: ChannelsResource;
  readonly triggers: TriggersResource;
  readonly orchestrations: OrchestrationsResource;
  readonly integration: IntegrationResource;
  readonly console: ConsoleResource;
  readonly tenancy: TenancyResource;

  constructor(options: AgentMuxClientOptions = {}) {
    this.baseUrl = (options.baseUrl ?? DEFAULT_BASE_URL).replace(/\/+$/, "");
    this.token = options.token;
    this.minVersion = options.minVersion;
    this.fetchImpl = options.fetch ?? globalThis.fetch.bind(globalThis);
    this.extraHeaders = options.headers ?? {};
    this.agents = new AgentsResource(this);
    this.channels = new ChannelsResource(this);
    this.triggers = new TriggersResource(this);
    this.orchestrations = new OrchestrationsResource(this);
    this.integration = new IntegrationResource(this);
    this.console = new ConsoleResource(this);
    this.tenancy = new TenancyResource(this);
  }

  /** @internal */
  async request(path: string, options: RequestOptions = {}): Promise<Response> {
    const url = new URL(this.baseUrl + path);
    for (const [key, value] of Object.entries(options.query ?? {})) {
      url.searchParams.set(key, value);
    }
    const headers: Record<string, string> = { ...this.extraHeaders };
    if (this.token) headers["Authorization"] = `Bearer ${this.token}`;
    if (options.accept) headers["Accept"] = options.accept;
    let body: string | undefined;
    if (options.body !== undefined) {
      headers["Content-Type"] = "application/json";
      body = JSON.stringify(options.body);
    }
    let response: Response;
    try {
      response = await this.fetchImpl(url.toString(), {
        method: options.method ?? "GET",
        headers,
        body,
        signal: options.signal,
      });
    } catch (cause) {
      if (cause instanceof DOMException && cause.name === "AbortError") throw cause;
      throw new AgentMuxUnreachableError(`AgentMux is unreachable at ${this.baseUrl}`);
    }
    if (!response.ok) {
      throw errorForStatus(response.status, await errorMessage(response));
    }
    return response;
  }

  private async json<T>(path: string, options: RequestOptions = {}): Promise<T> {
    const response = await this.request(path, options);
    return (await response.json()) as T;
  }

  // -- discovery ------------------------------------------------------------

  async capabilities(): Promise<Capabilities> {
    return this.json<Capabilities>("/api/v1/capabilities");
  }

  async status(): Promise<Record<string, unknown>> {
    return this.json<Record<string, unknown>>("/api/v1/status");
  }

  async runtimes(): Promise<string[]> {
    const response = await this.request("/api/v1/agents");
    return ((await response.json()) as string[] | null) ?? [];
  }

  async platforms(): Promise<string[]> {
    const response = await this.request("/api/v1/platforms");
    return ((await response.json()) as string[] | null) ?? [];
  }

  /**
   * Aggregate reachability, auth, contract and version into the unified
   * 5-state machine. Never throws for expected conditions.
   */
  async health(): Promise<HealthReport> {
    let response: Response;
    try {
      response = await this.request("/api/v1/capabilities").catch(async (error: unknown) => {
        if (error instanceof AgentMuxUnreachableError) throw error;
        throw error;
      });
    } catch (error) {
      if (error instanceof AgentMuxUnreachableError) {
        return { state: "unreachable", message: "AgentMux is unreachable" };
      }
      const status = (error as { statusCode?: number }).statusCode;
      if (status === 401) {
        return { state: "unauthorized", message: "AgentMux rejected the bridge token" };
      }
      if (status === 404) {
        return this.legacyHealth();
      }
      return {
        state: "unreachable",
        message: `AgentMux capabilities returned HTTP ${status ?? "error"}`,
      };
    }
    const capabilities = (await response.json()) as Capabilities;
    const major = contractMajor(capabilities.contract_version);
    if (major !== null && major !== SUPPORTED_CONTRACT_MAJOR) {
      return {
        state: "incompatible",
        message:
          `AgentMux speaks contract ${capabilities.contract_version}; ` +
          `this SDK supports major ${SUPPORTED_CONTRACT_MAJOR}`,
        version: capabilities.version,
        contractVersion: capabilities.contract_version,
        capabilities,
      };
    }
    if (this.minVersion && versionKey(capabilities.version) && versionKey(this.minVersion)) {
      if (compareVersions(capabilities.version, this.minVersion) < 0) {
        return {
          state: "incompatible",
          message: `AgentMux ${this.minVersion} or newer is required`,
          version: capabilities.version,
          contractVersion: capabilities.contract_version,
          capabilities,
        };
      }
    }
    return {
      state: "ready",
      message: "AgentMux is ready",
      version: capabilities.version,
      contractVersion: capabilities.contract_version,
      capabilities,
    };
  }

  /** Health for pre-capabilities servers (contract < 1.0). */
  private async legacyHealth(): Promise<HealthReport> {
    try {
      const status = await this.json<{ ok?: boolean; version?: string }>("/api/v1/status");
      if (status.ok !== true) {
        return { state: "unreachable", message: "AgentMux status response is not healthy" };
      }
      for (const path of ["/api/v1/agent-instances", "/api/v1/channels"]) {
        const payload = await this.json<unknown>(path);
        if (!Array.isArray(payload)) {
          return {
            state: "incompatible",
            message: "AgentMux list contract mismatch",
            version: status.version,
          };
        }
      }
      if (this.minVersion && compareVersions(status.version ?? "", this.minVersion) < 0) {
        return {
          state: "incompatible",
          message: `AgentMux ${this.minVersion} or newer is required`,
          version: status.version,
        };
      }
      return { state: "ready", message: "AgentMux is ready", version: status.version };
    } catch (error) {
      if ((error as { statusCode?: number }).statusCode === 401) {
        return { state: "unauthorized", message: "AgentMux rejected the bridge token" };
      }
      return { state: "unreachable", message: "AgentMux API check failed" };
    }
  }

  // -- invocations ----------------------------------------------------------

  async invoke(options: InvokeOptions): Promise<InvocationResult> {
    return this.json<InvocationResult>("/api/v1/invocations", {
      method: "POST",
      body: buildInvocationPayload(options),
      signal: options.signal,
    });
  }

  /**
   * Stream invocation events over SSE. The `text` of `output`/`thinking`
   * events is a full snapshot: replace previous text instead of appending.
   * Keepalive comments are filtered out by the SDK.
   */
  async *invokeStream(options: InvokeOptions): AsyncGenerator<InvocationEvent> {
    const response = await this.request("/api/v1/invocations/stream", {
      method: "POST",
      body: buildInvocationPayload(options),
      accept: "text/event-stream",
      signal: options.signal,
    });
    if (!response.body) {
      throw new AgentMuxUnreachableError("invocation stream has no response body");
    }
    for await (const payload of iterSSEPayloads(response.body)) {
      yield payload as unknown as InvocationEvent;
    }
  }

  // -- messaging & usage ------------------------------------------------------

  async send(options: {
    text: string;
    project?: string;
    channelId?: string;
    conversationKey?: string;
    images?: string[];
    files?: string[];
  }): Promise<void> {
    const body: Record<string, unknown> = { text: options.text };
    if (options.project) body.project = options.project;
    if (options.channelId) body.channel_id = options.channelId;
    if (options.conversationKey) body.conversation_key = options.conversationKey;
    if (options.images?.length) body.images = options.images;
    if (options.files?.length) body.files = options.files;
    await this.request("/api/v1/send", { method: "POST", body });
  }

  async usage(options: { period?: string; from?: string; to?: string } = {}): Promise<
    Record<string, unknown>
  > {
    const query: Record<string, string> = { period: options.period ?? "daily" };
    if (options.from) query.from = options.from;
    if (options.to) query.to = options.to;
    return this.json<Record<string, unknown>>("/api/v1/usage", { query });
  }
}

class AgentsResource {
  constructor(private readonly client: AgentMuxClient) {}

  async list(): Promise<AgentInstance[]> {
    const response = await this.client.request("/api/v1/agent-instances");
    return ((await response.json()) as AgentInstance[] | null) ?? [];
  }

  async upsert(agent: AgentInstance): Promise<AgentInstance> {
    const response = await this.client.request("/api/v1/agent-instances", {
      method: "POST",
      body: agent,
    });
    return (await response.json()) as AgentInstance;
  }

  async delete(agentId: string): Promise<void> {
    await this.client.request("/api/v1/agent-instances", {
      method: "DELETE",
      query: { id: agentId },
    });
  }
}

class ChannelsResource {
  constructor(private readonly client: AgentMuxClient) {}

  async list(): Promise<Channel[]> {
    const response = await this.client.request("/api/v1/channels");
    return ((await response.json()) as Channel[] | null) ?? [];
  }

  async upsert(channel: Channel): Promise<Channel> {
    const response = await this.client.request("/api/v1/channels", {
      method: "POST",
      body: channel,
    });
    return (await response.json()) as Channel;
  }

  async delete(channelId: string): Promise<void> {
    await this.client.request("/api/v1/channels", { method: "DELETE", query: { id: channelId } });
  }

  async restart(channelId: string): Promise<void> {
    await this.client.request("/api/v1/channels/restart", {
      method: "POST",
      query: { id: channelId },
    });
  }
}

class TriggersResource {
  constructor(private readonly client: AgentMuxClient) {}

  async list(): Promise<Trigger[]> {
    const response = await this.client.request("/api/v1/triggers");
    return ((await response.json()) as Trigger[] | null) ?? [];
  }

  async upsert(trigger: Trigger): Promise<Trigger> {
    const response = await this.client.request("/api/v1/triggers", {
      method: "POST",
      body: trigger,
    });
    return (await response.json()) as Trigger;
  }

  async delete(triggerId: string): Promise<void> {
    await this.client.request("/api/v1/triggers", { method: "DELETE", query: { id: triggerId } });
  }

  async run(triggerId: string): Promise<void> {
    await this.client.request("/api/v1/triggers/run", {
      method: "POST",
      query: { id: triggerId },
    });
  }
}

class OrchestrationsResource {
  constructor(private readonly client: AgentMuxClient) {}

  async create(
    tasks: OrchestrationTask[],
    options: { name?: string; maxConcurrency?: number } = {},
  ): Promise<Orchestration> {
    const body: Record<string, unknown> = { tasks };
    if (options.name) body.name = options.name;
    if (options.maxConcurrency) body.max_concurrency = options.maxConcurrency;
    const response = await this.client.request("/api/v1/orchestrations", {
      method: "POST",
      body,
    });
    return (await response.json()) as Orchestration;
  }

  async get(orchestrationId: string): Promise<Orchestration> {
    const response = await this.client.request("/api/v1/orchestrations", {
      query: { id: orchestrationId },
    });
    return (await response.json()) as Orchestration;
  }

  async list(options: { active?: boolean; limit?: number } = {}): Promise<Orchestration[]> {
    const query: Record<string, string> = {};
    if (options.active) query.active = "true";
    if (options.limit) query.limit = String(options.limit);
    const response = await this.client.request("/api/v1/orchestrations", { query });
    return ((await response.json()) as Orchestration[] | null) ?? [];
  }

  async cancel(orchestrationId: string): Promise<void> {
    await this.client.request("/api/v1/orchestrations/cancel", {
      method: "POST",
      body: { id: orchestrationId },
    });
  }
}

class IntegrationResource {
  constructor(private readonly client: AgentMuxClient) {}

  /** Compose the tenant-scoped capability surface used by a host BFF. */
  async snapshot(options: { orchestrationLimit?: number } = {}): Promise<IntegrationSnapshot> {
    const [capabilities, identity, runtimes, platforms, agents, channels, triggers, orchestrations] =
      await Promise.all([
        this.client.capabilities(),
        this.client.tenancy.self(),
        this.client.runtimes(),
        this.client.platforms(),
        this.client.agents.list(),
        this.client.channels.list(),
        this.client.triggers.list(),
        this.client.orchestrations.list({ limit: options.orchestrationLimit ?? 8 }),
      ]);
    return { capabilities, identity, runtimes, platforms, agents, channels, triggers, orchestrations };
  }
}

class ConsoleResource {
  constructor(private readonly client: AgentMuxClient) {}

  /**
   * Mint a one-time Console entry URL (requires a bearer token; backend only).
   *
   * The session inherits this client's scope: a tenant token yields a Console
   * that shows only that application's resources.
   */
  async createSession(options: { landing?: string } = {}): Promise<ConsoleSession> {
    const query = options.landing ? { landing: options.landing } : undefined;
    const response = await this.client.request("/api/v1/console/sessions", {
      method: "POST",
      query,
    });
    return (await response.json()) as ConsoleSession;
  }
}

class TenancyResource {
  constructor(private readonly client: AgentMuxClient) {}

  /** Read back this credential's identity and scope. */
  async self(): Promise<TenancySelf> {
    const response = await this.client.request("/api/v1/tenancy/self");
    return (await response.json()) as TenancySelf;
  }

  async register(name: string, kind: Tenant["kind"] = "app"): Promise<TenantRegistration> {
    const response = await this.client.request("/api/v1/tenancy/register", {
      method: "POST",
      body: { name, kind },
    });
    return (await response.json()) as TenantRegistration;
  }
}
