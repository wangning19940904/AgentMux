import { CheckCircle2, CircleAlert, RefreshCw } from "lucide-react";
import type { FrameworkSmokeResult } from "../../api";
import { useI18n } from "../../i18n";

type Translate = (key: string, values?: Record<string, string | number>) => string;

export function smokeErrorLabel(t: Translate, kind?: string) {
  const key = `frameworks.health.error.${kind || "unknown"}`;
  const label = t(key);
  return label === key ? t("frameworks.health.error.unknown") : label;
}

export function formatLatency(ms: number) {
  return ms < 1000 ? `${ms} ms` : `${(ms / 1000).toFixed(1)} s`;
}

// FrameworkSmokeResultView shows the outcome of one "reply ok" test. A failure
// shows its classified reason; the framework's own message is in the detail.
export function FrameworkSmokeResultView({ result, busy, showModel = false }: {
  result?: FrameworkSmokeResult;
  busy?: boolean;
  showModel?: boolean;
}) {
  const { t } = useI18n();
  if (busy) {
    return <span className="framework-health-state"><RefreshCw className="spin" size={13} />{t("frameworks.health.testing")}</span>;
  }
  if (!result) return <span className="framework-health-state muted">{t("frameworks.health.untested")}</span>;
  const model = result.model || t("frameworks.health.defaultModel");
  const resolved = result.resolved_model && result.resolved_model !== result.model ? ` → ${result.resolved_model}` : "";
  const when = new Date(result.checked_at).toLocaleString();
  const modelLine = showModel && <span className="framework-health-model mono" title={`${model}${resolved}`}>{model}{resolved}</span>;
  if (result.ok) {
    return (
      <span className="framework-health-result">
        <span className="framework-health-state success" title={t("frameworks.health.checkedAt", { time: when })}>
          <CheckCircle2 size={13} />
          <span className="framework-health-label">{t("frameworks.health.ok", { latency: formatLatency(result.latency_ms) })}</span>
        </span>
        {modelLine}
      </span>
    );
  }
  return (
    <span className="framework-health-result framework-health-failure">
      <span className="framework-health-state warning" title={t("frameworks.health.checkedAt", { time: when })}>
        <CircleAlert size={13} />
        <span className="framework-health-label">{smokeErrorLabel(t, result.error_kind)}</span>
      </span>
      {modelLine}
      {(result.error_detail || result.reply) && (
        <small className="framework-health-detail" title={result.error_detail || result.reply}>
          {result.error_detail || t("frameworks.health.replyWas", { reply: result.reply ?? "" })}
        </small>
      )}
    </span>
  );
}
