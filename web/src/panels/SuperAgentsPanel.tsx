import { Bot, Play, Plus, RefreshCw, RotateCcw, Save, Sparkles, Square, Trash2, X } from "lucide-react";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { api } from "../api";
import type { AgentInstance, SuperAgent, SuperAgentSetupResult, SuperAgentSource, SuperAgentSourceKind, SuperAgentWorker } from "../api";
import { usePolling } from "../hooks/usePolling";
import { useI18n } from "../i18n";
import { useAsync } from "../useAsync";
import { SOURCE_FIELDS, SOURCE_KINDS, githubWebhookPath, newSource, newSuperAgent, newWorker, parseActors, superAgentProblems } from "./superAgentModel";

export type SuperAgentsPanelProps = {
  createRequested?: boolean;
  onCreateRequestHandled?: () => void;
};

export function SuperAgentsPanel({ createRequested = false, onCreateRequestHandled }: SuperAgentsPanelProps = {}) {
  const { t, language } = useI18n();
  const list = useAsync(() => api.superAgents(), []);
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
        <div className="surface-header">
          <div><h2>{t("superAgents.title")}</h2><p>{t("superAgents.subtitle")}</p></div>
          <div className="control-row">
            <button className="ghost-action" onClick={() => void list.reload()}><RefreshCw size={15} /> {t("common.refresh")}</button>
            <button className="action" onClick={() => setDraft(newSuperAgent())}><Plus size={15} /> {t("agents.newSuper")}</button>
          </div>
        </div>
        {notice && !draft && <div className="surface-body session-notice error" role="alert">{notice}</div>}
        {list.error && <div className="surface-body session-notice error" role="alert">{String(list.error)}</div>}
        <div className="orchestration-layout">
          <div className="orchestration-list">
            {items.map((item) => (
              <button key={item.id} className={selected?.id === item.id ? "active" : ""} onClick={() => setSelectedID(item.id)}>
                <Bot size={16} />
                <span><strong>{item.name}</strong><small>{t(`superAgents.mode.${item.mode}`)} · {item.auto_workers ? t("superAgents.autoWorkersOn") : t("superAgents.workerCount", { count: item.workers.length })}</small></span>
                <span className={`status-badge status-${item.enabled ? "running" : "cancelled"}`}>{t(item.enabled ? "common.enabled" : "common.disabled")}</span>
              </button>
            ))}
            {!list.loading && items.length === 0 && <div className="empty-state">{t("superAgents.empty")}</div>}
          </div>
          <div className="orchestration-detail">
            {selected ? (
              <>
                <header>
                  <div><h3>{selected.name}</h3><p className="muted">{selected.description || t(`superAgents.modeHelp.${selected.mode}`)}</p></div>
                  <div className="control-row">
                    <button className="ghost-action" onClick={() => setDraft(structuredClone(selected))}>{t("common.edit")}</button>
                    <button className="ghost-action danger-action" disabled={Boolean(busy)} onClick={() => {
                      if (window.confirm(t("superAgents.deleteConfirm", { name: selected.name }))) void run("delete", () => api.deleteSuperAgent(selected.id));
                    }}><Trash2 size={14} /> {t("common.delete")}</button>
                  </div>
                </header>

                <h4>{t("superAgents.workers")}</h4>
                <div className="super-agent-chips">
                  {selected.auto_workers && <span className="super-agent-chip"><i className="online" />{t("superAgents.autoWorkersOn")}</span>}
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
                        <header><strong>{t(`superAgents.kind.${source.kind}`)}</strong><span className={`status-badge status-${health?.last_error ? "failed" : source.enabled ? "succeeded" : "cancelled"}`}>{t(source.enabled ? "common.enabled" : "common.disabled")}</span></header>
                        {source.kind === "github_webhook" && <code>{window.location.origin}{githubWebhookPath(source)}</code>}
                        {health?.last_error && <div className="error">{health.last_error}</div>}
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
                      <small>{ticket.worker.label || ticket.worker.agent_id} · {ticket.worker.host_id ? t("superAgents.remote") : t("superAgents.local")}{ticket.actor ? ` · ${ticket.actor}` : ""}</small>
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
          onSave={() => void run("save", async () => {
            const saved = await api.saveSuperAgent(draft);
            setSelectedID(saved.id);
            setDraft(null);
          })}
        />
      )}
    </div>
  );
}

function isLocalTarget(targetID?: string) {
  return !targetID || targetID === "local";
}

function ticketStatusClass(status: string) {
  return { queued: "queued", running: "running", ready: "succeeded", failed: "failed", stopped: "cancelled" }[status] ?? status;
}

function triggerStatusClass(disposition: string) {
  return { pending: "queued", accepted: "succeeded", follow_up: "succeeded", observed: "running", failed: "failed" }[disposition] ?? "cancelled";
}

function SuperAgentDrawer({ draft, notice, busy, onChange, onClose, onSave }: {
  draft: SuperAgent;
  notice: string;
  busy: boolean;
  onChange: (next: SuperAgent) => void;
  onClose: () => void;
  onSave: () => void;
}) {
  const { t } = useI18n();
  const agents = useAsync(() => api.agentInstances(), []);
  const hosts = useAsync(() => api.remoteHosts().catch(() => []), []);
  const channels = useAsync(() => api.channels(), []);
  const [remoteAgents, setRemoteAgents] = useState<Record<string, AgentInstance[]>>({});
  // In the "all machines" scope the API tags this daemon's resources as "local".
  // An Agent a Super Agent launched for itself is not something to route through.
  const localAgents = useMemo(() => (agents.data ?? []).filter((agent) => isLocalTarget(agent.target_id) && agent.source !== "super_agent"), [agents.data]);
  const feishuChannels = useMemo(() => (channels.data ?? []).filter((channel) => isLocalTarget(channel.target_id) && (channel.type === "feishu" || channel.type === "lark")), [channels.data]);
  const problems = superAgentProblems(draft);
  const [instruction, setInstruction] = useState("");
  const [assisting, setAssisting] = useState(false);
  const [assistError, setAssistError] = useState("");
  const [setup, setSetup] = useState<SuperAgentSetupResult | null>(null);
  const sourceField = (source: SuperAgentSource, key: string) => SOURCE_FIELDS[source.kind].find((field) => field.key === key);

  async function assist() {
    setAssisting(true);
    setAssistError("");
    try {
      const result = await api.setupSuperAgent(draft, instruction, setup?.conversation_id);
      setSetup(result);
      setInstruction("");
      onChange(result.draft);
    } catch (error) {
      setAssistError(error instanceof Error ? error.message : String(error));
    } finally {
      setAssisting(false);
    }
  }

  const loadRemoteAgents = useCallback(async (hostID: string) => {
    if (!hostID) return;
    try {
      const found = await api.remoteAgentInstances(hostID);
      setRemoteAgents((current) => ({ ...current, [hostID]: found ?? [] }));
    } catch {
      setRemoteAgents((current) => ({ ...current, [hostID]: [] }));
    }
  }, []);

  const hostIDs = draft.workers.map((worker) => worker.host_id ?? "").join("|");
  useEffect(() => {
    for (const hostID of new Set(hostIDs.split("|"))) void loadRemoteAgents(hostID);
  }, [hostIDs, loadRemoteAgents]);

  const updateWorker = (index: number, patch: Partial<SuperAgentWorker>) =>
    onChange({ ...draft, workers: draft.workers.map((worker, position) => position === index ? { ...worker, ...patch } : worker) });
  const updateSource = (index: number, patch: Partial<SuperAgentSource>) =>
    onChange({ ...draft, sources: draft.sources.map((source, position) => position === index ? { ...source, ...patch } : source) });

  return (
    <div className="provider-drawer-layer">
      <button className="provider-drawer-backdrop" type="button" aria-label={t("common.close")} onClick={onClose} />
      <aside className="provider-drawer agent-drawer" role="dialog" aria-modal="true" aria-labelledby="super-agent-drawer-title">
        <div className="provider-builder-head">
          <div className="provider-form-title">
            <div>
              <h2 id="super-agent-drawer-title">{draft.id ? draft.name : t("agents.newSuper")}</h2>
              <span className="muted">{t("superAgents.drawerSubtitle")}</span>
            </div>
          </div>
          <div className="table-actions">
            <button className="ghost-action icon-action" onClick={onClose} title={t("common.close")}><X size={15} /></button>
          </div>
        </div>
        <div className="surface-body provider-builder-body agent-drawer-body super-agent-form">
          <label className="field"><span>{t("superAgents.router")}</span>
            <select value={draft.router_agent_id} onChange={(event) => onChange({ ...draft, router_agent_id: event.target.value })}>
              <option value="">{t("superAgents.chooseAgent")}</option>
              {localAgents.map((agent) => <option key={agent.id} value={agent.id}>{agent.name} ({agent.runtime_id})</option>)}
            </select>
            <small>{t("superAgents.routerHelp")}</small>
          </label>

          <label className="field"><span>{t("superAgents.instruction")}</span>
            <textarea rows={4} placeholder={t("superAgents.instructionPlaceholder")} value={instruction} onChange={(event) => setInstruction(event.target.value)} />
            <small>{t("superAgents.instructionHelp")}</small>
          </label>
          <div className="control-row">
            <button className="ghost-action" disabled={assisting || !draft.router_agent_id || !instruction.trim()} onClick={() => void assist()}><Sparkles size={14} /> {t(assisting ? "superAgents.assisting" : "superAgents.assist")}</button>
          </div>
          {assistError && <div className="session-notice error" role="alert">{assistError}</div>}
          {setup?.reply && <p className="super-agent-assist-reply" role="status">{setup.reply}</p>}
          {(setup || draft.sources.length > 0) && (
            <div className="super-agent-card super-agent-proposal">
              <strong>{t("superAgents.proposal")}</strong>
              <small className="muted">{t(`superAgents.modeHelp.${draft.mode}`)}</small>
              {draft.sources.length === 0 && <small className="muted">{t("superAgents.noSources")}</small>}
              {draft.sources.map((source) => (
                <div key={source.id}>
                  <div>{t(`superAgents.kind.${source.kind}`)}{source.enabled ? "" : ` (${t("common.disabled")})`}</div>
                  <small className="muted">{t("superAgents.allowedActors")}: {source.allowed_actors.join(", ") || "-"}</small>
                </div>
              ))}
              {setup?.problems.map((problem) => <small className="super-agent-problem" key={problem}>{problem}</small>)}
              {setup?.missing_secrets.map((secret) => {
                const index = draft.sources.findIndex((source) => source.id === secret.source_id);
                const source = draft.sources[index];
                const field = source && sourceField(source, secret.key);
                if (!source || !field) return null;
                return (
                  <label className="field" key={`${secret.source_id}/${secret.key}`}>
                    <span>{t(field.labelKey)} *</span>
                    <input type="password" autoComplete="off" value={source.config[secret.key] ?? ""} onChange={(event) => updateSource(index, { config: { ...source.config, [secret.key]: event.target.value } })} />
                  </label>
                );
              })}
              <small className="muted">{t("superAgents.proposalHelp")}</small>
            </div>
          )}

          <details className="super-agent-advanced">
            <summary>{t("superAgents.advanced")}</summary>
            <div className="super-agent-form">
          <label className="field"><span>{t("superAgents.name")}</span><input placeholder={t("superAgents.namePlaceholder")} value={draft.name} onChange={(event) => onChange({ ...draft, name: event.target.value })} /></label>
          <label className="field"><span>{t("superAgents.routerPrompt")}</span><textarea rows={3} value={draft.router_prompt ?? ""} onChange={(event) => onChange({ ...draft, router_prompt: event.target.value })} /></label>
          <div className="field-grid">
            <label className="field"><span>{t("superAgents.modeLabel")}</span>
              <select value={draft.mode} onChange={(event) => onChange({ ...draft, mode: event.target.value as SuperAgent["mode"] })}>
                <option value="observe">{t("superAgents.mode.observe")}</option>
                <option value="act">{t("superAgents.mode.act")}</option>
              </select>
              <small>{t(`superAgents.modeHelp.${draft.mode}`)}</small>
            </label>
            <label className="field"><span>{t("superAgents.concurrency")}</span><input type="number" min={1} max={32} value={draft.max_concurrency} onChange={(event) => onChange({ ...draft, max_concurrency: Number(event.target.value) })} /></label>
          </div>
          <label className="super-agent-check"><input type="checkbox" checked={draft.enabled} onChange={(event) => onChange({ ...draft, enabled: event.target.checked })} /> {t("common.enabled")}</label>

          <label className="super-agent-check"><input type="checkbox" checked={Boolean(draft.auto_workers)} onChange={(event) => onChange({ ...draft, auto_workers: event.target.checked })} /> {t("superAgents.autoWorkers")}</label>
          <p className="muted">{t("superAgents.autoWorkersHelp")}</p>
          {draft.auto_workers && (
            <div className="field-grid">
              <label className="field"><span>{t("superAgents.autoWorkerDir")}</span><input placeholder={t("superAgents.autoWorkerDirPlaceholder")} value={draft.auto_worker_dir ?? ""} onChange={(event) => onChange({ ...draft, auto_worker_dir: event.target.value })} /></label>
              <label className="field"><span>{t("superAgents.autoWorkerApproval")}</span>
                <select value={draft.auto_worker_approval_mode || "auto_edit"} onChange={(event) => onChange({ ...draft, auto_worker_approval_mode: event.target.value })}>
                  <option value="auto_edit">auto_edit</option>
                  <option value="auto">auto</option>
                  <option value="yolo">yolo</option>
                </select>
                <small>{t("superAgents.autoWorkerApprovalHelp")}</small>
              </label>
            </div>
          )}

          <div className="super-agent-section-head">
            <h4>{t("superAgents.workers")}</h4>
            <button className="ghost-action" onClick={() => onChange({ ...draft, workers: [...draft.workers, newWorker()] })}><Plus size={14} /> {t("superAgents.addWorker")}</button>
          </div>
          <p className="muted">{t("superAgents.workersHelp")}</p>
          {draft.workers.map((worker, index) => {
            const options = worker.host_id ? remoteAgents[worker.host_id] ?? [] : localAgents;
            return (
              <div className="super-agent-card" key={worker.id || `new-${index}`}>
                <div className="field-grid">
                  <label className="field"><span>{t("superAgents.machine")}</span>
                    <select value={worker.host_id ?? ""} onChange={(event) => updateWorker(index, { host_id: event.target.value, agent_id: "" })}>
                      <option value="">{t("superAgents.thisMachine")}</option>
                      {(hosts.data ?? []).map((host) => <option key={host.id} value={host.id}>{host.name}</option>)}
                    </select>
                  </label>
                  <label className="field"><span>{t("superAgents.agent")}</span>
                    <select value={worker.agent_id} onChange={(event) => updateWorker(index, { agent_id: event.target.value })}>
                      <option value="">{t("superAgents.chooseAgent")}</option>
                      {worker.agent_id && !options.some((agent) => agent.id === worker.agent_id) && <option value={worker.agent_id}>{worker.agent_id}</option>}
                      {options.map((agent) => <option key={agent.id} value={agent.id}>{agent.name} ({agent.runtime_id})</option>)}
                    </select>
                  </label>
                </div>
                <label className="field"><span>{t("superAgents.capabilities")}</span><input placeholder={t("superAgents.capabilitiesPlaceholder")} value={worker.capabilities ?? ""} onChange={(event) => updateWorker(index, { capabilities: event.target.value })} /></label>
                {(draft.workers.length > 1 || draft.auto_workers) && <button className="ghost-action danger-action" onClick={() => onChange({ ...draft, workers: draft.workers.filter((_, position) => position !== index) })}><Trash2 size={13} /> {t("superAgents.remove")}</button>}
              </div>
            );
          })}

          <div className="super-agent-section-head">
            <h4>{t("superAgents.sources")}</h4>
            <select aria-label={t("superAgents.addSource")} value="" onChange={(event) => {
              if (event.target.value) onChange({ ...draft, sources: [...draft.sources, newSource(event.target.value as SuperAgentSourceKind)] });
            }}>
              <option value="">{t("superAgents.addSource")}</option>
              {SOURCE_KINDS.map((kind) => <option key={kind} value={kind}>{t(`superAgents.kind.${kind}`)}</option>)}
            </select>
          </div>
          {draft.sources.map((source, index) => (
            <div className="super-agent-card" key={source.id || `new-${index}`}>
              <div className="super-agent-section-head">
                <strong>{t(`superAgents.kind.${source.kind}`)}</strong>
                <span className="control-row">
                  <label className="super-agent-check"><input type="checkbox" checked={source.enabled} onChange={(event) => updateSource(index, { enabled: event.target.checked })} /> {t("common.enabled")}</label>
                  <button className="ghost-action danger-action" onClick={() => onChange({ ...draft, sources: draft.sources.filter((_, position) => position !== index) })}><Trash2 size={13} /> {t("superAgents.remove")}</button>
                </span>
              </div>
              <p className="muted">{t(`superAgents.kindHelp.${source.kind}`)}</p>
              {SOURCE_FIELDS[source.kind].map((field) => {
                const value = source.config[field.key] ?? "";
                const setValue = (next: string) => updateSource(index, { config: { ...source.config, [field.key]: next } });
                return (
                  <label className="field" key={field.key}>
                    <span>{t(field.labelKey)}{field.required ? " *" : ""}</span>
                    {field.channel ? (
                      <select value={value} onChange={(event) => setValue(event.target.value)}>
                        <option value="">{t("superAgents.chooseChannel")}</option>
                        {feishuChannels.map((channel) => <option key={channel.id} value={channel.id}>{channel.name}</option>)}
                      </select>
                    ) : field.multiline ? (
                      <textarea rows={3} placeholder={field.placeholder} value={value} onChange={(event) => setValue(event.target.value)} />
                    ) : (
                      <input type={field.secret ? "password" : "text"} autoComplete="off" placeholder={field.placeholder} value={value} onChange={(event) => setValue(event.target.value)} />
                    )}
                  </label>
                );
              })}
              <label className="field">
                <span>{t("superAgents.allowedActors")} *</span>
                <textarea rows={2} value={source.allowed_actors.join(", ")} onChange={(event) => updateSource(index, { allowed_actors: parseActors(event.target.value) })} />
                <small>{t(`superAgents.actorsHelp.${source.kind}`)}</small>
              </label>
            </div>
          ))}
            </div>
          </details>
          {problems.length > 0 && <ul className="super-agent-problems">{problems.map((problem) => <li key={problem}>{t(problem)}</li>)}</ul>}
          <div className="agent-drawer-actions">
            {notice && <div className="session-notice error super-agent-save-error" role="alert">{notice}</div>}
            <button className="ghost-action" onClick={onClose}>{t("common.cancel")}</button>
            <button className="action" disabled={busy || problems.length > 0} onClick={onSave}><Save size={15} /> {t("common.save")}</button>
          </div>
        </div>
      </aside>
    </div>
  );
}
