import { AlarmClock, Bot, GitBranch, Inbox, Mail, MessageSquare, RotateCcw, Square, TriangleAlert, X } from "lucide-react";
import { useEffect, useId, useRef, useState, type ReactNode } from "react";
import { useI18n } from "../i18n";
import { formatDuration, isActiveTicket, ticketSince, timerTitle, type TaskLink, type TaskNode, type WorkTask, type WorkTicket, type WorkTimer } from "./workModel";

// Every node is a box of a fixed size, so the lines between them follow from
// the layout alone. The first column holds only requests, which are narrower.
const NODE_W = 236;
const REQUEST_W = 172;
const NODE_H = 66;
const GAP_X = 36;
const GAP_Y = 12;
const PAD = 3;
const CORNER = 8;

const widthOf = (col: number) => (col === 0 ? REQUEST_W : NODE_W);
const leftOf = (col: number) => PAD + (col === 0 ? 0 : REQUEST_W + GAP_X + (col - 1) * (NODE_W + GAP_X));
const left = (node: TaskNode) => leftOf(node.col);
const right = (node: TaskNode) => leftOf(node.col) + widthOf(node.col);
const top = (node: TaskNode) => PAD + node.row * (NODE_H + GAP_Y);
const middle = (node: TaskNode) => top(node) + NODE_H / 2;

export type NodeTone = "running" | "queued" | "done" | "failed" | "stopped" | "neutral";

export function nodeTone(node: TaskNode): NodeTone {
  switch (node.kind) {
    case "ticket": return node.ticket.status === "ready" ? "done" : node.ticket.status;
    case "helper": return node.helper.status;
    case "routing": return node.item.routing ? "running" : "queued";
    default: return "neutral";
  }
}

// The line from a node to its parent. A first child continues its parent's
// row; later ones branch off a trunk halfway between the two columns. A
// later message is drawn from the message to the worker it was sent to.
function treeEdge(parent: TaskNode, node: TaskNode) {
  const fromX = right(parent);
  const toX = left(node);
  const trunk = fromX + GAP_X / 2;
  const [y1, y2] = [middle(parent), middle(node)];
  if (node.kind === "message") {
    return y1 === y2 ? `M${toX} ${y2}H${fromX}` : `M${toX} ${y2}H${trunk + CORNER}Q${trunk} ${y2} ${trunk} ${y2 - CORNER}V${y1}`;
  }
  return y1 === y2 ? `M${fromX} ${y1}H${toX}` : `M${trunk} ${y1}V${y2 - CORNER}Q${trunk} ${y2} ${trunk + CORNER} ${y2}H${toX}`;
}

// A link between two Tickets that are not parent and child curves through
// the gaps, entering and leaving below the tree's own lines.
function linkEdge(from: TaskNode, to: TaskNode) {
  const low = NODE_H * 0.8;
  const [sx, sy] = [right(from), top(from) + low];
  if (to.col > from.col) {
    const [ex, ey] = [left(to), top(to) + low];
    return `M${sx} ${sy}C${sx + GAP_X} ${sy} ${ex - GAP_X} ${ey} ${ex} ${ey}`;
  }
  const [ex, ey] = [right(to), top(to) + low];
  const bulge = Math.max(sx, ex) + GAP_X * 0.7;
  return `M${sx} ${sy}C${bulge} ${sy} ${bulge} ${ey} ${ex} ${ey}`;
}

type Props = {
  task: WorkTask;
  selected: string;
  busy: boolean;
  onSelect: (nodeID: string) => void;
  onStop: (ticket: WorkTicket) => void;
  onCancel: (timer: WorkTimer) => void;
  time: (value?: string) => string;
};

// WorkGraph draws one task as a tree that grows to the right: the request,
// the workers started for it and what hangs off each of them.
export function WorkGraph({ task, selected, busy, onSelect, onStop, onCancel, time }: Props) {
  const { t } = useI18n();
  const markers = useId().replace(/:/g, "");
  // A flow wider than its card scrolls sideways; the card's edge fades where
  // more of it lies beyond.
  const scroller = useRef<HTMLDivElement>(null);
  const [more, setMore] = useState({ left: false, right: false });
  const measure = () => {
    const box = scroller.current;
    if (!box) return;
    const next = { left: box.scrollLeft > 1, right: box.scrollLeft + box.clientWidth < box.scrollWidth - 1 };
    setMore((current) => (current.left === next.left && current.right === next.right ? current : next));
  };
  // A long flow opens where the work is now: at its first running step, or
  // failing that its first queued one.
  useEffect(() => {
    const box = scroller.current;
    const current = task.nodes.find((node) => nodeTone(node) === "running") ?? task.nodes.find((node) => nodeTone(node) === "queued");
    if (box && current) box.scrollLeft = Math.max(0, Math.min(left(current) - PAD, right(current) + GAP_X + NODE_W * 0.6 - box.clientWidth));
    // Only when the flow first appears: later the place is the reader's.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);
  useEffect(() => {
    measure();
    if (!scroller.current || typeof ResizeObserver === "undefined") return;
    const observer = new ResizeObserver(measure);
    observer.observe(scroller.current);
    return () => observer.disconnect();
  }, [task.cols]);
  const byID = new Map(task.nodes.map((node) => [node.id, node]));
  const width = leftOf(task.cols - 1) + widthOf(task.cols - 1) + PAD;
  const height = PAD * 2 + task.rows * NODE_H + (task.rows - 1) * GAP_Y;
  const since = (value?: string) => (value ? formatDuration(Date.now() - Date.parse(value)) : "");
  const titleOf = (id: string) => {
    const node = byID.get(id);
    return node?.kind === "ticket" ? node.ticket.title : "";
  };
  const linkText = (link: TaskLink) => t(`work.link.${link.kind}`, { from: titleOf(link.from), to: titleOf(link.to) });

  const face = (node: TaskNode): { icon: ReactNode; title: string; meta: string; badges?: ReactNode; action?: ReactNode } => {
    switch (node.kind) {
      case "request": {
        const ticket = node.ticket;
        const who = ticket.origin_kind
          ? t(`work.eventKind.${ticket.origin_kind}`)
          : [ticket.requester_name || ticket.actor, t(`superAgents.kind.${ticket.source_kind || "console"}`)].filter(Boolean).join(" · ");
        return { icon: <MessageSquare size={14} />, title: who || t("work.node.request"), meta: ticket.request || time(ticket.created_at) };
      }
      case "ticket": {
        const ticket = node.ticket;
        const active = isActiveTicket(ticket);
        const state = ticket.status === "ready" ? t("work.status.done") : t(`superAgents.ticket.${ticket.status}`);
        const when = active ? since(ticketSince(ticket)) : time(ticket.finished_at || ticket.updated_at);
        // The machine tells workers of one kind apart, so it comes first.
        const worker = [ticket.machine_name, ticket.worker.label || ticket.worker.agent_id || ticket.worker.id].filter(Boolean).join(" · ");
        const warned = (ticket.signals ?? []).some((signal) => !signal.cleared_at);
        return {
          icon: <Bot size={14} />,
          title: ticket.title,
          meta: [state, when, worker].filter(Boolean).join(" · "),
          badges: (
            <>
              {warned && <span className="work-node-badge warn" title={(ticket.signals ?? []).filter((signal) => !signal.cleared_at).map((signal) => `${t(`superAgents.signal.${signal.kind}`)}: ${signal.detail}`).join("\n")}><TriangleAlert size={11} /></span>}
              {(ticket.rework_rounds ?? 0) > 0 && <span className="work-node-badge" title={t("work.node.reworked", { count: ticket.rework_rounds ?? 0 })}><RotateCcw size={11} />{ticket.rework_rounds}</span>}
              {(ticket.pending_inputs ?? 0) > 0 && <span className="work-node-badge" title={t("work.node.waitingInputs", { count: ticket.pending_inputs ?? 0 })}><Mail size={11} />{ticket.pending_inputs}</span>}
            </>
          ),
          action: active && (
            <button type="button" className="work-node-action" disabled={busy} title={t("superAgents.stop")} aria-label={`${t("superAgents.stop")}: ${ticket.title}`} onClick={() => onStop(ticket)}>
              <Square size={11} />
            </button>
          ),
        };
      }
      case "helper":
        return {
          icon: <GitBranch size={14} />,
          title: node.helper.title || node.helper.instructions,
          meta: [t(`superAgents.helper.${node.helper.status}`), node.helper.worker_id].filter(Boolean).join(" · "),
        };
      case "message":
        return {
          icon: <Mail size={14} />,
          title: t("work.node.messageFrom", { name: node.event.actor_name || t("work.node.someone"), time: time(node.event.received_at) }),
          meta: node.event.text,
        };
      case "routing":
        return {
          icon: <Inbox size={14} />,
          title: node.item.routing ? t("work.node.routingNow") : t("work.node.routingWaiting"),
          meta: t("work.ago", { duration: since(node.item.received_at) }),
        };
      case "timer": {
        const timer = node.timer;
        const wait = Date.parse(timer.due_at) - Date.now();
        const meta = timer.status !== "active"
          ? t("work.node.firedAt", { time: time(timer.fired_at || timer.updated_at) })
          : wait > 0 ? t("work.node.dueIn", { time: time(timer.due_at), duration: formatDuration(wait) }) : t("work.node.overdue", { time: time(timer.due_at) });
        return {
          icon: <AlarmClock size={14} />,
          title: timerTitle(timer),
          meta,
          action: timer.status === "active" && (
            <button type="button" className="work-node-action" disabled={busy} title={t("common.cancel")} aria-label={`${t("common.cancel")}: ${timerTitle(timer)}`} onClick={() => onCancel(timer)}>
              <X size={12} />
            </button>
          ),
        };
      }
    }
  };

  return (
    <div ref={scroller} className={`work-graph-scroll${more.left ? " more-left" : ""}${more.right ? " more-right" : ""}`} onScroll={measure}>
      <div className="work-graph" style={{ width, height }}>
        <svg className="work-graph-edges" width={width} height={height} aria-hidden="true">
          <defs>
            <marker id={`${markers}-in`} className="work-arrow" viewBox="0 0 8 8" refX="7.5" refY="4" markerWidth="7" markerHeight="7" orient="auto"><path d="M0 0.5 8 4 0 7.5z" /></marker>
            <marker id={`${markers}-out`} className="work-arrow" viewBox="0 0 8 8" refX="-1" refY="4" markerWidth="7" markerHeight="7" orient="auto"><path d="M0 0.5 8 4 0 7.5z" /></marker>
            <marker id={`${markers}-link`} className="work-arrow link" viewBox="0 0 8 8" refX="7.5" refY="4" markerWidth="7" markerHeight="7" orient="auto"><path d="M0 0.5 8 4 0 7.5z" /></marker>
          </defs>
          {task.nodes.map((node) => {
            const parent = byID.get(node.parent ?? "");
            if (!parent) return null;
            const out = node.kind === "message";
            return (
              <path
                key={node.id}
                className={`work-edge work-edge-${node.kind}`}
                d={treeEdge(parent, node)}
                markerStart={out ? `url(#${markers}-out)` : undefined}
                markerEnd={out ? undefined : `url(#${markers}-in)`}
              />
            );
          })}
          {task.links.map((link) => {
            const [from, to] = [byID.get(link.from), byID.get(link.to)];
            return from && to && <path key={`${link.from}>${link.to}`} className="work-edge work-edge-link" d={linkEdge(from, to)} markerEnd={`url(#${markers}-link)`} />;
          })}
        </svg>
        {task.nodes.map((node) => {
          const { icon, title, meta, badges, action } = face(node);
          const linked = task.links.filter((link) => link.to === node.id || link.from === node.id).map(linkText);
          return (
            <div
              key={node.id}
              id={nodeDomID(task, node.id)}
              className={`work-node work-node-${node.kind} tone-${nodeTone(node)}${selected === node.id ? " selected" : ""}`}
              style={{ left: left(node), top: top(node), width: widthOf(node.col), height: NODE_H }}
            >
              <button type="button" className="work-node-main" aria-pressed={selected === node.id} title={[title, meta, ...linked].filter(Boolean).join("\n")} onClick={() => onSelect(node.id)}>
                <span className="work-node-icon">{icon}</span>
                <span className="work-node-text">
                  <strong>{title}</strong>
                  <small><span className="work-node-dot" />{meta}</small>
                </span>
                {badges && <span className="work-node-badges">{badges}</span>}
              </button>
              {action}
            </div>
          );
        })}
      </div>
    </div>
  );
}

export function nodeDomID(task: WorkTask, nodeID: string) {
  return `work-node-${task.key}-${nodeID}`.replace(/[^A-Za-z0-9_-]/g, "_");
}
