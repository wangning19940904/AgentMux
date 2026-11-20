import { useState } from "react";
import { api, MCPServer } from "../api";
import { useI18n } from "../i18n";
import { useAsync } from "../useAsync";
import { TargetBadge, targetKey } from "../components/TargetBadge";

const EMPTY: MCPServer = { name: "", transport: "stdio", command: "", enabled: true };

export function MCPPanel() {
  const { t } = useI18n();
  const servers = useAsync(() => api.mcp(), []);
  const [draft, setDraft] = useState<MCPServer>(EMPTY);
  const [busy, setBusy] = useState(false);
  const [registering, setRegistering] = useState(false);

  async function save() {
    if (!draft.name.trim()) return;
    setBusy(true);
    try {
      await api.upsertMCP(draft);
      setDraft(EMPTY);
      setRegistering(false);
      servers.reload();
    } finally {
      setBusy(false);
    }
  }

  async function remove(name: string, targetID?: string) {
    await api.deleteMCP(name, targetID);
    servers.reload();
  }

  return (
    <div className="page-stack">
      <p className="subtle-copy">{t("mcp.subtitle")}</p>

      <section className="surface">
        <div className="surface-header">
          <div className="control-row">
            <h2>{t("mcp.registered")}</h2>
            <span className="pill">{servers.data?.length ?? 0}</span>
          </div>
          <button className="action" aria-expanded={registering} aria-controls="mcp-editor" onClick={() => setRegistering((open) => !open)}>
            {registering ? t("common.close") : t("mcp.register")}
          </button>
        </div>
        {registering && <form id="mcp-editor" className="inline-editor" onSubmit={(event) => { event.preventDefault(); void save(); }}>
          <div className="field-grid">
            <label className="field">
              <span>{t("common.name")}</span>
              <input
                placeholder={t("mcp.namePlaceholder")}
                value={draft.name}
                onChange={(e) => setDraft({ ...draft, name: e.target.value })}
              />
            </label>
            <label className="field">
              <span>{t("mcp.transport")}</span>
              <select
                value={draft.transport}
                onChange={(e) => setDraft({ ...draft, transport: e.target.value })}
              >
                <option value="stdio">stdio</option>
                <option value="sse">sse</option>
                <option value="http">http</option>
              </select>
            </label>
          </div>
          <label className="field">
            <span>{t("mcp.commandUrl")}</span>
            <input
              placeholder={draft.transport === "stdio" ? t("mcp.commandPlaceholder") : t("mcp.urlPlaceholder")}
              value={draft.transport === "stdio" ? draft.command : draft.url}
              onChange={(e) =>
                draft.transport === "stdio"
                  ? setDraft({ ...draft, command: e.target.value })
                  : setDraft({ ...draft, url: e.target.value })
              }
            />
          </label>
          <div className="control-row">
            <button className="action" type="submit" disabled={busy || !draft.name.trim()}>{t("common.save")}</button>
            <button className="ghost-action" type="button" onClick={() => setRegistering(false)}>{t("common.cancel")}</button>
          </div>
        </form>}
        {servers.error && <div className="surface-body error">{servers.error}</div>}
        <div className="table-wrap">
          <table>
            <thead>
              <tr>
                <th>{t("common.name")}</th>
                <th>{t("mcp.transport")}</th>
                <th>{t("mcp.commandUrl")}</th>
                <th>{t("common.status")}</th>
                <th>{t("remote.currentMachine")}</th>
                <th></th>
              </tr>
            </thead>
            <tbody>
              {(servers.data ?? []).map((server) => (
                <tr key={targetKey(server.target_id, server.name)}>
                  <td>{server.name}</td>
                  <td>
                    <span className="pill">{server.transport}</span>
                  </td>
                  <td className="muted mono">{server.command || server.url || "—"}</td>
                  <td>
                    {server.enabled ? (
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
                  <td><TargetBadge target_id={server.target_id} target_name={server.target_name} /></td>
                  <td>
                    <button className="ghost-action" onClick={() => remove(server.name, server.target_id)}>
                      {t("common.delete")}
                    </button>
                  </td>
                </tr>
              ))}
              {servers.data?.length === 0 && (
                <tr>
                  <td colSpan={6} className="empty-state">
                    {t("mcp.empty")}
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
