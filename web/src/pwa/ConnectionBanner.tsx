import { useEffect, useState } from "react";
import { RefreshCw, WifiOff } from "lucide-react";
import { useI18n } from "../i18n";
import { applyUpdate, checkForUpdate, UPDATE_READY_EVENT, updateWaiting } from "./register";

const PROBE_INTERVAL_MS = 60_000;
const RETRY_INTERVAL_MS = 5_000;

// probeReachable reports whether the AgentMux server answers at all. Any HTTP
// response counts, including a refused sign-in: only a network failure means
// the Console is showing data it cannot refresh.
export async function probeReachable(fetcher: typeof fetch = fetch): Promise<boolean> {
  try {
    await fetcher("/api/v1/status", { cache: "no-store", headers: { "X-AgentMux-Console": "1" } });
    return true;
  } catch {
    return false;
  }
}

// ConnectionBanner says plainly when the Console is offline (so cached pages
// are not mistaken for live ones) and when a new build is ready to load.
export function ConnectionBanner() {
  const { t } = useI18n();
  const [online, setOnline] = useState(() => navigator.onLine !== false);
  const [reachable, setReachable] = useState(true);
  const [updateReady, setUpdateReady] = useState(updateWaiting);

  useEffect(() => {
    let active = true;
    let timer = 0;
    let wasReachable = true;
    let inFlight = false;
    const probe = async () => {
      if (inFlight) return;
      window.clearTimeout(timer);
      if (document.visibilityState !== "hidden") {
        inFlight = true;
        const ok = await probeReachable();
        inFlight = false;
        if (!active) return;
        setReachable(ok);
        // Back in reach: the server may be running a newer build by now.
        if (ok && !wasReachable) void checkForUpdate();
        wasReachable = ok;
      }
      if (active) timer = window.setTimeout(() => void probe(), wasReachable ? PROBE_INTERVAL_MS : RETRY_INTERVAL_MS);
    };
    const goOnline = () => {
      setOnline(true);
      void probe();
    };
    const goOffline = () => setOnline(false);
    const onVisible = () => {
      if (document.visibilityState === "visible") void probe();
    };
    const onUpdate = () => setUpdateReady(true);
    window.addEventListener("online", goOnline);
    window.addEventListener("offline", goOffline);
    window.addEventListener(UPDATE_READY_EVENT, onUpdate);
    document.addEventListener("visibilitychange", onVisible);
    void probe();
    return () => {
      active = false;
      window.removeEventListener("online", goOnline);
      window.removeEventListener("offline", goOffline);
      window.removeEventListener(UPDATE_READY_EVENT, onUpdate);
      document.removeEventListener("visibilitychange", onVisible);
      window.clearTimeout(timer);
    };
  }, []);

  if (!online || !reachable) {
    return (
      <div className="connection-banner visible" role="status">
        <WifiOff size={15} aria-hidden="true" />
        <span>{t("app.offline")}</span>
      </div>
    );
  }
  if (updateReady) {
    return (
      <div className="connection-banner visible update" role="status">
        <RefreshCw size={15} aria-hidden="true" />
        <span>{t("app.updateReady")}</span>
        <button type="button" className="ghost-action" onClick={applyUpdate}>
          {t("app.updateReload")}
        </button>
      </div>
    );
  }
  return null;
}
