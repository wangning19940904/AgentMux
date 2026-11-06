import type { FrameworkAuthStatus, FrameworkSpec } from "../../api";

const companies: Record<string, string> = {
  claudecode: "Anthropic",
  codex: "OpenAI",
  cursor: "Cursor",
  gemini: "Google",
  qoder: "Alibaba",
  opencode: "Anomaly",
  traecli: "ByteDance",
  iflow: "iFlow AI",
  kimi: "Moonshot AI",
  deepagents: "LangChain",
};

const browserLoginFrameworks = new Set(["claudecode", "codex", "cursor", "traecli"]);

export function frameworkCompanyName(spec: FrameworkSpec) {
  return spec.company?.trim() || companies[spec.kind] || spec.display;
}

export function frameworkSupportsLogin(spec: FrameworkSpec, auth?: FrameworkAuthStatus) {
  return Boolean(auth?.login_supported || browserLoginFrameworks.has(spec.kind));
}

export interface FrameworkAccountSummary {
  /** The most specific identity available: email, then user ID, then name. */
  identity: string;
  /** Short qualifiers shown next to the identity (plan, organization). */
  details: string[];
  /** Every known field, for the hover tooltip. */
  fields: { key: FrameworkAccountFieldKey; value: string }[];
}

export type FrameworkAccountFieldKey = "email" | "user_id" | "name" | "organization" | "plan" | "method" | "region";

const accountFieldOrder: FrameworkAccountFieldKey[] = ["email", "user_id", "name", "organization", "plan", "method", "region"];

export function frameworkAccountSummary(auth?: FrameworkAuthStatus): FrameworkAccountSummary | null {
  if (auth?.state !== "authenticated" || !auth.account) return null;
  const account = auth.account;
  const value = (key: FrameworkAccountFieldKey) => account[key]?.trim() || "";
  const identity = value("email") || value("user_id") || value("name") || value("method");
  if (!identity) return null;
  const plan = formatAccountPlan(value("plan"));
  const organization = value("organization");
  const details = [plan, isPersonalOrganization(organization, account) ? "" : organization, value("region")]
    .filter((detail) => detail && detail !== identity);
  const fields = accountFieldOrder
    .map((key) => ({ key, value: key === "plan" ? plan : value(key) }))
    .filter((field) => field.value);
  return { identity, details, fields };
}

// CLIs report plans as lowercase slugs ("pro", "max"); keep vendor casing otherwise.
function formatAccountPlan(plan: string) {
  return plan && plan === plan.toLowerCase() ? plan.charAt(0).toUpperCase() + plan.slice(1) : plan;
}

// Personal workspaces ("Personal", "dev@example.com's Organization") restate the
// identity, so they stay in the tooltip rather than the visible summary.
function isPersonalOrganization(organization: string, account: NonNullable<FrameworkAuthStatus["account"]>) {
  const normalized = organization.toLowerCase();
  if (!normalized || normalized === "personal") return true;
  return [account.email, account.name, account.user_id]
    .some((owner) => owner && normalized.startsWith(`${owner.toLowerCase()}'s `));
}
