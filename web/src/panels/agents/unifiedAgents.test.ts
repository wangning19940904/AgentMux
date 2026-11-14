import { describe, expect, it } from "vitest";
import type { SuperAgent } from "../../api";
import { canStartWorkers, ownsBrain, superAgentProblems } from "../superAgentModel";
import { agentsWithoutRow, isAgentPart, newAgent, unifiedAgentFor, workPolicy } from "./agentUtils";

function agent(patch: Partial<SuperAgent>): SuperAgent {
  return { id: "", name: "", router_agent_id: "", max_concurrency: 2, workers: [], sources: [], enabled: true, ...patch };
}

describe("unified agents", () => {
  const chat = agent({ id: "ning", name: "宁宝", router_agent_id: "ning" });
  const ops = agent({ id: "sa_ops", name: "Ops", router_agent_id: "sar_ops", auto_workers: true, worker_runtimes: ["codex", "claudecode"] });

  it("finds the agent an Agent row is the brain of, on this machine only", () => {
    const row = { ...newAgent(["codex"]), id: "ning" };
    expect(unifiedAgentFor(row, [ops, chat])).toBe(chat);
    expect(unifiedAgentFor({ ...row, target_id: "dev_1" }, [chat])).toBeUndefined();
    expect(unifiedAgentFor({ ...row, target_id: "local" }, [chat])).toBe(chat);
  });

  it("lists agents that answer with a router agent as rows of their own", () => {
    const rows = [{ ...newAgent(["codex"]), id: "ning" }, { ...newAgent(["codex"]), id: "sar_ops", source: "super_agent_router" }];
    expect(agentsWithoutRow([chat, ops], rows)).toEqual([ops]);
    expect(rows.map(isAgentPart)).toEqual([false, true]);
    expect(isAgentPart({ ...newAgent(["codex"]), id: "saw_x_codex", source: "super_agent" })).toBe(true);
  });

  it("labels what an agent does besides chatting", () => {
    expect(workPolicy(chat)).toEqual({ key: "agents.work.policyChat" });
    expect(workPolicy(undefined)).toEqual({ key: "agents.work.policyChat" });
    expect(workPolicy(ops)).toEqual({ key: "agents.work.policyAuto", params: { frameworks: "codex, claudecode" } });
    expect(workPolicy(agent({ workers: [{ id: "w", agent_id: "a" }] }))).toEqual({ key: "agents.work.policyRoster", params: { count: 1 } });
  });

  it("lets an agent with its own brain only chat, and ignores its bound channels", () => {
    expect(ownsBrain(chat)).toBe(true);
    expect(ownsBrain(ops)).toBe(false);
    expect(canStartWorkers(chat)).toBe(false);
    const withChannel = { ...chat, sources: [{ id: "channel:c1", kind: "channel" as const, enabled: true, allowed_actors: [], config: { channel_id: "c1" } }] };
    expect(superAgentProblems(withChannel)).toEqual([]);
    expect(superAgentProblems(agent({ router_agent_id: "sar_x" }))).toContain("superAgents.problem.noWorkers");
  });
});
