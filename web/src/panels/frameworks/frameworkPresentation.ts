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
