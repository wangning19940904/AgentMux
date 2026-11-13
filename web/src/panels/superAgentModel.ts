import type { SuperAgent, SuperAgentRouter, SuperAgentSource, SuperAgentSourceKind, SuperAgentWorker } from "../api";

export type SourceField = {
  key: string;
  labelKey: string;
  required?: boolean;
  secret?: boolean;
  multiline?: boolean;
  placeholder?: string;
  channel?: boolean;
  // Picked from the groups the channel's bot is in.
  chats?: "one" | "many";
};

export const SOURCE_KINDS: SuperAgentSourceKind[] = ["feishu_message", "feishu_doc", "email", "github_webhook"];

// Fields mirror what the backend requires for each kind (superagent/config.go).
// A GitHub trigger's token or webhook secret is required by its delivery; see
// requiredKeys.
export const SOURCE_FIELDS: Record<SuperAgentSourceKind, SourceField[]> = {
  feishu_message: [
    { key: "channel_id", labelKey: "superAgents.field.channel", required: true, channel: true },
    { key: "chat_ids", labelKey: "superAgents.field.chatIDs", chats: "many" },
  ],
  feishu_doc: [
    { key: "channel_id", labelKey: "superAgents.field.channel", required: true, channel: true },
    { key: "documents", labelKey: "superAgents.field.documents", required: true, multiline: true, placeholder: "https://example.feishu.cn/docx/…" },
    { key: "notify_chat_id", labelKey: "superAgents.field.notifyChat", chats: "one" },
    { key: "instructions", labelKey: "superAgents.field.instructions", multiline: true },
    { key: "debounce_seconds", labelKey: "superAgents.field.debounce", placeholder: "600" },
  ],
  email: [
    { key: "imap_host", labelKey: "superAgents.field.imapHost", required: true, placeholder: "imap.example.com" },
    { key: "imap_port", labelKey: "superAgents.field.imapPort", placeholder: "993" },
    { key: "username", labelKey: "superAgents.field.username", required: true },
    { key: "password", labelKey: "superAgents.field.password", required: true, secret: true },
    { key: "mailbox", labelKey: "superAgents.field.mailbox", placeholder: "INBOX" },
    { key: "smtp_host", labelKey: "superAgents.field.smtpHost", placeholder: "smtp.example.com" },
    { key: "smtp_port", labelKey: "superAgents.field.smtpPort", placeholder: "465" },
    { key: "from_address", labelKey: "superAgents.field.fromAddress" },
  ],
  // Bound channels are set in the agent's own settings; they have no fields here.
  channel: [],
  github_webhook: [
    { key: "agent_login", labelKey: "superAgents.field.agentLogin", required: true, placeholder: "my-agent-bot" },
    { key: "repo", labelKey: "superAgents.field.repo", placeholder: "owner/name" },
    { key: "webhook_secret", labelKey: "superAgents.field.webhookSecret", secret: true },
    { key: "github_token", labelKey: "superAgents.field.githubToken", secret: true },
    { key: "public_url", labelKey: "superAgents.field.publicURL", placeholder: "https://agentmux.example.com" },
    { key: "api_base", labelKey: "superAgents.field.apiBase", placeholder: "https://api.github.com" },
  ],
};

export function newSuperAgent(): SuperAgent {
  return {
    id: "", name: "", router_agent_id: "", mode: "observe", max_concurrency: 2,
    workers: [], auto_workers: false, worker_runtimes: [], sources: [], enabled: true,
  };
}

// Auto workers are on exactly when at least one worker framework is chosen.
export function withWorkerRuntimes(agent: SuperAgent, runtimes: string[]): SuperAgent {
  return { ...agent, worker_runtimes: runtimes, auto_workers: runtimes.length > 0 };
}

// The router select's value for creating a router agent instead of choosing one.
export const NEW_ROUTER = "__new_router__";

export function newRouter(runtimeID = ""): SuperAgentRouter {
  return { id: "", name: "", runtime_id: runtimeID, model: "", reasoning_effort: "", service_tier: "", approval_mode: "", mcp_servers: [] };
}

export function newWorker(): SuperAgentWorker {
  return { id: "", agent_id: "" };
}

// A new Source gets its id now, not on save, so the setup guide can show its
// webhook address while it is being configured. The backend keeps a given id.
export function newSourceID(): string {
  return `src_${crypto.randomUUID()}`;
}

// A webhook secret nobody has to invent: 32 random bytes as hex.
export function generateSecret(): string {
  const bytes = crypto.getRandomValues(new Uint8Array(32));
  return Array.from(bytes, (byte) => byte.toString(16).padStart(2, "0")).join("");
}

// A GitHub trigger reads the agent's notifications unless GitHub can reach this
// page, in which case a webhook answers sooner. The secret is made either way,
// so switching to a webhook needs nothing new.
export function newSource(kind: SuperAgentSourceKind, reachableByGitHub = false): SuperAgentSource {
  const config: Record<string, string> = kind === "github_webhook"
    ? { delivery: reachableByGitHub ? "webhook" : "poll", webhook_secret: generateSecret() }
    : {};
  return { id: newSourceID(), kind, enabled: true, allowed_actors: [], config };
}

export function githubPolls(source: SuperAgentSource): boolean {
  return source.kind === "github_webhook" && source.config.delivery === "poll";
}

export function requiredKeys(source: SuperAgentSource): string[] {
  const keys = SOURCE_FIELDS[source.kind].filter((field) => field.required).map((field) => field.key);
  if (source.kind === "github_webhook") keys.push(githubPolls(source) ? "github_token" : "webhook_secret");
  return keys;
}

export function sourceField(kind: SuperAgentSourceKind, key: string): SourceField {
  const field = SOURCE_FIELDS[kind].find((item) => item.key === key);
  if (!field) throw new Error(`unknown ${kind} field ${key}`);
  return field;
}

export function parseActors(value: string): string[] {
  return value.split(/[\s,]+/).map((item) => item.trim()).filter(Boolean);
}

export function isFeishuSource(kind: SuperAgentSourceKind) {
  return kind === "feishu_message" || kind === "feishu_doc";
}

// ownsBrain reports whether an agent answers with its own Agent instance, made
// with it, rather than with a router agent.
export function ownsBrain(agent: SuperAgent): boolean {
  return Boolean(agent.id) && agent.router_agent_id === agent.id;
}

// A channel source is a channel bound to the agent's brain: read-only here.
export function isChannelSource(source: SuperAgentSource): boolean {
  return source.kind === "channel";
}

// canStartWorkers reports whether an agent has a worker policy. Without one it
// only chats.
export function canStartWorkers(agent: SuperAgent): boolean {
  return Boolean(agent.auto_workers) || agent.workers.length > 0;
}

// routerDraft is a router agent the form creates or changes when it saves.
export function superAgentProblems(agent: SuperAgent, routerDraft?: SuperAgentRouter | null): string[] {
  const problems: string[] = [];
  if (routerDraft) {
    if (!routerDraft.runtime_id) problems.push("superAgents.problem.routerFramework");
  } else if (!agent.router_runtime && !agent.router_agent_id) {
    problems.push("superAgents.problem.router");
  }
  // An agent with its own brain may only chat; a router needs someone to work.
  if (!canStartWorkers(agent) && !ownsBrain(agent)) problems.push("superAgents.problem.noWorkers");
  if (agent.workers.some((worker) => !worker.agent_id)) problems.push("superAgents.problem.worker");
  for (const source of agent.sources.filter((item) => !isChannelSource(item))) {
    if (source.enabled && source.allowed_actors.length === 0) problems.push("superAgents.problem.actors");
    if (source.allowed_actors.includes("*") && source.kind !== "feishu_message") problems.push("superAgents.problem.wildcard");
    if (requiredKeys(source).some((key) => !source.config[key]?.trim())) {
      problems.push("superAgents.problem.sourceField");
    }
  }
  return [...new Set(problems)];
}

export function githubWebhookPath(source: SuperAgentSource): string {
  return `/hook/super-agent/github/${source.id}`;
}
