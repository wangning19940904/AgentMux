import { CheckCircle2, LogOut, Moon, Smartphone } from "lucide-react";
import { useCallback, useEffect, useState } from "react";
import { api } from "../api";
import type { PhoneDevice } from "../api";
import { confirmAction } from "../components/ConfirmDialog";
import { useI18n } from "../i18n";

// A phone signed in within this long is marked new.
const NEW_PHONE_MS = 24 * 60 * 60 * 1000;

export function phoneName(phone: PhoneDevice, t: (key: string) => string) {
  return phone.name || t(phone.platform === "ios" ? "remote.phoneIOS" : "remote.phoneAndroid");
}

// PhoneDevices lists the phone apps signed in to this hub's accounts. While
// the phone QR code is open it checks often, so a phone that just scanned it
// shows up within seconds.
export function PhoneDevices({ watching = false }: { watching?: boolean }) {
  const { t } = useI18n();
  const [phones, setPhones] = useState<PhoneDevice[] | null>(null);
  const [error, setError] = useState("");
  const [busyID, setBusyID] = useState("");

  const load = useCallback(async () => {
    try {
      setPhones(await api.phones());
      setError("");
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : String(cause));
    }
  }, []);

  useEffect(() => {
    void load();
    const timer = window.setInterval(() => void load(), watching ? 5_000 : 30_000);
    return () => window.clearInterval(timer);
  }, [load, watching]);

  const signOut = async (phone: PhoneDevice) => {
    const name = phoneName(phone, t);
    if (!(await confirmAction(t("remote.phoneSignOutConfirm", { name }), { danger: true }))) return;
    setBusyID(phone.id);
    try {
      await api.signOutPhone(phone.id);
      await load();
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : String(cause));
    } finally {
      setBusyID("");
    }
  };

  if (phones === null && !error) return null;
  const list = phones ?? [];
  return (
    <div className="remote-phone-list" aria-label={t("remote.phones")}>
      <h3>
        {t("remote.phones")}
        <span className="pill">{list.length}</span>
      </h3>
      {error && <div className="remote-feedback error" role="alert">{error}</div>}
      {!error && list.length === 0 && <div className="empty-state">{t("remote.phonesEmpty")}</div>}
      {list.map((phone) => {
        const signedIn = new Date(phone.signed_in_at);
        const fresh = Date.now() - signedIn.getTime() < NEW_PHONE_MS;
        const details = [
          phone.account_name,
          phone.os,
          phone.app_version && `v${phone.app_version}`,
          t("remote.phoneSignedIn", { time: signedIn.toLocaleString() }),
        ].filter(Boolean).join(" · ");
        return (
          <article key={phone.id} className="remote-host-card">
            <div className="remote-host-icon">
              <Smartphone size={19} />
            </div>
            <div className="remote-host-main">
              <header>
                <div>
                  <strong>{phoneName(phone, t)}</strong>
                  <span>{details}</span>
                  {!phone.active && phone.last_seen_at && (
                    <span>{t("remote.lastSeen", { time: new Date(phone.last_seen_at).toLocaleString() })}</span>
                  )}
                </div>
                <span className="remote-host-badges">
                  {fresh && <span className="status-badge">{t("remote.phoneNew")}</span>}
                  <span className={`status-badge ${phone.active ? "success" : "warning"}`}>
                    {phone.active ? <CheckCircle2 size={13} /> : <Moon size={13} />}
                    {t(phone.active ? "remote.phoneActive" : "remote.phoneIdle")}
                  </span>
                </span>
              </header>
            </div>
            <div className="remote-host-actions">
              <button
                className="ghost-action danger-action"
                type="button"
                disabled={busyID === phone.id}
                onClick={() => void signOut(phone)}
              >
                <LogOut size={14} />
                {t("remote.phoneSignOut")}
              </button>
            </div>
          </article>
        );
      })}
    </div>
  );
}
