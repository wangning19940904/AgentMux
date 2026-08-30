import { ServerCog } from "lucide-react";
import { useCallback, useEffect, useRef, useState } from "react";
import {
  activeMachineScope,
  api,
  REMOTE_HOSTS_CHANGED_EVENT,
  MachineTarget,
  setActiveMachineScope,
} from "./api";
import { useI18n } from "./i18n";

export function RemoteTargetSelector({
	onAddMachine,
	allowedTargetID,
}: {
	onAddMachine?: () => void;
	allowedTargetID?: string;
}) {
  const { t } = useI18n();
  const [hosts, setHosts] = useState<MachineTarget[]>([]);
  const [loadFailed, setLoadFailed] = useState(false);
  const requestVersion = useRef(0);
  const [activeID, setActiveID] = useState(activeMachineScope());

  const applyHosts = useCallback((items: MachineTarget[]) => {
		const next = (items ?? []).filter((item) => item.kind === "ssh" && (!allowedTargetID || item.id === allowedTargetID));
    setHosts(next);
    setLoadFailed(false);
    const selectedID = activeMachineScope();
		const allowed = !allowedTargetID || selectedID === allowedTargetID;
		if (!allowed || (selectedID !== "all" && selectedID !== "local" && !next.some((item) => item.id === selectedID))) {
			const fallback = allowedTargetID || "all";
			setActiveMachineScope(fallback);
			setActiveID(fallback);
    }
	}, [allowedTargetID]);

  const load = useCallback(async () => {
    const version = ++requestVersion.current;
    try {
      const next = await api.fleetTargets();
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
      void event;
      void load();
    };
    const handleVisible = () => {
      if (document.visibilityState === "visible") void load();
    };
    const intervalID = window.setInterval(handleVisible, 15_000);

    void load();
    const handleScopeChanged = () => setActiveID(activeMachineScope());
    window.addEventListener(REMOTE_HOSTS_CHANGED_EVENT, handleHostsChanged);
    window.addEventListener("agentmux:machine-scope-changed", handleScopeChanged);
    window.addEventListener("focus", load);
    window.addEventListener("pageshow", load);
    document.addEventListener("visibilitychange", handleVisible);
    return () => {
      window.clearInterval(intervalID);
      window.removeEventListener(REMOTE_HOSTS_CHANGED_EVENT, handleHostsChanged);
      window.removeEventListener("agentmux:machine-scope-changed", handleScopeChanged);
      window.removeEventListener("focus", load);
      window.removeEventListener("pageshow", load);
      document.removeEventListener("visibilitychange", handleVisible);
    };
  }, [applyHosts, load]);

  const active = hosts.find((host) => host.id === activeID);
  const remoteScope = activeID !== "all" && activeID !== "local";

  return (
    <div className={`remote-target-selector${remoteScope ? " remote" : ""}`}>
      <ServerCog size={16} />
      <label>
        <span>{t("remote.currentMachine")}</span>
        <select
          value={activeID}
          aria-label={t("remote.currentMachine")}
          onFocus={() => void load()}
          onPointerDown={() => void load()}
          onChange={(event) => {
            if (event.target.value === "add-ssh-machine") {
              event.target.value = activeID;
              onAddMachine?.();
              return;
            }
            setActiveMachineScope(event.target.value);
            setActiveID(event.target.value);
          }}
        >
		  {!allowedTargetID && <option value="all">{t("remote.allMachines")}</option>}
		  {(!allowedTargetID || allowedTargetID === "local") && <option value="local">{t("remote.localMachine")}</option>}
          {hosts.map((host) => (
            <option key={host.id} value={host.id} disabled={!host.trusted}>
              {host.name}{!host.trusted ? ` · ${t("remote.untrustedShort")}` : !host.online ? ` · ${t("remote.offlineShort")}` : ""}
            </option>
          ))}
          {loadFailed && <option disabled>{t("remote.loadFailed")}</option>}
          {remoteScope && !active && <option value={activeID}>{t("remote.unavailable")}</option>}
          {onAddMachine && <option value="add-ssh-machine">＋ {t("remote.add")}</option>}
        </select>
      </label>
    </div>
  );
}
