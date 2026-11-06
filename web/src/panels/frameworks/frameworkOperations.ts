import { useSyncExternalStore } from "react";
import { api } from "../../api";
import type { Framework, FrameworkInstallResult, OperationProgress } from "../../api";
import { targetKey } from "../../components/TargetBadge";

export type FrameworkAction = "install" | "update" | "uninstall";

// A running framework install, update or uninstall. Operations live outside
// FrameworksPanel because the install request keeps streaming after the user
// navigates to another page: its progress and outcome must still be there
// when the panel mounts again.
export interface FrameworkOperation {
  key: string;
  item: Framework;
  action: FrameworkAction;
  progress: OperationProgress;
  // Set once the request settles. The panel reports it, reloads the catalog,
  // then calls finishFrameworkOperation, so the row never disappears before
  // the refreshed catalog replaces it.
  outcome?: { result?: FrameworkInstallResult; error?: string };
}

let operations: Readonly<Record<string, FrameworkOperation>> = {};
const listeners = new Set<() => void>();

function publish(next: Record<string, FrameworkOperation>) {
  operations = next;
  listeners.forEach((listener) => listener());
}

function patch(key: string, update: Partial<FrameworkOperation>) {
  const current = operations[key];
  if (current) publish({ ...operations, [key]: { ...current, ...update } });
}

function subscribe(listener: () => void) {
  listeners.add(listener);
  return () => { listeners.delete(listener); };
}

export function useFrameworkOperations() {
  return useSyncExternalStore(subscribe, () => operations);
}

// startFrameworkOperation runs one catalog action for a framework instance.
// It resolves with whether the action succeeded; a second request for an
// instance that already has an operation is ignored. confirmedCommand echoes
// a custom command the user approved.
export async function startFrameworkOperation(
  item: Framework,
  action: FrameworkAction,
  acknowledgeInternal = false,
  confirmedCommand?: string,
): Promise<boolean> {
  const key = targetKey(item.target_id, item.spec.kind);
  if (operations[key]) return false;
  publish({
    ...operations,
    [key]: { key, item, action, progress: { phase: action === "update" ? "checking" : "preparing", percent: 4, started_at: Date.now() } },
  });
  try {
    const result = await api.installFramework(item.spec.kind, action, (update) => {
      patch(key, { progress: { ...update, started_at: operations[key]?.progress.started_at ?? Date.now() } });
    }, acknowledgeInternal, item.target_id, confirmedCommand);
    patch(key, { outcome: { result } });
    return result.ok;
  } catch (error) {
    patch(key, { outcome: { error: error instanceof Error ? error.message : String(error) } });
    return false;
  }
}

export function finishFrameworkOperation(key: string) {
  if (!operations[key]?.outcome) return;
  const next = { ...operations };
  delete next[key];
  publish(next);
}
