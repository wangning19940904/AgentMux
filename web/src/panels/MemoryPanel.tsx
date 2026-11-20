import { useState } from "react";
import { api } from "../api";
import { useI18n } from "../i18n";
import { useAsync } from "../useAsync";
import { TargetBadge, targetKey } from "../components/TargetBadge";

export function MemoryPanel() {
  const { t, language } = useI18n();
  const [scope, setScope] = useState("");
  const [q, setQ] = useState("");
  const entries = useAsync(() => api.memory(scope, q), [scope, q]);
  const [content, setContent] = useState("");
  const [newScope, setNewScope] = useState("global");
  const [busy, setBusy] = useState(false);
  const [adding, setAdding] = useState(false);

  async function add() {
    if (!content.trim()) return;
    setBusy(true);
    try {
      await api.putMemory({ scope: newScope, content });
      setContent("");
      setAdding(false);
      entries.reload();
    } finally {
      setBusy(false);
    }
  }

  async function remove(id: string, targetID?: string) {
    await api.deleteMemory(id, targetID);
    entries.reload();
  }

  return (
    <div className="page-stack">
      <p className="subtle-copy">{t("memory.subtitle")}</p>

      <section className="surface">
        <div className="surface-header">
          <div className="control-row resource-filters">
            <input aria-label={t("memory.filterScope")} placeholder={t("memory.filterScope")} value={scope} onChange={(e) => setScope(e.target.value)} />
            <input aria-label={t("memory.searchText")} placeholder={t("memory.searchText")} value={q} onChange={(e) => setQ(e.target.value)} />
          </div>
          <button className="action" aria-expanded={adding} aria-controls="memory-editor" onClick={() => setAdding((open) => !open)}>
            {adding ? t("common.close") : t("memory.add")}
          </button>
        </div>
        {adding && <form id="memory-editor" className="inline-editor" onSubmit={(event) => { event.preventDefault(); void add(); }}>
          <label className="field">
            <span>{t("memory.scope")}</span>
            <input value={newScope} placeholder={t("memory.scopePlaceholder")} onChange={(e) => setNewScope(e.target.value)} />
          </label>
          <label className="field">
            <span>{t("memory.content")}</span>
            <textarea value={content} placeholder={t("memory.contentPlaceholder")} onChange={(e) => setContent(e.target.value)} rows={4} />
          </label>
          <div className="control-row">
            <button className="action" type="submit" disabled={busy || !content.trim()}>{t("common.save")}</button>
            <button className="ghost-action" type="button" onClick={() => setAdding(false)}>{t("common.cancel")}</button>
          </div>
        </form>}
        {entries.error && <div className="surface-body error">{entries.error}</div>}
        <div className="table-wrap">
          <table>
            <thead>
              <tr>
                <th>{t("memory.scope")}</th>
                <th>{t("memory.content")}</th>
                <th>{t("remote.currentMachine")}</th>
                <th>{t("memory.updated")}</th>
                <th></th>
              </tr>
            </thead>
            <tbody>
              {(entries.data ?? []).map((entry) => (
                <tr key={targetKey(entry.target_id, entry.id)}>
                  <td>
                    <span className="pill">{entry.scope}</span>
                  </td>
                  <td>{entry.content}</td>
                  <td><TargetBadge target_id={entry.target_id} target_name={entry.target_name} /></td>
                  <td className="muted">{new Date(entry.updated_at).toLocaleString(language === "zh" ? "zh-CN" : "en-US")}</td>
                  <td>
                    <button className="ghost-action" onClick={() => remove(entry.id, entry.target_id)}>
                      {t("common.delete")}
                    </button>
                  </td>
                </tr>
              ))}
              {entries.data?.length === 0 && (
                <tr>
                  <td colSpan={5} className="empty-state">
                    {t("memory.empty")}
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
