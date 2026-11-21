import type { MachineWork, SuperAgentHelper, SuperAgentRoutingItem, SuperAgentWorkEvent, SuperAgentWorkTicket, SuperAgentWorkTimer } from "../api";

// Each item carries the machine its agent lives on, which is where an action
// on it runs.
type OnMachine = { target_id: string; target_name: string };
export type WorkRouting = SuperAgentRoutingItem & OnMachine;
export type WorkTicket = SuperAgentWorkTicket & OnMachine;
export type WorkTimer = SuperAgentWorkTimer & OnMachine;
export type WorkEvent = SuperAgentWorkEvent & OnMachine;

export type FleetWork = {
  routing: WorkRouting[];
  tickets: WorkTicket[];
  timers: WorkTimer[];
  events: WorkEvent[];
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
  const out: FleetWork = { routing: [], tickets: [], timers: [], events: [], unreachable: [] };
  for (const machine of machines) {
    const where: OnMachine = { target_id: machine.target_id || "local", target_name: machine.target_name || "" };
    if (!machine.work) {
      out.unreachable.push({ ...where, error: machine.error || "unavailable" });
      continue;
    }
    out.routing.push(...(machine.work.routing ?? []).map((item) => ({ ...item, ...where })));
    out.tickets.push(...(machine.work.tickets ?? []).map((item) => ({ ...item, ...where })));
    out.timers.push(...(machine.work.timers ?? []).map((item) => ({ ...item, ...where })));
    out.events.push(...(machine.work.events ?? []).map((item) => ({ ...item, ...where })));
  }
  const time = (value?: string) => (value ? Date.parse(value) || 0 : 0);
  out.routing.sort((a, b) => time(a.received_at) - time(b.received_at));
  out.events.sort((a, b) => time(a.received_at) - time(b.received_at));
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

// A task is everything one request set in motion: the Tickets a router
// started for it, the ones started from their results, what was delivered
// to them later and the follow-ups set for them. It is drawn as a tree that
// grows to the right, one node per step.
export type TaskNode = { id: string; parent?: string; col: number; row: number } & (
  // The message a Ticket came from.
  | { kind: "request"; ticket: WorkTicket }
  // A Ticket, which is one worker's job. via says why it hangs off its
  // parent: started from that Ticket's result or follow-up, built on its
  // commit, or waiting for it.
  | { kind: "ticket"; ticket: WorkTicket; via?: "update" | "timer" | "base" | "depends" }
  // A sub-task the Ticket's worker handed to another worker.
  | { kind: "helper"; ticket: WorkTicket; helper: SuperAgentHelper }
  // A later message a router delivered to the Ticket's worker.
  | { kind: "message"; event: WorkEvent }
  // A follow-up about the Ticket; loose when only its conversation ties it
  // to the task.
  | { kind: "timer"; timer: WorkTimer; loose: boolean }
  // An event about the Ticket that its router has yet to decide on.
  | { kind: "routing"; item: WorkRouting }
);

// A connection between two Tickets besides parent and child: one waits for
// the other, builds on its commit, or was sent its result.
export type TaskLink = { from: string; to: string; kind: "depends" | "base" | "handoff" };

export type WorkTally = { total: number; running: number; queued: number; done: number; failed: number; stopped: number };

export type WorkTask = OnMachine & {
  key: string;
  title: string;
  requester: string;
  super_agent_id: string;
  super_agent_name: string;
  tickets: WorkTicket[];
  timers: WorkTimer[];
  nodes: TaskNode[];
  links: TaskLink[];
  cols: number;
  rows: number;
  // Its workers by state: Tickets and the sub-tasks their workers handed on.
  tally: WorkTally;
  active: boolean;
  started_at: string;
};

export type TaskBoard = {
  tasks: WorkTask[];
  // Requests no Ticket exists for yet.
  incoming: WorkRouting[];
  // Where each follow-up sits: its task and node, when it belongs to one.
  timers: Array<{ timer: WorkTimer; task?: WorkTask; ticket?: WorkTicket; loose: boolean }>;
};

type Unplaced<Node> = Node extends unknown ? Omit<Node, "col" | "row"> : never;

const stamp = (value?: string) => (value ? Date.parse(value) || 0 : 0);
const NODE_ORDER = { request: 0, ticket: 0, helper: 1, message: 2, routing: 3, timer: 4 } as const;

function nodeTime(node: TaskNode) {
  switch (node.kind) {
    case "request": case "ticket": return stamp(node.ticket.created_at || node.ticket.updated_at);
    case "helper": return stamp(node.helper.created_at);
    case "message": return stamp(node.event.received_at);
    case "routing": return stamp(node.item.received_at);
    case "timer": return stamp(node.timer.created_at || node.timer.due_at);
  }
}

// buildTasks sorts a fleet's work into tasks. Tickets belong together when
// one came from, waits for, builds on or was sent the result of another, or
// when one event started both.
export function buildTasks(work: FleetWork): TaskBoard {
  const at = (target: string, id?: string) => `${target}::${id ?? ""}`;
  const tickets = new Map(work.tickets.map((ticket) => [at(ticket.target_id, ticket.id), ticket]));
  const group = new Map([...tickets.keys()].map((key) => [key, key]));
  const find = (key: string): string => {
    let root = key;
    while (group.get(root) !== root) root = group.get(root)!;
    group.set(key, root);
    return root;
  };
  const join = (a: string, b: string) => {
    if (tickets.has(a) && tickets.has(b)) group.set(find(a), find(b));
  };
  const firstOfEvent = new Map<string, string>();
  for (const [key, ticket] of tickets) {
    for (const other of [ticket.origin_ticket_id, ticket.base_ticket, ...(ticket.depends_on ?? [])]) join(key, at(ticket.target_id, other));
    if (!ticket.trigger_id) continue;
    const event = at(ticket.target_id, ticket.trigger_id);
    if (firstOfEvent.has(event)) join(key, firstOfEvent.get(event)!);
    else firstOfEvent.set(event, key);
  }
  for (const event of work.events) join(at(event.target_id, event.ticket_id), at(event.target_id, event.from_ticket_id));

  // A follow-up belongs to the Ticket it names, or failing that to the
  // newest Ticket of its agent on the same conversation.
  const timerHome = (timer: WorkTimer) => {
    const named = tickets.get(at(timer.target_id, timer.ticket_id || timer.from_ticket_id));
    if (named) return { ticket: named, loose: false };
    const sameThread = work.tickets
      .filter((ticket) => ticket.target_id === timer.target_id && ticket.super_agent_id === timer.super_agent_id && Boolean(timer.thread_key) && ticket.thread_key === timer.thread_key)
      .sort((a, b) => stamp(b.created_at) - stamp(a.created_at))[0];
    return { ticket: sameThread, loose: true };
  };

  const members = new Map<string, { tickets: WorkTicket[]; timers: Array<{ timer: WorkTimer; ticket: WorkTicket; loose: boolean }>; events: WorkEvent[]; routing: WorkRouting[] }>();
  const taskOf = (ticket: WorkTicket) => {
    const root = find(at(ticket.target_id, ticket.id));
    if (!members.has(root)) members.set(root, { tickets: [], timers: [], events: [], routing: [] });
    return members.get(root)!;
  };
  for (const ticket of work.tickets) taskOf(ticket).tickets.push(ticket);
  const timerHomes = work.timers.map((timer) => ({ timer, ...timerHome(timer) }));
  for (const home of timerHomes) if (home.ticket) taskOf(home.ticket).timers.push({ timer: home.timer, ticket: home.ticket, loose: home.loose });
  for (const event of work.events) {
    const ticket = tickets.get(at(event.target_id, event.ticket_id));
    if (ticket) taskOf(ticket).events.push(event);
  }
  const incoming: WorkRouting[] = [];
  for (const item of work.routing) {
    const ticket = tickets.get(at(item.target_id, item.ticket_id));
    if (ticket) taskOf(ticket).routing.push(item);
    else incoming.push(item);
  }

  const tasks = [...members.values()].map((member) => layoutTask(member.tickets, member.timers, member.events, member.routing));
  tasks.sort((a, b) => Number(b.active) - Number(a.active) || stamp(b.started_at) - stamp(a.started_at) || a.key.localeCompare(b.key));
  const taskOfTicket = new Map(tasks.flatMap((task) => task.tickets.map((ticket) => [at(ticket.target_id, ticket.id), task] as const)));
  return {
    tasks,
    incoming,
    timers: timerHomes.map((home) => ({ timer: home.timer, ticket: home.ticket, loose: home.loose, task: home.ticket && taskOfTicket.get(at(home.ticket.target_id, home.ticket.id)) })),
  };
}

function layoutTask(
  tickets: WorkTicket[],
  timers: Array<{ timer: WorkTimer; ticket: WorkTicket; loose: boolean }>,
  events: WorkEvent[],
  routing: WorkRouting[],
): WorkTask {
  tickets = [...tickets].sort((a, b) => stamp(a.created_at) - stamp(b.created_at) || a.id.localeCompare(b.id));
  const inTask = new Set(tickets.map((ticket) => ticket.id));
  const nodes = new Map<string, TaskNode>();
  const add = (node: Unplaced<TaskNode>) => nodes.set(node.id, { ...node, col: 0, row: 0 });

  // A follow-up is drawn while it is set, and afterwards only when a Ticket
  // was started from it.
  const spawning = new Set(tickets.map((ticket) => ticket.origin_timer_id).filter(Boolean));
  const shownTimers = timers.filter(({ timer }) => timer.status === "active" || spawning.has(timer.id));
  const timerIDs = new Set(shownTimers.map(({ timer }) => timer.id));

  const requestID = (ticket: WorkTicket) => `q:${ticket.trigger_id || ticket.id}`;
  const parentOf = (ticket: WorkTicket): { parent: string; via?: "update" | "timer" | "base" | "depends" } => {
    if (ticket.origin_timer_id && timerIDs.has(ticket.origin_timer_id)) return { parent: `f:${ticket.origin_timer_id}`, via: "timer" };
    if (ticket.origin_ticket_id && inTask.has(ticket.origin_ticket_id)) return { parent: `t:${ticket.origin_ticket_id}`, via: ticket.origin_kind === "timer" ? "timer" : "update" };
    if (ticket.base_ticket && inTask.has(ticket.base_ticket)) return { parent: `t:${ticket.base_ticket}`, via: "base" };
    const waits = (ticket.depends_on ?? []).find((id) => inTask.has(id));
    if (waits) return { parent: `t:${waits}`, via: "depends" };
    return { parent: requestID(ticket) };
  };
  for (const ticket of tickets) add({ id: `t:${ticket.id}`, kind: "ticket", ticket, ...parentOf(ticket) });
  for (const { timer, ticket, loose } of shownTimers) add({ id: `f:${timer.id}`, kind: "timer", timer, loose, parent: `t:${ticket.id}` });
  // A Ticket that would be its own ancestor hangs off its request instead.
  for (const ticket of tickets) {
    const self = nodes.get(`t:${ticket.id}`)!;
    for (let up = nodes.get(self.parent ?? ""), steps = 0; up; up = nodes.get(up.parent ?? ""), steps++) {
      if (up !== self && steps <= nodes.size) continue;
      self.parent = requestID(ticket);
      if (self.kind === "ticket") self.via = undefined;
      break;
    }
  }
  for (const ticket of tickets) {
    const self = nodes.get(`t:${ticket.id}`)!;
    if (self.parent === requestID(ticket) && !nodes.has(self.parent)) add({ id: self.parent, kind: "request", ticket });
    for (const helper of ticket.helpers ?? []) add({ id: `h:${ticket.id}:${helper.id}`, kind: "helper", ticket, helper, parent: self.id });
  }
  // A follow-up tied to the task only by its conversation hangs off the
  // request its Ticket goes back to.
  for (const { timer, ticket, loose } of shownTimers) {
    if (!loose) continue;
    let up = nodes.get(`t:${ticket.id}`);
    while (up?.parent && nodes.has(up.parent)) up = nodes.get(up.parent);
    nodes.get(`f:${timer.id}`)!.parent = up?.id;
  }
  const links: TaskLink[] = [];
  const link = (from: string, to: string, kind: TaskLink["kind"]) => {
    if (from === to || nodes.get(to)?.parent === from || links.some((item) => item.from === from && item.to === to)) return;
    links.push({ from, to, kind });
  };
  for (const ticket of tickets) {
    for (const id of ticket.depends_on ?? []) if (inTask.has(id)) link(`t:${id}`, `t:${ticket.id}`, "depends");
    if (ticket.base_ticket && inTask.has(ticket.base_ticket)) link(`t:${ticket.base_ticket}`, `t:${ticket.id}`, "base");
  }
  for (const event of events) {
    if (!inTask.has(event.ticket_id)) continue;
    if (event.from_ticket_id && inTask.has(event.from_ticket_id)) link(`t:${event.from_ticket_id}`, `t:${event.ticket_id}`, "handoff");
    else add({ id: `m:${event.id}`, kind: "message", event, parent: `t:${event.ticket_id}` });
  }
  for (const item of routing) add({ id: `r:${item.id}`, kind: "routing", item, parent: `t:${item.ticket_id}` });

  // Each node sits one column right of its parent, on its first child's row;
  // later children take the rows below.
  const children = new Map<string, TaskNode[]>();
  const roots: TaskNode[] = [];
  for (const node of nodes.values()) {
    if (!node.parent || !nodes.has(node.parent)) roots.push(node);
    else children.set(node.parent, [...(children.get(node.parent) ?? []), node]);
  }
  const inOrder = (list: TaskNode[]) => list.sort((a, b) => NODE_ORDER[a.kind] - NODE_ORDER[b.kind] || nodeTime(a) - nodeTime(b) || a.id.localeCompare(b.id));
  let rows = 0;
  let cols = 0;
  const place = (node: TaskNode, col: number) => {
    node.col = col;
    cols = Math.max(cols, col + 1);
    const kids = inOrder(children.get(node.id) ?? []);
    if (kids.length === 0) {
      node.row = rows++;
      return;
    }
    for (const kid of kids) place(kid, col + 1);
    node.row = kids[0].row;
  };
  for (const root of inOrder(roots)) place(root, 0);

  const tally: WorkTally = { total: 0, running: 0, queued: 0, done: 0, failed: 0, stopped: 0 };
  const count = (status: string) => {
    tally.total++;
    tally[({ ready: "done" } as Record<string, keyof WorkTally>)[status] ?? (status as keyof WorkTally)]++;
  };
  for (const ticket of tickets) {
    count(ticket.status);
    for (const helper of ticket.helpers ?? []) count(helper.status);
  }
  const first = tickets[0];
  const asked = tickets.find((ticket) => !ticket.origin_kind) ?? first;
  return {
    key: `${first.target_id}::${first.id}`,
    title: first.title,
    requester: asked.requester_name || asked.actor || "",
    target_id: first.target_id,
    target_name: first.target_name,
    super_agent_id: first.super_agent_id,
    super_agent_name: first.super_agent_name,
    tickets,
    timers: timers.map(({ timer }) => timer),
    nodes: [...nodes.values()].sort((a, b) => a.row - b.row || a.col - b.col),
    links,
    cols,
    rows,
    tally,
    active: tally.running + tally.queued > 0 || routing.length > 0 || timers.some(({ timer }) => timer.status === "active"),
    started_at: first.created_at || first.updated_at,
  };
}

const matchesText = (query: string, texts: Array<string | undefined>) => texts.some((text) => (text ?? "").toLocaleLowerCase().includes(query));

// filterBoard keeps the tasks, requests and follow-ups of one agent that
// mention the query anywhere. A task is kept whole.
export function filterBoard(board: TaskBoard, filter: WorkFilter): TaskBoard {
  const query = filter.query.trim().toLocaleLowerCase();
  const ofAgent = (item: { target_id: string; super_agent_id: string }) => !filter.agent || agentKey(item) === filter.agent;
  const taskMatches = (task: WorkTask) => ofAgent(task) && (!query || task.nodes.some((node) => {
    switch (node.kind) {
      case "request": return matchesText(query, [node.ticket.request, node.ticket.requester_name, node.ticket.actor]);
      case "ticket": return matchesText(query, [
        node.ticket.title, node.ticket.super_agent_name, node.ticket.worker.label, node.ticket.worker.agent_id,
        node.ticket.worker.id, node.ticket.machine_name, node.ticket.id,
      ]);
      case "helper": return matchesText(query, [node.helper.title, node.helper.instructions, node.helper.worker_id]);
      case "message": return matchesText(query, [node.event.text, node.event.actor_name]);
      case "timer": return matchesText(query, [node.timer.title, node.timer.note]);
      case "routing": return matchesText(query, [node.item.title, node.item.text]);
    }
  }));
  const tasks = board.tasks.filter(taskMatches);
  const kept = new Set(tasks);
  return {
    tasks,
    incoming: board.incoming.filter((item) => ofAgent(item) && (!query || matchesText(query, [item.title, item.text, item.actor, item.actor_name, item.super_agent_name, item.thread_key]))),
    timers: board.timers.filter(({ timer, task }) => ofAgent(timer) && (!query || (task && kept.has(task)) || matchesText(query, [timer.title, timer.note, timer.ticket_title, timer.super_agent_name, timer.thread_key]))),
  };
}

export function boardCounts(board: TaskBoard) {
  const sum = (pick: (tally: WorkTally) => number) => board.tasks.reduce((total, task) => total + pick(task.tally), 0);
  return {
    tasks: board.tasks.filter((task) => task.active).length,
    routing: board.incoming.length + board.tasks.reduce((total, task) => total + task.nodes.filter((node) => node.kind === "routing").length, 0),
    running: sum((tally) => tally.running),
    queued: sum((tally) => tally.queued),
    failed: sum((tally) => tally.failed),
    timers: board.timers.filter(({ timer }) => timer.status === "active").length,
  };
}

const TITLE_RUNES = 60;

// timerTitle is the short name a follow-up is listed by: the one its router
// gave, or, for one set before follow-ups had titles, the first sentence of
// its note.
export function timerTitle(timer: { title?: string; note: string }) {
  const squeeze = (text: string) => {
    const runes = Array.from(text.split(/\s+/).filter(Boolean).join(" "));
    return runes.length > TITLE_RUNES ? `${runes.slice(0, TITLE_RUNES).join("")}…` : runes.join("");
  };
  const title = squeeze(timer.title ?? "");
  if (title) return title;
  let note = timer.note.trim().split("\n")[0];
  // A period inside a name such as example.com ends no sentence.
  for (const mark of ["。", "！", "？", "；", ". ", "! ", "? ", "; "]) {
    const end = note.indexOf(mark);
    if (end > 0) note = note.slice(0, end);
  }
  return squeeze(note);
}
