import { useEffect, useRef, useSyncExternalStore } from "react";
import { TriangleAlert, Trash2 } from "lucide-react";
import { useI18n } from "../i18n";

// The desktop app's WebView (Wails on macOS) has no handler for JavaScript
// dialogs, so window.confirm returns false there without showing anything.
// confirmAction shows an in-app dialog instead and resolves with the answer.

export type ConfirmOptions = {
  title?: string;
  confirmLabel?: string;
  danger?: boolean;
};

type Request = ConfirmOptions & {
  id: number;
  message: string;
  resolve: (accepted: boolean) => void;
};

let queue: Request[] = [];
let nextID = 1;
let hosts = 0;
const listeners = new Set<() => void>();

function emit() {
  for (const listener of listeners) listener();
}

function subscribe(listener: () => void) {
  listeners.add(listener);
  return () => { listeners.delete(listener); };
}

function current() {
  return queue[0] ?? null;
}

export function confirmAction(message: string, options: ConfirmOptions = {}): Promise<boolean> {
  // Without a mounted host (a panel rendered on its own), use the browser dialog.
  if (hosts === 0) {
    return Promise.resolve(typeof window !== "undefined" && typeof window.confirm === "function" ? window.confirm(message) : false);
  }
  return new Promise((resolve) => {
    queue = [...queue, { ...options, id: nextID++, message, resolve }];
    emit();
  });
}

function settle(id: number, accepted: boolean) {
  const request = queue.find((item) => item.id === id);
  if (!request) return;
  queue = queue.filter((item) => item.id !== id);
  request.resolve(accepted);
  emit();
}

// ConfirmHost renders pending confirmations; mount it once inside I18nProvider.
export function ConfirmHost() {
  const { t } = useI18n();
  const request = useSyncExternalStore(subscribe, current, current);
  const confirmButton = useRef<HTMLButtonElement>(null);

  useEffect(() => {
    hosts += 1;
    return () => {
      hosts -= 1;
      if (hosts === 0) {
        const pending = queue;
        queue = [];
        for (const item of pending) item.resolve(false);
        emit();
      }
    };
  }, []);

  useEffect(() => {
    if (!request) return;
    confirmButton.current?.focus();
    const onKey = (event: KeyboardEvent) => {
      if (event.key === "Escape") {
        event.preventDefault();
        settle(request.id, false);
      }
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [request]);

  if (!request) return null;
  const cancel = () => settle(request.id, false);
  const accept = () => settle(request.id, true);
  return (
    <div className="meeting-dialog-layer confirm-dialog-layer" role="presentation">
      <button aria-label={t("common.cancel")} className="meeting-dialog-backdrop internal-dialog-backdrop" onClick={cancel} type="button" />
      <section
        aria-describedby="confirm-dialog-message"
        aria-labelledby="confirm-dialog-title"
        aria-modal="true"
        className={`surface meeting-dialog${request.danger ? " confirm-delete-dialog" : ""}`}
        role="alertdialog"
      >
        <div className={`meeting-dialog-icon${request.danger ? " confirm-delete-dialog-icon" : ""}`}>
          {request.danger ? <Trash2 size={22} /> : <TriangleAlert size={22} />}
        </div>
        <div className="meeting-dialog-heading">
          <h2 id="confirm-dialog-title">{request.title ?? t("common.confirmTitle")}</h2>
          <p id="confirm-dialog-message" style={{ whiteSpace: "pre-line" }}>{request.message}</p>
        </div>
        <div className="meeting-dialog-actions">
          <button className="ghost-action" onClick={cancel} type="button">{t("common.cancel")}</button>
          <button
            className={request.danger ? "ghost-action danger-action" : "action"}
            onClick={accept}
            ref={confirmButton}
            type="button"
          >
            {request.confirmLabel ?? t("common.confirm")}
          </button>
        </div>
      </section>
    </div>
  );
}
