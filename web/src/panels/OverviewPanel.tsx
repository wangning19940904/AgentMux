import { api } from "../api";
import { useAsync } from "../useAsync";

export function OverviewPanel() {
  const status = useAsync(() => api.status(), []);
  const usage = useAsync(() => api.usage("daily"), []);

  return (
    <div>
      <h1>Overview</h1>
      <div className="card">
        {status.error && <div className="error">{status.error}</div>}
        {status.data && (
          <div className="stat-grid">
            <div className="stat">
              <div className="value">{status.data.ok ? "Running" : "Down"}</div>
              <div className="label">Daemon</div>
            </div>
            <div className="stat">
              <div className="value">{status.data.projects}</div>
              <div className="label">Projects</div>
            </div>
            <div className="stat">
              <div className="value">v{status.data.version}</div>
              <div className="label">Version</div>
            </div>
          </div>
        )}
      </div>

      <h2>Today</h2>
      <div className="card">
        {usage.loading && <div className="muted">Loading usage…</div>}
        {usage.data && (
          <div className="stat-grid">
            <div className="stat">
              <div className="value">${usage.data.totals.cost_usd.toFixed(2)}</div>
              <div className="label">Estimated cost</div>
            </div>
            <div className="stat">
              <div className="value">{fmt(usage.data.totals.input_tokens)}</div>
              <div className="label">Input tokens</div>
            </div>
            <div className="stat">
              <div className="value">{fmt(usage.data.totals.output_tokens)}</div>
              <div className="label">Output tokens</div>
            </div>
            <div className="stat">
              <div className="value">{usage.data.totals.records}</div>
              <div className="label">Records</div>
            </div>
          </div>
        )}
      </div>
    </div>
  );
}

export function fmt(n: number): string {
  if (n >= 1e9) return (n / 1e9).toFixed(2) + "B";
  if (n >= 1e6) return (n / 1e6).toFixed(2) + "M";
  if (n >= 1e3) return (n / 1e3).toFixed(2) + "K";
  return String(n);
}
