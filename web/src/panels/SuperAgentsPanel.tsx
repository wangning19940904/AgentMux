import { Bot, Check, Play, Plus, RefreshCw, RotateCcw, Square, Trash2, X } from "lucide-react";
import { useEffect, useRef, useState } from "react";
import { api } from "../api";
import type { SuperAgent, SuperAgentRuntimeHealth } from "../api";
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
  const [declineNotes, setDeclineNotes] = useState<Record<string, string>>({});
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
  const tierLabel = (rosterID: string) => {
    const tier = activity.data?.cost_tiers?.[rosterID];
    return tier ? t(`superAgents.costTier.${tier}`) : "";
  };
  // A framework that is failing or at its usage limit on a machine; unknown
  // and ok show nothing.
  const healthMarks = (hostID: string | undefined, runtimes: Array<string | undefined>) => runtimes
    .map((runtime) => activity.data?.runtime_health?.find((record) => (record.host_id ?? "") === (hostID ?? "") && record.runtime === runtime))
    .filter((record): record is SuperAgentRuntimeHealth => Boolean(record && (record.state === "failing" || record.state === "usage_limit")))
    .map((record) => (
      <span key={`${record.host_id ?? ""}-${record.runtime}`} className={`super-agent-health ${record.state}`} title={record.reason}>
        {runtimes.length > 1 || hostID ? `${record.runtime} · ` : ""}{t(`superAgents.health.${record.state}`)}{record.resets_at ? ` ${t("superAgents.health.until", { time: time(record.resets_at) })}` : ""}
      </span>
    ));

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
                <span><strong>{item.name}</strong><small>{item.auto_workers ? t("superAgents.autoWorkersOn") : t("superAgents.workerCount", { count: item.workers.length })}</small></span>
                <span className={`status-badge status-${item.enabled ? "running" : "cancelled"}`}>{t(item.enabled ? "common.enabled" : "common.disabled")}</span>
              </button>
            ))}
            {!list.loading && items.length === 0 && <div className="empty-state">{t("superAgents.empty")}</div>}
          </div>}
          <div className="orchestration-detail">
            {selected ? (
              <>
                <header>
                  <div><h3>{selected.name}</h3><p className="muted">{selected.description || t("superAgents.defaultDescription")}</p></div>
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
                    <span key={runtime} className="super-agent-chip"><i className="online" />{runtime}<small>{[t("superAgents.autoWorkersOn"), tierLabel(`auto:${runtime}`)].filter(Boolean).join(" · ")}</small>{healthMarks("", [runtime])}</span>
                  ))}
                  {selected.auto_workers && (selected.worker_machines ?? []).map((machine, index) => {
                    const local = !machine.host_id;
                    const status = activity.data?.machines?.find((item) => item.host_id === machine.host_id);
                    const name = local ? t("superAgents.thisMachine") : status?.name || machine.host_id;
                    const tools = [...new Set(Object.values(status?.tools ?? {}).flat())];
                    return (
                      <span key={machine.id || `${machine.host_id}-${index}`} className="super-agent-chip" title={[status?.detail || machine.capabilities, machine.dir].filter(Boolean).join(" · ")}>
                        <i className={local || status?.available ? "online" : "offline"} />
                        {machine.dir ? `${name} · ${machine.dir}` : name}
                        <small>{[t("superAgents.workerMachine"), ...tools.map((tool) => tool === "computer_use" ? t("superAgents.toolComputerUse") : t("superAgents.toolBrowser"))].join(" · ")}</small>
                        {!local && healthMarks(machine.host_id, selected.worker_runtimes ?? [])}
                      </span>
                    );
                  })}
                  {selected.workers.map((worker) => {
                    const status = activity.data?.workers?.find((item) => item.worker_id === worker.id);
                    return (
                      <span key={worker.id} className="super-agent-chip" title={status?.detail}>
                        <i className={status?.available ? "online" : "offline"} />
                        {worker.label || worker.agent_id}
                        <small>{[worker.host_id ? t("superAgents.remote") : t("superAgents.local"), tierLabel(worker.id)].filter(Boolean).join(" · ")}</small>
                        {healthMarks(worker.host_id, [status?.runtime_id])}
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

                {(activity.data?.plans ?? []).length > 0 && (
                  <>
                    <h4>{t("superAgents.plans")}</h4>
                    <div className="orchestration-task-list super-agent-plans">
                      {(activity.data?.plans ?? []).map((plan) => (
                        <article key={plan.id} className={`super-agent-plan plan-${plan.status}`}>
                          <header>
                            <strong>{plan.title}</strong>
                            <span className="control-row">
                              <span className={`super-agent-tier tier-${plan.tier}`}>{t(`superAgents.costTier.${plan.tier}`)}</span>
                              <span className={`status-badge status-${planStatusClass(plan.status)}`}>{t(`superAgents.plan.${plan.status}`)}</span>
                            </span>
                          </header>
                          {(plan.requester_name || plan.requester) && <small>{t("superAgents.plan.requestedBy", { name: plan.requester_name || plan.requester || "" })}</small>}
                          {plan.summary && <p>{plan.summary}</p>}
                          <ol>
                            {plan.tickets.map((item, index) => (
                              <li key={item.ref || index}>
                                <strong>{item.title}</strong>{" "}
                                <small>
                                  {[t("superAgents.plan.item", { worker: item.placed_worker_id || item.worker_id, where: item.where }), t(`superAgents.costTier.${item.tier}`),
                                    (item.depends_on ?? []).length > 0 ? t("superAgents.plan.after", { refs: (item.depends_on ?? []).join(", ") }) : ""].filter(Boolean).join(" · ")}
                                </small>
                                {item.instructions && <div className="muted">{item.instructions}</div>}
                              </li>
                            ))}
                          </ol>
                          {plan.status === "pending" ? (
                            <div className="control-row">
                              <button className="action" disabled={Boolean(busy)} onClick={() => void run("approve-plan", () => api.approveSuperAgentPlan(selected.id, plan.id, false))}><Check size={13} /> {t("superAgents.plan.approve")}</button>
                              {(plan.requester || "") !== "" && <button className="ghost-action" disabled={Boolean(busy)} onClick={() => void run("approve-plan", () => api.approveSuperAgentPlan(selected.id, plan.id, true))}>{t("superAgents.plan.approveRemember", { tier: t(`superAgents.costTier.${plan.tier}`) })}</button>}
                              <input aria-label={t("superAgents.plan.declineNote")} placeholder={t("superAgents.plan.declineNote")} value={declineNotes[plan.id] ?? ""} onChange={(event) => setDeclineNotes({ ...declineNotes, [plan.id]: event.target.value })} />
                              <button className="ghost-action danger-action" disabled={Boolean(busy)} onClick={() => void run("decline-plan", () => api.declineSuperAgentPlan(selected.id, plan.id, declineNotes[plan.id] ?? ""))}><X size={13} /> {t("superAgents.plan.decline")}</button>
                            </div>
                          ) : (
                            <small>{plan.decided_by ? t("superAgents.plan.decidedBy", { status: t(`superAgents.plan.${plan.status}`), name: plan.decided_by }) : ""}{plan.note ? ` · ${plan.note}` : ""}</small>
                          )}
                          <time>{time(plan.decided_at || plan.created_at)}</time>
                        </article>
                      ))}
                    </div>
                  </>
                )}

                {(activity.data?.trust ?? []).length > 0 && (
                  <>
                    <h4>{t("superAgents.trust")}</h4>
                    <div className="orchestration-task-list super-agent-trust">
                      {(activity.data?.trust ?? []).map((entry) => (
                        <article key={entry.requester}>
                          <header>
                            <strong>{t("superAgents.trust.entry", { name: entry.requester_name || entry.requester, tier: t(`superAgents.costTier.${entry.tier}`) })}</strong>
                            <button className="ghost-action danger-action" disabled={Boolean(busy)} onClick={() => void run("revoke-trust", () => api.revokeSuperAgentTrust(selected.id, entry.requester))}><X size={13} /> {t("superAgents.trust.revoke")}</button>
                          </header>
                          <time>{entry.approved_by ? `${entry.approved_by} · ` : ""}{time(entry.at)}</time>
                        </article>
                      ))}
                    </div>
                  </>
                )}

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
                      {ticket.workspace?.held_since && ["queued", "running"].includes(ticket.status) && (
                        <small className="super-agent-ticket-holds">{t("superAgents.ticketHolds", { dir: ticket.workspace.dir ?? "", machine: ticket.workspace.host_id ? (activity.data?.machines?.find((item) => item.host_id === ticket.workspace?.host_id)?.name ?? ticket.workspace.host_id) : t("superAgents.thisMachine") })}</small>
                      )}
                      {ticket.placement && (
                        <small className="super-agent-ticket-moved" title={ticket.placement.reason}>
                          {t("superAgents.ticketMoved", { from: ticket.placement.requested_worker_id, to: ticket.placement.worker_id, machine: ticket.placement.machine || t("superAgents.thisMachine") })}: {ticket.placement.reason}
                        </small>
                      )}
                      {(ticket.done_when ?? []).length > 0 && (
                        <div className="super-agent-ticket-criteria">
                          <small>{t("superAgents.ticketDoneWhen")}{ticket.expect_minutes ? ` · ${t("superAgents.ticketExpect", { minutes: ticket.expect_minutes })}` : ""}</small>
                          <ul>{(ticket.done_when ?? []).map((criterion) => <li key={criterion}>{criterion}</li>)}</ul>
                        </div>
                      )}
                      {(ticket.signals ?? []).filter((signal) => !signal.cleared_at).map((signal) => (
                        <div key={`${signal.kind}-${signal.since}`} className="warning super-agent-source-warning" role="status">
                          {t(`superAgents.signal.${signal.kind}`)}: {signal.detail}
                        </div>
                      ))}
                      {(() => {
                        const review = ticket.reviews?.[ticket.reviews.length - 1];
                        if (!review) return null;
                        const label = review.refused ? t("superAgents.review.refused")
                          : review.verdict === "rework" ? t("superAgents.review.reworkRound", { round: review.round ?? 0, limit: 2 })
                          : t(`superAgents.review.${review.verdict}`);
                        return (
                          <div className="super-agent-ticket-review">
                            <small><strong>{t("superAgents.review.title")}:</strong> {label}{review.note ? ` · ${review.note}` : ""}</small>
                            {(review.checks ?? []).length > 0 && (
                              <ul>{(review.checks ?? []).map((check) => <li key={check.criterion}>{check.met ? "✓" : "✗"} {check.criterion}{check.evidence ? ` (${check.evidence})` : ""}</li>)}</ul>
                            )}
                          </div>
                        );
                      })()}
                      {ticket.auto_review && (
                        <details className="super-agent-ticket-auto-review">
                          <summary><small>{ticket.auto_review.error ? t("superAgents.autoReview.failed", { runtime: ticket.auto_review.runtime })
                            : ticket.auto_review.count >= 0 ? t("superAgents.autoReview.summary", { runtime: ticket.auto_review.runtime, count: ticket.auto_review.count, blocking: ticket.auto_review.blocking })
                            : t("superAgents.autoReview.summaryUncounted", { runtime: ticket.auto_review.runtime })}</small></summary>
                          <pre>{ticket.auto_review.error || ticket.auto_review.findings}</pre>
                        </details>
                      )}
                      {ticket.output && <pre>{ticket.output}</pre>}
                      {ticket.error && <div className="error">{ticket.error}</div>}
                      {ticket.reply_error && <div className="error">{t("superAgents.replyFailed")}: {ticket.reply_error}</div>}
                      <time>{time(ticket.updated_at)}</time>
                    </article>
                  ))}
                </div>

                {(activity.data?.workspace_claims ?? []).length > 0 && (
                  <>
                    <h4>{t("superAgents.workspaceClaims")}</h4>
                    <div className="orchestration-task-list super-agent-workspace-claims">
                      {(activity.data?.workspace_claims ?? []).map((claim) => (
                        <article key={`${claim.ticket_id}-${claim.dir}`}>
                          <header><strong>{claim.dir}</strong><span className="status-badge status-running">{claim.host_id ? claim.machine : t("superAgents.thisMachine")}</span></header>
                          <small>{claim.title} · {claim.ticket_id}{claim.agent_id !== selected.id ? ` · ${t("superAgents.workspaceClaimOther", { agent: claim.agent_id })}` : ""}</small>
                          <time>{time(claim.since)}</time>
                        </article>
                      ))}
                    </div>
                  </>
                )}

                {((activity.data?.timers ?? []).length > 0 || activity.data?.timer_warning) && (
                  <>
                    <h4>{t("superAgents.timers")}</h4>
                    {activity.data?.timer_warning && <div className="warning super-agent-source-warning" role="status">{activity.data.timer_warning}</div>}
                    <div className="orchestration-task-list">
                      {(activity.data?.timers ?? []).map((timer) => {
                        const about = timer.ticket_id ? activity.data?.tickets.find((ticket) => ticket.id === timer.ticket_id)?.title : "";
                        return (
                          <article key={timer.id}>
                            <header>
                              <strong>{timer.note}</strong>
                              <span className="control-row">
                                <span className={`status-badge status-${timerStatusClass(timer.status)}`}>{t(`superAgents.timer.${timer.status}`)}</span>
                                {timer.status === "active" && <button className="ghost-action danger-action" disabled={Boolean(busy)} onClick={() => void run("cancel-timer", () => api.cancelSuperAgentTimer(timer.id))}><X size={13} /> {t("common.cancel")}</button>}
                              </span>
                            </header>
                            <small>{timer.thread_key}{about ? ` · ${about}` : ""}</small>
                            {timer.status === "active" && <time>{t("superAgents.timerDue")}: {time(timer.due_at)}</time>}
                            {timer.status === "done" && <time>{t("superAgents.timerFired")}: {time(timer.fired_at || timer.updated_at)}</time>}
                            {timer.status === "cancelled" && <time>{t("superAgents.timerCancelled")}: {time(timer.updated_at)}</time>}
                          </article>
                        );
                      })}
                    </div>
                  </>
                )}

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

function planStatusClass(status: string) {
  return { pending: "queued", approved: "succeeded", declined: "cancelled", expired: "cancelled" }[status] ?? status;
}

function timerStatusClass(status: string) {
  return { active: "queued", done: "succeeded", cancelled: "cancelled" }[status] ?? status;
}

function triggerStatusClass(disposition: string) {
  return { pending: "queued", accepted: "succeeded", follow_up: "succeeded", answered: "succeeded", observed: "running", failed: "failed" }[disposition] ?? "cancelled";
}
