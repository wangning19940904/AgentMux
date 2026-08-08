import { useEffect, useState } from "react";
import type { OperationProgress as Progress } from "../api";
import { useI18n } from "../i18n";

const knownPhases = new Set(["preparing", "checking", "installing", "updating", "syncing", "verifying", "complete"]);

export function OperationProgress({ progress }: { progress: Progress }) {
  const { t } = useI18n();
  const [elapsed, setElapsed] = useState(() => elapsedSeconds(progress.started_at));

  useEffect(() => {
    const update = () => setElapsed(elapsedSeconds(progress.started_at));
    update();
    const timer = window.setInterval(update, 1_000);
    return () => window.clearInterval(timer);
  }, [progress.started_at]);

  const phase = knownPhases.has(progress.phase) ? progress.phase : "installing";
  const label = t(`installProgress.${phase}`);
  const percent = Math.min(100, Math.max(4, progress.percent));

  return (
    <div className="operation-progress" aria-live="polite">
      <div className="operation-progress-head">
        <strong>{label}</strong>
        <span>{t("installProgress.elapsed", { time: formatElapsed(elapsed) })}</span>
      </div>
      <div
        className="operation-progress-track"
        role="progressbar"
        aria-label={label}
        aria-valuemin={0}
        aria-valuemax={100}
        aria-valuenow={progress.percent}
      >
        <span className="operation-progress-bar" style={{ width: `${percent}%` }} />
      </div>
      {progress.detail && <code title={progress.detail}>{progress.detail}</code>}
    </div>
  );
}

function elapsedSeconds(startedAt?: number) {
  return startedAt ? Math.max(0, Math.floor((Date.now() - startedAt) / 1_000)) : 0;
}

function formatElapsed(totalSeconds: number) {
  const minutes = Math.floor(totalSeconds / 60);
  const seconds = totalSeconds % 60;
  return `${String(minutes).padStart(2, "0")}:${String(seconds).padStart(2, "0")}`;
}
