import type { Framework } from "../../api";
import { targetKey } from "../../components/TargetBadge";

export type FrameworkGroupBy = "framework" | "machine";

export interface FrameworkTableGroup {
  key: string;
  by: FrameworkGroupBy;
  members: Framework[];
}

const GROUP_BY_STORAGE_KEY = "agentmux.frameworks.groupBy";

export function frameworkInstanceKey(item: Framework) {
  return targetKey(item.target_id, item.spec.kind);
}

export function isLocalFramework(item: Framework) {
  return !item.target_id || item.target_id === "local";
}

// groupFrameworks lays out the catalog table: one group per framework with a
// row per machine, or one group per machine with a row per framework. The
// local machine comes first, then the others by name.
export function groupFrameworks(items: Framework[], by: FrameworkGroupBy, machineLabel: (item: Framework) => string): FrameworkTableGroup[] {
  const byName = (left: Framework, right: Framework) => left.spec.display.localeCompare(right.spec.display);
  const byMachine = (left: Framework, right: Framework) => {
    const leftLocal = isLocalFramework(left);
    const rightLocal = isLocalFramework(right);
    if (leftLocal !== rightLocal) return leftLocal ? -1 : 1;
    return machineLabel(left).localeCompare(machineLabel(right));
  };
  const groups = new Map<string, FrameworkTableGroup>();
  for (const item of items) {
    const key = by === "framework" ? `framework:${item.spec.kind}` : `machine:${item.target_id || "local"}`;
    const group = groups.get(key);
    if (group) group.members.push(item);
    else groups.set(key, { key, by, members: [item] });
  }
  const list = [...groups.values()];
  list.forEach((group) => group.members.sort(by === "framework" ? byMachine : byName));
  return list.sort((left, right) => (by === "framework" ? byName : byMachine)(left.members[0], right.members[0]));
}

export function loadFrameworkGroupBy(): FrameworkGroupBy {
  try {
    return localStorage.getItem(GROUP_BY_STORAGE_KEY) === "machine" ? "machine" : "framework";
  } catch {
    return "framework";
  }
}

export function saveFrameworkGroupBy(value: FrameworkGroupBy) {
  try {
    localStorage.setItem(GROUP_BY_STORAGE_KEY, value);
  } catch {
    // The choice only lasts for this visit.
  }
}

// Groups start collapsed; the ones a person opens stay open while they move
// between pages.
const expandedGroups = new Set<string>();

export function expandedFrameworkGroups() {
  return new Set(expandedGroups);
}

export function rememberExpandedFrameworkGroups(keys: Set<string>) {
  expandedGroups.clear();
  keys.forEach((key) => expandedGroups.add(key));
}
