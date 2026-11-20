import type { Framework, FrameworkUpdateCheck } from "../../api";
import { targetKey } from "../../components/TargetBadge";

/**
 * Whether AgentMux can act on a check: a newer version it can install, or a
 * broken installation it can reinstall. A copy shipped inside another
 * application (Codex in ChatGPT.app) updates with that application instead.
 */
export function frameworkCheckActionable(check: FrameworkUpdateCheck | undefined) {
  if (!check) return false;
  if (check.repairable) return true;
  return check.update_available && !check.error && !check.managed_by;
}

export function frameworkUpdateCandidates(items: Framework[], checks: Record<string, FrameworkUpdateCheck>) {
  return items.filter((item) => {
    const check = checks[targetKey(item.target_id, item.spec.kind)];
    return item.installed && item.spec.supported && item.spec.update_supported
      && frameworkCheckActionable(check);
  });
}
