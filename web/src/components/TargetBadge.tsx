import type { TargetMetadata } from "../api";
import { useI18n } from "../i18n";

export function TargetBadge({ target_id, target_name }: TargetMetadata) {
  const { t } = useI18n();
  if (!target_id) return null;
  return <span className="target-badge">{target_id === "local" ? t("remote.localMachine") : target_name || target_id}</span>;
}

export function targetKey(targetID: string | undefined, resourceID: string) {
  return `${targetID || "local"}::${resourceID}`;
}
