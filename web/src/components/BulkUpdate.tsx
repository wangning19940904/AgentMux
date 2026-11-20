import { useEffect, useRef } from "react";
import { Download, RefreshCw, X } from "lucide-react";
import type { OperationProgress } from "../api";
import { useI18n } from "../i18n";
import type { BulkActionResult } from "./bulkUpdateModel";
import { OperationProgress as OperationProgressView } from "./OperationProgress";

export interface BulkUpdateProgress {
  completed: number;
  total: number;
}

// One item of a bulk update. Finished items carry a result; the item being
// updated is running and may carry its live progress; the rest are queued.
export interface BulkUpdateEntry {
  key: string;
  label: string;
  result?: BulkActionResult;
  running?: boolean;
  progress?: OperationProgress;
}

export function BulkUpdateButton({ count, progress, disabled, hint, label, onClick }: {
  count: number;
  progress: BulkUpdateProgress | null;
  disabled: boolean;
  hint: string;
  label?: string;
  onClick: () => void;
}) {
  const { t } = useI18n();
  return (
    <button className="ghost-action" disabled={disabled || Boolean(progress) || count === 0} onClick={onClick} title={hint} type="button">
      {progress ? <RefreshCw className="spin" size={15} /> : <Download size={15} />}
      {progress ? t("common.updatingAll") : label ?? t("common.updateAll")}
      {!progress && count > 0 && <span className="pill on">{count}</span>}
    </button>
  );
}

export function BulkUpdateResults({ progress, results, successLabel, onDismiss }: {
  progress: BulkUpdateProgress | null;
  results: BulkUpdateEntry[];
  successLabel?: string;
  onDismiss?: () => void;
}) {
  const { t } = useI18n();
  if (!progress && results.length === 0) return null;
  const finished = results.filter((entry) => entry.result);
  const failed = finished.filter((entry) => !entry.result?.ok).length;
  const current = results.find((entry) => entry.running && !entry.result);
  const queued = results.filter((entry) => !entry.running && !entry.result);
  return (
    <section className="surface">
      <div className="surface-header bulk-update-header">
        <h2>{t("common.updateAllResults")}</h2>
        {!progress && onDismiss && (
          <button aria-label={t("common.close")} className="ghost-action" onClick={onDismiss} title={t("common.close")} type="button"><X size={15} /></button>
        )}
      </div>
      <div className="surface-body bulk-update-results">
        <div className={`session-notice${!progress && failed ? " error" : ""}`} role="status">
          {progress
            ? <>{t("common.updateAllProgress", { ...progress })}{current && <> {t("common.updateAllCurrent", { name: current.label })}</>}</>
            : t("common.updateAllComplete", { succeeded: finished.length - failed, failed })}
        </div>
        {current && (
          <div className="bulk-update-current">
            <strong>{current.label}</strong>
            <OperationProgressView progress={current.progress ?? { phase: "preparing", percent: 4 }} />
            {current.progress?.log && (
              <details open>
                <summary>{t("common.updateLiveOutput")}</summary>
                <LiveLog text={current.progress.log} />
              </details>
            )}
          </div>
        )}
        {finished.map(({ key, label, result }) => result && (
          <details key={key} open={!result.ok || Boolean(result.warning)}>
            <summary>
              {label} — <span className={result.ok ? result.warning ? "bulk-update-warning" : "bulk-update-ok" : "error"}>
                {result.ok ? result.warning ? t("common.updatedWithWarning") : successLabel ?? t("tools.updated") : t("common.updateFailed")}
              </span>
            </summary>
            {!result.ok && <p className="error">{result.error || t("common.updateFailed")}</p>}
            {result.ok && result.warning && <p className="bulk-update-warning">{result.warning}</p>}
            {result.log && <pre className="framework-log">{result.log}</pre>}
          </details>
        ))}
        {queued.length > 0 && (
          <p className="bulk-update-queued">{t("common.updateQueued", { count: queued.length })} {queued.map((entry) => entry.label).join(" · ")}</p>
        )}
      </div>
    </section>
  );
}

// LiveLog keeps the newest output in view while it grows, unless the reader
// scrolled up to look at something earlier.
function LiveLog({ text }: { text: string }) {
  const ref = useRef<HTMLPreElement>(null);
  const following = useRef(true);
  useEffect(() => {
    const element = ref.current;
    if (element && following.current) element.scrollTop = element.scrollHeight;
  }, [text]);
  return (
    <pre
      className="framework-log"
      onScroll={(event) => {
        const element = event.currentTarget;
        following.current = element.scrollHeight - element.scrollTop - element.clientHeight < 24;
      }}
      ref={ref}
    >{text}</pre>
  );
}
