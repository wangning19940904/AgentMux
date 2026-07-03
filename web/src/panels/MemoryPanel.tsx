import { useState } from "react";
import { api } from "../api";
import { useAsync } from "../useAsync";

export function MemoryPanel() {
  const [scope, setScope] = useState("");
  const [q, setQ] = useState("");
  const entries = useAsync(() => api.memory(scope, q), [scope, q]);
  const [content, setContent] = useState("");
  const [newScope, setNewScope] = useState("global");
  const [busy, setBusy] = useState(false);

  async function add() {
    if (!content.trim()) return;
    setBusy(true);
    try {
      await api.putMemory({ scope: newScope, content });
      setContent("");
      entries.reload();
    } finally {
      setBusy(false);
    }
  }

  async function remove(id: string) {
    await api.deleteMemory(id);
    entries.reload();
  }

  return (
    <div>
      <h1>Memory</h1>
      <p className="muted">AgentNexus Memory — 跨 Agent、跨会话的统一记忆层。</p>

      <h2>Add entry</h2>
      <div className="card">
        <div className="form-row">
          <input
            placeholder="scope (global / project:foo / session:id)"
            value={newScope}
            onChange={(e) => setNewScope(e.target.value)}
          />
        </div>
        <div className="form-row">
          <textarea
            placeholder="memory content…"
            value={content}
            onChange={(e) => setContent(e.target.value)}
            rows={3}
          />
        </div>
        <button className="action" disabled={busy} onClick={add}>
          Save
        </button>
      </div>

      <h2>Search</h2>
      <div className="card">
        <div className="form-row">
          <input placeholder="filter scope" value={scope} onChange={(e) => setScope(e.target.value)} />
          <input placeholder="search text" value={q} onChange={(e) => setQ(e.target.value)} />
        </div>
        {entries.error && <div className="error">{entries.error}</div>}
        <table>
          <thead>
            <tr>
              <th>Scope</th>
              <th>Content</th>
              <th>Updated</th>
              <th></th>
            </tr>
          </thead>
          <tbody>
            {(entries.data ?? []).map((e) => (
              <tr key={e.id}>
                <td>
                  <span className="pill">{e.scope}</span>
                </td>
                <td>{e.content}</td>
                <td className="muted">{new Date(e.updated_at).toLocaleString()}</td>
                <td>
                  <button className="action" onClick={() => remove(e.id)}>
                    Delete
                  </button>
                </td>
              </tr>
            ))}
            {entries.data?.length === 0 && (
              <tr>
                <td colSpan={4} className="muted">
                  No memory entries yet.
                </td>
              </tr>
            )}
          </tbody>
        </table>
      </div>
    </div>
  );
}
