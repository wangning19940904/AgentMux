import { useState } from "react";
import { api } from "../api";
import { useI18n } from "../i18n";
import { useAsync } from "../useAsync";

export function MemoryPanel() {
  const { t, language } = useI18n();
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
    <div className="page-stack">
      <p className="subtle-copy">{t("memory.subtitle")}</p>

      <section className="surface">
        <div className="surface-header">
          <h2>{t("memory.add")}</h2>
        </div>
        <div className="surface-body">
          <div className="form-row">
            <input
              placeholder={t("memory.scopePlaceholder")}
              value={newScope}
              onChange={(e) => setNewScope(e.target.value)}
            />
          </div>
          <div className="form-row">
            <textarea
              placeholder={t("memory.contentPlaceholder")}
              value={content}
              onChange={(e) => setContent(e.target.value)}
              rows={3}
            />
          </div>
          <button className="action" disabled={busy} onClick={add}>
            {t("common.save")}
          </button>
        </div>
      </section>

      <section className="surface">
        <div className="surface-header">
          <h2>{t("memory.search")}</h2>
          <div className="control-row">
            <input placeholder={t("memory.filterScope")} value={scope} onChange={(e) => setScope(e.target.value)} />
            <input placeholder={t("memory.searchText")} value={q} onChange={(e) => setQ(e.target.value)} />
          </div>
        </div>
        {entries.error && <div className="surface-body error">{entries.error}</div>}
        <div className="table-wrap">
          <table>
            <thead>
              <tr>
                <th>{t("memory.scope")}</th>
                <th>{t("memory.content")}</th>
                <th>{t("memory.updated")}</th>
                <th></th>
              </tr>
            </thead>
            <tbody>
              {(entries.data ?? []).map((entry) => (
                <tr key={entry.id}>
                  <td>
                    <span className="pill">{entry.scope}</span>
                  </td>
                  <td>{entry.content}</td>
                  <td className="muted">{new Date(entry.updated_at).toLocaleString(language === "zh" ? "zh-CN" : "en-US")}</td>
                  <td>
                    <button className="ghost-action" onClick={() => remove(entry.id)}>
                      {t("common.delete")}
                    </button>
                  </td>
                </tr>
              ))}
              {entries.data?.length === 0 && (
                <tr>
                  <td colSpan={4} className="empty-state">
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
