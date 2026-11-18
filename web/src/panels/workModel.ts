import type { MachineWork, SuperAgentRoutingItem, SuperAgentWorkTicket, SuperAgentWorkTimer } from "../api";

// Each item carries the machine its agent lives on, which is where an action
// on it runs.
type OnMachine = { target_id: string; target_name: string };
export type WorkRouting = SuperAgentRoutingItem & OnMachine;
export type WorkTicket = SuperAgentWorkTicket & OnMachine;
export type WorkTimer = SuperAgentWorkTimer & OnMachine;

export type FleetWork = {
  routing: WorkRouting[];
  tickets: WorkTicket[];
  timers: WorkTimer[];
  // Machines that did not answer, with why.
  unreachable: Array<{ target_id: string; target_name: string; error: string }>;
};

export type WorkFilter = { agent: string; query: string };

// agentKey names an agent across machines: two machines may have agents with
// the same id.
export function agentKey(item: { target_id: string; super_agent_id: string }) {
  return `${item.target_id}::${item.super_agent_id}`;
}

export function isActiveTicket(ticket: { status: string }) {
  return ticket.status === "queued" || ticket.status === "running";
}

export function flattenWork(machines: MachineWork[]): FleetWork {
  const out: FleetWork = { routing: [], tickets: [], timers: [], unreachable: [] };
  for (const machine of machines) {
    const where: OnMachine = { target_id: machine.target_id || "local", target_name: machine.target_name || "" };
    if (!machine.work) {
      out.unreachable.push({ ...where, error: machine.error || "unavailable" });
      continue;
    }
    out.routing.push(...(machine.work.routing ?? []).map((item) => ({ ...item, ...where })));
    out.tickets.push(...(machine.work.tickets ?? []).map((item) => ({ ...item, ...where })));
    out.timers.push(...(machine.work.timers ?? []).map((item) => ({ ...item, ...where })));
  }
  const time = (value?: string) => (value ? Date.parse(value) || 0 : 0);
  out.routing.sort((a, b) => time(a.received_at) - time(b.received_at));
  // Running first, then queued, then finished; newest first within each.
  const rank = (ticket: { status: string }) => ({ running: 0, queued: 1 } as Record<string, number>)[ticket.status] ?? 2;
  out.tickets.sort((a, b) => rank(a) - rank(b) || time(b.created_at || b.updated_at) - time(a.created_at || a.updated_at));
  out.timers.sort((a, b) => Number(b.status === "active") - Number(a.status === "active")
    || (a.status === "active" ? time(a.due_at) - time(b.due_at) : time(b.updated_at) - time(a.updated_at)));
  return out;
}

// The agents that appear anywhere on the page, for the agent filter.
export function workAgents(work: FleetWork) {
  const agents = new Map<string, { key: string; name: string; target_id: string; target_name: string }>();
  for (const item of [...work.routing, ...work.tickets, ...work.timers]) {
    const key = agentKey(item);
    if (!agents.has(key)) agents.set(key, { key, name: item.super_agent_name || item.super_agent_id, target_id: item.target_id, target_name: item.target_name });
  }
  return [...agents.values()].sort((a, b) => a.name.localeCompare(b.name));
}

function matches(filter: WorkFilter, item: { target_id: string; super_agent_id: string }, texts: Array<string | undefined>) {
  if (filter.agent && agentKey(item) !== filter.agent) return false;
  const query = filter.query.trim().toLocaleLowerCase();
  if (!query) return true;
  return texts.some((text) => (text ?? "").toLocaleLowerCase().includes(query));
}

export function filterWork(work: FleetWork, filter: WorkFilter): FleetWork {
  return {
    ...work,
    routing: work.routing.filter((item) => matches(filter, item, [item.title, item.text, item.actor, item.actor_name, item.super_agent_name, item.thread_key])),
    tickets: work.tickets.filter((item) => matches(filter, item, [
      item.title, item.request, item.requester_name, item.actor, item.super_agent_name,
      item.worker.label, item.worker.agent_id, item.worker.id, item.machine_name, item.id,
    ])),
    timers: work.timers.filter((item) => matches(filter, item, [item.note, item.ticket_title, item.super_agent_name, item.thread_key])),
  };
}

export function workCounts(work: FleetWork) {
  return {
    routing: work.routing.length,
    running: work.tickets.filter((ticket) => ticket.status === "running").length,
    queued: work.tickets.filter((ticket) => ticket.status === "queued").length,
    timers: work.timers.filter((timer) => timer.status === "active").length,
  };
}

// formatDuration writes a span as "45s", "12m", "3h 5m" or "2d 4h".
export function formatDuration(milliseconds: number) {
  const seconds = Math.max(0, Math.round(milliseconds / 1000));
  if (seconds < 60) return `${seconds}s`;
  const minutes = Math.floor(seconds / 60);
  if (minutes < 60) return `${minutes}m`;
  const hours = Math.floor(minutes / 60);
  if (hours < 24) return minutes % 60 ? `${hours}h ${minutes % 60}m` : `${hours}h`;
  const days = Math.floor(hours / 24);
  return hours % 24 ? `${days}d ${hours % 24}h` : `${days}d`;
}

// ticketSince is when a Ticket's current state began: when its worker
// started, or when it was queued.
export function ticketSince(ticket: SuperAgentWorkTicket) {
  return ticket.status === "running" ? ticket.started_at || ticket.created_at : ticket.created_at || ticket.updated_at;
}
