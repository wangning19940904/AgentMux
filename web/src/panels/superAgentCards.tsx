import { Check, ChevronDown, RotateCcw, Square, X } from "lucide-react";
import { useState } from "react";
import ReactMarkdown from "react-markdown";
import remarkGfm from "remark-gfm";
import { api } from "../api";
import type { SuperAgentActivity, SuperAgentPlan, SuperAgentTicket } from "../api";
import { useI18n } from "../i18n";
import { WorkerFace, workerPersona } from "./agents/avatars";

// What a card needs from the page around it: the agent's activity, the
// action in flight, and how to run one.
export type CardContext = {
  agentID: string;
  activity: SuperAgentActivity | null | undefined;
  busy: string;
  run: (label: string, action: () => Promise<unknown>) => void;
  time: (value?: string) => string;
};

export function ticketStatusClass(status: string) {
  return { queued: "queued", running: "running", ready: "succeeded", failed: "failed", stopped: "cancelled" }[status] ?? status;
}

export function planStatusClass(status: string) {
  return { pending: "queued", approved: "succeeded", declined: "cancelled", expired: "cancelled" }[status] ?? status;
}

export function timerStatusClass(status: string) {
  return { active: "queued", done: "succeeded", cancelled: "cancelled" }[status] ?? status;
}

export function triggerStatusClass(disposition: string) {
  return { pending: "queued", accepted: "succeeded", follow_up: "succeeded", answered: "succeeded", observed: "running", failed: "failed" }[disposition] ?? "cancelled";
}

function machineName(activity: SuperAgentActivity | null | undefined, hostID: string | undefined) {
  return activity?.machines?.find((item) => item.host_id === hostID)?.name;
}

function TicketActions({ ticket, context }: { ticket: SuperAgentTicket; context: CardContext }) {
  const { t } = useI18n();
  const { busy, run } = context;
  return (
    <>
      {["queued", "running"].includes(ticket.status) && <button className="ghost-action danger-action" disabled={Boolean(busy)} onClick={() => run("stop", () => api.stopSuperAgentTicket(ticket.id))}><Square size={13} /> {t("superAgents.stop")}</button>}
      {ticket.status === "failed" && <button className="ghost-action" disabled={Boolean(busy)} onClick={() => run("retry", () => api.retrySuperAgentTicket(ticket.id))}><RotateCcw size={13} /> {t("common.retry")}</button>}
    </>
  );
}

// ticketWhere says who works on a Ticket, where, and for whom. The chat names
// the worker above the card, so it leaves the persona out.
export function ticketWhere(ticket: SuperAgentTicket, context: CardContext, t: (key: string) => string, persona = true) {
  // The requester is named as the event that started the Ticket names them.
  const requester = context.activity?.triggers?.find((trigger) => trigger.id === ticket.trigger_id)?.actor_name;
  return [
    persona ? workerPersona(ticket.worker).name : "",
    ticket.worker.label || ticket.worker.agent_id,
    ticket.worker.host_id ? (machineName(context.activity, ticket.worker.host_id) ?? t("superAgents.remote")) : t("superAgents.local"),
    requester || ticket.actor || "",
  ].filter(Boolean).join(" · ");
}

// TicketFacts are what supervision, review and helpers recorded about a
// ticket, beyond its result.
function TicketFacts({ ticket, context }: { ticket: SuperAgentTicket; context: CardContext }) {
  const { t } = useI18n();
  const { activity } = context;
  const review = ticket.reviews?.[ticket.reviews.length - 1];
  const reviewLabel = !review ? "" : review.refused ? t("superAgents.review.refused")
    : review.verdict === "rework" ? t("superAgents.review.reworkRound", { round: review.round ?? 0, limit: 2 })
    : t(`superAgents.review.${review.verdict}`);
  return (
    <>
      {ticket.workspace?.held_since && ["queued", "running"].includes(ticket.status) && (
        <small className="super-agent-ticket-holds">{t("superAgents.ticketHolds", { dir: ticket.workspace.dir ?? "", machine: ticket.workspace.host_id ? (machineName(activity, ticket.workspace.host_id) ?? ticket.workspace.host_id) : t("superAgents.thisMachine") })}</small>
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
      {review && (
        <div className="super-agent-ticket-review">
          <small><strong>{t("superAgents.review.title")}:</strong> {reviewLabel}{review.note ? ` · ${review.note}` : ""}</small>
          {(review.checks ?? []).length > 0 && (
            <ul>{(review.checks ?? []).map((check) => <li key={check.criterion}>{check.met ? "✓" : "✗"} {check.criterion}{check.evidence ? ` (${check.evidence})` : ""}</li>)}</ul>
          )}
        </div>
      )}
      {ticket.auto_review && (
        <details className="super-agent-ticket-auto-review">
          <summary><small>{ticket.auto_review.error ? t("superAgents.autoReview.failed", { runtime: ticket.auto_review.runtime })
            : ticket.auto_review.count >= 0 ? t("superAgents.autoReview.summary", { runtime: ticket.auto_review.runtime, count: ticket.auto_review.count, blocking: ticket.auto_review.blocking })
            : t("superAgents.autoReview.summaryUncounted", { runtime: ticket.auto_review.runtime })}</small></summary>
          <pre>{ticket.auto_review.error || ticket.auto_review.findings}</pre>
        </details>
      )}
      {(ticket.helpers ?? []).length > 0 && (
        <div className="super-agent-ticket-review super-agent-ticket-helpers">
          <small><strong>{t("superAgents.helpers.title")}:</strong> {t("superAgents.helpers.count", { running: (ticket.helpers ?? []).filter((helper) => helper.status === "running").length, total: (ticket.helpers ?? []).length })}</small>
          <ul>{(ticket.helpers ?? []).map((helper) => (
            <li key={helper.id}>
              <details>
                <summary><small>{t(`superAgents.helper.${helper.status}`)} · {helper.title || helper.id} · {helper.worker_id}</small></summary>
                <small>{helper.instructions}</small>
                {helper.error && <div className="error">{helper.error}</div>}
                {helper.result && <pre>{helper.result}</pre>}
              </details>
            </li>
          ))}</ul>
        </div>
      )}
    </>
  );
}

const hasFacts = (ticket: SuperAgentTicket) => Boolean(
  (ticket.workspace?.held_since && ["queued", "running"].includes(ticket.status)) || ticket.placement || (ticket.done_when ?? []).length
  || (ticket.signals ?? []).some((signal) => !signal.cleared_at) || ticket.reviews?.length || ticket.auto_review || (ticket.helpers ?? []).length,
);

// TicketCard is one ticket: as a row of the form view, or as the agent's
// message in a chat, where the result reads as formatted text and the
// supervision details fold away.
export function TicketCard({ ticket, context, variant = "form" }: { ticket: SuperAgentTicket; context: CardContext; variant?: "form" | "chat" }) {
  const { t } = useI18n();
  const [expanded, setExpanded] = useState(false);
  const status = <span className={`status-badge status-${ticketStatusClass(ticket.status)}`}>{t(`superAgents.ticket.${ticket.status}`)}</span>;
  if (variant === "form") {
    return (
      <article>
        <header>
          <strong>{ticket.title}</strong>
          <span className="control-row">{status}<TicketActions ticket={ticket} context={context} /></span>
        </header>
        <small className="super-agent-ticket-worker"><WorkerFace worker={ticket.worker} className="avatar-mini" iconSize={11} />{ticketWhere(ticket, context, t)}</small>
        <TicketFacts ticket={ticket} context={context} />
        {ticket.output && <pre>{ticket.output}</pre>}
        {ticket.error && <div className="error">{ticket.error}</div>}
        {ticket.reply_error && <div className="error">{t("superAgents.replyFailed")}: {ticket.reply_error}</div>}
        <time>{context.time(ticket.updated_at)}</time>
      </article>
    );
  }
  const long = (ticket.output ?? "").length > 600 || (ticket.output ?? "").split("\n").length > 14;
  return (
    <div className={`super-agent-chat-card ticket-${ticket.status}`}>
      <header>
        <span className="super-agent-chat-card-label">{t("superAgents.chat.ticket")}</span>
        {status}
      </header>
      <strong className="super-agent-chat-card-title">{ticket.title}</strong>
      <small className="muted">{ticketWhere(ticket, context, t, false)}</small>
      {ticket.output && (
        <div className={`message-content super-agent-chat-output${long && !expanded ? " collapsed" : ""}`}>
          <ReactMarkdown remarkPlugins={[remarkGfm]}>{ticket.output}</ReactMarkdown>
        </div>
      )}
      {long && (
        <button type="button" className="super-agent-chat-more" onClick={() => setExpanded(!expanded)}>
          {expanded ? t("superAgents.chat.showLess") : t("superAgents.chat.showMore")}
        </button>
      )}
      {ticket.status === "running" && !ticket.output && <div className="session-typing"><span /><span /><span />{t("superAgents.chat.working")}</div>}
      {ticket.error && <div className="error">{ticket.error}</div>}
      {ticket.reply_error && <div className="error">{t("superAgents.replyFailed")}: {ticket.reply_error}</div>}
      {hasFacts(ticket) && (
        <details className="super-agent-chat-facts">
          <summary><ChevronDown size={13} /> {t("superAgents.chat.details")}</summary>
          <TicketFacts ticket={ticket} context={context} />
        </details>
      )}
      {["queued", "running", "failed"].includes(ticket.status) && <div className="control-row"><TicketActions ticket={ticket} context={context} /></div>}
    </div>
  );
}

export function PlanCard({ plan, context, declineNote, onDeclineNote, variant = "form" }: {
  plan: SuperAgentPlan;
  context: CardContext;
  declineNote: string;
  onDeclineNote: (value: string) => void;
  variant?: "form" | "chat";
}) {
  const { t } = useI18n();
  const { agentID, busy, run, time } = context;
  const body = (
    <>
      <header>
        {variant === "chat" ? <span className="super-agent-chat-card-label">{t("superAgents.chat.plan")}</span> : <strong>{plan.title}</strong>}
        <span className="control-row">
          <span className={`super-agent-tier tier-${plan.tier}`}>{t(`superAgents.costTier.${plan.tier}`)}</span>
          <span className={`status-badge status-${planStatusClass(plan.status)}`}>{t(`superAgents.plan.${plan.status}`)}</span>
        </span>
      </header>
      {variant === "chat" && <strong className="super-agent-chat-card-title">{plan.title}</strong>}
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
          <button className="action" disabled={Boolean(busy)} onClick={() => run("approve-plan", () => api.approveSuperAgentPlan(agentID, plan.id, false))}><Check size={13} /> {t("superAgents.plan.approve")}</button>
          {(plan.requester || "") !== "" && <button className="ghost-action" disabled={Boolean(busy)} onClick={() => run("approve-plan", () => api.approveSuperAgentPlan(agentID, plan.id, true))}>{t("superAgents.plan.approveRemember", { tier: t(`superAgents.costTier.${plan.tier}`) })}</button>}
          <input aria-label={t("superAgents.plan.declineNote")} placeholder={t("superAgents.plan.declineNote")} value={declineNote} onChange={(event) => onDeclineNote(event.target.value)} />
          <button className="ghost-action danger-action" disabled={Boolean(busy)} onClick={() => run("decline-plan", () => api.declineSuperAgentPlan(agentID, plan.id, declineNote))}><X size={13} /> {t("superAgents.plan.decline")}</button>
        </div>
      ) : (
        <small>{plan.decided_by ? t("superAgents.plan.decidedBy", { status: t(`superAgents.plan.${plan.status}`), name: plan.decided_by }) : ""}{plan.note ? ` · ${plan.note}` : ""}</small>
      )}
      {variant === "form" && <time>{time(plan.decided_at || plan.created_at)}</time>}
    </>
  );
  return variant === "form"
    ? <article className={`super-agent-plan plan-${plan.status}`}>{body}</article>
    : <div className={`super-agent-chat-card super-agent-plan plan-${plan.status}`}>{body}</div>;
}
