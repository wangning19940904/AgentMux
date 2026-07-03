import { api } from "../api";
import { useAsync } from "../useAsync";

export function SkillsPanel() {
  const skills = useAsync(() => api.skills(), []);

  async function toggle(name: string, enabled: boolean) {
    await api.toggleSkill(name, enabled);
    skills.reload();
  }

  return (
    <div>
      <h1>Skills</h1>
      <p className="muted">
        AgentNexus Skills — 统一发现、安装与管理 Agent Skills(扫描 ~/.agentnexus/skills 下的 SKILL.md)。
      </p>

      <div className="card">
        {skills.error && <div className="error">{skills.error}</div>}
        <table>
          <thead>
            <tr>
              <th>Name</th>
              <th>Description</th>
              <th>Source</th>
              <th>Status</th>
              <th></th>
            </tr>
          </thead>
          <tbody>
            {(skills.data ?? []).map((s) => (
              <tr key={s.name}>
                <td>{s.name}</td>
                <td className="muted">{s.description || "—"}</td>
                <td>
                  <span className="pill">{s.source || "local"}</span>
                </td>
                <td>
                  {s.enabled ? (
                    <span className="pill on">enabled</span>
                  ) : (
                    <span className="pill">disabled</span>
                  )}
                </td>
                <td>
                  <button className="action" onClick={() => toggle(s.name, !s.enabled)}>
                    {s.enabled ? "Disable" : "Enable"}
                  </button>
                </td>
              </tr>
            ))}
            {skills.data?.length === 0 && (
              <tr>
                <td colSpan={5} className="muted">
                  No skills discovered. Drop a SKILL.md under ~/.agentnexus/skills.
                </td>
              </tr>
            )}
          </tbody>
        </table>
      </div>
    </div>
  );
}
