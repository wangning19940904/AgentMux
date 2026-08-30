import { useState } from "react";
import { api } from "../api";
import { useI18n } from "../i18n";
import { useAsync } from "../useAsync";
import { TargetBadge, targetKey } from "../components/TargetBadge";

export function GuardPanel() {
  const { t } = useI18n();
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
    const normalized = decision.toLowerCase();
    const cls =
      normalized === "allow"
        ? "status-badge success"
        : normalized === "deny"
          ? "status-badge danger"
          : "status-badge warning";
    return (
      <span className={cls}>
        <span className="status-dot" />
        {decision}
      </span>
    );
  }

  return (
    <div className="page-stack">
      <p className="subtle-copy">{t("guard.subtitle")}</p>

      <section className="surface">
        <div className="surface-header">
          <h2>{t("guard.evaluate")}</h2>
        </div>
        <div className="surface-body">
          <div className="form-row">
            <input placeholder={t("guard.toolPlaceholder")} value={tool} onChange={(e) => setTool(e.target.value)} />
            <input placeholder={t("guard.actionPlaceholder")} value={action} onChange={(e) => setAction(e.target.value)} />
            <button className="action" onClick={evaluate}>
              {t("guard.evaluate")}
            </button>
          </div>
          {result && (
            <p className="control-row">
              <span className="muted">{t("guard.result")}</span>
              {badge(result)}
            </p>
          )}
        </div>
      </section>

      <section className="surface">
        <div className="surface-header">
          <h2>{t("guard.policies")}</h2>
          <span className="pill on">{policies.data?.length ?? 0}</span>
        </div>
        {policies.error && <div className="surface-body error">{policies.error}</div>}
        <div className="table-wrap">
          <table>
            <thead>
              <tr>
                <th>{t("guard.priority")}</th>
                <th>{t("guard.tool")}</th>
                <th>{t("guard.action")}</th>
                <th>{t("overview.decision")}</th>
                <th>{t("remote.currentMachine")}</th>
              </tr>
            </thead>
            <tbody>
              {(policies.data ?? []).map((policy) => (
                <tr key={targetKey(policy.target_id, policy.id)}>
                  <td>{policy.priority}</td>
                  <td>
                    <span className="pill">{policy.tool}</span>
                  </td>
                  <td className="muted">{policy.action || "*"}</td>
                  <td>{badge(policy.decision)}</td>
                  <td><TargetBadge target_id={policy.target_id} target_name={policy.target_name} /></td>
                </tr>
              ))}
              {policies.data?.length === 0 && (
                <tr>
                  <td colSpan={5} className="empty-state">
                    {t("guard.empty")}
                  </td>
                </tr>
              )}
            </tbody>
          </table>
        </div>
      </section>
    </div>
  );
}
