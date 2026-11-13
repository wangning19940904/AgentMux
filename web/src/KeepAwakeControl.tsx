import { useEffect, useState } from "react";
import { AlertTriangle, Coffee } from "lucide-react";
import { api } from "./api";
import type { KeepAwakeMode, KeepAwakeRequest, KeepAwakeStatus } from "./api";
import { useI18n } from "./i18n";

const PREFS_KEY = "agentmux.keepAwake.prefs";
const MAX_MINUTES = 1440;
/** Preset durations in minutes; 0 keeps the Mac awake until turned off. */
export const KEEP_AWAKE_PRESETS = [30, 60, 120, 240, 480, 0] as const;
const CUSTOM = "custom";

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

export function KeepAwakeControl({ onError }: { onError: (message: string) => void }) {
  const { t } = useI18n();
  const [status, setStatus] = useState<KeepAwakeStatus | null>(null);
  const [prefs, setPrefs] = useState<KeepAwakePrefs>(() => loadKeepAwakePrefs());
  const [custom, setCustom] = useState(() => !KEEP_AWAKE_PRESETS.includes(prefs.duration as typeof KEEP_AWAKE_PRESETS[number]));
  const [customMinutes, setCustomMinutes] = useState(() => String(prefs.duration || 90));
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
          if (!isPreset) setCustomMinutes(String(running.duration));
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
      setCustom(true);
      const minutes = Number(customMinutes);
      if (Number.isInteger(minutes) && minutes >= 1 && minutes <= MAX_MINUTES) change({ duration: minutes });
      return;
    }
    setCustom(false);
    change({ duration: Number(value) });
  }

  const parsedCustom = Number(customMinutes);
  const customValid = Number.isInteger(parsedCustom) && parsedCustom >= 1 && parsedCustom <= MAX_MINUTES;

  function commitCustom() {
    if (customValid && parsedCustom !== prefs.duration) change({ duration: parsedCustom });
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
          disabled={busy || (!enabled && custom && !customValid)}
          aria-label={t("app.keepAwake")}
          onChange={(event) => void apply(event.target.checked, prefs)}
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
        <div className="preference-keep-awake-controls">
          <label>
            <input
              type="number"
              min={1}
              max={MAX_MINUTES}
              step={1}
              value={customMinutes}
              disabled={busy}
              aria-label={t("app.keepAwakeDuration")}
              aria-invalid={!customValid}
              onChange={(event) => setCustomMinutes(event.target.value)}
              onBlur={commitCustom}
              onKeyDown={(event) => {
                if (event.key === "Enter") commitCustom();
              }}
            />
            <span>{t("app.minutesShort")}</span>
          </label>
        </div>
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
