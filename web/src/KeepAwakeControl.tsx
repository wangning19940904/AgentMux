import { useEffect, useState } from "react";
import { AlertTriangle, Check, ChevronDown, ChevronUp, Coffee } from "lucide-react";
import { api } from "./api";
import type { KeepAwakeMode, KeepAwakeRequest, KeepAwakeStatus } from "./api";
import { useI18n } from "./i18n";

const PREFS_KEY = "agentmux.keepAwake.prefs";
const MAX_MINUTES = 1440;
/** Preset durations in minutes; 0 keeps the Mac awake until turned off. */
export const KEEP_AWAKE_PRESETS = [30, 60, 120, 240, 480, 0] as const;
const CUSTOM = "custom";
/** The minutes column of the custom picker steps in 5-minute increments. */
const MINUTE_STEP = 5;

export interface KeepAwakePrefs {
  duration: number;
  mode: KeepAwakeMode;
  simulate: boolean;
}

const DEFAULT_PREFS: KeepAwakePrefs = { duration: 60, mode: "display", simulate: false };

export function loadKeepAwakePrefs(storage: Pick<Storage, "getItem"> | undefined = globalThis.localStorage): KeepAwakePrefs {
  try {
    const raw = storage?.getItem(PREFS_KEY);
    if (!raw) return DEFAULT_PREFS;
    const parsed = JSON.parse(raw) as Partial<KeepAwakePrefs>;
    const duration = Number(parsed.duration);
    return {
      duration: Number.isInteger(duration) && duration >= 0 && duration <= MAX_MINUTES ? duration : DEFAULT_PREFS.duration,
      mode: parsed.mode === "system" ? "system" : "display",
      simulate: parsed.simulate === true,
    };
  } catch {
    return DEFAULT_PREFS;
  }
}

function saveKeepAwakePrefs(prefs: KeepAwakePrefs) {
  try {
    globalThis.localStorage?.setItem(PREFS_KEY, JSON.stringify(prefs));
  } catch {
    // Preferences are a convenience; private browsing may refuse storage.
  }
}

export function keepAwakeRequest(enabled: boolean, prefs: KeepAwakePrefs): KeepAwakeRequest {
  if (!enabled) return { enabled: false };
  return {
    enabled: true,
    duration_minutes: prefs.duration,
    mode: prefs.mode,
    simulate_activity: prefs.mode === "display" && prefs.simulate,
  };
}

function formatRemaining(seconds: number, t: ReturnType<typeof useI18n>["t"]): string {
  const minutes = Math.max(1, Math.ceil(seconds / 60));
  if (minutes < 60) return t("app.keepAwakeRemaining", { minutes });
  const hours = Math.floor(minutes / 60);
  const rest = minutes % 60;
  return rest === 0
    ? t("app.keepAwakeRemainingHours", { hours })
    : t("app.keepAwakeRemainingHoursMinutes", { hours, minutes: rest });
}

function clampMinutes(total: number): number {
  return Math.min(MAX_MINUTES, Math.max(0, total));
}

/** Keeps the last two digits typed, so typing over a padded "05" still works. */
function parseDigits(raw: string): number {
  const digits = raw.replace(/\D/g, "").slice(-2);
  return digits ? Number(digits) : 0;
}

interface DurationPickerProps {
  /** Draft duration in minutes; not applied until confirmed. */
  value: number;
  disabled: boolean;
  canConfirm: boolean;
  onChange: (minutes: number) => void;
  onConfirm: () => void;
}

/** An hours and minutes picker whose value only takes effect on Confirm. */
export function KeepAwakeDurationPicker({ value, disabled, canConfirm, onChange, onConfirm }: DurationPickerProps) {
  const { t } = useI18n();
  const hours = Math.floor(value / 60);
  const minutes = value % 60;

  const columns = [
    {
      key: "hours",
      name: t("app.keepAwakePickerHours"),
      unit: t("app.hoursShort"),
      display: String(hours),
      canDecrease: hours > 0,
      canIncrease: value < MAX_MINUTES,
      step: (direction: 1 | -1) => onChange(clampMinutes(value + direction * 60)),
      set: (next: number) => onChange(clampMinutes(next * 60 + minutes)),
    },
    {
      key: "minutes",
      name: t("app.keepAwakePickerMinutes"),
      unit: t("app.minutesShort"),
      display: String(minutes).padStart(2, "0"),
      canDecrease: value > 0,
      canIncrease: value < MAX_MINUTES,
      // Snap to the 5-minute grid, carrying into the hours column.
      step: (direction: 1 | -1) => onChange(clampMinutes(
        direction > 0
          ? Math.floor(value / MINUTE_STEP) * MINUTE_STEP + MINUTE_STEP
          : Math.ceil(value / MINUTE_STEP) * MINUTE_STEP - MINUTE_STEP,
      )),
      set: (next: number) => onChange(clampMinutes(hours * 60 + Math.min(next, 59))),
    },
  ];

  return (
    <div className="keep-awake-picker" role="group" aria-label={t("app.keepAwakeDuration")}>
      <div className="keep-awake-picker-columns">
        {columns.map((column, index) => (
          <div key={column.key} className="keep-awake-picker-column-wrap">
            {index > 0 && <span className="keep-awake-picker-separator" aria-hidden="true">:</span>}
            <div className="keep-awake-picker-column">
              <button
                type="button"
                tabIndex={-1}
                aria-label={t("app.keepAwakeIncrease", { unit: column.name })}
                disabled={disabled || !column.canIncrease}
                onClick={() => column.step(1)}
              >
                <ChevronUp size={14} />
              </button>
              <label>
                <input
                  type="text"
                  inputMode="numeric"
                  autoComplete="off"
                  value={column.display}
                  disabled={disabled}
                  aria-label={column.name}
                  onFocus={(event) => event.target.select()}
                  onChange={(event) => column.set(parseDigits(event.target.value))}
                  onKeyDown={(event) => {
                    if (event.key === "ArrowUp" || event.key === "ArrowDown") {
                      event.preventDefault();
                      column.step(event.key === "ArrowUp" ? 1 : -1);
                    } else if (event.key === "Enter" && canConfirm) {
                      onConfirm();
                    }
                  }}
                />
                <span>{column.unit}</span>
              </label>
              <button
                type="button"
                tabIndex={-1}
                aria-label={t("app.keepAwakeDecrease", { unit: column.name })}
                disabled={disabled || !column.canDecrease}
                onClick={() => column.step(-1)}
              >
                <ChevronDown size={14} />
              </button>
            </div>
          </div>
        ))}
      </div>
      <div className="keep-awake-picker-actions">
        <button type="button" className="action" disabled={disabled || !canConfirm} onClick={onConfirm}>
          <Check size={13} />
          <span>{t("app.keepAwakeConfirm")}</span>
        </button>
        {value < 1 && <small className="keep-awake-picker-hint">{t("app.keepAwakeDurationTooShort")}</small>}
      </div>
    </div>
  );
}

export function KeepAwakeControl({ onError }: { onError: (message: string) => void }) {
  const { t } = useI18n();
  const [status, setStatus] = useState<KeepAwakeStatus | null>(null);
  const [prefs, setPrefs] = useState<KeepAwakePrefs>(() => loadKeepAwakePrefs());
  const [custom, setCustom] = useState(() => !KEEP_AWAKE_PRESETS.includes(prefs.duration as typeof KEEP_AWAKE_PRESETS[number]));
  // The custom picker's value, applied only when the user confirms it.
  const [draft, setDraft] = useState(() => prefs.duration || 90);
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    let active = true;
    api.keepAwakeStatus()
      .then((next) => {
        if (!active) return;
        setStatus(next);
        if (next.enabled) {
          // The running assertion is the source of truth for the controls.
          const running: KeepAwakePrefs = {
            duration: next.duration_minutes,
            mode: next.mode,
            simulate: next.simulate_activity,
          };
          setPrefs(running);
          const isPreset = KEEP_AWAKE_PRESETS.includes(running.duration as typeof KEEP_AWAKE_PRESETS[number]);
          setCustom(!isPreset);
          if (!isPreset) setDraft(running.duration);
        }
      })
      .catch((error) => {
        if (active) onError(error instanceof Error ? error.message : String(error));
      });
    return () => {
      active = false;
    };
  }, [onError]);

  useEffect(() => {
    if (!status?.enabled) return;
    const timer = window.setInterval(() => {
      api.keepAwakeStatus()
        .then(setStatus)
        .catch(() => undefined);
    }, 15_000);
    return () => window.clearInterval(timer);
  }, [status?.enabled]);

  async function apply(enabled: boolean, next: KeepAwakePrefs) {
    setPrefs(next);
    saveKeepAwakePrefs(next);
    setBusy(true);
    onError("");
    try {
      setStatus(await api.setKeepAwake(keepAwakeRequest(enabled, next)));
    } catch (error) {
      onError(error instanceof Error ? error.message : String(error));
    } finally {
      setBusy(false);
    }
  }

  /** Saves a preference change and re-applies it if keep-awake is running. */
  function change(patch: Partial<KeepAwakePrefs>) {
    const next = { ...prefs, ...patch };
    if (status?.enabled) {
      void apply(true, next);
      return;
    }
    setPrefs(next);
    saveKeepAwakePrefs(next);
  }

  function selectDuration(value: string) {
    if (value === CUSTOM) {
      // Start the picker from the current duration; nothing changes until Confirm.
      setCustom(true);
      if (prefs.duration > 0) setDraft(prefs.duration);
      return;
    }
    const minutes = Number(value);
    setCustom(false);
    if (minutes > 0) setDraft(minutes);
    change({ duration: minutes });
  }

  const draftValid = draft >= 1 && draft <= MAX_MINUTES;
  const draftPending = custom && draftValid && draft !== prefs.duration;

  function confirmDraft() {
    if (draftPending) change({ duration: draft });
  }

  if (!status?.supported) return null;

  const enabled = status.enabled;
  const durationLabel = (minutes: number) => {
    if (minutes === 0) return t("app.keepAwakeUntilOff");
    if (minutes < 60) return t("app.keepAwakeMinutes", { minutes });
    return t("app.keepAwakeHours", { hours: minutes / 60 });
  };

  let summary = t("app.keepAwakeHint");
  if (enabled) {
    summary = status.duration_minutes === 0
      ? t("app.keepAwakeActiveUntilOff")
      : formatRemaining(status.remaining_seconds, t);
  }

  const simulateAvailable = prefs.mode === "display";
  const activityState = enabled && status.simulate_activity ? status.activity_state : undefined;

  return (
    <div className={`preference-keep-awake${enabled ? " active" : ""}`}>
      <label className="preference-keep-awake-title">
        <Coffee size={16} />
        <span>
          <strong>{t("app.keepAwake")}</strong>
          <small>{summary}</small>
        </span>
        <input
          type="checkbox"
          role="switch"
          checked={enabled}
          disabled={busy}
          aria-label={t("app.keepAwake")}
          // Turning on with an unconfirmed custom duration uses what the picker shows.
          onChange={(event) => void apply(event.target.checked, event.target.checked && draftPending ? { ...prefs, duration: draft } : prefs)}
        />
      </label>

      <div className="preference-keep-awake-row">
        <span>{t("app.keepAwakeDurationLabel")}</span>
        <select
          value={custom ? CUSTOM : String(prefs.duration)}
          disabled={busy}
          aria-label={t("app.keepAwakeDurationLabel")}
          onChange={(event) => selectDuration(event.target.value)}
        >
          {KEEP_AWAKE_PRESETS.map((minutes) => (
            <option key={minutes} value={String(minutes)}>{durationLabel(minutes)}</option>
          ))}
          <option value={CUSTOM}>{t("app.keepAwakeCustom")}</option>
        </select>
      </div>
      {custom && (
        <KeepAwakeDurationPicker
          value={draft}
          disabled={busy}
          canConfirm={draftPending}
          onChange={setDraft}
          onConfirm={confirmDraft}
        />
      )}

      <div className="segmented keep-awake-mode" role="radiogroup" aria-label={t("app.keepAwakeModeLabel")}>
        {(["display", "system"] as const).map((mode) => (
          <button
            key={mode}
            type="button"
            role="radio"
            aria-checked={prefs.mode === mode}
            className={prefs.mode === mode ? "active" : ""}
            disabled={busy}
            title={t(mode === "display" ? "app.keepAwakeModeDisplayHint" : "app.keepAwakeModeSystemHint")}
            onClick={() => prefs.mode !== mode && change({ mode })}
          >
            <span>{t(mode === "display" ? "app.keepAwakeModeDisplay" : "app.keepAwakeModeSystem")}</span>
          </button>
        ))}
      </div>

      {simulateAvailable && (
        <label className="preference-keep-awake-simulate">
          <input
            type="checkbox"
            checked={prefs.simulate}
            disabled={busy}
            onChange={(event) => change({ simulate: event.target.checked })}
          />
          <span>
            <strong>{t("app.keepAwakeSimulate")}</strong>
            <small>{t("app.keepAwakeSimulateHint")}</small>
          </span>
        </label>
      )}

      {activityState === "active" && (
        <small className="keep-awake-activity ok">{t("app.keepAwakeActivityActive")}</small>
      )}
      {activityState === "waiting" && (
        <small className="keep-awake-activity">{t("app.keepAwakeActivityWaiting")}</small>
      )}
      {(activityState === "blocked" || activityState === "error") && (
        <div className="keep-awake-activity warn" role="alert">
          <AlertTriangle size={13} />
          <span>
            {activityState === "blocked" ? t("app.keepAwakeActivityBlocked") : t("app.keepAwakeActivityError", { error: status.activity_error || "" })}
            {activityState === "blocked" && (
              <button
                type="button"
                onClick={() => {
                  api.requestKeepAwakeAccessibility().catch((error) => onError(error instanceof Error ? error.message : String(error)));
                }}
              >
                {t("app.keepAwakeGrantAccessibility")}
              </button>
            )}
          </span>
        </div>
      )}
    </div>
  );
}
