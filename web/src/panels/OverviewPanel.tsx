import {
  Bot,
  Boxes,
  CheckCircle2,
  CircleDollarSign,
  Clock3,
  ShieldCheck,
  Package,
} from "lucide-react";
import { useMemo, type ReactNode } from "react";
import { api } from "../api";
import { formatUsageCost, validCNYRate } from "../currency";
import { useI18n } from "../i18n";
import { useAsync } from "../useAsync";

export function OverviewPanel() {
  const { language, t } = useI18n();
  const today = formatLocalDate(new Date());
  const status = useAsync(() => api.status(), []);
  const usage = useAsync(() => api.usage("daily", today, today), [today]);
  const currencyPreferences = useAsync(() => api.menubarSettings(), []);
  const providers = useAsync(() => api.providers(), []);
  const policies = useAsync(() => api.guardPolicies(), []);

  const currency = currencyPreferences.data?.currency ?? "cny";
  const cnyRate = validCNYRate(currencyPreferences.data?.cny_rate ?? 7);
  const providerRows = providers.data ?? [];
  const policyRows = policies.data ?? [];
  const totals = usage.data?.totals;
  const totalTokens = totals
    ? totals.input_tokens + totals.output_tokens + totals.cache_read_tokens + totals.cache_write_tokens
    : 0;

  const updatedAt = useMemo(
    () => new Date().toLocaleTimeString(),
    [status.data, usage.data, providers.data, policies.data],
  );

  return (
    <div className="page-stack">
      <div className="metrics-grid">
        <Metric
          icon={<Package size={21} />}
          label={t("overview.daemon")}
          value={status.data?.ok ? t("overview.running") : status.error ? t("overview.down") : t("common.loading")}
        />
        <Metric
          icon={<Boxes size={21} />}
          label={t("overview.projects")}
          value={String(status.data?.projects ?? 0)}
        />
        <Metric
          icon={<Bot size={21} />}
          label={t("overview.version")}
          value={status.data?.version ? `v${status.data.version}` : "—"}
        />
        <Metric
          icon={<CircleDollarSign size={21} />}
          label={t("overview.cost")}
          value={totals ? formatUsageCost(totals.cost_usd, currency, cnyRate, language) : "—"}
        />
        <Metric
          icon={<ShieldCheck size={21} />}
          label={t("overview.records")}
          value={totals ? fmt(totals.records) : "—"}
        />
      </div>

      <div className="content-grid">
        <section className="surface">
          <div className="surface-header">
            <h2>{t("overview.todayUsage")}</h2>
          </div>
          <div className="surface-body">
            <div className="chart-summary">
              <div className="chart-total">{fmt(totalTokens)}</div>
              <div className="trend">{t("overview.tokens")}</div>
            </div>
            <div className="mini-stats">
              <Stat label={t("overview.input")} value={fmt(totals?.input_tokens ?? 0)} />
              <Stat label={t("overview.output")} value={fmt(totals?.output_tokens ?? 0)} />
            </div>
          </div>
        </section>

        <section className="surface">
          <div className="surface-header">
            <h2>{t("overview.providerHealth")}</h2>
          </div>
          <div className="table-wrap">
            <table>
              <thead>
                <tr>
                  <th>{t("overview.provider")}</th>
                  <th>{t("overview.agent")}</th>
                  <th>{t("overview.model")}</th>
                  <th>{t("common.status")}</th>
                </tr>
              </thead>
              <tbody>
                {providerRows.slice(0, 8).map((provider) => (
                  <tr key={provider.id}>
                    <td>
                      <span className="provider-name">
                        <span className="provider-icon">{provider.name.slice(0, 1).toUpperCase()}</span>
                        {provider.name}
                      </span>
                    </td>
                    <td>{(typeof provider.meta?.api_format === "string" && provider.meta.api_format) || "—"}</td>
                    <td className="mono">{provider.model || "—"}</td>
                    <td>
                      <span className={provider.enabled ? "status-badge success" : "status-badge warning"}>
                        <span className="status-dot" />
                        {provider.enabled ? t("common.operational") : t("common.idle")}
                      </span>
                    </td>
                  </tr>
                ))}
                {providerRows.length === 0 && (
                  <tr>
                    <td colSpan={4} className="empty-state">
                      {t("overview.noRoutes")}
                    </td>
                  </tr>
                )}
              </tbody>
            </table>
          </div>
        </section>
      </div>

      <section className="surface">
        <div className="surface-header">
          <h2>{t("overview.policyDecisions")}</h2>
        </div>
        <div className="table-wrap">
          <table>
            <thead>
              <tr>
                <th>{t("overview.policy")}</th>
                <th>{t("overview.scope")}</th>
                <th>{t("overview.decision")}</th>
                <th>{t("overview.priority")}</th>
              </tr>
            </thead>
            <tbody>
              {policyRows.slice(0, 6).map((policy) => (
                <tr key={policy.id}>
                  <td>{policy.tool}</td>
                  <td>{policy.action || "*"}</td>
                  <td>{decisionBadge(policy.decision, t)}</td>
                  <td className="mono">{policy.priority}</td>
                </tr>
              ))}
              {policyRows.length === 0 && (
                <tr>
                  <td colSpan={4} className="empty-state">
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
          {t("common.updated")} {updatedAt}
        </span>
      </div>
    </div>
  );
}

function Metric({ icon, label, value }: { icon: ReactNode; label: string; value: string }) {
  return (
    <div className="metric-card">
      <div className="metric-icon">{icon}</div>
      <div>
        <div className="label">{label}</div>
        <div className="value">{value}</div>
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

export function fmt(n: number): string {
  if (n >= 1e9) return (n / 1e9).toFixed(2) + "B";
  if (n >= 1e6) return (n / 1e6).toFixed(2) + "M";
  if (n >= 1e3) return (n / 1e3).toFixed(2) + "K";
  return String(n);
}

function formatLocalDate(value: Date) {
  const year = value.getFullYear();
  const month = String(value.getMonth() + 1).padStart(2, "0");
  const day = String(value.getDate()).padStart(2, "0");
  return `${year}-${month}-${day}`;
}
