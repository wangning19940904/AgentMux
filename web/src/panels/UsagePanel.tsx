import { useState } from "react";
import { Activity } from "lucide-react";
import { api } from "../api";
import { useI18n } from "../i18n";
import { useAsync } from "../useAsync";
import { fmt } from "./OverviewPanel";

const PERIODS = ["daily", "weekly", "monthly", "session", "blocks"];

export function UsagePanel() {
  const { t } = useI18n();
  const [period, setPeriod] = useState("daily");
  const { data, error, loading } = useAsync(() => api.usage(period), [period]);

  return (
    <div className="page-stack">
      <section className="surface">
        <div className="surface-header">
          <div>
            <h2>{t("usage.title")}</h2>
            <p className="subtle-copy">{t("usage.period")}: {t(`period.${period}`)}</p>
          </div>
          <div className="control-row">
            <a className="ghost-action" href="#observability/traces">
              <Activity size={14} />
              {t("usage.viewTraces")}
            </a>
            <label className="control-row">
              <span className="muted">{t("usage.period")}</span>
              <select value={period} onChange={(e) => setPeriod(e.target.value)}>
                {PERIODS.map((p) => (
                  <option key={p} value={p}>
                    {t(`period.${p}`)}
                  </option>
                ))}
              </select>
            </label>
          </div>
        </div>
      </section>

      {error && <div className="surface surface-body error">{error}</div>}
      {loading && <div className="surface surface-body muted">{t("common.loading")}</div>}

      {data && (
        <>
          <div className="metrics-grid">
            <Stat label={t("usage.cost")} value={`$${data.totals.cost_usd.toFixed(2)}`} />
            <Stat label={t("overview.input")} value={fmt(data.totals.input_tokens)} />
            <Stat label={t("overview.output")} value={fmt(data.totals.output_tokens)} />
            <Stat label={t("usage.cacheRead")} value={fmt(data.totals.cache_read_tokens)} />
            <Stat label={t("overview.records")} value={String(data.totals.records)} />
          </div>

          <section className="surface">
            <div className="surface-header">
              <h2>
                {t("usage.byPeriod")} · {t(`period.${period}`)}
              </h2>
            </div>
            <div className="table-wrap">
              <table>
                <thead>
                  <tr>
                    <th>{t(`period.${period}`)}</th>
                    <th>{t("overview.input")}</th>
                    <th>{t("overview.output")}</th>
                    <th>{t("usage.cost")}</th>
                  </tr>
                </thead>
                <tbody>
                  {data.buckets.map((b) => (
                    <tr key={b.key}>
                      <td>{b.key}</td>
                      <td>{fmt(b.totals.input_tokens)}</td>
                      <td>{fmt(b.totals.output_tokens)}</td>
                      <td>${b.totals.cost_usd.toFixed(2)}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          </section>

          <section className="surface">
            <div className="surface-header">
              <h2>{t("usage.byModel")}</h2>
            </div>
            <div className="table-wrap">
              <table>
                <thead>
                  <tr>
                    <th>{t("usage.model")}</th>
                    <th>{t("usage.tokens")}</th>
                    <th>{t("usage.cost")}</th>
                  </tr>
                </thead>
                <tbody>
                  {data.by_model.map((m) => (
                    <tr key={m.model}>
                      <td>{m.model}</td>
                      <td>{fmt(m.tokens)}</td>
                      <td>${m.cost_usd.toFixed(2)}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          </section>
        </>
      )}
    </div>
  );
}

function Stat({ label, value }: { label: string; value: string }) {
  return (
    <div className="metric-card">
      <div>
        <div className="label">{label}</div>
        <div className="value">{value}</div>
      </div>
    </div>
  );
}
