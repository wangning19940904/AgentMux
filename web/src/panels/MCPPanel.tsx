import { Boxes, Trash2 } from "lucide-react";
import { RowDisclosure } from "../components/RowDisclosure";
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
        <div className="resource-card-grid mcp-card-grid">
          {(servers.data ?? []).map((server) => (
            <RowDisclosure
              className="resource-card mcp-card"
              key={targetKey(server.target_id, server.name)}
              name={server.name}
              summary={<div className="resource-card-heading"><span className="brand-icon"><Boxes size={18} /></span><strong>{server.name}</strong></div>}
              preview={<>
                <code className="resource-card-command" title={server.command || server.url}>{server.command || server.url || "—"}</code>
                <div className="resource-card-meta">
                  <span className="pill">{server.transport}</span>
                  <span className={`status-badge${server.enabled ? " success" : ""}`}><span className="status-dot" />{t(server.enabled ? "common.enabled" : "common.disabled")}</span>
                  <TargetBadge target_id={server.target_id} target_name={server.target_name} />
                </div>
              </>}
            >
              <code className="resource-command-detail">{[server.command || server.url, ...(server.args ?? [])].filter(Boolean).join(" ") || "—"}</code>
              <div className="table-actions">
                <button className="ghost-action danger-action" onClick={() => remove(server.name, server.target_id)}>
                  <Trash2 size={14} />{t("common.delete")}
                </button>
              </div>
            </RowDisclosure>
          ))}
          {servers.data?.length === 0 && <div className="empty-state">{t("mcp.empty")}</div>}
        </div>
      </section>
    </div>
  );
}
