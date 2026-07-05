import {
  ArrowUpRight,
  Bot,
  Boxes,
  CheckCircle2,
  CircleDollarSign,
  Clock3,
  ShieldCheck,
  Package,
} from "lucide-react";
import type { CSSProperties, ReactNode } from "react";
import { api, GuardPolicy, Provider, UsageReport } from "../api";
import { useI18n } from "../i18n";
import { useAsync } from "../useAsync";

const CHART_BARS = [54, 64, 58, 68, 75, 78, 82, 88, 91, 84, 72, 68, 75, 85, 90, 88, 83, 75, 69];

const SAMPLE_USAGE: UsageReport = {
  period: "daily",
  totals: {
    input_tokens: 2450000,
    output_tokens: 1170000,
    cache_read_tokens: 820000,
    cache_write_tokens: 210000,
    cost_usd: 1842.31,
    records: 1240,
  },
  buckets: [],
  by_model: [],
  by_source: [],
};

const SAMPLE_PROVIDERS: Provider[] = [
  { id: "openai", name: "OpenAI", base_url: "", model: "gpt-4o", tools: ["codex"], enabled: true },
  { id: "anthropic", name: "Anthropic", base_url: "", model: "claude-3.5", tools: ["claude"], enabled: true },
  { id: "google", name: "Google", base_url: "", model: "gemini-1.5", tools: ["gemini"], enabled: true },
  { id: "perplexity", name: "Perplexity", base_url: "", model: "sonar", tools: ["search"], enabled: false },
];

const SAMPLE_POLICIES: GuardPolicy[] = [
  { id: "pii", tool: "PII Protection", action: "Input", decision: "deny", priority: 100 },
  { id: "allow", tool: "Tool Allowlist", action: "Tool Call", decision: "allow", priority: 80 },
  { id: "rate", tool: "Rate Limit", action: "Request", decision: "ask", priority: 60 },
];

export function OverviewPanel() {
  const { t } = useI18n();
  const status = useAsync(() => api.status(), []);
  const usage = useAsync(() => api.usage("daily"), []);
  const providers = useAsync(() => api.providers(), []);
  const policies = useAsync(() => api.guardPolicies(), []);

  const usageData = usage.data ?? SAMPLE_USAGE;
  const providerRows = providers.data?.length ? providers.data : SAMPLE_PROVIDERS;
  const policyRows = policies.data?.length ? policies.data : SAMPLE_POLICIES;
  const totalTokens =
    usageData.totals.input_tokens + usageData.totals.output_tokens + usageData.totals.cache_read_tokens;

  return (
    <div className="page-stack">
      <div className="metrics-grid">
        <Metric
          icon={<Package size={21} />}
          label={t("overview.daemon")}
          value={status.data?.ok ? t("overview.running") : status.error ? t("overview.down") : t("common.loading")}
          trend={status.data?.ok ? "+ 18.6%" : status.error ? "- 100%" : "+ 0.0%"}
        />
        <Metric
          icon={<Boxes size={21} />}
          label={t("overview.projects")}
          value={String(status.data?.projects ?? 0)}
          trend="+ 24.3%"
        />
        <Metric
          icon={<Bot size={21} />}
          label={t("overview.version")}
          value={status.data?.version ? `v${status.data.version}` : "v0.1.0"}
          trend="+ 16.8%"
        />
        <Metric
          icon={<CircleDollarSign size={21} />}
          label={t("overview.cost")}
          value={`$${usageData.totals.cost_usd.toFixed(2)}`}
          trend="+ 8.2%"
        />
        <Metric
          icon={<ShieldCheck size={21} />}
          label={t("overview.records")}
          value={fmt(usageData.totals.records)}
          trend="+ 6.7%"
        />
      </div>

      <div className="content-grid">
        <section className="surface">
          <div className="surface-header">
            <h2>{t("overview.todayUsage")}</h2>
            <select aria-label={t("overview.tokens")}>
              <option>{t("overview.tokens")}</option>
              <option>USD</option>
            </select>
          </div>
          <div className="surface-body">
            <div className="chart-summary">
              <div className="chart-total">{fmt(totalTokens)}</div>
              <div className="trend">
                <ArrowUpRight size={14} />
                24.3% {t("overview.vsYesterday")}
              </div>
            </div>
            <div className="bar-chart" aria-label={t("overview.todayUsage")}>
              {CHART_BARS.map((height, index) => (
                <span key={index} style={{ "--h": height } as CSSProperties} />
              ))}
            </div>
            <div className="mini-stats">
              <Stat label={t("overview.input")} value={fmt(usageData.totals.input_tokens)} />
              <Stat label={t("overview.output")} value={fmt(usageData.totals.output_tokens)} />
              <Stat label={t("overview.avgLatency")} value="682 ms" />
              <Stat label={t("overview.errorRate")} value="0.37%" />
            </div>
          </div>
        </section>

        <section className="surface">
          <div className="surface-header">
            <h2>{t("overview.activeRoutes")}</h2>
            <button className="ghost-action">{t("common.viewAll")}</button>
          </div>
          <div className="table-wrap">
            <table>
              <thead>
                <tr>
                  <th>{t("overview.route")}</th>
                  <th>{t("overview.agent")}</th>
                  <th>{t("overview.provider")}</th>
                  <th>{t("overview.reqMin")}</th>
                  <th>{t("overview.p95")}</th>
                  <th>{t("common.status")}</th>
                </tr>
              </thead>
              <tbody>
                {providerRows.slice(0, 7).map((provider, index) => (
                  <tr key={`${provider.id}-${index}`}>
                    <td className="route-name">{routeName(provider, index)}</td>
                    <td>{provider.tools[0] || "agent"}</td>
                    <td>{provider.name}</td>
                    <td>{412 - index * 37}</td>
                    <td>{245 + index * 73} ms</td>
                    <td>
                      <span className={provider.enabled ? "status-badge success" : "status-badge warning"}>
                        <span className="status-dot" />
                        {provider.enabled ? t("common.healthy") : t("common.degraded")}
                      </span>
                    </td>
                  </tr>
                ))}
                {providerRows.length === 0 && (
                  <tr>
                    <td colSpan={6} className="empty-state">
                      {t("overview.noRoutes")}
                    </td>
                  </tr>
                )}
              </tbody>
            </table>
          </div>
        </section>

        <section className="surface">
          <div className="surface-header">
            <h2>{t("overview.providerHealth")}</h2>
            <button className="ghost-action">{t("common.viewAll")}</button>
          </div>
          <div className="table-wrap">
            <table>
              <thead>
                <tr>
                  <th>{t("overview.provider")}</th>
                  <th>{t("common.status")}</th>
                  <th>{t("overview.successRate")}</th>
                  <th>{t("overview.p95")}</th>
                </tr>
              </thead>
              <tbody>
                {providerRows.slice(0, 8).map((provider, index) => (
                  <tr key={provider.id}>
                    <td>
                      <span className="provider-name">
                        <span className="provider-icon">{provider.name.slice(0, 1).toUpperCase()}</span>
                        {provider.name}
                      </span>
                    </td>
                    <td>
                      <span className={provider.enabled ? "status-badge success" : "status-badge warning"}>
                        <span className="status-dot" />
                        {provider.enabled ? t("common.operational") : t("common.idle")}
                      </span>
                    </td>
                    <td>{(99.62 - index * 0.18).toFixed(2)}%</td>
                    <td>{245 + index * 61} ms</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
          <div className="surface-footer">
            <span className="status-badge success">
              <span className="status-dot" />
              {t("app.status")}
            </span>
            <button className="ghost-action">{t("common.viewAll")}</button>
          </div>
        </section>
      </div>

      <section className="surface">
        <div className="surface-header">
          <h2>{t("overview.policyDecisions")}</h2>
          <button className="ghost-action">{t("common.viewAll")}</button>
        </div>
        <div className="table-wrap">
          <table>
            <thead>
              <tr>
                <th>Time</th>
                <th>{t("overview.policy")}</th>
                <th>{t("overview.scope")}</th>
                <th>{t("overview.decision")}</th>
                <th>{t("overview.reason")}</th>
                <th>{t("overview.matchedAt")}</th>
                <th>{t("overview.requestId")}</th>
              </tr>
            </thead>
            <tbody>
              {policyRows.slice(0, 6).map((policy, index) => (
                <tr key={policy.id}>
                  <td className="mono">10:2{index}:3{index}</td>
                  <td>{policy.tool}</td>
                  <td>{policy.action || "*"}</td>
                  <td>{decisionBadge(policy.decision, t)}</td>
                  <td className="muted">{reasonFor(policy.decision)}</td>
                  <td>{policy.action || "Request"}</td>
                  <td className="mono">req_01HZ8{index}A9M{index}</td>
                </tr>
              ))}
              {policyRows.length === 0 && (
                <tr>
                  <td colSpan={7} className="empty-state">
                    {t("overview.noPolicies")}
                  </td>
                </tr>
              )}
            </tbody>
          </table>
        </div>
      </section>

      <div className="activity-line">
        <Clock3 size={14} />
        <span>
          {t("common.updated")} 10:24:45 · {t("common.autoRefresh")}
        </span>
      </div>
    </div>
  );
}

function Metric({
  icon,
  label,
  value,
  trend,
}: {
  icon: ReactNode;
  label: string;
  value: string;
  trend: string;
}) {
  const positive = !trend.startsWith("-");
  return (
    <div className="metric-card">
      <div className="metric-icon">{icon}</div>
      <div>
        <div className="label">{label}</div>
        <div className="value">{value}</div>
        <div className="trend" style={{ color: positive ? "var(--green)" : "var(--red)" }}>
          <ArrowUpRight size={14} />
          {trend}
        </div>
      </div>
    </div>
  );
}

function Stat({ label, value }: { label: string; value: string }) {
  return (
    <div className="stat">
      <div className="label">{label}</div>
      <div className="value">{value}</div>
    </div>
  );
}

function routeName(provider: Provider, index: number) {
  const base = provider.id || provider.name.toLowerCase().replace(/\s+/g, "-");
  const suffixes = ["support", "code", "summary", "creative", "embed", "research", "fallback"];
  return `${suffixes[index % suffixes.length]}-${base}`;
}

function decisionBadge(decision: string, t: (key: string) => string) {
  const normalized = decision.toLowerCase();
  const className =
    normalized === "allow" ? "status-badge success" : normalized === "deny" ? "status-badge danger" : "status-badge warning";
  const label =
    normalized === "allow"
      ? t("common.allowed")
      : normalized === "deny"
        ? t("common.blocked")
        : t("common.throttled");
  return (
    <span className={className}>
      <CheckCircle2 size={13} />
      {label}
    </span>
  );
}

function reasonFor(decision: string) {
  const normalized = decision.toLowerCase();
  if (normalized === "allow") return "Policy satisfied";
  if (normalized === "deny") return "Matched a restricted operation";
  return "Requires operator review";
}

export function fmt(n: number): string {
  if (n >= 1e9) return (n / 1e9).toFixed(2) + "B";
  if (n >= 1e6) return (n / 1e6).toFixed(2) + "M";
  if (n >= 1e3) return (n / 1e3).toFixed(2) + "K";
  return String(n);
}
