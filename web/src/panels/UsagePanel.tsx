import { useState } from "react";
import { api } from "../api";
import { useAsync } from "../useAsync";
import { fmt } from "./OverviewPanel";

const PERIODS = ["daily", "weekly", "monthly", "session", "blocks"];

export function UsagePanel() {
  const [period, setPeriod] = useState("daily");
  const { data, error, loading } = useAsync(() => api.usage(period), [period]);

  return (
    <div>
      <h1>Token Usage</h1>
      <div className="card">
        <label>
          Period:{" "}
          <select value={period} onChange={(e) => setPeriod(e.target.value)}>
            {PERIODS.map((p) => (
              <option key={p} value={p}>
                {p}
              </option>
            ))}
          </select>
        </label>
      </div>

      {error && <div className="card error">{error}</div>}
      {loading && <div className="card muted">Loading…</div>}

      {data && (
        <>
          <div className="card">
            <div className="stat-grid">
              <Stat label="Cost" value={`$${data.totals.cost_usd.toFixed(2)}`} />
              <Stat label="Input" value={fmt(data.totals.input_tokens)} />
              <Stat label="Output" value={fmt(data.totals.output_tokens)} />
              <Stat label="Cache read" value={fmt(data.totals.cache_read_tokens)} />
              <Stat label="Records" value={String(data.totals.records)} />
            </div>
          </div>

          <h2>By {period}</h2>
          <div className="card">
            <table>
              <thead>
                <tr>
                  <th>{period}</th>
                  <th>Input</th>
                  <th>Output</th>
                  <th>Cost</th>
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

          <h2>By model</h2>
          <div className="card">
            <table>
              <thead>
                <tr>
                  <th>Model</th>
                  <th>Tokens</th>
                  <th>Cost</th>
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
        </>
      )}
    </div>
  );
}

function Stat({ label, value }: { label: string; value: string }) {
  return (
    <div className="stat">
      <div className="value">{value}</div>
      <div className="label">{label}</div>
    </div>
  );
}
