import { useEffect, useState } from "react";
import { Activity } from "lucide-react";
import { api, MenubarSettings } from "../api";
import { formatUsageCost, SupportedCurrency, validCNYRate } from "../currency";
import { useI18n } from "../i18n";
import { useAsync } from "../useAsync";
import { fmt } from "./OverviewPanel";

const PERIODS = ["daily", "weekly", "monthly", "session", "blocks"];
const RANGES = ["all", "today", "7d", "month", "custom"] as const;
type UsageRange = (typeof RANGES)[number];

export function UsagePanel() {
  const { language, t } = useI18n();
  const [period, setPeriod] = useState("daily");
  const [range, setRange] = useState<UsageRange>("all");
  const today = formatLocalDate(new Date());
  const [customFrom, setCustomFrom] = useState(today);
  const [customTo, setCustomTo] = useState(today);
  const bounds = usageRangeBounds(range, customFrom, customTo);
  const { data, error, loading } = useAsync(
    () => api.usage(period, bounds.from, bounds.to),
    [period, bounds.from, bounds.to],
  );
  const currencyPreferences = useAsync(() => api.menubarSettings(), []);
  const [preferences, setPreferences] = useState<MenubarSettings | null>(null);
  const [currencyStatus, setCurrencyStatus] = useState<"idle" | "saving" | "saved" | "error">("idle");

  useEffect(() => {
    if (currencyPreferences.data) setPreferences(currencyPreferences.data);
  }, [currencyPreferences.data]);

  const currency = preferences?.currency ?? "cny";
  const cnyRate = validCNYRate(preferences?.cny_rate ?? 7);
  const formatCost = (costUSD: number) => formatUsageCost(costUSD, currency, cnyRate, language);

  const updateCurrencyPreferences = (patch: Partial<MenubarSettings>) => {
    setPreferences((current) => (current ? { ...current, ...patch } : current));
    setCurrencyStatus("idle");
  };

  const saveCurrencyPreferences = async () => {
    if (!preferences) return;
    setCurrencyStatus("saving");
    try {
      const saved = await api.saveMenubarSettings({
        ...preferences,
        cny_rate: validCNYRate(preferences.cny_rate),
      });
      setPreferences(saved);
      setCurrencyStatus("saved");
    } catch {
      setCurrencyStatus("error");
    }
  };

  const updateCustomFrom = (value: string) => {
    setCustomFrom(value);
    if (customTo && value > customTo) setCustomTo(value);
  };

  const updateCustomTo = (value: string) => {
    setCustomTo(value);
    if (customFrom && value < customFrom) setCustomFrom(value);
  };

  return (
    <div className="page-stack">
      <section className="surface">
        <div className="surface-header">
          <div>
            <h2>{t("usage.title")}</h2>
            <p className="subtle-copy">
              {t("usage.range")}: {t(`range.${range}`)} · {t("usage.groupBy")}: {t(`period.${period}`)}
            </p>
          </div>
          <div className="control-row usage-controls">
            <a className="ghost-action" href="#observability/traces">
              <Activity size={14} />
              {t("usage.viewTraces")}
            </a>
            <label className="control-row">
              <span className="muted">{t("usage.range")}</span>
              <select value={range} onChange={(e) => setRange(e.target.value as UsageRange)}>
                {RANGES.map((value) => (
                  <option key={value} value={value}>
                    {t(`range.${value}`)}
                  </option>
                ))}
              </select>
            </label>
            {range === "custom" && (
              <>
                <label className="control-row">
                  <span className="muted">{t("usage.from")}</span>
                  <input type="date" value={customFrom} onChange={(e) => updateCustomFrom(e.target.value)} />
                </label>
                <label className="control-row">
                  <span className="muted">{t("usage.to")}</span>
                  <input type="date" value={customTo} onChange={(e) => updateCustomTo(e.target.value)} />
                </label>
              </>
            )}
            <label className="control-row">
              <span className="muted">{t("usage.groupBy")}</span>
              <select value={period} onChange={(e) => setPeriod(e.target.value)}>
                {PERIODS.map((p) => (
                  <option key={p} value={p}>
                    {t(`period.${p}`)}
                  </option>
                ))}
              </select>
            </label>
            <label className="control-row">
              <span className="muted">{t("usage.currency")}</span>
              <select
                value={currency}
                disabled={!preferences}
                onChange={(e) =>
                  updateCurrencyPreferences({ currency: e.target.value as SupportedCurrency })
                }
              >
                <option value="cny">{t("currency.cny")}</option>
                <option value="usd">{t("currency.usd")}</option>
              </select>
            </label>
            <label className="control-row">
              <span className="muted">{t("usage.cnyRate")}</span>
              <input
                className="usage-rate-input"
                type="number"
                min="0.01"
                step="0.01"
                value={preferences?.cny_rate ?? 7}
                disabled={!preferences}
                onChange={(e) => updateCurrencyPreferences({ cny_rate: Number(e.target.value) })}
              />
            </label>
            <button
              className="ghost-action"
              type="button"
              disabled={!preferences || currencyStatus === "saving"}
              onClick={() => void saveCurrencyPreferences()}
            >
              {currencyStatus === "saving" ? "…" : t("common.save")}
            </button>
            {currencyStatus === "saved" && <span className="muted">{t("menubar.saved")}</span>}
            {currencyStatus === "error" && <span className="error">{t("menubar.saveError")}</span>}
          </div>
        </div>
      </section>

      {error && <div className="surface surface-body error">{error}</div>}
      {loading && <div className="surface surface-body muted">{t("common.loading")}</div>}

      {data && (
        <>
          <div className="metrics-grid usage-metrics-grid">
            <Stat label={t("usage.estimatedCost")} value={formatCost(data.totals.cost_usd)} />
            <Stat label={t("overview.input")} value={fmt(data.totals.input_tokens)} />
            <Stat label={t("overview.output")} value={fmt(data.totals.output_tokens)} />
            <Stat label={t("usage.cacheRead")} value={fmt(data.totals.cache_read_tokens)} />
            <Stat label={t("usage.cacheWrite")} value={fmt(data.totals.cache_write_tokens)} />
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
                    <th>{t("usage.cacheRead")}</th>
                    <th>{t("usage.cacheWrite")}</th>
                    <th>{t("usage.estimatedCost")}</th>
                  </tr>
                </thead>
                <tbody>
                  {data.buckets.map((b) => (
                    <tr key={b.key}>
                      <td>{b.key}</td>
                      <td>{fmt(b.totals.input_tokens)}</td>
                      <td>{fmt(b.totals.output_tokens)}</td>
                      <td>{fmt(b.totals.cache_read_tokens)}</td>
                      <td>{fmt(b.totals.cache_write_tokens)}</td>
                      <td>{formatCost(b.totals.cost_usd)}</td>
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
                      <td>{formatCost(m.cost_usd)}</td>
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

function usageRangeBounds(range: UsageRange, customFrom: string, customTo: string) {
  const now = new Date();
  const today = formatLocalDate(now);
  switch (range) {
    case "today":
      return { from: today, to: today };
    case "7d": {
      const start = new Date(now);
      start.setDate(start.getDate() - 6);
      return { from: formatLocalDate(start), to: today };
    }
    case "month":
      return {
        from: formatLocalDate(new Date(now.getFullYear(), now.getMonth(), 1)),
        to: today,
      };
    case "custom":
      return { from: customFrom, to: customTo };
    default:
      return { from: "", to: "" };
  }
}

function formatLocalDate(value: Date) {
  const year = value.getFullYear();
  const month = String(value.getMonth() + 1).padStart(2, "0");
  const day = String(value.getDate()).padStart(2, "0");
  return `${year}-${month}-${day}`;
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
