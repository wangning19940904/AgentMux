import type { SuperAgent, SuperAgentSource, SuperAgentSourceKind, SuperAgentWorker } from "../api";

export type SourceField = {
  key: string;
  labelKey: string;
  required?: boolean;
  secret?: boolean;
  multiline?: boolean;
  placeholder?: string;
  channel?: boolean;
};

export const SOURCE_KINDS: SuperAgentSourceKind[] = ["feishu_message", "feishu_doc", "email", "github_webhook"];

// Fields mirror what the backend requires for each kind (superagent/config.go).
export const SOURCE_FIELDS: Record<SuperAgentSourceKind, SourceField[]> = {
  feishu_message: [
    { key: "channel_id", labelKey: "superAgents.field.channel", required: true, channel: true },
    { key: "chat_ids", labelKey: "superAgents.field.chatIDs", placeholder: "oc_xxx, oc_yyy" },
  ],
  feishu_doc: [
    { key: "channel_id", labelKey: "superAgents.field.channel", required: true, channel: true },
    { key: "documents", labelKey: "superAgents.field.documents", required: true, multiline: true, placeholder: "https://example.feishu.cn/docx/…" },
    { key: "notify_chat_id", labelKey: "superAgents.field.notifyChat", placeholder: "oc_xxx" },
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
  github_webhook: [
    { key: "agent_login", labelKey: "superAgents.field.agentLogin", required: true, placeholder: "my-agent-bot" },
    { key: "repo", labelKey: "superAgents.field.repo", placeholder: "owner/name" },
    { key: "webhook_secret", labelKey: "superAgents.field.webhookSecret", required: true, secret: true },
    { key: "github_token", labelKey: "superAgents.field.githubToken", secret: true },
    { key: "api_base", labelKey: "superAgents.field.apiBase", placeholder: "https://api.github.com" },
  ],
};

export function newSuperAgent(): SuperAgent {
  return {
    id: "", name: "", router_agent_id: "", mode: "observe", max_concurrency: 2,
    workers: [], auto_workers: true, sources: [], enabled: true,
  };
}

export function newWorker(): SuperAgentWorker {
  return { id: "", agent_id: "" };
}

export function newSource(kind: SuperAgentSourceKind): SuperAgentSource {
  return { id: "", kind, enabled: true, allowed_actors: [], config: {} };
}

export function parseActors(value: string): string[] {
  return value.split(/[\s,]+/).map((item) => item.trim()).filter(Boolean);
}

export function superAgentProblems(agent: SuperAgent): string[] {
  const problems: string[] = [];
  if (!agent.router_runtime && !agent.router_agent_id) problems.push("superAgents.problem.router");
  if (agent.workers.length === 0 && !agent.auto_workers) problems.push("superAgents.problem.noWorkers");
  if (agent.workers.some((worker) => !worker.agent_id)) problems.push("superAgents.problem.worker");
  for (const source of agent.sources) {
    if (source.enabled && source.allowed_actors.length === 0) problems.push("superAgents.problem.actors");
    if (source.allowed_actors.includes("*") && source.kind !== "feishu_message") problems.push("superAgents.problem.wildcard");
    if (SOURCE_FIELDS[source.kind].some((field) => field.required && !source.config[field.key]?.trim())) {
      problems.push("superAgents.problem.sourceField");
    }
  }
  return [...new Set(problems)];
}

export function githubWebhookPath(source: SuperAgentSource): string {
  return `/hook/super-agent/github/${source.id}`;
}
