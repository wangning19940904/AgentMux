import { useSyncExternalStore } from "react";
import { api } from "../../api";
import type { Framework, OperationProgress } from "../../api";
import { targetKey } from "../../components/TargetBadge";
import type { BulkActionResult } from "../../components/bulkUpdateModel";
import { runBulkUpdates } from "../../components/bulkUpdateModel";
import { forgetUpdateCheck } from "./frameworkPageCache";

// "Update all" on the Agent frameworks page runs here, outside the panel, so
// leaving the page neither loses the run's progress nor lets a second run
// start beside it. The results stay until the user dismisses them or starts
// another framework action.

export interface FrameworkBulkEntry {
  key: string;
  item: Framework;
  label: string;
  running?: boolean;
  progress?: OperationProgress;
  result?: BulkActionResult;
}

export interface FrameworkBulkRun {
  id: number;
  running: boolean;
  entries: FrameworkBulkEntry[];
  finishedAt?: number;
}

let run: FrameworkBulkRun | null = null;
let nextRunID = 1;
const listeners = new Set<() => void>();

function publish(next: FrameworkBulkRun | null) {
  run = next;
  listeners.forEach((listener) => listener());
}

function patchEntry(id: number, key: string, update: Partial<FrameworkBulkEntry>) {
  if (!run || run.id !== id) return;
  publish({ ...run, entries: run.entries.map((entry) => entry.key === key ? { ...entry, ...update } : entry) });
}

function subscribe(listener: () => void) {
  listeners.add(listener);
  return () => { listeners.delete(listener); };
}

export function useFrameworkBulkRun() {
  return useSyncExternalStore(subscribe, () => run);
}

// subscribeFrameworkBulkRun calls listener with the run after every change.
export function subscribeFrameworkBulkRun(listener: (current: FrameworkBulkRun | null) => void) {
  return subscribe(() => listener(run));
}

export function frameworkBulkRunning() {
  return Boolean(run?.running);
}

export function bulkRunProgress(current: FrameworkBulkRun | null) {
  if (!current?.running) return null;
  return { completed: current.entries.filter((entry) => entry.result).length, total: current.entries.length };
}

// clearFrameworkBulkRun drops finished results; a running batch is kept.
export function clearFrameworkBulkRun() {
  if (run && !run.running) publish(null);
}

// startFrameworkBulkUpdate updates the candidates one at a time, each on its
// own machine. targetID is the machine selection when the run began; it is
// only used for items that carry no machine of their own.
export async function startFrameworkBulkUpdate(
  candidates: { item: Framework; label: string }[],
  targetID: string,
  confirmed: Record<string, string>,
) {
  if (run?.running || candidates.length === 0) return;
  const id = nextRunID++;
  publish({
    id,
    running: true,
    entries: candidates.map(({ item, label }) => ({ key: targetKey(item.target_id, item.spec.kind), item, label })),
  });
  try {
    await runBulkUpdates(candidates, async ({ item }) => {
      const key = targetKey(item.target_id, item.spec.kind);
      const started = Date.now();
      patchEntry(id, key, { running: true, progress: { phase: "checking", percent: 4, started_at: started } });
      try {
        const result = await api.installFramework(item.spec.kind, "update", (progress) => {
          patchEntry(id, key, { progress: { ...progress, started_at: progress.started_at ?? started } });
        }, false, item.target_id || targetID, confirmed[key]);
        return { ok: result.ok, error: result.error, log: result.log, warning: result.warning };
      } finally {
        forgetUpdateCheck(key);
      }
    }, ({ item: { item }, result }) => {
      patchEntry(id, targetKey(item.target_id, item.spec.kind), { running: false, result });
    });
  } finally {
    if (run?.id === id) publish({ ...run, running: false, finishedAt: Date.now() });
  }
}
