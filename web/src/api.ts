// REST data layer. cc-switch used Tauri IPC (invoke); here we talk to the Go
// daemon's HTTP API so the same React UI serves both the WebUI and the Wails
// desktop shell.

export interface Provider {
  id: string;
  name: string;
  preset?: string;
  base_url: string;
  api_key_env?: string;
  model?: string;
  tools: string[];
  enabled: boolean;
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

async function get<T>(path: string): Promise<T> {
  const res = await fetch(path);
  if (!res.ok) throw new Error(`${path}: ${res.status}`);
  return res.json() as Promise<T>;
}

async function post<T>(path: string, body: unknown): Promise<T> {
  const res = await fetch(path, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(body),
  });
  if (!res.ok) throw new Error(`${path}: ${res.status}`);
  return res.json() as Promise<T>;
}

async function del<T>(path: string): Promise<T> {
  const res = await fetch(path, { method: "DELETE" });
  if (!res.ok) throw new Error(`${path}: ${res.status}`);
  return res.json() as Promise<T>;
}

export const api = {
  status: () => get<Status>("/api/v1/status"),
  platforms: () => get<string[]>("/api/v1/platforms"),
  agents: () => get<string[]>("/api/v1/agents"),
  providers: () => get<Provider[] | null>("/api/v1/providers"),
  presets: () => get<Provider[]>("/api/v1/providers/presets"),
  upsertProvider: (p: Provider) => post<Provider>("/api/v1/providers", p),
  switchProvider: (id: string, tool: string) =>
    post<{ ok: boolean }>("/api/v1/providers/switch", { id, tool }),
  usage: (period: string) =>
    get<UsageReport>(`/api/v1/usage?period=${encodeURIComponent(period)}`),

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
};
