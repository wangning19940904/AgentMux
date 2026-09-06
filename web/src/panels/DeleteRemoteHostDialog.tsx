import { RefreshCw, Trash2 } from "lucide-react";
import { useEffect, useRef } from "react";
import type { RemoteHost } from "../api";
import { useI18n } from "../i18n";

export function DeleteRemoteHostDialog({ host, busy, error, onClose, onConfirm }: {
  host: RemoteHost;
  busy: boolean;
  error: string;
  onClose: () => void;
  onConfirm: () => void;
}) {
  const { t } = useI18n();
  const dialogRef = useRef<HTMLElement>(null);
  const cancelRef = useRef<HTMLButtonElement>(null);

  useEffect(() => {
    const previousFocus = document.activeElement;
    const previousOverflow = document.body.style.overflow;
    document.body.style.overflow = "hidden";
    cancelRef.current?.focus();
    return () => {
      document.body.style.overflow = previousOverflow;
      if (previousFocus instanceof HTMLElement && previousFocus.isConnected) previousFocus.focus();
    };
  }, []);

  useEffect(() => {
    // Keep keyboard focus in the dialog while both actions are disabled.
    if (busy) dialogRef.current?.focus();
    else cancelRef.current?.focus();
  }, [busy]);

  return (
    <div className="meeting-dialog-layer" role="presentation">
      <button
        aria-label={t("common.cancel")}
        className="meeting-dialog-backdrop internal-dialog-backdrop"
        disabled={busy}
        onClick={onClose}
        tabIndex={-1}
        type="button"
      />
      <section
        ref={dialogRef}
        aria-labelledby="remote-delete-title"
        aria-describedby="remote-delete-description"
        aria-modal="true"
        aria-busy={busy}
        className="surface meeting-dialog tenant-delete-dialog"
        role="alertdialog"
        tabIndex={-1}
        onKeyDown={(event) => {
          if (event.key === "Escape") {
            event.preventDefault();
            event.stopPropagation();
            if (!busy) onClose();
          }
          if (event.key === "Tab") {
            const buttons = Array.from(event.currentTarget.querySelectorAll<HTMLButtonElement>("button:not(:disabled)"));
            const first = buttons[0];
            const last = buttons[buttons.length - 1];
            if (!first || (event.shiftKey && document.activeElement === first) || (!event.shiftKey && document.activeElement === last)) {
              event.preventDefault();
              (event.shiftKey ? last : first)?.focus();
            }
          }
        }}
      >
        <div className="meeting-dialog-icon tenant-delete-dialog-icon"><Trash2 size={22} /></div>
        <div className="meeting-dialog-heading">
          <h2 id="remote-delete-title">{t("remote.deleteConfirm", { name: host.name })}</h2>
          <p id="remote-delete-description">{t("remote.deleteHint")}</p>
        </div>
        {error && <div className="session-notice error" role="alert">{error}</div>}
        <div className="meeting-dialog-actions">
          <button ref={cancelRef} className="ghost-action" disabled={busy} onClick={onClose} type="button">
            {t("common.cancel")}
          </button>
          <button className="ghost-action danger-action" disabled={busy} onClick={onConfirm} type="button">
            {busy ? <RefreshCw className="spin" size={14} /> : <Trash2 size={14} />}
            {busy ? t("remote.deleting") : t("common.delete")}
          </button>
        </div>
      </section>
    </div>
  );
}
