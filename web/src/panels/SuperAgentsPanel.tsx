import { Bot, Play, Plus, RefreshCw, RotateCcw, Square, Trash2 } from "lucide-react";
import { useEffect, useRef, useState } from "react";
import { api } from "../api";
import type { SuperAgent } from "../api";
import { usePolling } from "../hooks/usePolling";
import { useI18n } from "../i18n";
import { useAsync } from "../useAsync";
import { sourceHookAddress } from "./superAgentSetup";
import { githubPolls, isChannelSource, newSuperAgent } from "./superAgentModel";
import { SuperAgentDrawer } from "./agents/SuperAgentForm";
import { confirmAction } from "../components/ConfirmDialog";

export type SuperAgentsPanelProps = {
  createRequested?: boolean;
  onCreateRequestHandled?: () => void;
  // agentID shows one agent's workers, triggers and Tickets, as the Agents
  // page does for each agent, with no list and no create or delete.
  agentID?: string;
  // onChanged runs after a save, so the page around it can reload.
  onChanged?: () => void;
};

export function SuperAgentsPanel({ createRequested = false, onCreateRequestHandled, agentID, onChanged }: SuperAgentsPanelProps = {}) {
  const { t, language } = useI18n();
  const embedded = Boolean(agentID);
  const list = useAsync(() => api.superAgents().then((items) => agentID ? items.filter((item) => item.id === agentID) : items), [agentID]);
  const [selectedID, setSelectedID] = useState("");
  const [draft, setDraft] = useState<SuperAgent | null>(null);
  const [notice, setNotice] = useState("");
  const [busy, setBusy] = useState("");
  const [request, setRequest] = useState("");
  const handledCreate = useRef(false);
  const items = list.data ?? [];
  const selected = items.find((item) => item.id === selectedID) ?? items[0];
  const activity = useAsync(() => selected ? api.superAgentActivity(selected.id) : Promise.resolve(null), [selected?.id]);
  usePolling(activity.reload, 3_000, { enabled: Boolean(selected) });

  useEffect(() => {
    if (!createRequested) {
      handledCreate.current = false;
      return;
    }
    if (handledCreate.current) return;
    handledCreate.current = true;
    setDraft(newSuperAgent());
    onCreateRequestHandled?.();
  }, [createRequested, onCreateRequestHandled]);

  async function run(label: string, action: () => Promise<unknown>) {
    setBusy(label);
    setNotice("");
    try {
      await action();
      await Promise.all([list.reload(), activity.reload()]);
      onChanged?.();
    } catch (error) {
      setNotice(error instanceof Error ? error.message : String(error));
    } finally {
      setBusy("");
    }
  }

  const time = (value?: string) => value ? new Date(value).toLocaleString(language === "zh" ? "zh-CN" : "en-US") : "";

  return (
    <div className="page-stack">
      <section className="surface">
        {!embedded && <div className="surface-header">
          <div><h2>{t("superAgents.title")}</h2><p>{t("superAgents.subtitle")}</p></div>
          <div className="control-row">
            <button className="ghost-action" onClick={() => void list.reload()}><RefreshCw size={15} /> {t("common.refresh")}</button>
            <button className="action" onClick={() => setDraft(newSuperAgent())}><Plus size={15} /> {t("agents.newSuper")}</button>
          </div>
        </div>}
        {notice && !draft && <div className="surface-body session-notice error" role="alert">{notice}</div>}
        {list.error && <div className="surface-body session-notice error" role="alert">{String(list.error)}</div>}
        <div className={embedded ? "orchestration-layout agent-work-layout" : "orchestration-layout"}>
          {!embedded && <div className="orchestration-list">
            {items.map((item) => (
              <button key={item.id} className={selected?.id === item.id ? "active" : ""} onClick={() => setSelectedID(item.id)}>
                <Bot size={16} />
                <span><strong>{item.name}</strong><small>{t(`superAgents.mode.${item.mode}`)} · {item.auto_workers ? t("superAgents.autoWorkersOn") : t("superAgents.workerCount", { count: item.workers.length })}</small></span>
                <span className={`status-badge status-${item.enabled ? "running" : "cancelled"}`}>{t(item.enabled ? "common.enabled" : "common.disabled")}</span>
              </button>
            ))}
            {!list.loading && items.length === 0 && <div className="empty-state">{t("superAgents.empty")}</div>}
          </div>}
          <div className="orchestration-detail">
            {selected ? (
              <>
                <header>
                  <div><h3>{selected.name}</h3><p className="muted">{selected.description || t(`superAgents.modeHelp.${selected.mode}`)}</p></div>
                  <div className="control-row">
                    <button className="ghost-action" onClick={() => setDraft(structuredClone(selected))}>{embedded ? t("agents.work.edit") : t("common.edit")}</button>
                    {!embedded && <button className="ghost-action danger-action" disabled={Boolean(busy)} onClick={async () => {
                      if (await confirmAction(t("superAgents.deleteConfirm", { name: selected.name }), { danger: true })) void run("delete", () => api.deleteSuperAgent(selected.id));
                    }}><Trash2 size={14} /> {t("common.delete")}</button>}
                  </div>
                </header>

                <h4>{t("superAgents.workers")}</h4>
                {!selected.auto_workers && selected.workers.length === 0 && <p className="muted">{t("agents.work.chatOnly")}</p>}
                <div className="super-agent-chips">
                  {selected.auto_workers && (selected.worker_runtimes ?? []).map((runtime) => (
                    <span key={runtime} className="super-agent-chip"><i className="online" />{runtime}<small>{t("superAgents.autoWorkersOn")}</small></span>
                  ))}
                  {selected.auto_workers && (selected.worker_machines ?? []).map((machine) => {
                    const status = activity.data?.machines?.find((item) => item.host_id === machine.host_id);
                    return (
                      <span key={machine.host_id} className="super-agent-chip" title={status?.detail || machine.capabilities}>
                        <i className={status?.available ? "online" : "offline"} />
                        {status?.name || machine.host_id}
                        <small>{t("superAgents.workerMachine")}</small>
                      </span>
                    );
                  })}
                  {selected.workers.map((worker) => {
                    const status = activity.data?.workers?.find((item) => item.worker_id === worker.id);
                    return (
                      <span key={worker.id} className="super-agent-chip" title={status?.detail}>
                        <i className={status?.available ? "online" : "offline"} />
                        {worker.label || worker.agent_id}
                        <small>{worker.host_id ? t("superAgents.remote") : t("superAgents.local")}</small>
                      </span>
                    );
                  })}
                </div>

                <h4>{t("superAgents.sources")}</h4>
                <div className="orchestration-task-list">
                  {selected.sources.length === 0 && <p className="muted">{t("superAgents.noSources")}</p>}
                  {selected.sources.map((source) => {
                    const health = activity.data?.sources?.[source.id];
                    return (
                      <article key={source.id}>
                        <header><strong>{t(`superAgents.kind.${source.kind}`)}{isChannelSource(source) && source.name ? ` · ${source.name}` : ""}</strong><span className={`status-badge status-${health?.last_error ? "failed" : health?.warning ? "warning" : source.enabled ? "succeeded" : "cancelled"}`}>{t(source.enabled ? "common.enabled" : "common.disabled")}</span></header>
                        {isChannelSource(source) && <small>{t("agents.work.channelSourceHelp")}</small>}
                        {source.kind === "github_webhook" && (githubPolls(source) ? <small>{t("superAgents.setup.github.pollSummary")}</small> : <code>{sourceHookAddress(source)}</code>)}
                        {health?.last_error && <div className="error">{health.last_error}</div>}
                        {health?.warning && <div className="warning super-agent-source-warning" role="status">{health.warning}</div>}
                        {health?.last_event_at && <time>{t("superAgents.lastEvent")}: {time(health.last_event_at)}</time>}
                      </article>
                    );
                  })}
                </div>

                <h4>{t("superAgents.tryIt")}</h4>
                <div className="control-row super-agent-request">
                  <input aria-label={t("superAgents.tryIt")} placeholder={t("superAgents.tryItPlaceholder")} value={request} onChange={(event) => setRequest(event.target.value)} />
                  <button className="action" disabled={!request.trim() || Boolean(busy)} onClick={() => void run("trigger", async () => {
                    await api.triggerSuperAgent(selected.id, request);
                    setRequest("");
                  })}><Play size={14} /> {t("superAgents.send")}</button>
                </div>

                <h4>{t("superAgents.tickets")}</h4>
                <div className="orchestration-task-list">
                  {(activity.data?.tickets ?? []).length === 0 && <p className="muted">{t("superAgents.noTickets")}</p>}
                  {(activity.data?.tickets ?? []).map((ticket) => (
                    <article key={ticket.id}>
                      <header>
                        <strong>{ticket.title}</strong>
                        <span className="control-row">
                          <span className={`status-badge status-${ticketStatusClass(ticket.status)}`}>{t(`superAgents.ticket.${ticket.status}`)}</span>
                          {["queued", "running"].includes(ticket.status) && <button className="ghost-action danger-action" disabled={Boolean(busy)} onClick={() => void run("stop", () => api.stopSuperAgentTicket(ticket.id))}><Square size={13} /> {t("superAgents.stop")}</button>}
                          {ticket.status === "failed" && <button className="ghost-action" disabled={Boolean(busy)} onClick={() => void run("retry", () => api.retrySuperAgentTicket(ticket.id))}><RotateCcw size={13} /> {t("common.retry")}</button>}
                        </span>
                      </header>
                      <small>{ticket.worker.label || ticket.worker.agent_id} · {ticket.worker.host_id ? (activity.data?.machines?.find((item) => item.host_id === ticket.worker.host_id)?.name ?? t("superAgents.remote")) : t("superAgents.local")}{ticket.actor ? ` · ${ticket.actor}` : ""}</small>
                      {ticket.output && <pre>{ticket.output}</pre>}
                      {ticket.error && <div className="error">{ticket.error}</div>}
                      {ticket.reply_error && <div className="error">{t("superAgents.replyFailed")}: {ticket.reply_error}</div>}
                      <time>{time(ticket.updated_at)}</time>
                    </article>
                  ))}
                </div>

                <h4>{t("superAgents.triggers")}</h4>
                <div className="orchestration-task-list">
                  {(activity.data?.triggers ?? []).length === 0 && <p className="muted">{t("superAgents.noTriggers")}</p>}
                  {(activity.data?.triggers ?? []).map((trigger) => (
                    <article key={trigger.id}>
                      <header>
                        <strong>{trigger.title || trigger.text.slice(0, 80)}</strong>
                        <span className={`status-badge status-${triggerStatusClass(trigger.disposition)}`}>{t(`superAgents.disposition.${trigger.disposition}`)}</span>
                      </header>
                      <small>{trigger.source_kind === "console" ? t("superAgents.kind.console") : t(`superAgents.kind.${trigger.source_kind}`)}{trigger.actor ? ` · ${trigger.actor_name || trigger.actor}` : ""}</small>
                      {trigger.reason && <p>{trigger.reason}</p>}
                      <time>{time(trigger.received_at)}</time>
                    </article>
                  ))}
                </div>
              </>
            ) : <div className="empty-state">{t("superAgents.select")}</div>}
          </div>
        </div>
      </section>
      {draft && (
        <SuperAgentDrawer
          draft={draft}
          notice={notice}
          busy={busy === "save"}
          onChange={setDraft}
          onClose={() => { setDraft(null); setNotice(""); }}
          onSave={(prepare) => void run("save", async () => {
            const saved = await api.saveSuperAgent(await prepare());
            setSelectedID(saved.id);
            setDraft(null);
          })}
        />
      )}
    </div>
  );
}

function ticketStatusClass(status: string) {
  return { queued: "queued", running: "running", ready: "succeeded", failed: "failed", stopped: "cancelled" }[status] ?? status;
}

function triggerStatusClass(disposition: string) {
  return { pending: "queued", accepted: "succeeded", follow_up: "succeeded", answered: "succeeded", observed: "running", failed: "failed" }[disposition] ?? "cancelled";
}
