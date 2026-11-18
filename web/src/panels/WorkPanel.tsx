import { AlarmClock, ChevronDown, Inbox, ListChecks, RefreshCw, Search, Square, TriangleAlert, X } from "lucide-react";
import { useMemo, useState } from "react";
import { api } from "../api";
import { confirmAction } from "../components/ConfirmDialog";
import { usePolling } from "../hooks/usePolling";
import { useI18n } from "../i18n";
import { useAsync } from "../useAsync";
import { ticketStatusClass, timerStatusClass } from "./superAgentCards";
import {
  filterWork,
  flattenWork,
  formatDuration,
  isActiveTicket,
  ticketSince,
  workAgents,
  workCounts,
  type WorkRouting,
  type WorkTicket,
  type WorkTimer,
} from "./workModel";

const POLL_INTERVAL_MS = 5_000;

// WorkPanel shows what every agent on every machine is doing: the requests
// still with a router, the Tickets its workers hold and the follow-ups the
// routers set, with a way to stop each.
export function WorkPanel() {
  const { t, language } = useI18n();
  const machines = useAsync(() => api.superAgentWork(), []);
  usePolling(machines.reload, POLL_INTERVAL_MS);
  const [agent, setAgent] = useState("");
  const [query, setQuery] = useState("");
  const [busy, setBusy] = useState("");
  const [notice, setNotice] = useState("");

  const all = useMemo(() => flattenWork(machines.data ?? []), [machines.data]);
  const agents = useMemo(() => workAgents(all), [all]);
  const work = useMemo(() => filterWork(all, { agent, query }), [all, agent, query]);
  const counts = workCounts(work);
  const activeTickets = work.tickets.filter(isActiveTicket);
  const finishedTickets = work.tickets.filter((ticket) => !isActiveTicket(ticket));
  const setTimers = work.timers.filter((timer) => timer.status === "active");
  const pastTimers = work.timers.filter((timer) => timer.status !== "active");
  const locale = language === "zh" ? "zh-CN" : "en-US";
  const time = (value?: string) => value ? new Date(value).toLocaleString(locale, { month: "short", day: "numeric", hour: "2-digit", minute: "2-digit" }) : "";
  const since = (value?: string) => value ? formatDuration(Date.now() - Date.parse(value)) : "";
  const machineLabel = (targetID: string, name: string) => targetID === "local" ? t("remote.localMachine") : name || targetID;
  const multiMachine = new Set((machines.data ?? []).map((machine) => machine.target_id)).size > 1;

  async function run(label: string, action: () => Promise<unknown>) {
    setBusy(label);
    setNotice("");
    try {
      await action();
      await machines.reload();
    } catch (error) {
      setNotice(error instanceof Error ? error.message : String(error));
    } finally {
      setBusy("");
    }
  }

  async function stopTicket(ticket: WorkTicket) {
    if (!(await confirmAction(t("work.stopConfirm", { title: ticket.title }), { danger: true, confirmLabel: t("superAgents.stop") }))) return;
    await run(`stop:${ticket.id}`, () => api.stopSuperAgentTicket(ticket.id, ticket.target_id));
  }

  // The agent, and its machine when there is more than one.
  const owner = (item: WorkRouting | WorkTicket | WorkTimer) => multiMachine
    ? `${item.super_agent_name || item.super_agent_id} · ${machineLabel(item.target_id, item.target_name)}`
    : item.super_agent_name || item.super_agent_id;

  const eventKind = (item: WorkRouting) => item.kind
    ? t(`work.eventKind.${item.kind}`)
    : t(`superAgents.kind.${item.source_kind || "console"}`);

  const ticketCard = (ticket: WorkTicket) => {
    const worker = ticket.worker.label || ticket.worker.agent_id || ticket.worker.id;
    const workerMachine = ticket.machine_name || machineLabel(ticket.target_id, ticket.target_name);
    const requester = ticket.requester_name || ticket.actor;
    const active = isActiveTicket(ticket);
    const openSignals = (ticket.signals ?? []).filter((signal) => !signal.cleared_at);
    return (
      <article key={`${ticket.target_id}-${ticket.id}`} className={`work-item work-ticket ticket-${ticket.status}`}>
        <header>
          <strong>{ticket.title}</strong>
          <span className="control-row">
            <span className={`status-badge status-${ticketStatusClass(ticket.status)}`}>{t(`superAgents.ticket.${ticket.status}`)}</span>
            {active && (
              <button className="ghost-action danger-action" disabled={Boolean(busy)} onClick={() => void stopTicket(ticket)}>
                <Square size={13} /> {t("superAgents.stop")}
              </button>
            )}
          </span>
        </header>
        {ticket.request && <blockquote className="work-request">{ticket.request}</blockquote>}
        <dl className="work-facts">
          {requester && <div><dt>{t("work.requester")}</dt><dd>{requester}</dd></div>}
          <div><dt>{t("work.agent")}</dt><dd>{owner(ticket)}</dd></div>
          <div><dt>{t("work.worker")}</dt><dd>{worker} · {workerMachine}</dd></div>
          <div>
            <dt>{active ? (ticket.status === "running" ? t("work.runningFor") : t("work.queuedFor")) : t("work.finished")}</dt>
            <dd>{active ? since(ticketSince(ticket)) : time(ticket.finished_at || ticket.updated_at)}</dd>
          </div>
          {(ticket.pending_inputs ?? 0) > 0 && <div><dt>{t("work.waitingInputs")}</dt><dd>{ticket.pending_inputs}</dd></div>}
        </dl>
        {openSignals.map((signal) => (
          <div key={`${signal.kind}-${signal.since}`} className="warning super-agent-source-warning" role="status">
            {t(`superAgents.signal.${signal.kind}`)}: {signal.detail}
          </div>
        ))}
        {ticket.error && <div className="error">{ticket.error}</div>}
        {ticket.output && (
          <details className="work-output">
            <summary><ChevronDown size={13} /> {active ? t("work.latestOutput") : t("work.result")}</summary>
            <pre>{ticket.output}</pre>
          </details>
        )}
      </article>
    );
  };

  const timerCard = (timer: WorkTimer) => (
    <article key={`${timer.target_id}-${timer.id}`} className="work-item">
      <header>
        <strong>{timer.note}</strong>
        <span className="control-row">
          <span className={`status-badge status-${timerStatusClass(timer.status)}`}>{t(`superAgents.timer.${timer.status}`)}</span>
          {timer.status === "active" && (
            <button className="ghost-action danger-action" disabled={Boolean(busy)} onClick={() => void run(`cancel:${timer.id}`, () => api.cancelSuperAgentTimer(timer.id, timer.target_id))}>
              <X size={13} /> {t("common.cancel")}
            </button>
          )}
        </span>
      </header>
      <dl className="work-facts">
        <div><dt>{t("work.agent")}</dt><dd>{owner(timer)}</dd></div>
        {timer.ticket_title && <div><dt>{t("work.about")}</dt><dd>{timer.ticket_title}</dd></div>}
        <div><dt>{t("work.thread")}</dt><dd className="work-mono">{timer.thread_key}</dd></div>
        {timer.status === "active" && <div><dt>{t("superAgents.timerDue")}</dt><dd>{time(timer.due_at)} · {t("work.inDuration", { duration: formatDuration(Date.parse(timer.due_at) - Date.now()) })}</dd></div>}
        {timer.status === "done" && <div><dt>{t("superAgents.timerFired")}</dt><dd>{time(timer.fired_at || timer.updated_at)}</dd></div>}
        {timer.status === "cancelled" && <div><dt>{t("superAgents.timerCancelled")}</dt><dd>{time(timer.updated_at)}</dd></div>}
      </dl>
    </article>
  );

  const routingCard = (item: WorkRouting) => (
    <article key={`${item.target_id}-${item.id}`} className="work-item">
      <header>
        <strong>{item.title || [item.actor_name || item.actor, eventKind(item)].filter(Boolean).join(" · ")}</strong>
        <span className={`status-badge status-${item.routing ? "running" : "queued"}`}>{item.routing ? t("work.routingNow") : t("work.routingWaiting")}</span>
      </header>
      {item.text && <blockquote className="work-request">{item.text}</blockquote>}
      <dl className="work-facts">
        {item.title && (item.actor_name || item.actor) && <div><dt>{t("work.requester")}</dt><dd>{item.actor_name || item.actor}</dd></div>}
        <div><dt>{t("work.agent")}</dt><dd>{owner(item)}</dd></div>
        {item.title && <div><dt>{t("work.from")}</dt><dd>{eventKind(item)}</dd></div>}
        <div><dt>{t("work.received")}</dt><dd>{t("work.ago", { duration: since(item.received_at) })}</dd></div>
      </dl>
    </article>
  );

  const firstLoad = machines.loading && !machines.data;
  return (
    <div className="page-stack work-page">
      <section className="surface">
        <div className="surface-header">
          <div><h2>{t("work.title")}</h2><p>{t("work.subtitle")}</p></div>
          <div className="control-row">
            <button className="ghost-action" onClick={() => void machines.reload()}><RefreshCw size={15} /> {t("common.refresh")}</button>
          </div>
        </div>
        <div className="surface-body work-toolbar">
          <div className="work-summary" role="status">
            <span><Inbox size={14} /> {t("work.count.routing", { count: counts.routing })}</span>
            <span><ListChecks size={14} /> {t("work.count.running", { count: counts.running })}</span>
            <span>{t("work.count.queued", { count: counts.queued })}</span>
            <span><AlarmClock size={14} /> {t("work.count.timers", { count: counts.timers })}</span>
          </div>
          <div className="control-row work-filters">
            <label className="work-search">
              <Search size={14} />
              <input aria-label={t("work.search")} placeholder={t("work.search")} value={query} onChange={(event) => setQuery(event.target.value)} />
            </label>
            <select aria-label={t("work.agentFilter")} value={agent} onChange={(event) => setAgent(event.target.value)}>
              <option value="">{t("work.allAgents")}</option>
              {agents.map((item) => (
                <option key={item.key} value={item.key}>{multiMachine ? `${item.name} · ${machineLabel(item.target_id, item.target_name)}` : item.name}</option>
              ))}
            </select>
          </div>
        </div>
        {notice && <div className="surface-body session-notice error" role="alert">{notice}</div>}
        {machines.error && !machines.data && <div className="surface-body session-notice error" role="alert">{machines.error}</div>}
        {all.unreachable.length > 0 && (
          <div className="surface-body work-unreachable" role="status">
            <TriangleAlert size={14} />
            <span>{t("work.unreachable", { machines: all.unreachable.map((machine) => machineLabel(machine.target_id, machine.target_name)).join(", ") })}</span>
            <small title={all.unreachable.map((machine) => `${machineLabel(machine.target_id, machine.target_name)}: ${machine.error}`).join("\n")}>{t("work.unreachableDetail")}</small>
          </div>
        )}
      </section>

      {firstLoad ? <div className="empty-state">{t("common.loading")}</div> : (
        <>
          {work.routing.length > 0 && (
            <section className="surface">
              <div className="surface-header"><div><h3>{t("work.routing")}</h3><p>{t("work.routingHelp")}</p></div></div>
              <div className="surface-body orchestration-task-list work-list">{work.routing.map(routingCard)}</div>
            </section>
          )}

          <section className="surface">
            <div className="surface-header"><div><h3>{t("work.tickets")}</h3><p>{t("work.ticketsHelp")}</p></div></div>
            <div className="surface-body orchestration-task-list work-list">
              {activeTickets.length === 0 && <p className="muted">{t("work.noActiveTickets")}</p>}
              {activeTickets.map(ticketCard)}
              {finishedTickets.length > 0 && (
                <details className="work-finished">
                  <summary><ChevronDown size={13} /> {t("work.finishedTickets", { count: finishedTickets.length })}</summary>
                  <div className="orchestration-task-list">{finishedTickets.map(ticketCard)}</div>
                </details>
              )}
            </div>
          </section>

          <section className="surface">
            <div className="surface-header"><div><h3>{t("work.timers")}</h3><p>{t("work.timersHelp")}</p></div></div>
            <div className="surface-body orchestration-task-list work-list">
              {setTimers.length === 0 && <p className="muted">{t("work.noTimers")}</p>}
              {setTimers.map(timerCard)}
              {pastTimers.length > 0 && (
                <details className="work-finished">
                  <summary><ChevronDown size={13} /> {t("work.pastTimers", { count: pastTimers.length })}</summary>
                  <div className="orchestration-task-list">{pastTimers.map(timerCard)}</div>
                </details>
              )}
            </div>
          </section>
        </>
      )}
    </div>
  );
}

