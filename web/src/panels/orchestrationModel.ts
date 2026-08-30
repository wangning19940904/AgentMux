import type { OrchestrationTask } from "../api";

export type OrchestrationTaskDraft = Pick<OrchestrationTask, "id" | "agent_id" | "input" | "depends_on">;

export function parseOrchestrationTasksJSON(value: string): OrchestrationTaskDraft[] {
  const parsed: unknown = JSON.parse(value);
  if (!Array.isArray(parsed)) throw new Error("Tasks must be a JSON array.");
  const ids = new Set<string>();
  const tasks = parsed.map((raw, index) => {
    if (!raw || typeof raw !== "object" || Array.isArray(raw)) throw new Error(`Task ${index + 1} must be an object.`);
    const item = raw as Record<string, unknown>;
    const id = stringValue(item.id);
    const agentID = stringValue(item.agent_id);
    const input = stringValue(item.input);
    if (!/^[A-Za-z0-9][A-Za-z0-9_.-]{0,63}$/.test(id) || ids.has(id)) throw new Error(`Task id "${id}" is invalid or duplicated.`);
		if (!agentID) throw new Error(`Task "${id}" must set agent_id.`);
    if (!input) throw new Error(`Task "${id}" input is required.`);
    ids.add(id);
    const dependsOn = Array.isArray(item.depends_on)
      ? item.depends_on.map(stringValue).filter(Boolean)
      : [];
		return { id, agent_id: agentID, input, depends_on: dependsOn };
  });
  for (const task of tasks) {
    for (const dependency of task.depends_on ?? []) {
      if (dependency === task.id || !ids.has(dependency)) throw new Error(`Task "${task.id}" has invalid dependency "${dependency}".`);
    }
  }
  return tasks;
}

function stringValue(value: unknown): string {
  return typeof value === "string" ? value.trim() : "";
}
