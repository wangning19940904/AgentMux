import { describe, expect, it } from "vitest";
import { parseOrchestrationTasksJSON } from "./orchestrationModel";

describe("orchestration task parser", () => {
  it("accepts a dependency DAG and trims fields", () => {
    const tasks = parseOrchestrationTasksJSON(JSON.stringify([
      { id: "research", agent_id: " agent-a ", input: " inspect " },
      { id: "review", project: "lead", input: "review", depends_on: ["research"] },
    ]));
    expect(tasks).toEqual([
      { id: "research", agent_id: "agent-a", input: "inspect", depends_on: [] },
      { id: "review", project: "lead", input: "review", depends_on: ["research"] },
    ]);
  });

  it.each([
    [[{ id: "a", agent_id: "one", project: "two", input: "bad" }]],
    [[{ id: "a", agent_id: "one", input: "bad", depends_on: ["missing"] }]],
    [[{ id: "a", agent_id: "one", input: "one" }, { id: "a", agent_id: "two", input: "two" }]],
  ])("rejects an invalid task graph", (tasks) => {
    expect(() => parseOrchestrationTasksJSON(JSON.stringify(tasks))).toThrow();
  });
});
