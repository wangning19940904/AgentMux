import { describe, expect, it } from "vitest";
import { newSuperAgent, superAgentProblems, withWorkerRuntimes } from "./superAgentModel";

describe("worker profiles", () => {
  const agent = { ...withWorkerRuntimes(newSuperAgent(), ["claudecode", "codex"]), router_agent_id: "router" };

  it("drop with their framework", () => {
    const withProfiles = { ...agent, worker_profiles: [{ id: "deep", runtime_id: "claudecode", model: "opus" }, { id: "fast", runtime_id: "codex", reasoning_effort: "low" }] };
    expect(withWorkerRuntimes(withProfiles, ["codex"]).worker_profiles).toEqual([{ id: "fast", runtime_id: "codex", reasoning_effort: "low" }]);
  });

  it("need a usable name, a unique one, and a model or effort", () => {
    const problems = (profiles: { id: string; runtime_id: string; model?: string; reasoning_effort?: string }[]) =>
      superAgentProblems({ ...agent, worker_profiles: profiles });
    expect(problems([{ id: "deep", runtime_id: "claudecode", model: "opus" }])).toEqual([]);
    expect(problems([{ id: "Deep one", runtime_id: "claudecode", model: "opus" }])).toContain("superAgents.problem.profileID");
    expect(problems([{ id: "deep", runtime_id: "claudecode", model: "a" }, { id: "deep", runtime_id: "claudecode", model: "b" }])).toContain("superAgents.problem.profileDuplicate");
    expect(problems([{ id: "deep", runtime_id: "claudecode", model: "a" }, { id: "deep", runtime_id: "codex", model: "b" }])).toEqual([]);
    expect(problems([{ id: "deep", runtime_id: "claudecode" }])).toContain("superAgents.problem.profileEmpty");
  });
});
