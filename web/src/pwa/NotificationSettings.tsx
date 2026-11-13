import { useCallback, useEffect, useMemo, useState } from "react";
import { Bell, BellOff, Download, Send, Smartphone, Trash2 } from "lucide-react";
import { useI18n } from "../i18n";
import { pushAPI, type PushDelivery, type PushKind, type PushStatus } from "../api/push";
import { INSTALL_AVAILABLE_EVENT, installAvailable, isStandalone, promptInstall, serviceWorkerSupported } from "./register";
import {
  applicationServerKey,
  bytesToBase64url,
  currentPushEnvironment,
  deviceLabel,
  isIOS,
  pushBlocker,
  sameKey,
  subscriptionID,
  type PushBlocker,
} from "./pushSupport";

const ALL_KINDS: PushKind[] = ["waiting", "tickets", "machines", "messages"];

type Phase = "loading" | "idle" | "working";

async function workerRegistration(): Promise<ServiceWorkerRegistration | null> {
  if (!serviceWorkerSupported()) return null;
  const registration = await navigator.serviceWorker.getRegistration("/");
  if (!registration) return null;
  // Wait for an installing worker to become active before subscribing.
  return navigator.serviceWorker.ready.then(() => registration).catch(() => registration);
}

// NotificationSettings lets a person turn push on for this device, pick what
// they hear about, send a test, and see their other subscribed devices. It
// also offers to install the Console where the browser allows it.
export function NotificationSettings() {
  const { t, language } = useI18n();
  const [phase, setPhase] = useState<Phase>("loading");
  const [status, setStatus] = useState<PushStatus | null>(null);
  const [blocker, setBlocker] = useState<PushBlocker | null>(null);
  const [subscription, setSubscription] = useState<PushSubscription | null>(null);
  const [currentID, setCurrentID] = useState("");
  const [error, setError] = useState("");
  const [notice, setNotice] = useState("");
  const [canInstall, setCanInstall] = useState(installAvailable);

  const refresh = useCallback(async () => {
    setError("");
    const registration = await workerRegistration().catch(() => null);
    const env = currentPushEnvironment(Boolean(registration));
    setBlocker(pushBlocker(env));
    let current: PushSubscription | null = null;
    if (registration && env.hasPushManager) {
      current = await registration.pushManager.getSubscription().catch(() => null);
    }
    setSubscription(current);
    setCurrentID(current ? await subscriptionID(current.endpoint) : "");
    try {
      setStatus(await pushAPI.status());
    } catch (err) {
      setStatus(null);
      setError(err instanceof Error ? err.message : String(err));
    }
    setPhase("idle");
  }, []);

  useEffect(() => {
    void refresh();
  }, [refresh]);

  useEffect(() => {
    const update = () => setCanInstall(installAvailable());
    window.addEventListener(INSTALL_AVAILABLE_EVENT, update);
    return () => window.removeEventListener(INSTALL_AVAILABLE_EVENT, update);
  }, []);

  const record = useMemo(
    () => status?.subscriptions.find((item) => item.id === currentID) ?? null,
    [currentID, status],
  );
  const enabled = Boolean(subscription && record);
  const otherDevices = status?.subscriptions.filter((item) => item.id !== currentID) ?? [];

  async function run(action: () => Promise<void>) {
    setPhase("working");
    setError("");
    setNotice("");
    try {
      await action();
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      await refresh();
    }
  }

  async function save(sub: PushSubscription, kinds: PushKind[]) {
    const json = sub.toJSON();
    await pushAPI.subscribe({
      endpoint: sub.endpoint,
      keys: {
        p256dh: json.keys?.p256dh ?? bytesToBase64url(sub.getKey("p256dh")),
        auth: json.keys?.auth ?? bytesToBase64url(sub.getKey("auth")),
      },
      kinds,
      label: deviceLabel(navigator.userAgent, navigator.maxTouchPoints),
      lang: language,
    });
  }

  function enable() {
    void run(async () => {
      if (!status) throw new Error(t("push.serverUnavailable"));
      const permission = await Notification.requestPermission();
      if (permission !== "granted") {
        throw new Error(t(permission === "denied" ? "push.blocked.denied" : "push.permissionDismissed"));
      }
      const registration = await workerRegistration();
      if (!registration) throw new Error(t("push.blocked.no-worker"));
      let sub = await registration.pushManager.getSubscription();
      // A subscription made for another server key cannot be reused.
      if (sub && !sameKey(sub.options.applicationServerKey, status.public_key)) {
        await sub.unsubscribe();
        sub = null;
      }
      if (!sub) {
        sub = await registration.pushManager.subscribe({
          userVisibleOnly: true,
          applicationServerKey: applicationServerKey(status.public_key) as BufferSource,
        });
      }
      await save(sub, record?.kinds.length ? record.kinds : ALL_KINDS);
      setNotice(t("push.enabledNotice"));
    });
  }

  function disable() {
    void run(async () => {
      if (currentID) await pushAPI.unsubscribe(currentID);
      await subscription?.unsubscribe();
    });
  }

  function toggleKind(kind: PushKind, on: boolean) {
    if (!subscription || !record) return;
    const kinds = ALL_KINDS.filter((item) => (item === kind ? on : record.kinds.includes(item)));
    void run(() => save(subscription, kinds));
  }

  function sendTest() {
    void run(async () => {
      const { results } = await pushAPI.test(currentID);
      const result: PushDelivery | undefined = results[0];
      if (!result) throw new Error(t("push.testNoDevice"));
      if (result.removed) throw new Error(t("push.testRemoved"));
      if (!result.delivered) throw new Error(t("push.testFailed", { error: result.error ?? String(result.status ?? "") }));
      setNotice(t("push.testSent"));
    });
  }

  function removeDevice(id: string) {
    void run(async () => {
      await pushAPI.unsubscribe(id);
    });
  }

  const busy = phase !== "idle";
  const ios = isIOS(navigator.userAgent, navigator.maxTouchPoints);
  const standalone = isStandalone();

  return (
    <section className="surface notification-settings" aria-labelledby="notification-settings-title">
      <div className="surface-header">
        <div>
          <h2 id="notification-settings-title">{t("push.title")}</h2>
          <p className="subtle-copy">{t("push.subtitle")}</p>
        </div>
        {enabled ? (
          <div className="control-row">
            <button className="ghost-action" type="button" disabled={busy} onClick={sendTest}>
              <Send size={14} /> {t("push.sendTest")}
            </button>
            <button className="ghost-action" type="button" disabled={busy} onClick={disable}>
              <BellOff size={14} /> {t("push.disable")}
            </button>
          </div>
        ) : (
          <button className="action" type="button" disabled={busy || Boolean(blocker) || !status} onClick={enable}>
            <Bell size={14} /> {t("push.enable")}
          </button>
        )}
      </div>
      <div className="surface-body notification-settings-body">
        {phase === "loading" && <p className="subtle-copy">{t("common.loading")}</p>}
        {phase !== "loading" && blocker && (
          <div className={`notification-callout${blocker === "denied" ? " warning" : ""}`} role="note">
            <strong>{t(`push.blocked.${blocker}`)}</strong>
            <span>{t(`push.blocked.${blocker}.hint`, { host: window.location.host })}</span>
          </div>
        )}
        {phase !== "loading" && !blocker && !enabled && <p className="subtle-copy">{t("push.offHint")}</p>}

        {enabled && record && (
          <fieldset className="notification-kinds" disabled={busy}>
            <legend>{t("push.kindsLegend")}</legend>
            {ALL_KINDS.map((kind) => (
              <label key={kind} className="switch-row">
                <span>
                  <strong>{t(`push.kind.${kind}`)}</strong>
                  <small>{t(`push.kind.${kind}.hint`)}</small>
                </span>
                <input type="checkbox" checked={record.kinds.includes(kind)} onChange={(event) => toggleKind(kind, event.target.checked)} />
              </label>
            ))}
          </fieldset>
        )}

        {notice && <p className="notification-notice" role="status">{notice}</p>}
        {error && <p className="notification-error" role="alert">{error}</p>}

        {otherDevices.length > 0 && (
          <div className="notification-devices">
            <h3>{t("push.otherDevices")}</h3>
            <ul>
              {otherDevices.map((device) => (
                <li key={device.id}>
                  <Smartphone size={16} aria-hidden="true" />
                  <span>
                    <strong>{device.label || t("push.unnamedDevice")}</strong>
                    <small>
                      {device.kinds.length ? device.kinds.map((kind) => t(`push.kind.${kind}`)).join(" · ") : t("push.noKinds")}
                      {device.last_error ? ` · ${t("push.lastError")}: ${device.last_error}` : ""}
                    </small>
                  </span>
                  <button className="ghost-action" type="button" disabled={busy} title={t("push.removeDevice")} aria-label={t("push.removeDevice")} onClick={() => removeDevice(device.id)}>
                    <Trash2 size={14} />
                  </button>
                </li>
              ))}
            </ul>
          </div>
        )}

        {!standalone && (
          <div className="notification-install">
            <h3>{t("push.installTitle")}</h3>
            {canInstall ? (
              <button className="ghost-action" type="button" onClick={() => void promptInstall()}>
                <Download size={14} /> {t("push.installButton")}
              </button>
            ) : (
              <p className="subtle-copy">{t(ios ? "push.installIOS" : "push.installOther")}</p>
            )}
          </div>
        )}
      </div>
    </section>
  );
}
