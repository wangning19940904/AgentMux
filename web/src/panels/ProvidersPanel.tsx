import { useState } from "react";
import { api, Provider } from "../api";
import { useAsync } from "../useAsync";

export function ProvidersPanel() {
  const providers = useAsync(() => api.providers(), []);
  const presets = useAsync(() => api.presets(), []);
  const [busy, setBusy] = useState<string | null>(null);

  async function importPreset(p: Provider) {
    setBusy(p.id);
    try {
      await api.upsertProvider(p);
      providers.reload();
    } finally {
      setBusy(null);
    }
  }

  async function switchTo(p: Provider) {
    const tool = p.tools[0];
    setBusy(p.id);
    try {
      await api.switchProvider(p.id, tool);
      providers.reload();
    } finally {
      setBusy(null);
    }
  }

  return (
    <div>
      <h1>Providers</h1>

      <h2>Configured</h2>
      <div className="card">
        {providers.error && <div className="error">{providers.error}</div>}
        <table>
          <thead>
            <tr>
              <th>Name</th>
              <th>Tools</th>
              <th>Model</th>
              <th>Status</th>
              <th></th>
            </tr>
          </thead>
          <tbody>
            {(providers.data ?? []).map((p) => (
              <tr key={p.id}>
                <td>{p.name}</td>
                <td>
                  {p.tools.map((t) => (
                    <span className="pill" key={t}>
                      {t}
                    </span>
                  ))}
                </td>
                <td className="muted">{p.model || "—"}</td>
                <td>
                  {p.enabled ? <span className="pill on">enabled</span> : <span className="pill">idle</span>}
                </td>
                <td>
                  <button className="action" disabled={busy === p.id} onClick={() => switchTo(p)}>
                    Switch
                  </button>
                </td>
              </tr>
            ))}
            {providers.data?.length === 0 && (
              <tr>
                <td colSpan={5} className="muted">
                  No providers yet — import a preset below.
                </td>
              </tr>
            )}
          </tbody>
        </table>
      </div>

      <h2>Presets ({presets.data?.length ?? 0})</h2>
      <div className="card">
        <table>
          <thead>
            <tr>
              <th>Name</th>
              <th>Tools</th>
              <th>Base URL</th>
              <th></th>
            </tr>
          </thead>
          <tbody>
            {(presets.data ?? []).map((p) => (
              <tr key={p.id}>
                <td>{p.name}</td>
                <td>
                  {p.tools.map((t) => (
                    <span className="pill" key={t}>
                      {t}
                    </span>
                  ))}
                </td>
                <td className="muted">{p.base_url}</td>
                <td>
                  <button className="action" disabled={busy === p.id} onClick={() => importPreset(p)}>
                    Import
                  </button>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </div>
  );
}
