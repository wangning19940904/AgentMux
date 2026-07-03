import { useState } from "react";
import { api } from "../api";
import { useAsync } from "../useAsync";

export function GuardPanel() {
  const policies = useAsync(() => api.guardPolicies(), []);
  const [tool, setTool] = useState("");
  const [action, setAction] = useState("");
  const [result, setResult] = useState<string | null>(null);

  async function evaluate() {
    if (!tool.trim()) return;
    const r = await api.evaluateGuard({ tool, action: action || undefined });
    setResult(r.decision);
  }

  function badge(decision: string) {
    const cls = decision === "allow" ? "pill on" : decision === "deny" ? "pill off" : "pill";
    return <span className={cls}>{decision}</span>;
  }

  return (
    <div>
      <h1>Guard</h1>
      <p className="muted">AgentNexus Guard — 工具调用的权限审批与策略闸门。</p>

      <h2>Evaluate a tool call</h2>
      <div className="card">
        <div className="form-row">
          <input placeholder="tool (e.g. shell, * )" value={tool} onChange={(e) => setTool(e.target.value)} />
          <input placeholder="action (optional)" value={action} onChange={(e) => setAction(e.target.value)} />
          <button className="action" onClick={evaluate}>
            Evaluate
          </button>
        </div>
        {result && (
          <p>
            Decision: {badge(result)}
          </p>
        )}
      </div>

      <h2>Policies ({policies.data?.length ?? 0})</h2>
      <div className="card">
        {policies.error && <div className="error">{policies.error}</div>}
        <table>
          <thead>
            <tr>
              <th>Priority</th>
              <th>Tool</th>
              <th>Action</th>
              <th>Decision</th>
            </tr>
          </thead>
          <tbody>
            {(policies.data ?? []).map((p) => (
              <tr key={p.id}>
                <td>{p.priority}</td>
                <td>
                  <span className="pill">{p.tool}</span>
                </td>
                <td className="muted">{p.action || "*"}</td>
                <td>{badge(p.decision)}</td>
              </tr>
            ))}
            {policies.data?.length === 0 && (
              <tr>
                <td colSpan={4} className="muted">
                  No policies yet — unmatched calls fall back to "ask".
                </td>
              </tr>
            )}
          </tbody>
        </table>
      </div>
    </div>
  );
}
