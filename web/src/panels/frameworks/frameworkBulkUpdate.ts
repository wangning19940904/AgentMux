import type { Framework, FrameworkUpdateCheck } from "../../api";
import { targetKey } from "../../components/TargetBadge";

export function frameworkUpdateCandidates(items: Framework[], checks: Record<string, FrameworkUpdateCheck>) {
  return items.filter((item) => {
    const check = checks[targetKey(item.target_id, item.spec.kind)];
    return item.installed && item.spec.supported && item.spec.update_supported
      && check?.update_available && !check.error;
  });
}
