import { api } from "../api";
import { useAsync } from "../useAsync";

export function GatewayPanel() {
  const platforms = useAsync(() => api.platforms(), []);
  const agents = useAsync(() => api.agents(), []);

  return (
    <div>
      <h1>IM Platforms & Agents</h1>

      <h2>Messaging platforms ({platforms.data?.length ?? 0})</h2>
      <div className="card">
        {platforms.error && <div className="error">{platforms.error}</div>}
        {(platforms.data ?? []).map((p) => (
          <span className="pill on" key={p}>
            {p}
          </span>
        ))}
        <p className="muted">
          Registered platform adapters. Bind them to a project in config.toml under
          [[projects.platforms]].
        </p>
      </div>

      <h2>Agent frameworks ({agents.data?.length ?? 0})</h2>
      <div className="card">
        {agents.error && <div className="error">{agents.error}</div>}
        {(agents.data ?? []).map((a) => (
          <span className="pill on" key={a}>
            {a}
          </span>
        ))}
        <p className="muted">
          Registered coding-agent adapters. Set one per project via the "agent" field.
        </p>
      </div>
    </div>
  );
}
