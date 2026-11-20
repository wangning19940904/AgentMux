import type { UsageTotals } from "../../api";

// formatTokens abbreviates a token count. Averages and merged buckets can be
// fractional; a count below a thousand is shown whole.
export function formatTokens(value: number): string {
  if (!Number.isFinite(value)) return "—";
  if (value >= 1e9) return (value / 1e9).toFixed(2) + "B";
  if (value >= 1e6) return (value / 1e6).toFixed(2) + "M";
  if (value >= 1e3) return (value / 1e3).toFixed(2) + "K";
  return String(Math.round(value));
}

export function totalTokens(totals: Pick<UsageTotals, "input_tokens" | "output_tokens" | "cache_read_tokens" | "cache_write_tokens">) {
  return totals.input_tokens + totals.output_tokens + totals.cache_read_tokens + totals.cache_write_tokens;
}

export function emptyTotals(): UsageTotals {
  return {
    input_tokens: 0, output_tokens: 0, cache_read_tokens: 0, cache_write_tokens: 0,
    cost_usd: 0, records: 0, sessions: 0, estimated_tokens: 0, estimated_records: 0,
  };
}

// formatShare never rounds a non-zero share down to "0%".
export function formatShare(part: number, whole: number): string {
  if (whole <= 0 || part <= 0) return "0%";
  const percent = (part / whole) * 100;
  if (percent < 1) return "<1%";
  return `${Math.round(percent)}%`;
}

// The agent dimension mixes AgentMux agents ("agent:<id>"), channels
// ("channel:<id>") and the working directories of transcript records.
export type AgentNames = { agents: Map<string, string>; channels: Map<string, string> };

export function agentLabel(key: string, names: AgentNames, t: (key: string) => string): string {
  if (key === "unknown" || !key) return t("usage.unknownAgent");
  if (key === "(other)") return t("usage.otherAgents");
  if (key === "(temporary directory)") return t("usage.temporaryAgent");
  if (key.startsWith("agent:")) {
    const id = key.slice("agent:".length);
    return names.agents.get(id) ?? id;
  }
  if (key.startsWith("channel:")) {
    const id = key.slice("channel:".length);
    return `${t("usage.channelPrefix")} ${names.channels.get(id) ?? id}`;
  }
  return projectLabel(key);
}

// projectLabel shortens a working directory to the part that identifies it.
// The full key stays available as the element's title.
export function projectLabel(key: string): string {
  const codexWorktree = "/.codex/worktrees/*/";
  const codexIndex = key.indexOf(codexWorktree);
  if (codexIndex >= 0) return key.slice(codexIndex + codexWorktree.length);
  if (key.startsWith("/")) {
    const parts = key.split("/").filter(Boolean);
    return parts.slice(-2).join("/") || key;
  }
  // Claude Code replaces every separator in the directory with "-", which
  // cannot be undone; drop the home prefix it always starts with.
  const claude = key.match(/^-(?:Users|home)-[^-]+-(.+)$/);
  if (claude) return claude[1];
  return key;
}
