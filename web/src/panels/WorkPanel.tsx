import { AlarmClock, ChevronDown, ChevronRight, Inbox, RefreshCw, Search, Square, TriangleAlert, Workflow, X } from "lucide-react";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { activeMachineScope, api, type MachineWork } from "../api";
import { confirmAction } from "../components/ConfirmDialog";
import { usePolling } from "../hooks/usePolling";
import { useI18n } from "../i18n";
import { ticketStatusClass, timerStatusClass } from "./superAgentCards";
import { nodeDomID, WorkGraph } from "./WorkGraph";
import {
  boardCounts,
  buildTasks,
  filterBoard,
  flattenWork,
  formatDuration,
  isActiveTicket,
  ticketSince,
  timerTitle,
  workAgents,
  type TaskBoard,
  type TaskNode,
  type WorkRouting,
  type WorkTask,
  type WorkTicket,
  type WorkTimer,
} from "./workModel";

const POLL_INTERVAL_MS = 5_000;
const TALLY_STATES = ["running", "queued", "done", "failed", "stopped"] as const;

// lastWork is the page's last answer for a machine scope, so coming back to
// the page shows it at once while a fresh one loads.
let lastWork: { scope: string; machines: MachineWork[] } | null = null;

// useWork loads what every machine is doing. Each machine's answer replaces
// its part of the page as soon as it arrives, so a slow machine neither
// holds up the others nor blanks its own part while it is asked again. A
// poll can start while a slow machine is still answering the one before;
// an answer is only dropped when a later one from that machine is shown.
function useWork() {
  const [scope] = useState(activeMachineScope);
  const [data, setData] = useState<MachineWork[] | null>(lastWork?.scope === scope ? lastWork.machines : null);
  const [error, setError] = useState<string | null>(null);
  const [loading, setLoading] = useState(true);
  const mounted = useRef(true);
  const requests = useRef(0);
  const shown = useRef<Record<string, number>>({});

  useEffect(() => {
    mounted.current = true;
    return () => { mounted.current = false; };
  }, []);

  const reload = useCallback(async () => {
    const request = ++requests.current;
    const answered = new Set<string>();
    const show = (targetID: string, machines: MachineWork[]) => {
      answered.add(targetID);
      if (!mounted.current || (shown.current[targetID] ?? 0) > request) return;
      shown.current[targetID] = request;
      setData((current) => [...(current ?? []).filter((machine) => machine.target_id !== targetID), ...machines]);
    };
    setLoading(true);
    try {
      const all = await api.superAgentWork(show);
      if (!mounted.current || request !== requests.current) return;
      // Whatever was not passed on as it came in, and then a machine no
      // longer in the fleet goes.
      for (const targetID of new Set(all.map((machine) => machine.target_id ?? ""))) {
        if (!answered.has(targetID)) show(targetID, all.filter((machine) => (machine.target_id ?? "") === targetID));
      }
      setData((current) => (current ?? []).filter((machine) => answered.has(machine.target_id ?? "")));
      setError(null);
    } catch (loadError) {
      if (mounted.current && request === requests.current) setError(loadError instanceof Error ? loadError.message : String(loadError));
    } finally {
      if (mounted.current && request === requests.current) setLoading(false);
    }
  }, []);

  useEffect(() => { void reload(); }, [reload]);
  useEffect(() => { if (data) lastWork = { scope, machines: data }; }, [scope, data]);
  return { data, error, loading, reload };
}

// WorkPanel shows what every agent on every machine is doing, one flow per
// task: the request, the workers started for it, what reached them later
// and the follow-ups set for it, with a way to stop each.
export function WorkPanel() {
  const { t, language } = useI18n();
  const machines = useWork();
  usePolling(machines.reload, POLL_INTERVAL_MS);
  const [agent, setAgent] = useState("");
  const [query, setQuery] = useState("");
  const [busy, setBusy] = useState("");
  const [notice, setNotice] = useState("");
  // Which tasks are opened or closed by hand; the rest follow their state.
  const [opened, setOpened] = useState<Record<string, boolean>>({});
  const [showFinished, setShowFinished] = useState(false);
  const [selected, setSelected] = useState<{ task: string; node: string } | null>(null);
  // The follow-up whose details are open, by timerKey.
  const [openTimer, setOpenTimer] = useState("");

  const all = useMemo(() => flattenWork(machines.data ?? []), [machines.data]);
  const agents = useMemo(() => workAgents(all), [all]);
  const board = useMemo(() => filterBoard(buildTasks(all), { agent, query }), [all, agent, query]);
  const counts = boardCounts(board);
  const activeTasks = board.tasks.filter((task) => task.active);
  const finishedTasks = board.tasks.filter((task) => !task.active);
  const setTimers = board.timers.filter(({ timer }) => timer.status === "active");
  const pastTimers = board.timers.filter(({ timer }) => timer.status !== "active");
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

  const cancelTimer = (timer: WorkTimer) => void run(`cancel:${timer.id}`, () => api.cancelSuperAgentTimer(timer.id, timer.target_id));

  // The agent, and its machine when there is more than one.
  const owner = (item: { super_agent_id: string; super_agent_name: string; target_id: string; target_name: string }) => multiMachine
    ? `${item.super_agent_name || item.super_agent_id} · ${machineLabel(item.target_id, item.target_name)}`
    : item.super_agent_name || item.super_agent_id;

  const eventKind = (item: { kind?: string; source_kind?: string }) => item.kind
    ? t(`work.eventKind.${item.kind}`)
    : t(`superAgents.kind.${item.source_kind || "console"}`);

  const isOpen = (task: WorkTask) => opened[task.key] ?? task.active;

  // reveal opens a task and selects one of its steps, for a follow-up that
  // points back at where it came from.
  function reveal(task: WorkTask, nodeID: string) {
    setOpened((current) => ({ ...current, [task.key]: true }));
    if (!task.active) setShowFinished(true);
    setSelected({ task: task.key, node: nodeID });
    window.setTimeout(() => document.getElementById(nodeDomID(task, nodeID))?.scrollIntoView?.({ block: "center", inline: "center", behavior: "smooth" }), 0);
  }

  const ticketFacts = (ticket: WorkTicket) => {
    const active = isActiveTicket(ticket);
    const worker = ticket.worker.label || ticket.worker.agent_id || ticket.worker.id;
    const workerMachine = ticket.machine_name || machineLabel(ticket.target_id, ticket.target_name);
    return (
      <>
        <div><dt>{t("work.agent")}</dt><dd>{owner(ticket)}</dd></div>
        <div><dt>{t("work.worker")}</dt><dd>{worker} · {workerMachine}</dd></div>
        <div>
          <dt>{active ? (ticket.status === "running" ? t("work.runningFor") : t("work.queuedFor")) : t("work.finished")}</dt>
          <dd>{active ? since(ticketSince(ticket)) : time(ticket.finished_at || ticket.updated_at)}</dd>
        </div>
        {(ticket.pending_inputs ?? 0) > 0 && <div><dt>{t("work.waitingInputs")}</dt><dd>{ticket.pending_inputs}</dd></div>}
      </>
    );
  };

  // What the selected step is, in full.
  const detail = (task: WorkTask, node: TaskNode) => {
    const ticketTitle = (id?: string) => {
      const found = task.nodes.find((item) => item.id === id);
      return found?.kind === "ticket" ? found.ticket.title : "";
    };
    const close = (
      <button className="ghost-action" aria-label={t("common.close")} onClick={() => setSelected(null)}><X size={13} /></button>
    );
    switch (node.kind) {
      case "request": {
        const ticket = node.ticket;
        const requester = ticket.requester_name || ticket.actor;
        return (
          <>
            <header><strong>{t("work.node.request")}</strong><span className="control-row">{close}</span></header>
            {ticket.request && <blockquote className="work-request full">{ticket.request}</blockquote>}
            <dl className="work-facts">
              {requester && <div><dt>{t("work.requester")}</dt><dd>{requester}</dd></div>}
              <div><dt>{t("work.from")}</dt><dd>{eventKind({ kind: ticket.origin_kind, source_kind: ticket.source_kind })}</dd></div>
              <div><dt>{t("work.agent")}</dt><dd>{owner(ticket)}</dd></div>
              <div><dt>{t("work.received")}</dt><dd>{time(ticket.created_at)}</dd></div>
            </dl>
          </>
        );
      }
      case "ticket": {
        const ticket = node.ticket;
        const active = isActiveTicket(ticket);
        const openSignals = (ticket.signals ?? []).filter((signal) => !signal.cleared_at);
        const cameFrom = node.via && ticketTitle(node.via === "timer" && node.parent?.startsWith("f:") ? task.nodes.find((item) => item.id === node.parent)?.parent : node.parent);
        return (
          <>
            <header>
              <strong>{ticket.title}</strong>
              <span className="control-row">
                <span className={`status-badge status-${ticketStatusClass(ticket.status)}`}>{t(`superAgents.ticket.${ticket.status}`)}</span>
                {active && (
                  <button className="ghost-action danger-action" disabled={Boolean(busy)} onClick={() => void stopTicket(ticket)}>
                    <Square size={13} /> {t("superAgents.stop")}
                  </button>
                )}
                {close}
              </span>
            </header>
            <dl className="work-facts">
              {ticketFacts(ticket)}
              {node.via && cameFrom && <div><dt>{t(`work.via.${node.via}`)}</dt><dd>{cameFrom}</dd></div>}
              {(ticket.rework_rounds ?? 0) > 0 && <div><dt>{t("work.node.reworked", { count: ticket.rework_rounds ?? 0 })}</dt><dd /></div>}
            </dl>
            {task.links.filter((link) => link.to === node.id || link.from === node.id).map((link) => (
              <p key={`${link.from}>${link.to}`} className="muted work-link-note">{t(`work.link.${link.kind}`, { from: ticketTitle(link.from), to: ticketTitle(link.to) })}</p>
            ))}
            {openSignals.map((signal) => (
              <div key={`${signal.kind}-${signal.since}`} className="warning super-agent-source-warning" role="status">
                {t(`superAgents.signal.${signal.kind}`)}: {signal.detail}
              </div>
            ))}
            {ticket.error && <div className="error">{ticket.error}</div>}
            {ticket.instructions && (
              <details className="work-output">
                <summary><ChevronDown size={13} /> {t("work.instructions")}</summary>
                <pre>{ticket.instructions}</pre>
              </details>
            )}
            {ticket.output && (
              <details className="work-output" open>
                <summary><ChevronDown size={13} /> {active ? t("work.latestOutput") : t("work.result")}</summary>
                <pre>{ticket.output}</pre>
              </details>
            )}
          </>
        );
      }
      case "helper":
        return (
          <>
            <header>
              <strong>{node.helper.title || t("work.node.helper")}</strong>
              <span className="control-row">
                <span className={`status-badge status-${ticketStatusClass(node.helper.status === "done" ? "ready" : node.helper.status)}`}>{t(`superAgents.helper.${node.helper.status}`)}</span>
                {close}
              </span>
            </header>
            <dl className="work-facts">
              <div><dt>{t("work.worker")}</dt><dd>{node.helper.worker_id}</dd></div>
              <div><dt>{t("work.helperOf")}</dt><dd>{node.ticket.title}</dd></div>
              <div><dt>{node.helper.finished_at ? t("work.finished") : t("work.runningFor")}</dt><dd>{node.helper.finished_at ? time(node.helper.finished_at) : since(node.helper.created_at)}</dd></div>
            </dl>
            <blockquote className="work-request full">{node.helper.instructions}</blockquote>
            {node.helper.error && <div className="error">{node.helper.error}</div>}
            {node.helper.result && (
              <details className="work-output" open>
                <summary><ChevronDown size={13} /> {t("work.result")}</summary>
                <pre>{node.helper.result}</pre>
              </details>
            )}
          </>
        );
      case "message": {
        const event = node.event;
        const target = ticketTitle(node.parent);
        const ticket = task.tickets.find((item) => item.id === event.ticket_id);
        return (
          <>
            <header><strong>{t("work.node.message")}</strong><span className="control-row">{close}</span></header>
            <blockquote className="work-request full">{event.text}</blockquote>
            <dl className="work-facts">
              <div><dt>{t("work.from")}</dt><dd>{[event.actor_name, eventKind(event)].filter(Boolean).join(" · ")}</dd></div>
              <div><dt>{t("work.received")}</dt><dd>{time(event.received_at)}</dd></div>
              <div><dt>{t("work.messageRouted")}</dt><dd>{target}</dd></div>
              {ticket && ticket.thread_key !== event.thread_key && <div><dt>{t("work.otherThread")}</dt><dd className="work-mono">{event.thread_key}</dd></div>}
            </dl>
          </>
        );
      }
      case "routing":
        return (
          <>
            <header>
              <strong>{node.item.title || eventKind(node.item)}</strong>
              <span className="control-row">
                <span className={`status-badge status-${node.item.routing ? "running" : "queued"}`}>{node.item.routing ? t("work.routingNow") : t("work.routingWaiting")}</span>
                {close}
              </span>
            </header>
            {node.item.text && <blockquote className="work-request full">{node.item.text}</blockquote>}
            <dl className="work-facts">
              <div><dt>{t("work.received")}</dt><dd>{t("work.ago", { duration: since(node.item.received_at) })}</dd></div>
            </dl>
          </>
        );
      case "timer":
        return (
          <>
            <header>
              <strong>{t("work.node.timer")}</strong>
              <span className="control-row">
                <span className={`status-badge status-${timerStatusClass(node.timer.status)}`}>{t(`superAgents.timer.${node.timer.status}`)}</span>
                {node.timer.status === "active" && (
                  <button className="ghost-action danger-action" disabled={Boolean(busy)} onClick={() => cancelTimer(node.timer)}>
                    <X size={13} /> {t("common.cancel")}
                  </button>
                )}
                {close}
              </span>
            </header>
            <blockquote className="work-request full">{node.timer.note}</blockquote>
            <dl className="work-facts">{timerFacts(node.timer)}</dl>
          </>
        );
    }
  };

  const timerFacts = (timer: WorkTimer) => (
    <>
      <div><dt>{t("work.agent")}</dt><dd>{owner(timer)}</dd></div>
      <div><dt>{t("work.thread")}</dt><dd className="work-mono">{timer.thread_key}</dd></div>
      {timer.status === "active" && <div><dt>{t("superAgents.timerDue")}</dt><dd>{time(timer.due_at)} · {t("work.inDuration", { duration: formatDuration(Date.parse(timer.due_at) - Date.now()) })}</dd></div>}
      {timer.status === "done" && <div><dt>{t("superAgents.timerFired")}</dt><dd>{time(timer.fired_at || timer.updated_at)}</dd></div>}
      {timer.status === "cancelled" && <div><dt>{t("superAgents.timerCancelled")}</dt><dd>{time(timer.updated_at)}</dd></div>}
    </>
  );

  const taskCard = (task: WorkTask) => {
    const open = isOpen(task);
    const chosen = selected?.task === task.key ? task.nodes.find((node) => node.id === selected.node) : undefined;
    const followUps = task.timers.filter((timer) => timer.status === "active").length;
    return (
      <article key={task.key} className={`work-task${task.active ? " active" : ""}`}>
        <header className="work-task-head">
          <button
            type="button"
            className="work-task-toggle"
            aria-expanded={open}
            aria-label={t(open ? "work.hideTask" : "work.showTask", { title: task.title })}
            onClick={() => setOpened((current) => ({ ...current, [task.key]: !open }))}
          >
            {open ? <ChevronDown size={15} /> : <ChevronRight size={15} />}
          </button>
          <div className="work-task-title">
            <strong>{task.title}</strong>
            <small>{[task.requester, owner(task), t("work.taskStarted", { duration: since(task.started_at) })].filter(Boolean).join(" · ")}</small>
          </div>
          <div className="work-tally" role="status">
            <b>{task.tally.total === 1 ? t("work.tally.oneWorker") : t("work.tally.workers", { count: task.tally.total })}</b>
            {TALLY_STATES.filter((state) => task.tally[state] > 0).map((state) => (
              <span key={state} className={`work-tally-chip tone-${state}`}><i />{t(`work.tally.${state}`, { count: task.tally[state] })}</span>
            ))}
            {followUps > 0 && <span className="work-tally-chip"><AlarmClock size={12} />{t("work.tally.timers", { count: followUps })}</span>}
          </div>
        </header>
        {open && (
          <WorkGraph
            task={task}
            selected={chosen?.id ?? ""}
            busy={Boolean(busy)}
            onSelect={(node) => setSelected(selected?.task === task.key && selected.node === node ? null : { task: task.key, node })}
            onStop={(ticket) => void stopTicket(ticket)}
            onCancel={cancelTimer}
            time={time}
          />
        )}
        {open && chosen && <div className="work-node-detail">{detail(task, chosen)}</div>}
      </article>
    );
  };

  // The task a follow-up belongs to, and the step in its graph to point at.
  const timerPlace = ({ timer, task, ticket }: TimerEntry) => ({
    nodeID: task?.nodes.some((node) => node.id === `f:${timer.id}`) ? `f:${timer.id}` : ticket ? `t:${ticket.id}` : "",
    label: task ? (ticket && ticket.title !== task.title ? `${task.title} › ${ticket.title}` : task.title) : "",
  });

  // When a follow-up is due, or when it fired or was cancelled.
  const timerWhen = (timer: WorkTimer) => {
    if (timer.status === "done") return `${t("superAgents.timerFired")} ${time(timer.fired_at || timer.updated_at)}`;
    if (timer.status === "cancelled") return `${t("superAgents.timerCancelled")} ${time(timer.updated_at)}`;
    const wait = Date.parse(timer.due_at) - Date.now();
    return wait > 0
      ? t("work.node.dueIn", { time: time(timer.due_at), duration: formatDuration(wait) })
      : t("work.node.overdue", { time: time(timer.due_at) });
  };

  // A follow-up in the list: what it waits for, its task and when it is due.
  // The note and the rest open in a dialog.
  const timerRow = (entry: TimerEntry) => {
    const { timer } = entry;
    const title = timerTitle(timer);
    const { label } = timerPlace(entry);
    return (
      <button key={timerKey(timer)} type="button" className="work-item work-timer-row" aria-label={t("work.timerOpen", { title })} onClick={() => setOpenTimer(timerKey(timer))}>
        <span className="work-timer-icon"><AlarmClock size={15} /></span>
        <span className="work-timer-main">
          <strong>{title}</strong>
          <small><Workflow size={12} /> {label || timer.ticket_title || t("work.timerNoTask")}</small>
        </span>
        <span className="work-timer-when">
          <span className={`status-badge status-${timerStatusClass(timer.status)}`}>{t(`superAgents.timer.${timer.status}`)}</span>
          <small>{timerWhen(timer)}</small>
        </span>
      </button>
    );
  };

  const timerDialog = (entry: TimerEntry) => {
    const { timer, task, loose } = entry;
    const title = timerTitle(timer);
    const { nodeID, label } = timerPlace(entry);
    const close = () => setOpenTimer("");
    return (
      <div className="meeting-dialog-layer">
        <button className="meeting-dialog-backdrop" aria-label={t("common.close")} onClick={close} type="button" />
        <section className="surface meeting-dialog work-timer-dialog" role="dialog" aria-modal="true" aria-labelledby="work-timer-title">
          <button className="meeting-dialog-close" aria-label={t("common.close")} onClick={close} type="button"><X size={17} /></button>
          <header className="work-timer-dialog-head">
            <span className="meeting-dialog-icon"><AlarmClock size={16} /></span>
            <div>
              <h2 id="work-timer-title">{title}</h2>
              <span className={`status-badge status-${timerStatusClass(timer.status)}`}>{t(`superAgents.timer.${timer.status}`)}</span>
            </div>
          </header>
          <dl className="work-facts work-timer-facts">
            <div>
              <dt>{t("work.timerTask")}</dt>
              <dd>
                {task && label
                  ? <button type="button" className="work-link" title={t("work.showInTask", { title: label })} onClick={() => { close(); reveal(task, nodeID); }}><Workflow size={12} /> {label}</button>
                  : <span className="muted">{timer.ticket_title || t("work.timerNoTask")}</span>}
                {task && loose && <small className="muted"> ({t("work.timerSameThread")})</small>}
              </dd>
            </div>
            {timerFacts(timer)}
            {timer.created_at && <div><dt>{t("work.timerSetAt")}</dt><dd>{time(timer.created_at)}</dd></div>}
          </dl>
          <div className="work-timer-note">
            <h3>{t("work.timerNote")}</h3>
            <blockquote className="work-request full">{timer.note}</blockquote>
          </div>
          {timer.status === "active" && (
            <div className="control-row work-timer-actions">
              <button className="ghost-action danger-action" disabled={Boolean(busy)} onClick={() => cancelTimer(timer)}>
                <X size={13} /> {t("work.cancelTimer")}
              </button>
            </div>
          )}
        </section>
      </div>
    );
  };

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

  const shownTimer = openTimer ? board.timers.find(({ timer }) => timerKey(timer) === openTimer) : undefined;
  useEffect(() => {
    if (!openTimer) return;
    const onKey = (event: KeyboardEvent) => { if (event.key === "Escape") setOpenTimer(""); };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [openTimer]);

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
            <span><Workflow size={14} /> {t("work.count.tasks", { count: counts.tasks })}</span>
            <span className="tone-running"><i />{t("work.count.running", { count: counts.running })}</span>
            <span className="tone-queued"><i />{t("work.count.queued", { count: counts.queued })}</span>
            {counts.failed > 0 && <span className="tone-failed"><i />{t("work.count.failed", { count: counts.failed })}</span>}
            <span><AlarmClock size={14} /> {t("work.count.timers", { count: counts.timers })}</span>
            <span><Inbox size={14} /> {t("work.count.routing", { count: counts.routing })}</span>
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
          {board.incoming.length > 0 && (
            <section className="surface">
              <div className="surface-header"><div><h3>{t("work.routing")}</h3><p>{t("work.routingHelp")}</p></div></div>
              <div className="surface-body orchestration-task-list work-list">{board.incoming.map(routingCard)}</div>
            </section>
          )}

          <section className="surface">
            <div className="surface-header">
              <div><h3>{t("work.tasks")}</h3><p>{t("work.tasksHelp")}</p></div>
            </div>
            <div className="surface-body work-legend" aria-hidden="true">
              <span><svg width="26" height="8"><path className="work-edge" d="M0 4H24" /><path className="work-arrow" d="M19 0.5 26 4 19 7.5z" /></svg>{t("work.legend.started")}</span>
              <span><svg width="26" height="8"><path className="work-edge work-edge-message" d="M2 4H26" /><path className="work-arrow" d="M7 0.5 0 4 7 7.5z" /></svg>{t("work.legend.message")}</span>
              <span><svg width="26" height="8"><path className="work-edge work-edge-timer" d="M0 4H26" /></svg>{t("work.legend.timer")}</span>
              <span><svg width="26" height="8"><path className="work-edge work-edge-link" d="M0 4H24" /><path className="work-arrow link" d="M19 0.5 26 4 19 7.5z" /></svg>{t("work.legend.link")}</span>
            </div>
            <div className="surface-body work-task-list">
              {activeTasks.length === 0 && <p className="muted">{t("work.noActiveTasks")}</p>}
              {activeTasks.map(taskCard)}
              {finishedTasks.length > 0 && (
                <details className="work-finished" open={showFinished} onToggle={(event) => setShowFinished(event.currentTarget.open)}>
                  <summary><ChevronDown size={13} /> {t("work.finishedTasks", { count: finishedTasks.length })}</summary>
                  <div className="work-task-list">{finishedTasks.map(taskCard)}</div>
                </details>
              )}
            </div>
          </section>

          <section className="surface">
            <div className="surface-header"><div><h3>{t("work.timers")}</h3><p>{t("work.timersHelp")}</p></div></div>
            <div className="surface-body orchestration-task-list work-list">
              {setTimers.length === 0 && <p className="muted">{t("work.noTimers")}</p>}
              {setTimers.map(timerRow)}
              {pastTimers.length > 0 && (
                <details className="work-finished">
                  <summary><ChevronDown size={13} /> {t("work.pastTimers", { count: pastTimers.length })}</summary>
                  <div className="orchestration-task-list">{pastTimers.map(timerRow)}</div>
                </details>
              )}
            </div>
          </section>
        </>
      )}
      {shownTimer && timerDialog(shownTimer)}
    </div>
  );
}

type TimerEntry = TaskBoard["timers"][number];

const timerKey = (timer: WorkTimer) => `${timer.target_id}-${timer.id}`;
