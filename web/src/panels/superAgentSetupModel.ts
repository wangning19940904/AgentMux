import type { SuperAgentSource } from "../api";
import { githubWebhookPath } from "./superAgentModel";

// Pure helpers behind the trigger setup guide (superAgentSetup.tsx).

export const REDACTED = "<redacted>";

// A provider's display name is the i18n key superAgents.setup.email.provider.<id>.
export type EmailProvider = {
  id: string;
  domains: string[];
  imapHost: string;
  smtpHost: string;
  smtpPort: string;
  // Where the provider issues the password an IMAP client signs in with.
  passwordURL?: string;
};

export const EMAIL_PROVIDERS: EmailProvider[] = [
  { id: "gmail", domains: ["gmail.com", "googlemail.com"], imapHost: "imap.gmail.com", smtpHost: "smtp.gmail.com", smtpPort: "465", passwordURL: "https://myaccount.google.com/apppasswords" },
  { id: "feishu", domains: [], imapHost: "imap.feishu.cn", smtpHost: "smtp.feishu.cn", smtpPort: "465" },
  { id: "lark", domains: [], imapHost: "imap.larksuite.com", smtpHost: "smtp.larksuite.com", smtpPort: "465" },
  { id: "qq", domains: ["qq.com", "foxmail.com"], imapHost: "imap.qq.com", smtpHost: "smtp.qq.com", smtpPort: "465", passwordURL: "https://mail.qq.com" },
  { id: "163", domains: ["163.com"], imapHost: "imap.163.com", smtpHost: "smtp.163.com", smtpPort: "465", passwordURL: "https://mail.163.com" },
  { id: "126", domains: ["126.com"], imapHost: "imap.126.com", smtpHost: "smtp.126.com", smtpPort: "465", passwordURL: "https://mail.126.com" },
  { id: "icloud", domains: ["icloud.com", "me.com", "mac.com"], imapHost: "imap.mail.me.com", smtpHost: "smtp.mail.me.com", smtpPort: "587", passwordURL: "https://account.apple.com" },
];

export function emailProviderByID(id: string): EmailProvider | undefined {
  return EMAIL_PROVIDERS.find((provider) => provider.id === id);
}

export function emailProviderForAddress(address: string): EmailProvider | undefined {
  const domain = address.trim().toLowerCase().split("@")[1] ?? "";
  return EMAIL_PROVIDERS.find((provider) => provider.domains.includes(domain));
}

// The provider a source's saved servers belong to, so a reopened form shows it.
export function emailProviderForConfig(config: Record<string, string>): EmailProvider | undefined {
  const host = (config.imap_host ?? "").trim().toLowerCase();
  return EMAIL_PROVIDERS.find((provider) => provider.imapHost === host);
}

export function emailProviderConfig(provider: EmailProvider): Record<string, string> {
  return { imap_host: provider.imapHost, imap_port: "993", smtp_host: provider.smtpHost, smtp_port: provider.smtpPort };
}

// isReachableOrigin tells whether GitHub could deliver to the page's own
// address: the desktop app's wails:// origin, loopback and private networks
// cannot be reached from the internet.
export function isReachableOrigin(origin: string): boolean {
  let url: URL;
  try {
    url = new URL(origin);
  } catch {
    return false;
  }
  if (url.protocol !== "https:" && url.protocol !== "http:") return false;
  const host = url.hostname.replace(/^\[|\]$/g, "").toLowerCase();
  if (host === "localhost" || host.endsWith(".localhost") || host.endsWith(".local")) return false;
  if (host.includes(":")) return !(host === "::1" || /^(fe80|fc|fd)/.test(host));
  const ipv4 = host.match(/^(\d+)\.(\d+)\.\d+\.\d+$/);
  if (!ipv4) return true;
  const [a, b] = [Number(ipv4[1]), Number(ipv4[2])];
  return !(a === 127 || a === 10 || a === 0 || (a === 172 && b >= 16 && b <= 31) || (a === 192 && b === 168) || (a === 169 && b === 254) || (a === 100 && b >= 64 && b <= 127));
}

export function trimBase(raw: string): string {
  return raw.trim().replace(/\/+$/, "");
}

// The address GitHub should deliver to: the public address the operator gave,
// or the page's own origin when GitHub can reach it.
export function githubPublicBase(source: SuperAgentSource, origin: string): string {
  const configured = trimBase(source.config.public_url ?? "");
  if (configured) return configured;
  return isReachableOrigin(origin) ? origin : "";
}

export const GITHUB_EVENTS = ["issues", "issue_comment", "pull_request", "pull_request_review_comment"];

// The repository's own GitHub site, from the API base for GitHub Enterprise.
export function githubWebBase(apiBase: string): string {
  const base = trimBase(apiBase);
  if (!base || base === "https://api.github.com") return "https://github.com";
  return base.replace(/\/api\/v3$/, "");
}

export function githubHost(apiBase: string): string {
  return new URL(githubWebBase(apiBase)).host;
}

export function isRepoName(repo: string): boolean {
  return /^[\w.-]+\/[\w.-]+$/.test(repo.trim());
}

export function githubHookSettingsURL(repo: string, apiBase: string): string {
  return `${githubWebBase(apiBase)}/${repo.trim()}/settings/hooks/new`;
}

// A classic token with the scope to comment on Issues and pull requests. Fine-
// grained tokens cannot reach a personal repository the agent only collaborates
// on, so the classic one is the one that always works.
export function githubTokenURL(apiBase: string): string {
  const params = new URLSearchParams({ scopes: "repo", description: "AgentMux Super Agent" });
  return `${githubWebBase(apiBase)}/settings/tokens/new?${params.toString()}`;
}

export function tokenCanComment(scopes: string[]): boolean {
  return scopes.includes("repo") || scopes.includes("public_repo");
}

// The GitHub CLI's forwarder relays only GitHub's deliveries for one
// repository, so a local install never has to expose its whole port.
// maskSecret is for showing the command; the copy keeps the real secret.
export function githubForwardCommand(source: SuperAgentSource, localURL: string, maskSecret = false): string {
  const repo = source.config.repo?.trim() || "OWNER/REPO";
  const secret = source.config.webhook_secret ?? "";
  const known = secret !== "" && secret !== REDACTED;
  const args = [
    "gh webhook forward",
    `--repo=${repo}`,
    `--events=${GITHUB_EVENTS.join(",")}`,
    `--url=${trimBase(localURL)}${githubWebhookPath(source)}`,
    `--secret=${!known ? "YOUR_WEBHOOK_SECRET" : maskSecret ? "•".repeat(8) : secret}`,
  ];
  const host = githubHost(source.config.api_base ?? "");
  if (host !== "github.com") args.push(`--github-host=${host}`);
  return args.join(" ");
}
