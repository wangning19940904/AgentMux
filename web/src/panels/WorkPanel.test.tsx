// @vitest-environment jsdom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { api } from "../api";
import type { MachineWork, SuperAgentWorkTicket, SuperAgentWorkTimer } from "../api";
import * as confirm from "../components/ConfirmDialog";
import { I18nProvider } from "../i18n";
import { WorkPanel } from "./WorkPanel";
import { filterWork, flattenWork, formatDuration, workAgents, workCounts } from "./workModel";

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
        ticket({ id: "tk_run", title: "Fix the build", request: "please fix the build", requester_name: "Alice", started_at: minutesAgo(12), machine_name: "studio", worker: { id: "auto:codex@dev_1", agent_id: "w", label: "codex", host_id: "dev_1" } }),
      ],
      timers: [timer({ id: "tm_1", ticket_title: "Fix the build" })],
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
  expect(workCounts(all)).toEqual({ routing: 1, running: 1, queued: 1, timers: 1 });
  expect(workAgents(all).map((agent) => agent.key)).toEqual(["local::sa_1", "dev_2::sa_2"]);

  expect(filterWork(all, { agent: "dev_2::sa_2", query: "" }).tickets.map((item) => item.id)).toEqual(["tk_q"]);
  const alice = filterWork(all, { agent: "", query: "alice" });
  expect(alice.tickets.map((item) => item.id)).toEqual(["tk_run"]);
  expect(alice.routing).toHaveLength(0);
  expect(filterWork(all, { agent: "", query: "studio" }).tickets.map((item) => item.id)).toEqual(["tk_run"]);
});

it("formats spans compactly", () => {
  expect(formatDuration(45_000)).toBe("45s");
  expect(formatDuration(12 * 60_000)).toBe("12m");
  expect(formatDuration(185 * 60_000)).toBe("3h 5m");
  expect(formatDuration(52 * 3_600_000)).toBe("2d 4h");
  expect(formatDuration(-5_000)).toBe("0s");
});

it("shows who asked, which worker runs it, and stops a ticket on the machine that keeps it", async () => {
  vi.spyOn(api, "superAgentWork").mockResolvedValue(machines);
  vi.spyOn(confirm, "confirmAction").mockResolvedValue(true);
  const stop = vi.spyOn(api, "stopSuperAgentTicket").mockResolvedValue({ ok: true });
  const cancel = vi.spyOn(api, "cancelSuperAgentTimer").mockResolvedValue({ ok: true });
  await act(async () => root.render(<I18nProvider language="en"><WorkPanel /></I18nProvider>));
  await settle();

  const text = container.textContent ?? "";
  expect(text).toContain("1 with a router");
  expect(text).toContain("1 running");
  expect(text).toContain("1 queued");
  expect(text).toContain("1 follow-ups set");
  expect(text).toContain("No answer from Old Mac");
  expect(text).toContain("Reading now");
  expect(text).toContain("Deploy the release");

  const running = [...container.querySelectorAll(".work-ticket")].find((card) => card.textContent?.includes("Fix the build"))!;
  expect(running.textContent).toContain("please fix the build");
  expect(running.textContent).toContain("Requested byAlice");
  expect(running.textContent).toContain("codex · studio");
  expect(running.textContent).toContain("Running for12m");
  // With more than one machine, each agent says where it lives.
  expect(running.textContent).toContain("Helper · This machine");

  const finished = container.querySelector<HTMLDetailsElement>(".work-finished");
  expect(finished?.textContent).toContain("Finished in the last 24 hours (1)");
  expect(finished?.textContent).toContain("Answer Li");

  const stopButton = [...running.querySelectorAll("button")].find((button) => button.textContent?.includes("Stop"))!;
  await act(async () => stopButton.click());
  await settle();
  expect(stop).toHaveBeenCalledWith("tk_run", "local");

  const queued = [...container.querySelectorAll(".work-ticket")].find((card) => card.textContent?.includes("Write report"))!;
  await act(async () => [...queued.querySelectorAll("button")].find((button) => button.textContent?.includes("Stop"))!.click());
  await settle();
  expect(stop).toHaveBeenLastCalledWith("tk_q", "dev_2");

  const followUp = [...container.querySelectorAll(".work-item")].find((card) => card.textContent?.includes("Check again") && card.textContent?.includes("Set"))!;
  expect(followUp.textContent).toContain("in 1h 30m");
  await act(async () => [...followUp.querySelectorAll("button")].find((button) => button.textContent?.includes("Cancel"))!.click());
  await settle();
  expect(cancel).toHaveBeenCalledWith("tm_1", "local");
});

it("does not stop a ticket the operator declines to stop", async () => {
  vi.spyOn(api, "superAgentWork").mockResolvedValue(machines);
  vi.spyOn(confirm, "confirmAction").mockResolvedValue(false);
  const stop = vi.spyOn(api, "stopSuperAgentTicket").mockResolvedValue({ ok: true });
  await act(async () => root.render(<I18nProvider language="zh"><WorkPanel /></I18nProvider>));
  await settle();
  expect(container.textContent).toContain("运行中的工作");
  const running = [...container.querySelectorAll(".work-ticket")].find((card) => card.textContent?.includes("Fix the build"))!;
  await act(async () => [...running.querySelectorAll("button")].find((button) => button.textContent?.includes("停止"))!.click());
  await settle();
  expect(stop).not.toHaveBeenCalled();
});
