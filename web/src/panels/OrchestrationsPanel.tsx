import { Play, Plus, RefreshCw, Square, Workflow } from "lucide-react";
import { useEffect, useMemo, useState } from "react";
import { api } from "../api";
import { useI18n } from "../i18n";
import { usePolling } from "../hooks/usePolling";
import { useAsync } from "../useAsync";
import { parseOrchestrationTasksJSON } from "./orchestrationModel";

const EXAMPLE_TASKS = JSON.stringify([
  { id: "research", agent_id: "agent-id", input: "Inspect the problem and gather evidence." },
  { id: "review", agent_id: "reviewer-agent-id", input: "Review the evidence and propose a solution.", depends_on: ["research"] },
], null, 2);

export function OrchestrationsPanel() {
  const { t, language } = useI18n();
  const [selectedID, setSelectedID] = useState("");
  const [creating, setCreating] = useState(false);
  const [name, setName] = useState("");
  const [workers, setWorkers] = useState(4);
  const [tasksJSON, setTasksJSON] = useState(EXAMPLE_TASKS);
  const [busy, setBusy] = useState("");
  const [notice, setNotice] = useState("");
  const list = useAsync(() => api.orchestrations(), []);
  usePolling(list.reload, 3_000);
  const selectedSummary = useMemo(
    () => (list.data ?? []).find((item) => item.id === selectedID) ?? (list.data ?? [])[0],
    [list.data, selectedID],
  );
  const detail = useAsync(
    () => selectedSummary ? api.orchestration(selectedSummary.id) : Promise.resolve(null),
    [selectedSummary?.id],
  );
  usePolling(detail.reload, 1_500, { enabled: Boolean(selectedSummary && ["queued", "running"].includes(selectedSummary.status)) });

  useEffect(() => {
    if (selectedSummary) setSelectedID(selectedSummary.id);
  }, [selectedSummary?.id]);

  async function create() {
    setBusy("create");
    setNotice("");
    try {
      const parsed = parseOrchestrationTasksJSON(tasksJSON);
      const created = await api.createOrchestration({ name, max_concurrency: workers, tasks: parsed });
      setCreating(false);
      setSelectedID(created.id);
      await list.reload();
    } catch (error) {
      setNotice(String(error));
    } finally {
      setBusy("");
    }
  }

  async function cancel() {
    if (!selectedSummary) return;
    setBusy("cancel");
    try {
      await api.cancelOrchestration(selectedSummary.id);
      await Promise.all([list.reload(), detail.reload()]);
    } catch (error) {
      setNotice(String(error));
    } finally {
      setBusy("");
    }
  }

  return (
    <div className="page-stack">
      <section className="surface">
        <div className="surface-header">
          <div><h2>{t("orchestrations.title")}</h2><p>{t("orchestrations.subtitle")}</p></div>
          <div className="control-row">
            <button className="ghost-action" onClick={list.reload}><RefreshCw size={15} /> {t("common.refresh")}</button>
            <button className="action" onClick={() => setCreating(true)}><Plus size={15} /> {t("orchestrations.create")}</button>
          </div>
        </div>
        {notice && <div className="surface-body error">{notice}</div>}
        <div className="orchestration-layout">
          <div className="orchestration-list">
            {(list.data ?? []).map((item) => (
              <button key={item.id} className={selectedSummary?.id === item.id ? "active" : ""} onClick={() => setSelectedID(item.id)}>
                <Workflow size={16} />
                <span><strong>{item.name}</strong><small>{item.id}</small></span>
                <span className={`status-badge status-${item.status}`}>{item.status}</span>
              </button>
            ))}
            {!list.loading && (list.data ?? []).length === 0 && <div className="empty-state">{t("orchestrations.empty")}</div>}
          </div>
          <div className="orchestration-detail">
            {detail.data ? (
              <>
                <header>
                  <div><h3>{detail.data.name}</h3><p className="muted">{detail.data.id}</p></div>
                  {["queued", "running"].includes(detail.data.status) && (
                    <button className="ghost-action danger-action" disabled={busy === "cancel"} onClick={cancel}><Square size={14} /> {t("orchestrations.cancel")}</button>
                  )}
                </header>
                {detail.data.error && <div className="error">{detail.data.error}</div>}
                <div className="orchestration-task-list">
                  {(detail.data.tasks ?? []).map((task) => (
                    <article key={task.id}>
                      <header><strong>{task.id}</strong><span className={`status-badge status-${task.status}`}>{task.status}</span></header>
                      <p>{task.input}</p>
                      {task.depends_on?.length ? <small>{t("orchestrations.dependsOn")}: {task.depends_on.join(", ")}</small> : null}
                      {task.output && <pre>{task.output}</pre>}
                      {task.error && <div className="error">{task.error}</div>}
                      <time>{new Date(task.updated_at).toLocaleString(language === "zh" ? "zh-CN" : "en-US")}</time>
                    </article>
                  ))}
                </div>
              </>
            ) : <div className="empty-state">{t("orchestrations.select")}</div>}
          </div>
        </div>
      </section>
      {creating && (
        <div className="modal-backdrop" role="presentation">
          <section className="dialog orchestration-dialog" role="dialog" aria-modal="true" aria-label={t("orchestrations.create")}>
            <header><div><h3>{t("orchestrations.create")}</h3><p>{t("orchestrations.createHelp")}</p></div></header>
            <label className="field"><span>{t("orchestrations.name")}</span><input value={name} onChange={(event) => setName(event.target.value)} /></label>
            <label className="field"><span>{t("orchestrations.workers")}</span><input type="number" min={1} max={12} value={workers} onChange={(event) => setWorkers(Number(event.target.value))} /></label>
            <label className="field"><span>{t("orchestrations.tasksJSON")}</span><textarea className="code-editor" rows={18} value={tasksJSON} onChange={(event) => setTasksJSON(event.target.value)} /></label>
            <footer className="control-row">
              <button className="ghost-action" onClick={() => setCreating(false)}>{t("common.cancel")}</button>
              <button className="action" disabled={busy === "create"} onClick={create}><Play size={15} /> {t("orchestrations.run")}</button>
            </footer>
          </section>
        </div>
      )}
    </div>
  );
}
