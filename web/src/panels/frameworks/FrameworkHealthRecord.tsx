import { useState } from "react";
import { Activity, CheckCircle2, CircleAlert, CircleDashed, Hourglass } from "lucide-react";
import type { FrameworkHealthRecord, FrameworkHealthSettings } from "../../api";
import { useI18n } from "../../i18n";

const STATE_CLASS: Record<FrameworkHealthRecord["state"], string> = {
  ok: "success",
  failing: "warning",
  usage_limit: "warning",
  unknown: "muted",
};

function StateIcon({ state }: { state: FrameworkHealthRecord["state"] }) {
  if (state === "ok") return <CheckCircle2 size={13} />;
  if (state === "usage_limit") return <Hourglass size={13} />;
  if (state === "failing") return <CircleAlert size={13} />;
  return <CircleDashed size={13} />;
}

// FrameworkHealthRecordView shows a framework's health record on its machine:
// its state, why it is not ok, and where and when that was learned.
export function FrameworkHealthRecordView({ record }: { record?: FrameworkHealthRecord }) {
  const { t } = useI18n();
  if (!record) return null;
  const state = record.state;
  const checked = record.checked_at ? new Date(record.checked_at).toLocaleString() : "";
  const source = record.source ? t(`frameworks.health.source.${record.source}`) : "";
  const resets = record.resets_at ? new Date(record.resets_at).toLocaleString() : "";
  return (
    <span className="framework-health-record" data-state={state}>
      <span className={`framework-health-state ${STATE_CLASS[state] ?? "muted"}`}>
        <StateIcon state={state} />
        <span className="framework-health-label">{t(`frameworks.health.state.${state}`)}</span>
      </span>
      {state !== "ok" && record.reason && (
        <small className="framework-health-detail" title={record.reason}>{record.reason}</small>
      )}
      {resets && state === "usage_limit" && <small className="muted">{t("frameworks.health.resetsAt", { time: resets })}</small>}
      {checked && <small className="muted">{t("frameworks.health.lastCheck", { source, time: checked })}</small>}
    </span>
  );
}

const INTERVALS = [15, 30, 60, 120, 360];

// FrameworkHealthChecksControl turns a machine's periodic checks on or off
// and picks their interval.
export function FrameworkHealthChecksControl({ settings, machine, onSave }: {
  settings?: FrameworkHealthSettings;
  machine: string;
  onSave: (settings: FrameworkHealthSettings) => Promise<unknown>;
}) {
  const { t } = useI18n();
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState("");
  const current = settings ?? { enabled: false, interval_minutes: 30 };
  const intervals = INTERVALS.includes(current.interval_minutes) ? INTERVALS : [...INTERVALS, current.interval_minutes].sort((a, b) => a - b);

  async function save(next: FrameworkHealthSettings) {
    setSaving(true);
    setError("");
    try {
      await onSave(next);
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setSaving(false);
    }
  }

  return (
    <span className="framework-health-checks" title={error || t("frameworks.health.checksHint", { machine })}>
      <label className="framework-health-checks-toggle">
        <input
          type="checkbox"
          checked={current.enabled}
          disabled={saving || !settings}
          onChange={(event) => void save({ ...current, enabled: event.target.checked })}
        />
        <Activity size={14} />{t("frameworks.health.checks")}
      </label>
      {current.enabled && (
        <select
          aria-label={t("frameworks.health.interval")}
          value={current.interval_minutes}
          disabled={saving}
          onChange={(event) => void save({ ...current, interval_minutes: Number(event.target.value) })}
        >
          {intervals.map((minutes) => (
            <option key={minutes} value={minutes}>{t("frameworks.health.intervalMinutes", { minutes })}</option>
          ))}
        </select>
      )}
      {error && <small className="error">{error}</small>}
    </span>
  );
}
