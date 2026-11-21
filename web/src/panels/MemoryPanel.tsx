import { Brain, Trash2 } from "lucide-react";
import { RowDisclosure } from "../components/RowDisclosure";
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
        <div className="resource-card-grid memory-card-grid">
          {(entries.data ?? []).map((entry) => (
            <RowDisclosure
              className="resource-card memory-card"
              key={targetKey(entry.target_id, entry.id)}
              name={entry.scope}
              summary={<div className="resource-card-heading"><span className="brand-icon"><Brain size={18} /></span><strong>{entry.scope}</strong></div>}
              preview={<>
                <p className="memory-preview">{entry.content}</p>
                <div className="resource-card-meta">
                  <TargetBadge target_id={entry.target_id} target_name={entry.target_name} />
                  <time dateTime={entry.updated_at}>{new Date(entry.updated_at).toLocaleDateString(language === "zh" ? "zh-CN" : "en-US")}</time>
                </div>
              </>}
            >
              <p className="memory-content">{entry.content}</p>
              <div className="table-actions">
                <button className="ghost-action danger-action" onClick={() => remove(entry.id, entry.target_id)}>
                  <Trash2 size={14} />{t("common.delete")}
                </button>
              </div>
            </RowDisclosure>
          ))}
          {entries.data?.length === 0 && <div className="empty-state">{t("memory.empty")}</div>}
        </div>
      </section>
    </div>
  );
}
