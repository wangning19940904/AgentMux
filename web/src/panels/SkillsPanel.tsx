import { api } from "../api";
import { useI18n } from "../i18n";
import { useAsync } from "../useAsync";

export function SkillsPanel() {
  const { t } = useI18n();
  const skills = useAsync(() => api.skills(), []);

  async function toggle(name: string, enabled: boolean) {
    await api.toggleSkill(name, enabled);
    skills.reload();
  }

  return (
    <div className="page-stack">
      <p className="subtle-copy">{t("skills.subtitle")}</p>

      <section className="surface">
        <div className="surface-header">
          <h2>{t("skills.title")}</h2>
          <span className="pill on">{skills.data?.length ?? 0}</span>
        </div>
        {skills.error && <div className="surface-body error">{skills.error}</div>}
        <div className="table-wrap">
          <table>
            <thead>
              <tr>
                <th>{t("common.name")}</th>
                <th>{t("common.description")}</th>
                <th>{t("common.source")}</th>
                <th>{t("common.status")}</th>
                <th></th>
              </tr>
            </thead>
            <tbody>
              {(skills.data ?? []).map((skill) => (
                <tr key={skill.name}>
                  <td>{skill.name}</td>
                  <td className="muted">{skill.description || "—"}</td>
                  <td>
                    <span className="pill">{skill.source || "local"}</span>
                  </td>
                  <td>
                    {skill.enabled ? (
                      <span className="status-badge success">
                        <span className="status-dot" />
                        {t("common.enabled")}
                      </span>
                    ) : (
                      <span className="status-badge">
                        <span className="status-dot" />
                        {t("common.disabled")}
                      </span>
                    )}
                  </td>
                  <td>
                    <button className="action" onClick={() => toggle(skill.name, !skill.enabled)}>
                      {skill.enabled ? t("common.disable") : t("common.enable")}
                    </button>
                  </td>
                </tr>
              ))}
              {skills.data?.length === 0 && (
                <tr>
                  <td colSpan={5} className="empty-state">
                    {t("skills.empty")}
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
