import { ServerCog, Settings2 } from "lucide-react";
import { useCallback, useEffect, useRef, useState } from "react";
import {
  activeRemoteID,
  api,
  REMOTE_HOSTS_CHANGED_EVENT,
  RemoteHost,
  setActiveRemoteID,
} from "./api";
import { useI18n } from "./i18n";

export function RemoteTargetSelector({ onManage }: { onManage: () => void }) {
  const { t } = useI18n();
  const [hosts, setHosts] = useState<RemoteHost[]>([]);
  const [loadFailed, setLoadFailed] = useState(false);
  const requestVersion = useRef(0);
  const activeID = activeRemoteID();

  const applyHosts = useCallback((items: RemoteHost[]) => {
    const next = items ?? [];
    setHosts(next);
    setLoadFailed(false);
    const selectedID = activeRemoteID();
    if (selectedID && !next.some((item) => item.id === selectedID)) {
      setActiveRemoteID("");
    }
  }, []);

  const load = useCallback(async () => {
    const version = ++requestVersion.current;
    try {
      const next = await api.remoteHosts();
      if (version !== requestVersion.current) return;
      applyHosts(next ?? []);
    } catch {
      if (version !== requestVersion.current) return;
      setHosts([]);
      setLoadFailed(true);
    }
  }, [applyHosts]);

  useEffect(() => {
    const handleHostsChanged = (event: Event) => {
      const next = (event as CustomEvent<RemoteHost[] | undefined>).detail;
      if (Array.isArray(next)) {
        // A successful mutation already fetched this exact snapshot. Apply it
        // directly and invalidate any older request still in flight.
        requestVersion.current += 1;
        applyHosts(next);
        return;
      }
      void load();
    };
    const handleVisible = () => {
      if (document.visibilityState === "visible") void load();
    };
    const intervalID = window.setInterval(handleVisible, 15_000);

    void load();
    window.addEventListener(REMOTE_HOSTS_CHANGED_EVENT, handleHostsChanged);
    window.addEventListener("focus", load);
    window.addEventListener("pageshow", load);
    document.addEventListener("visibilitychange", handleVisible);
    return () => {
      window.clearInterval(intervalID);
      window.removeEventListener(REMOTE_HOSTS_CHANGED_EVENT, handleHostsChanged);
      window.removeEventListener("focus", load);
      window.removeEventListener("pageshow", load);
      document.removeEventListener("visibilitychange", handleVisible);
    };
  }, [applyHosts, load]);

  const active = hosts.find((host) => host.id === activeID);

  return (
    <div className={`remote-target-selector${activeID ? " remote" : ""}`}>
      <ServerCog size={16} />
      <label>
        <span>{t("remote.currentMachine")}</span>
        <select
          value={activeID}
          aria-label={t("remote.currentMachine")}
          onFocus={() => void load()}
          onPointerDown={() => void load()}
          onChange={(event) => {
            setActiveRemoteID(event.target.value);
            window.location.reload();
          }}
        >
          <option value="">{t("remote.localMachine")}</option>
          {hosts.map((host) => (
            <option key={host.id} value={host.id} disabled={!host.trusted}>
              {host.name}{host.trusted ? "" : ` · ${t("remote.untrustedShort")}`}
            </option>
          ))}
          {loadFailed && <option disabled>{t("remote.loadFailed")}</option>}
          {activeID && !active && <option value={activeID}>{t("remote.unavailable")}</option>}
        </select>
      </label>
      <button
        type="button"
        className="remote-target-manage"
        onClick={onManage}
        title={t("remote.manage")}
        aria-label={t("remote.manage")}
      >
        <Settings2 size={15} />
      </button>
    </div>
  );
}
