import { ServerCog, Settings2 } from "lucide-react";
import { useEffect, useState } from "react";
import { activeRemoteID, api, RemoteHost, setActiveRemoteID } from "./api";
import { useI18n } from "./i18n";

export function RemoteTargetSelector({ onManage }: { onManage: () => void }) {
  const { t } = useI18n();
  const [hosts, setHosts] = useState<RemoteHost[]>([]);
  const activeID = activeRemoteID();

  useEffect(() => {
    api.remoteHosts()
      .then((items) => {
        setHosts(items ?? []);
        if (activeID && !items.some((item) => item.id === activeID)) {
          setActiveRemoteID("");
        }
      })
      .catch(() => setHosts([]));
  }, [activeID]);

  const active = hosts.find((host) => host.id === activeID);

  return (
    <div className={`remote-target-selector${activeID ? " remote" : ""}`}>
      <ServerCog size={16} />
      <label>
        <span>{t("remote.currentMachine")}</span>
        <select
          value={activeID}
          aria-label={t("remote.currentMachine")}
          onChange={(event) => {
            setActiveRemoteID(event.target.value);
            window.location.reload();
          }}
        >
          <option value="">{t("remote.localMachine")}</option>
          {hosts.map((host) => (
            <option key={host.id} value={host.id}>
              {host.name}{host.trusted ? "" : ` · ${t("remote.untrustedShort")}`}
            </option>
          ))}
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
