import { AlertTriangle, CheckCircle2, Clock3 } from "lucide-react";
import { useI18n } from "../../i18n";
import type { ProviderModelHealthRow } from "./providerModelHealth";

export function ProviderModelBadges({ rows }: { rows: ProviderModelHealthRow[] }) {
  const { t } = useI18n();
  if (rows.length === 0) return <span className="pill">—</span>;

  return (
    <>
      {rows.map((row) => {
        const unhealthy = row.state === "unhealthy";
        const stateLabel =
          row.state === "healthy"
            ? t("gateway.modelHealthHealthy")
            : unhealthy
              ? t("gateway.modelHealthUnhealthy")
              : t("gateway.modelHealthUnknown");
        const offlineLabel = row.offline ? ` · ${t("gateway.modelHealthOffline")}` : "";
        const errorDetail = row.message || (row.statusCode ? `HTTP ${row.statusCode}` : stateLabel);
        const accessibleLabel = unhealthy
          ? `${row.model}: ${stateLabel}${offlineLabel} · ${errorDetail}`
          : `${row.model}: ${stateLabel}`;

        return (
          <span
            className={`pill provider-model-pill ${row.state}${row.offline ? " offline" : ""}`}
            key={row.model}
            aria-label={accessibleLabel}
            tabIndex={unhealthy ? 0 : undefined}
            title={unhealthy ? accessibleLabel : stateLabel}
          >
            <span className="provider-model-name">{row.model}</span>
            <span className="provider-model-health-indicator" aria-hidden="true">
              {row.state === "healthy" ? (
                <CheckCircle2 size={13} />
              ) : unhealthy ? (
                <AlertTriangle size={13} />
              ) : (
                <Clock3 size={13} />
              )}
            </span>
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
