import { Download, RefreshCw } from "lucide-react";
import { useI18n } from "../i18n";
import type { BulkActionResult } from "./bulkUpdateModel";

export interface BulkUpdateProgress {
  completed: number;
  total: number;
}

export function BulkUpdateButton({ count, progress, disabled, hint, onClick }: {
  count: number;
  progress: BulkUpdateProgress | null;
  disabled: boolean;
  hint: string;
  onClick: () => void;
}) {
  const { t } = useI18n();
  return (
    <button className="ghost-action" disabled={disabled || Boolean(progress) || count === 0} onClick={onClick} title={hint} type="button">
      {progress ? <RefreshCw className="spin" size={15} /> : <Download size={15} />}
      {progress ? t("common.updatingAll") : t("common.updateAll")}
      {!progress && count > 0 && <span className="pill on">{count}</span>}
    </button>
  );
}

export function BulkUpdateResults({ progress, results }: {
  progress: BulkUpdateProgress | null;
  results: { key: string; label: string; result: BulkActionResult }[];
}) {
  const { t } = useI18n();
  if (!progress && results.length === 0) return null;
  const failed = results.filter((entry) => !entry.result.ok).length;
  return (
    <section className="surface">
      <div className="surface-header"><h2>{t("common.updateAllResults")}</h2></div>
      <div className="surface-body bulk-update-results">
        <div className={`session-notice${!progress && failed ? " error" : ""}`} role="status">
          {progress ? t("common.updateAllProgress", { ...progress }) : t("common.updateAllComplete", { succeeded: results.length - failed, failed })}
        </div>
        {results.map(({ key, label, result }) => (
          <details key={key} open={!result.ok}>
            <summary>{label} — {result.ok ? t("tools.updated") : t("common.updateFailed")}</summary>
            {!result.ok && <p className="error">{result.error || t("common.updateFailed")}</p>}
            {result.log && <pre className="framework-log">{result.log}</pre>}
          </details>
        ))}
      </div>
    </section>
  );
}
