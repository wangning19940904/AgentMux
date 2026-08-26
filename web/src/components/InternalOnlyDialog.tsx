import { TriangleAlert } from "lucide-react";
import { useI18n } from "../i18n";

export function InternalOnlyDialog({
  name,
  components = [],
  onCancel,
  onConfirm,
}: {
  name: string;
  components?: string[];
  onCancel: () => void;
  onConfirm: () => void;
}) {
  const { t } = useI18n();
  return (
    <div className="meeting-dialog-layer" role="presentation">
      <button className="meeting-dialog-backdrop internal-dialog-backdrop" aria-label={t("common.cancel")} onClick={onCancel} type="button" />
      <section className="surface meeting-dialog internal-only-dialog" role="dialog" aria-modal="true" aria-labelledby="internal-only-title">
        <div className="meeting-dialog-icon"><TriangleAlert size={22} /></div>
        <div className="meeting-dialog-heading">
          <span>{t("tools.internalBadge")}</span>
          <h2 id="internal-only-title">{t("tools.internalConfirmTitle")}</h2>
          <p>{t("tools.internalConfirmBody", { name })}</p>
        </div>
        {components.length > 0 && (
          <ul className="internal-component-list">
            {components.map((component) => <li key={component}>{component}</li>)}
          </ul>
        )}
        <p className="meeting-dialog-hint">{t("tools.internalConfirmHint")}</p>
        <div className="meeting-dialog-actions">
          <button className="ghost-action" onClick={onCancel} type="button">{t("common.cancel")}</button>
          <button className="action" onClick={onConfirm} type="button">{t("tools.internalConfirmAction")}</button>
        </div>
      </section>
    </div>
  );
}
