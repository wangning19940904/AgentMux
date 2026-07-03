import { useState } from "react";
import { api, MCPServer } from "../api";
import { useAsync } from "../useAsync";

const EMPTY: MCPServer = { name: "", transport: "stdio", command: "", enabled: true };

export function MCPPanel() {
  const servers = useAsync(() => api.mcp(), []);
  const [draft, setDraft] = useState<MCPServer>(EMPTY);
  const [busy, setBusy] = useState(false);

  async function save() {
    if (!draft.name.trim()) return;
    setBusy(true);
    try {
      await api.upsertMCP(draft);
      setDraft(EMPTY);
      servers.reload();
    } finally {
      setBusy(false);
    }
  }

  async function remove(name: string) {
    await api.deleteMCP(name);
    servers.reload();
  }

  return (
    <div>
      <h1>MCP Registry</h1>
      <p className="muted">AgentNexus MCP Registry — 注册、编排与下发 MCP Server 配置。</p>

      <h2>Register server</h2>
      <div className="card">
        <div className="form-row">
          <input
            placeholder="name"
            value={draft.name}
            onChange={(e) => setDraft({ ...draft, name: e.target.value })}
          />
          <select
            value={draft.transport}
            onChange={(e) => setDraft({ ...draft, transport: e.target.value })}
          >
            <option value="stdio">stdio</option>
            <option value="sse">sse</option>
            <option value="http">http</option>
          </select>
        </div>
        <div className="form-row">
          <input
            placeholder={draft.transport === "stdio" ? "command (e.g. npx)" : "url"}
            value={draft.transport === "stdio" ? draft.command : draft.url}
            onChange={(e) =>
              draft.transport === "stdio"
                ? setDraft({ ...draft, command: e.target.value })
                : setDraft({ ...draft, url: e.target.value })
            }
          />
        </div>
        <button className="action" disabled={busy} onClick={save}>
          Save
        </button>
      </div>

      <h2>Registered ({servers.data?.length ?? 0})</h2>
      <div className="card">
        {servers.error && <div className="error">{servers.error}</div>}
        <table>
          <thead>
            <tr>
              <th>Name</th>
              <th>Transport</th>
              <th>Command / URL</th>
              <th>Status</th>
              <th></th>
            </tr>
          </thead>
          <tbody>
            {(servers.data ?? []).map((m) => (
              <tr key={m.name}>
                <td>{m.name}</td>
                <td>
                  <span className="pill">{m.transport}</span>
                </td>
                <td className="muted">{m.command || m.url || "—"}</td>
                <td>
                  {m.enabled ? (
                    <span className="pill on">enabled</span>
                  ) : (
                    <span className="pill">disabled</span>
                  )}
                </td>
                <td>
                  <button className="action" onClick={() => remove(m.name)}>
                    Delete
                  </button>
                </td>
              </tr>
            ))}
            {servers.data?.length === 0 && (
              <tr>
                <td colSpan={5} className="muted">
                  No MCP servers registered yet.
                </td>
              </tr>
            )}
          </tbody>
        </table>
      </div>
    </div>
  );
}
