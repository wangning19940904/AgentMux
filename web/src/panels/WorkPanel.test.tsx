// @vitest-environment jsdom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { api } from "../api";
import type { MachineWork, SuperAgentWorkEvent, SuperAgentWorkTicket, SuperAgentWorkTimer } from "../api";
import * as confirm from "../components/ConfirmDialog";
import { I18nProvider } from "../i18n";
import { WorkPanel } from "./WorkPanel";
import { boardCounts, buildTasks, filterBoard, flattenWork, formatDuration, workAgents } from "./workModel";

let root: Root;
let container: HTMLDivElement;

const now = Date.parse("2026-11-18T10:00:00Z");
const minutesAgo = (minutes: number) => new Date(now - minutes * 60_000).toISOString();

function ticket(overrides: Partial<SuperAgentWorkTicket>): SuperAgentWorkTicket {
  return {
    id: "tk", title: "Ticket", status: "running", super_agent_id: "sa_1", super_agent_name: "Helper",
    worker: { id: "local", agent_id: "coder", label: "Codex" }, updated_at: minutesAgo(1), created_at: minutesAgo(20),
    ...overrides,
  };
}

function timer(overrides: Partial<SuperAgentWorkTimer>): SuperAgentWorkTimer {
  return {
    id: "tm", kind: "follow_up", status: "active", thread_key: "feishu:oc_1", note: "Check again",
    due_at: new Date(now + 90 * 60_000).toISOString(), super_agent_id: "sa_1", super_agent_name: "Helper",
    ...overrides,
  };
}

const machines: MachineWork[] = [
  {
    target_id: "local", target_name: "Local machine",
    work: {
      routing: [{
        id: "tr_1", super_agent_id: "sa_1", super_agent_name: "Helper", source_kind: "feishu_message", thread_key: "feishu:oc_1",
        actor_name: "Bob", text: "Deploy the release", routing: true, received_at: minutesAgo(2),
      }],
      tickets: [
        ticket({ id: "tk_done", title: "Answer Li", status: "ready", output: "Done.", finished_at: minutesAgo(30), created_at: minutesAgo(60) }),
        ticket({ id: "tk_run", title: "Fix the build", source_kind: "feishu_message", request: "please fix the build", requester_name: "Alice", started_at: minutesAgo(12), machine_name: "studio", worker: { id: "auto:codex@dev_1", agent_id: "w", label: "codex", host_id: "dev_1" } }),
      ],
      timers: [timer({ id: "tm_1", ticket_id: "tk_run", ticket_title: "Fix the build" })],
    },
  },
  {
    target_id: "dev_2", target_name: "Linux box",
    work: {
      routing: [],
      tickets: [ticket({ id: "tk_q", title: "Write report", status: "queued", super_agent_id: "sa_2", super_agent_name: "Writer", created_at: minutesAgo(3) })],
      timers: [timer({ id: "tm_old", status: "done", super_agent_id: "sa_2", super_agent_name: "Writer", fired_at: minutesAgo(60), updated_at: minutesAgo(60) })],
    },
  },
  { target_id: "gone", target_name: "Old Mac", error: "device is offline" },
];

beforeEach(() => {
  Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true });
  vi.spyOn(Date, "now").mockReturnValue(now);
  container = document.createElement("div");
  document.body.append(container);
  root = createRoot(container);
});

afterEach(async () => {
  await act(async () => root.unmount());
  container.remove();
  vi.restoreAllMocks();
});

async function settle() {
  await act(async () => { await new Promise((resolve) => setTimeout(resolve, 0)); });
}

it("orders every machine's work, active first, and filters it by agent and text", () => {
  const all = flattenWork(machines);
  expect(all.tickets.map((item) => item.id)).toEqual(["tk_run", "tk_q", "tk_done"]);
  expect(all.tickets.find((item) => item.id === "tk_q")?.target_id).toBe("dev_2");
  expect(all.timers.map((item) => item.id)).toEqual(["tm_1", "tm_old"]);
  expect(all.unreachable).toEqual([{ target_id: "gone", target_name: "Old Mac", error: "device is offline" }]);
  expect(workAgents(all).map((agent) => agent.key)).toEqual(["local::sa_1", "dev_2::sa_2"]);

  const board = buildTasks(all);
  // Unrelated Tickets are tasks of their own: in progress first, newest first.
  expect(board.tasks.map((task) => task.key)).toEqual(["dev_2::tk_q", "local::tk_run", "local::tk_done"]);
  expect(board.incoming.map((item) => item.id)).toEqual(["tr_1"]);
  expect(boardCounts(board)).toEqual({ tasks: 2, routing: 1, running: 1, queued: 1, failed: 0, timers: 1 });

  expect(filterBoard(board, { agent: "dev_2::sa_2", query: "" }).tasks.map((task) => task.key)).toEqual(["dev_2::tk_q"]);
  const alice = filterBoard(board, { agent: "", query: "alice" });
  expect(alice.tasks.map((task) => task.key)).toEqual(["local::tk_run"]);
  expect(alice.incoming).toHaveLength(0);
  // A follow-up stays with the task it belongs to.
  expect(alice.timers.map(({ timer }) => timer.id)).toEqual(["tm_1"]);
  expect(filterBoard(board, { agent: "", query: "studio" }).tasks.map((task) => task.key)).toEqual(["local::tk_run"]);
});

// One request that grew: its worker's result started a second worker, which
// handed a sub-task on, was sent a later message from another chat and has a
// follow-up; a third worker waits for both, and a follow-up that fired
// started a fourth.
function chain(): MachineWork[] {
  const event = (overrides: Partial<SuperAgentWorkEvent>): SuperAgentWorkEvent => ({
    id: "ev", super_agent_id: "sa_1", source_kind: "feishu_message", thread_key: "feishu:oc_1", text: "", ticket_id: "tk_b", received_at: minutesAgo(5),
    ...overrides,
  });
  return [{
    target_id: "local", target_name: "Local machine",
    work: {
      routing: [{ id: "tr_up", super_agent_id: "sa_1", super_agent_name: "Helper", source_kind: "feishu_message", kind: "ticket_update", thread_key: "feishu:oc_1", text: "Ticket tk_c finished", ticket_id: "tk_c", routing: true, received_at: minutesAgo(1) }],
      tickets: [
        ticket({ id: "tk_a", title: "Fix the bug", status: "ready", trigger_id: "tr_a", thread_key: "feishu:oc_1", request: "the login page is broken", requester_name: "Alice", created_at: minutesAgo(300), finished_at: minutesAgo(200) }),
        ticket({
          id: "tk_b", title: "Verify the fix", status: "running", trigger_id: "tr_b", thread_key: "feishu:oc_1", origin_kind: "ticket_update", origin_ticket_id: "tk_a",
          request: "Ticket tk_a finished a turn", requester_name: "AgentMux", created_at: minutesAgo(190), started_at: minutesAgo(189), pending_inputs: 1,
          helpers: [{ id: "h1", worker_id: "browser", title: "Check Safari", instructions: "open the page in Safari", status: "failed", error: "timed out", created_at: minutesAgo(100), finished_at: minutesAgo(90) }],
        }),
        ticket({ id: "tk_c", title: "Write release notes", status: "queued", trigger_id: "tr_b", thread_key: "feishu:oc_1", origin_kind: "ticket_update", origin_ticket_id: "tk_a", depends_on: ["tk_a", "tk_b"], created_at: minutesAgo(180) }),
        ticket({ id: "tk_d", title: "Ping the reviewer", status: "failed", error: "no reviewer", trigger_id: "tr_d", thread_key: "feishu:oc_1", origin_kind: "timer", origin_ticket_id: "tk_b", origin_timer_id: "tm_fired", created_at: minutesAgo(60), finished_at: minutesAgo(50) }),
        ticket({ id: "tk_other", title: "Unrelated", status: "ready", trigger_id: "tr_x", thread_key: "console:x", created_at: minutesAgo(40), finished_at: minutesAgo(30) }),
      ],
      timers: [
        timer({ id: "tm_fired", status: "done", ticket_id: "tk_b", note: "has the reviewer answered?", thread_key: "feishu:oc_1", fired_at: minutesAgo(61), created_at: minutesAgo(120) }),
        timer({ id: "tm_set", ticket_id: "tk_b", ticket_title: "Verify the fix", note: "check the deploy", thread_key: "feishu:oc_1", created_at: minutesAgo(20) }),
        timer({ id: "tm_thread", note: "ask Alice again", thread_key: "feishu:oc_1", created_at: minutesAgo(10) }),
        timer({ id: "tm_alone", note: "weekly report", thread_key: "feishu:oc_9", created_at: minutesAgo(10) }),
      ],
      events: [
        event({ id: "ev_msg", actor_name: "Bob", text: "also test on mobile", thread_key: "feishu:oc_2", ticket_id: "tk_b", received_at: minutesAgo(30) }),
        event({ id: "ev_hand", kind: "ticket_update", text: "Ticket tk_b finished", ticket_id: "tk_c", from_ticket_id: "tk_b", received_at: minutesAgo(15) }),
        event({ id: "ev_d", kind: "ticket_update", text: "Ticket tk_d failed", ticket_id: "tk_a", from_ticket_id: "tk_d", received_at: minutesAgo(12) }),
      ],
    },
  }];
}

it("draws one request and everything it set in motion as one tree", () => {
  const board = buildTasks(flattenWork(chain()));
  expect(board.tasks.map((task) => task.key)).toEqual(["local::tk_a", "local::tk_other"]);
  const task = board.tasks[0];
  expect(task.title).toBe("Fix the bug");
  expect(task.requester).toBe("Alice");
  expect(task.active).toBe(true);
  // Four Tickets and one sub-task handed on.
  expect(task.tally).toEqual({ total: 5, running: 1, queued: 1, done: 1, failed: 2, stopped: 0 });

  const placed = Object.fromEntries(task.nodes.map((node) => [node.id, `${node.col},${node.row} < ${node.parent ?? ""}`]));
  expect(placed).toEqual({
    // The request, its worker and the worker started from its result run
    // along the first row; what hangs off that worker takes the rows below.
    "q:tr_a": "0,0 < ",
    "t:tk_a": "1,0 < q:tr_a",
    "t:tk_b": "2,0 < t:tk_a",
    "h:tk_b:h1": "3,0 < t:tk_b",
    "m:ev_msg": "3,1 < t:tk_b",
    // The follow-up that fired stays, because a worker was started from it.
    "f:tm_fired": "3,2 < t:tk_b",
    "t:tk_d": "4,2 < f:tm_fired",
    "f:tm_set": "3,3 < t:tk_b",
    "t:tk_c": "2,4 < t:tk_a",
    "r:tr_up": "3,4 < t:tk_c",
    // Tied to the task by its conversation alone, so it hangs off the request.
    "f:tm_thread": "1,5 < q:tr_a",
  });
  expect([task.cols, task.rows]).toEqual([5, 6]);
  const via = Object.fromEntries(task.nodes.flatMap((node) => (node.kind === "ticket" ? [[node.ticket.id, node.via]] : [])));
  expect(via).toEqual({ tk_a: undefined, tk_b: "update", tk_c: "update", tk_d: "timer" });
  // A link is drawn only between Tickets that are not already parent and child.
  expect(task.links).toEqual([
    { from: "t:tk_b", to: "t:tk_c", kind: "depends" },
    { from: "t:tk_d", to: "t:tk_a", kind: "handoff" },
  ]);

  const homes = Object.fromEntries(board.timers.map(({ timer, task: home, ticket, loose }) => [timer.id, [home?.key, ticket?.id, loose]]));
  expect(homes).toEqual({
    tm_set: ["local::tk_a", "tk_b", false],
    tm_fired: ["local::tk_a", "tk_b", false],
    tm_thread: ["local::tk_a", "tk_d", true],
    tm_alone: [undefined, undefined, true],
  });
});

it("formats spans compactly", () => {
  expect(formatDuration(45_000)).toBe("45s");
  expect(formatDuration(12 * 60_000)).toBe("12m");
  expect(formatDuration(185 * 60_000)).toBe("3h 5m");
  expect(formatDuration(52 * 3_600_000)).toBe("2d 4h");
  expect(formatDuration(-5_000)).toBe("0s");
});

const cardOf = (title: string) => [...container.querySelectorAll<HTMLElement>(".work-task")].find((card) => card.querySelector(".work-task-title")?.textContent?.includes(title))!;
const nodeOf = (card: HTMLElement, text: string) => [...card.querySelectorAll<HTMLElement>(".work-node")].find((node) => node.textContent?.includes(text))!;
const buttonOf = (scope: HTMLElement, text: string) => [...scope.querySelectorAll("button")].find((button) => button.textContent?.includes(text) || button.getAttribute("aria-label")?.includes(text))!;

it("shows each task's workers and stops one on the machine that keeps it", async () => {
  vi.spyOn(api, "superAgentWork").mockResolvedValue(machines);
  vi.spyOn(confirm, "confirmAction").mockResolvedValue(true);
  const stop = vi.spyOn(api, "stopSuperAgentTicket").mockResolvedValue({ ok: true });
  const cancel = vi.spyOn(api, "cancelSuperAgentTimer").mockResolvedValue({ ok: true });
  await act(async () => root.render(<I18nProvider language="en"><WorkPanel /></I18nProvider>));
  await settle();

  const text = container.textContent ?? "";
  expect(text).toContain("2 tasks in progress");
  expect(text).toContain("1 workers running");
  expect(text).toContain("1 queued");
  expect(text).toContain("1 follow-ups set");
  expect(text).toContain("1 with a router");
  expect(text).toContain("No answer from Old Mac");
  expect(text).toContain("Reading now");
  expect(text).toContain("Deploy the release");

  const running = cardOf("Fix the build");
  // With more than one machine, each agent says where it lives.
  expect(running.querySelector(".work-task-title")?.textContent).toContain("Alice · Helper · This machine · started 20m ago");
  expect(running.querySelector(".work-tally")?.textContent).toBe("1 worker1 running1 follow-ups");
  expect(nodeOf(running, "please fix the build").textContent).toContain("Alice · Feishu message");
  expect(nodeOf(running, "codex").textContent).toContain("Running · 12m · studio · codex");
  expect(nodeOf(running, "Check again").textContent).toContain("in 1h 30m");

  // Selecting the worker shows it in full.
  await act(async () => buttonOf(nodeOf(running, "codex"), "Fix the build").click());
  const detail = running.querySelector<HTMLElement>(".work-node-detail")!;
  expect(detail.textContent).toContain("Running for12m");
  expect(detail.textContent).toContain("codex · studio");
  await act(async () => buttonOf(detail, "Stop").click());
  await settle();
  expect(stop).toHaveBeenCalledWith("tk_run", "local");

  // A worker can be stopped from its node too.
  await act(async () => buttonOf(nodeOf(cardOf("Write report"), "Write report"), "Stop: Write report").click());
  await settle();
  expect(stop).toHaveBeenLastCalledWith("tk_q", "dev_2");

  const finished = container.querySelector<HTMLDetailsElement>(".work-finished")!;
  expect(finished.textContent).toContain("Finished in the last 24 hours (1)");
  expect(finished.textContent).toContain("Answer Li");
  // A finished task keeps its flow closed until it is opened.
  expect(cardOf("Answer Li").querySelector(".work-graph")).toBeNull();
  await act(async () => buttonOf(cardOf("Answer Li"), "Show the flow of Answer Li").click());
  expect(nodeOf(cardOf("Answer Li"), "Answer Li").textContent).toContain("Done");

  const followUp = [...container.querySelectorAll<HTMLElement>(".work-item")].find((card) => card.textContent?.includes("Check again") && card.textContent?.includes("Set"))!;
  expect(followUp.textContent).toContain("in 1h 30m");
  // It names the task it came from, and goes there.
  await act(async () => buttonOf(followUp, "Fix the build").click());
  expect(running.querySelector(".work-node.selected")?.textContent).toContain("Check again");
  expect(running.querySelector(".work-node-detail")?.textContent).toContain("Follow-up");
  await act(async () => buttonOf(followUp, "Cancel").click());
  await settle();
  expect(cancel).toHaveBeenCalledWith("tm_1", "local");
});

it("shows what a request set in motion, and where a later message went", async () => {
  vi.spyOn(api, "superAgentWork").mockResolvedValue(chain());
  await act(async () => root.render(<I18nProvider language="en"><WorkPanel /></I18nProvider>));
  await settle();

  const task = cardOf("Fix the bug");
  expect(task.querySelector(".work-tally")?.textContent).toBe("5 workers1 running1 queued1 done2 failed2 follow-ups");
  expect(task.querySelectorAll(".work-node")).toHaveLength(11);
  // One line to each node's parent, and one for each link besides.
  expect(task.querySelectorAll(".work-graph-edges path.work-edge")).toHaveLength(12);
  expect(task.querySelectorAll(".work-edge-link")).toHaveLength(2);
  expect(nodeOf(task, "Check Safari").className).toContain("tone-failed");
  expect(nodeOf(task, "Router is reading its result")).toBeTruthy();

  await act(async () => buttonOf(nodeOf(task, "also test on mobile"), "Bob").click());
  const message = task.querySelector(".work-node-detail")!.textContent ?? "";
  expect(message).toContain("Delivered toVerify the fix");
  expect(message).toContain("From another conversationfeishu:oc_2");

  await act(async () => buttonOf(nodeOf(task, "Ping the reviewer"), "Ping the reviewer").click());
  const spawned = task.querySelector(".work-node-detail")!.textContent ?? "";
  expect(spawned).toContain("Started when a follow-up came due forVerify the fix");
  expect(spawned).toContain("The result of Ping the reviewer was sent to Fix the bug");
  expect(spawned).toContain("no reviewer");

  await act(async () => buttonOf(nodeOf(task, "Write release notes"), "Write release notes").click());
  const waiting = task.querySelector(".work-node-detail")!.textContent ?? "";
  expect(waiting).toContain("Started from the result ofFix the bug");
  expect(waiting).toContain("Write release notes waits for Verify the fix");

  // A follow-up no task on the page explains says so.
  const alone = [...container.querySelectorAll<HTMLElement>(".work-item")].find((card) => card.textContent?.includes("weekly report"))!;
  expect(alone.textContent).toContain("No task on this page");
  const thread = [...container.querySelectorAll<HTMLElement>(".work-item")].find((card) => card.textContent?.includes("ask Alice again"))!;
  expect(thread.textContent).toContain("(same conversation)");
});

it("does not stop a ticket the operator declines to stop", async () => {
  vi.spyOn(api, "superAgentWork").mockResolvedValue(machines);
  vi.spyOn(confirm, "confirmAction").mockResolvedValue(false);
  const stop = vi.spyOn(api, "stopSuperAgentTicket").mockResolvedValue({ ok: true });
  await act(async () => root.render(<I18nProvider language="zh"><WorkPanel /></I18nProvider>));
  await settle();
  expect(container.textContent).toContain("运行中的工作");
  await act(async () => buttonOf(nodeOf(cardOf("Fix the build"), "codex"), "停止").click());
  await settle();
  expect(stop).not.toHaveBeenCalled();
});
