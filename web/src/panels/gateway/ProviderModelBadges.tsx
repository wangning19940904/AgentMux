import { AlertTriangle, Ban, CheckCircle2, Clock3, RotateCcw } from "lucide-react";
import { useI18n } from "../../i18n";
import type { ProviderModelHealthRow } from "./providerModelHealth";

export function ProviderModelBadges({ rows, busy, onToggleBlocked }: {
  rows: ProviderModelHealthRow[];
  busy?: boolean;
  onToggleBlocked?: (model: string, blocked: boolean) => void;
}) {
  const { t } = useI18n();
  if (rows.length === 0) return <span className="pill">—</span>;

  return (
    <>
      {rows.map((row) => {
        const unhealthy = row.state === "unhealthy";
        const stateLabel =
          row.blocked ? t("gateway.modelBlocked") : row.state === "healthy"
            ? t("gateway.modelHealthHealthy")
            : unhealthy
              ? t("gateway.modelHealthUnhealthy")
              : t("gateway.modelHealthUnknown");
        const offlineLabel = row.offline && !row.blocked ? ` · ${t("gateway.modelHealthOffline")}` : "";
        const errorDetail = row.message || (row.statusCode ? `HTTP ${row.statusCode}` : stateLabel);
        const accessibleLabel = unhealthy
          ? `${row.model}: ${stateLabel}${offlineLabel} · ${errorDetail}`
          : `${row.model}: ${stateLabel}`;

        return (
          <span
            className={`pill provider-model-pill ${row.blocked ? "blocked" : row.state}${row.offline ? " offline" : ""}`}
            key={row.model}
            aria-label={accessibleLabel}
            tabIndex={unhealthy ? 0 : undefined}
            title={unhealthy ? accessibleLabel : stateLabel}
          >
            <span className="provider-model-name">{row.model}</span>
            <span className="provider-model-health-indicator" aria-hidden="true">
              {row.blocked ? <Ban size={13} /> : row.state === "healthy" ? (
                <CheckCircle2 size={13} />
              ) : unhealthy ? (
                <AlertTriangle size={13} />
              ) : (
                <Clock3 size={13} />
              )}
            </span>
            {onToggleBlocked && <button type="button" className="provider-model-block-action" disabled={busy}
              aria-label={t(row.blocked ? "gateway.restoreModel" : "gateway.blockModel", { model: row.model })}
              title={t(row.blocked ? "gateway.restoreModel" : "gateway.blockModel", { model: row.model })}
              onClick={() => onToggleBlocked(row.model, !row.blocked)}>
              {row.blocked ? <RotateCcw size={12} /> : <Ban size={12} />}
            </button>}
            {unhealthy && (
              <span className="provider-model-health-tooltip" role="tooltip">
                <strong>{row.model}</strong>
                <span>{stateLabel}{offlineLabel}</span>
                <span>{errorDetail}</span>
              </span>
            )}
          </span>
        );
      })}
    </>
  );
}
