import {
  CheckCircle2,
  Fingerprint,
  Copy,
  KeyRound,
  Laptop,
  Link2,
  Pencil,
  Plus,
  RefreshCw,
  Save,
  Server,
  ShieldAlert,
  Smartphone,
  Trash2,
  WifiOff,
  X,
} from "lucide-react";
import { FormEvent, useEffect, useRef, useState } from "react";
import {
  type Account,
  activeRemoteID,
  setActiveMachineScope,
  api,
  DevicePairingCode,
  DiscoveredRemoteHost,
  FleetView,
  notifyRemoteHostsChanged,
  type PairedDevice,
  RemoteConnectionError,
  RemoteHost,
} from "../api";
import { useI18n } from "../i18n";
import { PhoneAppLink } from "../pwa/PhoneAppLink";
import { accountLabel } from "../signIn";
import { DeleteRemoteHostDialog } from "./DeleteRemoteHostDialog";
import {
  fleetOnlyMachines,
  isRemoteUpdateAvailable,
  linkPresence,
  remoteUpdateCandidates,
  type RemoteHostSnapshot,
} from "./remoteHostModel";
import { confirmAction } from "../components/ConfirmDialog";

type RemoteForm = {
  id?: string;
  name: string;
  host: string;
  port: number;
  user: string;
  key_path: string;
  remote_addr: string;
  api_token: string;
  api_token_set?: boolean;
  clear_api_token: boolean;
};

const emptyForm = (): RemoteForm => ({
  name: "",
  host: "",
  port: 22,
  user: "",
  key_path: "",
  remote_addr: "127.0.0.1:8765",
  api_token: "",
  clear_api_token: false,
});

// The paired device behind a machine entry: the entry itself when the
// machine links to this hub, or its link path when the machine is listed
// under another entry, such as SSH.
function pairedDeviceID(host: RemoteHost): string | undefined {
  if (host.kind === "link") return host.id;
  return host.paths?.find((path) => (path.via === "link" || path.via === "passive") && path.host_id)?.host_id;
}

// Devices lists every machine this one reaches and connects new ones: a
// phone through the hub link it scans, a server over SSH or by pairing.
export function RemoteHostsPanel({ addRequest = 0 }: { addRequest?: number }) {
  const { t } = useI18n();
  const [hosts, setHosts] = useState<RemoteHost[]>([]);
  const [discoveredHosts, setDiscoveredHosts] = useState<DiscoveredRemoteHost[]>([]);
  const [form, setForm] = useState<RemoteForm | null>(null);
  const [replacementHost, setReplacementHost] = useState<RemoteHost | null>(null);
  const [importAddr, setImportAddr] = useState("");
  const [hostDialogOpen, setHostDialogOpen] = useState(false);
  const [loading, setLoading] = useState(true);
  const [discoveryLoading, setDiscoveryLoading] = useState(false);
  const [syncingSSHConfig, setSyncingSSHConfig] = useState(false);
  const [discoveryError, setDiscoveryError] = useState("");
  const [message, setMessage] = useState("");
  const [error, setError] = useState("");
  const [deleteCandidate, setDeleteCandidate] = useState<RemoteHost | null>(null);
  const [phoneOpen, setPhoneOpen] = useState(false);
  // Paired devices and the accounts that may own them, known only to an
  // administrator where accounts are on.
  const [owners, setOwners] = useState<{ devices: PairedDevice[]; accounts: Account[] } | null>(null);
  const [assigningID, setAssigningID] = useState("");
  const [pairingOpen, setPairingOpen] = useState(false);
  const [pairingName, setPairingName] = useState("");
  const [pairing, setPairing] = useState<DevicePairingCode | null>(null);
  const [pairingBusy, setPairingBusy] = useState(false);
  const [pairingError, setPairingError] = useState("");
  const [passiveInvite, setPassiveInvite] = useState("");
  const [passiveBusy, setPassiveBusy] = useState(false);
  const [deleting, setDeleting] = useState(false);
  const [deleteError, setDeleteError] = useState("");
  const deleteInFlight = useRef(false);
  const [updatingID, setUpdatingID] = useState("");
  const [updatingAll, setUpdatingAll] = useState(false);
  const [importingName, setImportingName] = useState("");
  const [localVersion, setLocalVersion] = useState("");
  // This machine, listed first: its name and its part as a hub ("hub",
  // "standby", or "" when it does not serve as one).
  const [localMachine, setLocalMachine] = useState<{ name: string; role: "" | "hub" | "standby" } | null>(null);
  const [snapshots, setSnapshots] = useState<Record<string, RemoteHostSnapshot>>({});
  // The hub's view of which machines are connected, or null before it is
  // known or where this machine has none.
  const [fleet, setFleet] = useState<FleetView | null>(null);
  const [pendingFingerprints, setPendingFingerprints] = useState<Record<string, string>>({});
  const [pendingImportFingerprints, setPendingImportFingerprints] = useState<Record<string, string>>({});
  const hostsRequestVersion = useRef(0);
  const probeVersions = useRef<Record<string, number>>({});
  const handledAddRequest = useRef(0);

  const probeHost = async (host: RemoteHost): Promise<RemoteHostSnapshot> => {
    const probeVersion = (probeVersions.current[host.id] ?? 0) + 1;
    probeVersions.current[host.id] = probeVersion;
    setSnapshots((current) => ({
      ...current,
      [host.id]: { health: host.trusted ? "checking" : "unverified" },
    }));
    try {
      const result = host.trusted
        ? await api.statusRemoteHost(host.id)
        : await api.testRemoteHost(host.id, false);
      const snapshot: RemoteHostSnapshot = {
        health: "healthy",
        version: result.status?.version,
      };
      if (probeVersions.current[host.id] === probeVersion) {
        setSnapshots((current) => ({ ...current, [host.id]: snapshot }));
        setPendingFingerprints((current) => {
          if (!current[host.id]) return current;
          const next = { ...current };
          delete next[host.id];
          return next;
        });
      }
      return snapshot;
    } catch (cause) {
      if (
        !host.trusted &&
        cause instanceof RemoteConnectionError &&
        cause.code === "host_key_untrusted" &&
        cause.fingerprint
      ) {
        const snapshot: RemoteHostSnapshot = { health: "unverified" };
        if (probeVersions.current[host.id] === probeVersion) {
          setSnapshots((current) => ({ ...current, [host.id]: snapshot }));
          setPendingFingerprints((current) => ({ ...current, [host.id]: cause.fingerprint! }));
        }
        return snapshot;
      }
      const snapshot: RemoteHostSnapshot = {
        health: host.trusted ? "error" : "unverified",
        error: cause instanceof Error ? cause.message : String(cause),
      };
      if (probeVersions.current[host.id] === probeVersion) {
        setSnapshots((current) => ({ ...current, [host.id]: snapshot }));
      }
      return snapshot;
    }
  };

  const inspectHosts = async (nextHosts: RemoteHost[]) => {
    setSnapshots((current) => ({
      ...current,
      ...Object.fromEntries(nextHosts.map((host) => [
        host.id,
        { health: host.trusted ? "checking" : "unverified" } satisfies RemoteHostSnapshot,
      ])),
    }));
    try {
      const status = await api.localStatus();
      setLocalVersion(status.version || "");
    } catch {
      setLocalVersion("");
    }
    await Promise.allSettled(nextHosts.map((host) => probeHost(host)));
  };

  const loadFleet = async () => {
    try {
      setFleet(await api.fleet());
    } catch {
      setFleet(null);
    }
  };

  const loadOwners = async () => {
    try {
      const session = await api.authSession();
      if (!session.admin || !session.accounts_enabled) {
        setOwners(null);
        return;
      }
      const [devices, accounts] = await Promise.all([api.pairedDevices(), api.accounts()]);
      setOwners({ devices, accounts });
    } catch {
      setOwners(null);
    }
  };

  const assignOwner = async (deviceID: string, accountID: string) => {
    setAssigningID(deviceID);
    setMessage("");
    setError("");
    try {
      await api.assignDeviceOwner(deviceID, accountID);
      await loadOwners();
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : String(cause));
    } finally {
      setAssigningID("");
    }
  };

  const load = async (): Promise<RemoteHost[] | null> => {
    void loadFleet();
    void loadOwners();
    const version = ++hostsRequestVersion.current;
    setLoading(true);
    try {
      const next = await api.remoteHosts();
      if (version !== hostsRequestVersion.current) return null;
      setHosts(next);
      setSnapshots((current) => Object.fromEntries(
        next.map((host) => [host.id, current[host.id] ?? {
          health: host.trusted ? "checking" : "unverified",
        }]),
      ));
      void inspectHosts(next);
      setError("");
      return next;
    } catch (cause) {
      if (version !== hostsRequestVersion.current) return null;
      setError(cause instanceof Error ? cause.message : String(cause));
      return null;
    } finally {
      if (version === hostsRequestVersion.current) setLoading(false);
    }
  };

  const loadDiscovered = async () => {
    setDiscoveryLoading(true);
    try {
      setDiscoveredHosts(await api.discoveredRemoteHosts());
      setDiscoveryError("");
    } catch (cause) {
      setDiscoveryError(cause instanceof Error ? cause.message : String(cause));
    } finally {
      setDiscoveryLoading(false);
    }
  };

  const syncSSHConfig = async () => {
    setSyncingSSHConfig(true);
    setMessage("");
    setError("");
    try {
      const result = await api.syncRemoteHostsFromSSHConfig();
      const next = await load();
      if (next) notifyRemoteHostsChanged(next);
      setMessage(t("remote.syncResult", {
        updated: result.updated,
        unchanged: result.unchanged,
        skipped: result.unmatched + result.ambiguous,
      }));
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : String(cause));
    } finally {
      setSyncingSSHConfig(false);
    }
  };

  useEffect(() => {
    const timer = window.setInterval(() => void loadFleet(), 15_000);
    return () => window.clearInterval(timer);
  }, []);

  useEffect(() => {
    void load();
    void api.hubReplication()
      .then((info) => setLocalMachine({
        name: info.name || t("remote.localMachine"),
        role: info.enabled && info.status?.role === "standby" ? "standby" : info.hub || info.enabled ? "hub" : "",
      }))
      .catch(() => setLocalMachine(null));
  }, []);

  useEffect(() => {
    if (!hostDialogOpen) return;
    const previousOverflow = document.body.style.overflow;
    const closeOnEscape = (event: KeyboardEvent) => {
      if (event.key === "Escape" && !importingName) {
        setHostDialogOpen(false);
        setForm(null);
        setReplacementHost(null);
      }
    };
    document.body.style.overflow = "hidden";
    window.addEventListener("keydown", closeOnEscape);
    return () => {
      document.body.style.overflow = previousOverflow;
      window.removeEventListener("keydown", closeOnEscape);
    };
  }, [hostDialogOpen, importingName]);

  const importDiscovered = async (host: DiscoveredRemoteHost, trustOnFirstUse = false) => {
    setImportingName(host.name);
    setMessage("");
    setError("");
    try {
      const result = await api.importRemoteHost(host, trustOnFirstUse, {
        remoteAddr: importAddr.trim() || undefined,
        existingID: replacementHost?.id,
      });
      setPendingImportFingerprints((current) => {
        const next = { ...current };
        delete next[host.name];
        return next;
      });
      if (replacementHost) {
        closeHostDialog();
        setMessage(t("remote.routeChanged", { name: result.host.name }));
      } else {
        setMessage(result.installed ? t("remote.importInstalled") : t("remote.importSucceeded"));
      }
      const next = await load();
      if (next) notifyRemoteHostsChanged(next);
    } catch (cause) {
      if (
        cause instanceof RemoteConnectionError &&
        cause.code === "host_key_untrusted" &&
        cause.fingerprint
      ) {
        setPendingImportFingerprints((current) => ({ ...current, [host.name]: cause.fingerprint! }));
      } else {
        setError(t("remote.importFailed", {
          name: host.name,
          error: cause instanceof Error ? cause.message : String(cause),
        }));
      }
    } finally {
      setImportingName("");
    }
  };

  const configuredHost = (candidate: DiscoveredRemoteHost) =>
    hosts.find(
      (host) =>
        host.host.toLowerCase() === candidate.host.toLowerCase() &&
        host.port === candidate.port &&
        host.user === candidate.user,
    );

  const edit = (host: RemoteHost) => {
    setReplacementHost(null);
    setForm({
      id: host.id,
      name: host.name,
      host: host.host,
      port: host.port,
      user: host.user,
      key_path: host.key_path ?? "",
      remote_addr: host.remote_addr || "127.0.0.1:8765",
      api_token: "",
      api_token_set: host.api_token_set,
      clear_api_token: false,
    });
    setHostDialogOpen(true);
    setMessage("");
    setError("");
  };

  const save = async (event: FormEvent) => {
    event.preventDefault();
    if (!form) return;
    setMessage("");
    setError("");
    try {
      const saved = await api.upsertRemoteHost(form);
      setSnapshots((current) => ({
        ...current,
        [saved.id]: { health: saved.trusted ? "checking" : "unverified" },
      }));
      setMessage(t("remote.savedChecking"));
      setForm(null);
      setHostDialogOpen(false);
      const next = await load();
      if (next) notifyRemoteHostsChanged(next);
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : String(cause));
    }
  };

  const verifyHost = async (host: RemoteHost) => {
    const probeVersion = (probeVersions.current[host.id] ?? 0) + 1;
    probeVersions.current[host.id] = probeVersion;
    setSnapshots((current) => ({ ...current, [host.id]: { health: "checking" } }));
    setMessage("");
    setError("");
    try {
      const result = await api.testRemoteHost(host.id, true);
      setSnapshots((current) => ({
        ...current,
        [host.id]: { health: "healthy", version: result.status?.version },
      }));
      setPendingFingerprints((current) => {
        const next = { ...current };
        delete next[host.id];
        return next;
      });
      setMessage(result.installed ? t("remote.importInstalled") : t("remote.connectionReady"));
      const next = await load();
      if (next) notifyRemoteHostsChanged(next);
    } catch (cause) {
      const detail = cause instanceof Error ? cause.message : String(cause);
      setSnapshots((current) => ({ ...current, [host.id]: { health: "error", error: detail } }));
      setError(detail);
    }
  };

  const remove = async (host: RemoteHost) => {
    if (deleteInFlight.current) return;
    deleteInFlight.current = true;
    setDeleting(true);
    setDeleteError("");
    setMessage("");
    setError("");
    try {
      await api.deleteRemoteHost(host.id);
      if (!hosts.some((item) => item.id === host.id)) {
        // One path of a machine listed under another entry: the machine
        // stays, with one path fewer.
        setDeleteCandidate(null);
        setMessage(t("remote.pathRemoved"));
        void load();
        return;
      }
      // A list or health request started before deletion must not restore the host.
      ++hostsRequestVersion.current;
      probeVersions.current[host.id] = (probeVersions.current[host.id] ?? 0) + 1;
      const next = hosts.filter((item) => item.id !== host.id);
      setHosts(next);
      setLoading(false);
      setDeleteCandidate(null);
      setMessage(t("remote.deleted"));
      if (activeRemoteID() === host.id) setActiveMachineScope("all");
      notifyRemoteHostsChanged(next);
    } catch (cause) {
      setDeleteError(cause instanceof Error ? cause.message : String(cause));
    } finally {
      deleteInFlight.current = false;
      setDeleting(false);
    }
  };

  const performUpdate = async (host: RemoteHost): Promise<string | null> => {
    setUpdatingID(host.id);
    setSnapshots((current) => ({
      ...current,
      [host.id]: { ...current[host.id], health: "checking" },
    }));
    try {
      const result = await api.updateRemoteHost(host.id);
      setSnapshots((current) => ({
        ...current,
        [host.id]: {
          health: "healthy",
          version: result.version || result.status?.version,
        },
      }));
      return null;
    } catch (cause) {
      const detail = cause instanceof Error ? cause.message : String(cause);
      setSnapshots((current) => ({
        ...current,
        [host.id]: { health: "error", error: detail },
      }));
      return detail;
    } finally {
      setUpdatingID("");
    }
  };

  const updateRemote = async (host: RemoteHost) => {
    setMessage("");
    setError("");
    const failure = await performUpdate(host);
    if (failure) {
      setError(failure);
      return;
    }
    setMessage(t("remote.updateMachineSucceeded", { name: host.name }));
    await inspectHosts(hosts);
  };

  const closeHostDialog = () => {
    setHostDialogOpen(false);
    setForm(null);
    setReplacementHost(null);
  };

  const openImportDialog = (existing: RemoteHost | null = null) => {
    setForm(null);
    setReplacementHost(existing);
    setImportAddr(existing?.remote_addr ?? "");
    setPendingImportFingerprints({});
    setMessage("");
    setError("");
    setHostDialogOpen(true);
    void loadDiscovered();
  };

  useEffect(() => {
    if (addRequest <= 0 || handledAddRequest.current === addRequest) return;
    handledAddRequest.current = addRequest;
    openImportDialog();
  }, [addRequest]);

  const updateCandidates = remoteUpdateCandidates(hosts, snapshots, localVersion);
  const editedHost = hosts.find((host) => host.id === form?.id);
  const checkingHosts = hosts.some((host) => snapshots[host.id]?.health === "checking");
  const updateCheckUnavailable = hosts.some((host) =>
    !host.trusted || snapshots[host.id]?.health === "error" || !snapshots[host.id]?.version,
  );

  const updateAll = async () => {
    const candidates = remoteUpdateCandidates(hosts, snapshots, localVersion);
    if (candidates.length === 0) return;
    const names = candidates.map((host) => host.name).join("\n");
    if (!(await confirmAction(t("remote.updateAllConfirm", { count: candidates.length, names })))) return;

    setUpdatingAll(true);
    setMessage("");
    setError("");
    const failures: string[] = [];
    let succeeded = 0;
    for (const host of candidates) {
      const failure = await performUpdate(host);
      if (failure) failures.push(host.name);
      else succeeded += 1;
    }
    await inspectHosts(hosts);
    if (failures.length > 0) {
      setError(t("remote.updateAllPartial", {
        succeeded,
        failed: failures.length,
        names: failures.join(", "),
      }));
    } else {
      setMessage(t("remote.updateAllSucceeded", { count: succeeded }));
    }
    setUpdatingAll(false);
  };

  const createPairingCode = async () => {
    setPairingBusy(true);
    setPairingError("");
    try {
      setPairing(await api.createDevicePairingCode(pairingName.trim()));
    } catch (cause) {
      setPairingError(cause instanceof Error ? cause.message : String(cause));
    } finally {
      setPairingBusy(false);
    }
  };

  // A machine that listens: this hub dials it, from the invite it printed.
  const addPassiveDevice = async () => {
    setPassiveBusy(true);
    setPairingError("");
    try {
      const added = await api.addPassiveDevice(passiveInvite.trim(), pairingName.trim());
      setMessage(added.online
        ? t("remote.passiveAdded", { name: added.device.name })
        : t("remote.passiveAddedOffline", { name: added.device.name, error: added.passive.last_error || "…" }));
      setPassiveInvite("");
      setPairingOpen(false);
      setPairingName("");
      await load();
    } catch (cause) {
      setPairingError(cause instanceof Error ? cause.message : String(cause));
    } finally {
      setPassiveBusy(false);
    }
  };

  const closePairing = () => {
    setPairingOpen(false);
    setPairing(null);
    setPairingName("");
    setPassiveInvite("");
    setPairingError("");
    // The machine may have joined while the command was on screen.
    void load();
  };

  return (
    <div className="page-stack remote-hosts-page">
      {deleteCandidate && (
        <DeleteRemoteHostDialog
          host={deleteCandidate}
          busy={deleting}
          error={deleteError}
          onClose={() => setDeleteCandidate(null)}
          onConfirm={() => void remove(deleteCandidate)}
        />
      )}
      {hostDialogOpen && (
        <button
          aria-label={t("common.close")}
          className="remote-host-dialog-backdrop"
          disabled={Boolean(importingName)}
          onClick={closeHostDialog}
          type="button"
        />
      )}

      {hostDialogOpen && form && (
        <section
          aria-labelledby="remote-host-dialog-title"
          aria-modal="true"
          className="surface remote-host-dialog"
          role="dialog"
        >
          <div className="surface-header">
            <div>
              <h2 id="remote-host-dialog-title">{form.id ? t("remote.edit") : t("remote.add")}</h2>
              <p className="subtle-copy">{t("remote.formHint")}</p>
            </div>
            <button className="ghost-action" onClick={closeHostDialog} aria-label={t("common.close")}>
              <X size={15} />
              {t("common.close")}
            </button>
          </div>
          {!form.id && (
            <div className="remote-host-dialog-tabs" role="tablist" aria-label={t("remote.add")}>
              <button
                aria-selected="false"
                onClick={() => setForm(null)}
                role="tab"
                type="button"
              >
                {t("remote.discovered")}
              </button>
              <button aria-selected="true" className="active" role="tab" type="button">
                {t("remote.manualAdd")}
              </button>
            </div>
          )}
          {error && <div className="remote-feedback error">{error}</div>}
          <form className="surface-body remote-host-form" onSubmit={save}>
            <div className="field-grid">
              <label className="field">
                <span>{t("common.name")}</span>
                <input
                  required
                  value={form.name}
                  placeholder={t("remote.namePlaceholder")}
                  onChange={(event) => setForm({ ...form, name: event.target.value })}
                />
              </label>
              <label className="field">
                <span>{t("remote.sshHost")}</span>
                <input
                  required
                  value={form.host}
                  placeholder="192.168.1.20"
                  onChange={(event) => setForm({ ...form, host: event.target.value })}
                />
              </label>
              <label className="field">
                <span>{t("remote.sshPort")}</span>
                <input
                  required
                  type="number"
                  min={1}
                  max={65535}
                  value={form.port}
                  onChange={(event) => setForm({ ...form, port: Number(event.target.value) })}
                />
              </label>
              <label className="field">
                <span>{t("remote.sshUser")}</span>
                <input
                  value={form.user}
                  placeholder={t("remote.currentUser")}
                  onChange={(event) => setForm({ ...form, user: event.target.value })}
                />
              </label>
              <label className="field">
                <span>{t("remote.keyPath")}</span>
                <input
                  value={form.key_path}
                  placeholder="~/.ssh/id_ed25519"
                  onChange={(event) => setForm({ ...form, key_path: event.target.value })}
                />
                <small>{t("remote.keyPathHint")}</small>
              </label>
              <label className="field">
                <span>{t("remote.remoteAddr")}</span>
                <input
                  required
                  value={form.remote_addr}
                  placeholder="127.0.0.1:8765"
                  onChange={(event) => setForm({ ...form, remote_addr: event.target.value })}
                />
                <small>{t("remote.remoteAddrHint")}</small>
              </label>
              <label className="field remote-token-field">
                <span>{t("remote.apiToken")}</span>
                <input
                  type="password"
                  autoComplete="off"
                  value={form.api_token}
                  placeholder={form.api_token_set ? t("remote.tokenSaved") : t("remote.tokenOptional")}
                  onChange={(event) => setForm({ ...form, api_token: event.target.value })}
                />
                <small>{t("remote.apiTokenHint")}</small>
              </label>
            </div>
            {form.api_token_set && (
              <label className="switch-row remote-clear-token">
                <span>{t("remote.clearToken")}</span>
                <input
                  type="checkbox"
                  checked={form.clear_api_token}
                  onChange={(event) => setForm({ ...form, clear_api_token: event.target.checked })}
                />
              </label>
            )}
            <div className="remote-form-actions">
              {editedHost?.trusted && (
                <button className="ghost-action" type="button" onClick={() => openImportDialog(editedHost)}>
                  {t("remote.changeSSHRoute")}
                </button>
              )}
              <button className="action" type="submit">
                <Save size={15} />
                {t("common.save")}
              </button>
            </div>
          </form>
        </section>
      )}

      {hostDialogOpen && !form && (
      <section
        aria-labelledby="remote-host-dialog-title"
        aria-modal="true"
        className="surface remote-host-dialog remote-discovery-dialog"
        role="dialog"
      >
        <div className="surface-header">
          <div>
            <h2 id="remote-host-dialog-title">
              {replacementHost ? t("remote.changeSSHRoute") : t("remote.add")}
            </h2>
            <p className="subtle-copy">
              {replacementHost
                ? t("remote.changeSSHRouteHint", { name: replacementHost.name })
                : t("remote.discoveredHint")}
            </p>
          </div>
          <div className="remote-discovery-summary">
            <span className="pill">{discoveredHosts.length}</span>
            <button
              className="ghost-action"
              type="button"
              disabled={discoveryLoading || Boolean(importingName)}
              onClick={() => void loadDiscovered()}
            >
              <RefreshCw size={14} className={discoveryLoading ? "spin" : ""} />
              {t("common.refresh")}
            </button>
            <button className="ghost-action" disabled={Boolean(importingName)} onClick={closeHostDialog} aria-label={t("common.close")}>
              <X size={15} />
              {t("common.close")}
            </button>
          </div>
        </div>
        {!replacementHost && <div className="remote-host-dialog-tabs" role="tablist" aria-label={t("remote.add")}>
          <button aria-selected="true" className="active" role="tab" type="button">
            {t("remote.discovered")}
          </button>
          <button
            aria-selected="false"
            disabled={Boolean(importingName)}
            onClick={() => setForm(emptyForm())}
            role="tab"
            type="button"
          >
            {t("remote.manualAdd")}
          </button>
        </div>}
        <div className="surface-body remote-host-form">
          <label className="field">
            <span>{t("remote.remoteAddr")}</span>
            <input
              value={importAddr}
              placeholder="127.0.0.1:8765"
              disabled={Boolean(importingName)}
              onChange={(event) => {
                setImportAddr(event.target.value);
                setPendingImportFingerprints({});
              }}
            />
            <small>{t("remote.remoteAddrHint")}</small>
          </label>
        </div>
        {(message || error) && (
          <div className={`remote-feedback ${error ? "error" : "success"}`}>
            {error || message}
          </div>
        )}
        {!discoveryLoading && discoveredHosts.some((host) => host.proxy_jump || host.proxy_command) && (
          <div className="remote-proxy-note">
            <ShieldAlert size={17} />
            <div>
              <strong>{t("remote.proxyRequired")}</strong>
              <span>{t("remote.proxySupported")}</span>
            </div>
          </div>
        )}
        <div className="remote-host-list remote-discovered-list">
          {discoveryLoading && <div className="empty-state">{t("common.loading")}</div>}
          {!discoveryLoading && discoveryError && (
            <div className="remote-discovery-error">
              <ShieldAlert size={17} />
              <span>{t("remote.discoveryError")}: {discoveryError}</span>
            </div>
          )}
          {!discoveryLoading && !discoveryError && discoveredHosts.length === 0 && (
            <div className="empty-state remote-discovery-empty">
              <KeyRound size={25} />
              <strong>{t("remote.noneDiscovered")}</strong>
              <span>{t("remote.noneDiscoveredHint")}</span>
            </div>
          )}
          {!discoveryLoading && !discoveryError && discoveredHosts.map((host) => {
            const configured = replacementHost ? undefined : configuredHost(host);
            const requiresProxy = Boolean(host.proxy_jump || host.proxy_command);
            const pendingFingerprint = pendingImportFingerprints[host.name];
            const importing = importingName === host.name;
            return (
              <article key={host.name} className="remote-host-card remote-discovered-card">
                <div className="remote-host-icon">
                  <KeyRound size={19} />
                </div>
                <div className="remote-host-main">
                  <header>
                    <div>
                      <strong>{host.name}</strong>
                      <span>{host.user}@{host.host}:{host.port}</span>
                    </div>
                    <span className={`status-badge ${requiresProxy ? "warning" : "neutral"}`}>
                      {requiresProxy ? <ShieldAlert size={13} /> : <CheckCircle2 size={13} />}
                      {requiresProxy ? t("remote.proxyRequired") : t("remote.readyToImport")}
                    </span>
                  </header>
                  <div className="remote-host-facts">
                    <span><KeyRound size={13} />{host.key_path || t("remote.agentOrDefaultKey")}</span>
                    <span>{host.source}</span>
                    {host.proxy_jump && <span>ProxyJump: {host.proxy_jump}</span>}
                    {host.proxy_command && <span>ProxyCommand</span>}
                  </div>
                  {pendingFingerprint && (
                    <div className="host-key-confirmation">
                      <Fingerprint size={18} />
                      <div>
                        <strong>{t("remote.confirmFingerprint")}</strong>
                        <code>{pendingFingerprint}</code>
                        <span>{t("remote.confirmFingerprintHint")}</span>
                      </div>
                      <button className="action" disabled={Boolean(importingName)} onClick={() => void importDiscovered(host, true)}>
                        {t("remote.trustInstallAndConnect")}
                      </button>
                    </div>
                  )}
                </div>
                <div className="remote-host-actions">
                  <button
                    className={configured?.trusted ? "ghost-action" : "action"}
                    type="button"
                    disabled={Boolean(configured?.trusted) || Boolean(importingName)}
                    onClick={() => void importDiscovered(host)}
                  >
                    {configured?.trusted ? <CheckCircle2 size={14} /> : <Plus size={14} />}
                    {importing
                      ? t("remote.importing")
                      : replacementHost
                        ? t("remote.useSSHRoute")
                        : configured?.trusted
                        ? t("remote.imported")
                        : configured
                          ? t("remote.finishImport")
                          : t("remote.import")}
                  </button>
                </div>
              </article>
            );
          })}
        </div>
      </section>
      )}

      <section className="surface remote-machines-surface">
        <div className="surface-header remote-machines-header">
          <div className="remote-machines-heading">
            <h2>{t("remote.title")}</h2>
            <span className="pill">{hosts.length + (localMachine ? 1 : 0)}</span>
          </div>
          <div className="remote-machines-toolbar">
            <button className="action" type="button" aria-expanded={phoneOpen} onClick={() => setPhoneOpen((open) => !open)}>
              <Smartphone size={15} />
              {t("remote.connectPhone")}
            </button>
            <button className="action" type="button" onClick={() => openImportDialog()}>
              <Server size={15} />
              {t("remote.connectServer")}
            </button>
            <button className="ghost-action" type="button" onClick={() => setPairingOpen(true)} title={t("remote.linkHint")}>
              <Link2 size={14} />
              {t("remote.linkAdd")}
            </button>
            <button
              className="ghost-action"
              type="button"
              disabled={loading || syncingSSHConfig || updatingAll || hosts.length === 0}
              onClick={() => void syncSSHConfig()}
              title={t("remote.syncSSHConfigHint")}
            >
              <RefreshCw size={14} className={syncingSSHConfig ? "spin" : ""} />
              {syncingSSHConfig ? t("remote.syncingSSHConfig") : t("remote.syncSSHConfig")}
            </button>
            <button
              className="ghost-action"
              type="button"
              disabled={loading || syncingSSHConfig || updatingAll || checkingHosts || updateCandidates.length === 0}
              onClick={() => void updateAll()}
              title={
                checkingHosts
                  ? t("remote.checkingUpdates")
                  : updateCandidates.length === 0
                    ? t(updateCheckUnavailable ? "remote.updateCheckUnavailable" : "remote.allUpToDate")
                    : t("remote.updateAll")
              }
            >
              <RefreshCw size={14} className={updatingAll ? "spin" : ""} />
              {updatingAll ? t("remote.updatingAll") : t("remote.updateAll")}
            </button>
          </div>
        </div>
        {(message || error) && (
          <div className={`remote-feedback ${error ? "error" : "success"}`} role="status">
            {error || message}
          </div>
        )}
        {phoneOpen && <PhoneAppLink inline onClose={() => setPhoneOpen(false)} />}
        {pairingOpen && (
          <div className="remote-pairing" role="group" aria-label={t("remote.linkAdd")}>
            <header>
              <div>
                <strong>{t("remote.linkAdd")}</strong>
                <span>{t("remote.linkHint")}</span>
              </div>
              <button className="ghost-action" type="button" onClick={closePairing}>
                <X size={15} />
                {t("common.close")}
              </button>
            </header>
            {!pairing && (
              <div className="remote-pairing-form">
                <input
                  aria-label={t("remote.linkName")}
                  placeholder={t("remote.linkName")}
                  value={pairingName}
                  maxLength={120}
                  onChange={(event) => setPairingName(event.target.value)}
                />
                <button className="action" type="button" disabled={pairingBusy} onClick={() => void createPairingCode()}>
                  {pairingBusy ? t("common.loading") : t("remote.linkCreate")}
                </button>
              </div>
            )}
            {pairing && (
              <div className="remote-pairing-result">
                <span>{t("remote.linkRun", { time: new Date(pairing.expires_at).toLocaleTimeString() })}</span>
                <div className="remote-pairing-command">
                  <code>{pairing.command}</code>
                  <button className="ghost-action" type="button" onClick={() => void navigator.clipboard?.writeText(pairing.command)}>
                    <Copy size={14} />
                    {t("remote.linkCopy")}
                  </button>
                </div>
                <span>{t("remote.linkDials", { url: pairing.hub_url })}</span>
                {pairing.warning && <span className="remote-pairing-warning">{pairing.warning}</span>}
              </div>
            )}
            {!pairing && (
              <div className="remote-pairing-passive">
                <strong>{t("remote.passiveTitle")}</strong>
                <span>{t("remote.passiveHint")}</span>
                <div className="remote-pairing-form">
                  <input
                    aria-label={t("remote.passiveInvite")}
                    placeholder={t("remote.passiveInvite")}
                    value={passiveInvite}
                    onChange={(event) => setPassiveInvite(event.target.value)}
                  />
                  <button
                    className="action"
                    type="button"
                    disabled={passiveBusy || !passiveInvite.trim().startsWith("amxi_")}
                    onClick={() => void addPassiveDevice()}
                  >
                    {passiveBusy ? t("common.loading") : t("remote.passiveAdd")}
                  </button>
                </div>
              </div>
            )}
            {pairingError && <div className="remote-feedback error" role="alert">{pairingError}</div>}
          </div>
        )}
        <div className="remote-host-list">
          {loading && <div className="empty-state">{t("common.loading")}</div>}
          {localMachine && (
            <article className="remote-host-card local-machine-card">
              <div className="remote-host-icon">
                <Laptop size={19} />
              </div>
              <div className="remote-host-main">
                <header>
                  <div>
                    <strong>{localMachine.name}</strong>
                    <span>
                      {t("remote.localSubtitle")}
                      {localVersion && ` · v${localVersion.replace(/^v/, "")}`}
                    </span>
                  </div>
                  <span className="remote-host-badges">
                    {localMachine.role && (
                      <span className="status-badge" title={t(localMachine.role === "hub" ? "remote.localHubHint" : "remote.localStandbyHint")}>
                        <Server size={13} />
                        {t(localMachine.role === "hub" ? "remote.localHub" : "remote.localStandby")}
                      </span>
                    )}
                    <span className="status-badge success">
                      <CheckCircle2 size={13} />
                      {t("remote.healthHealthy")}
                    </span>
                  </span>
                </header>
              </div>
              <div className="remote-host-actions" />
            </article>
          )}
          {fleet?.stale && (
            <div className="remote-feedback error" role="status" title={fleet.error}>
              {t("remote.fleetStale", { time: new Date(fleet.fetched_at).toLocaleString() })}
            </div>
          )}
          {!loading && hosts.length === 0 && fleetOnlyMachines(hosts, fleet).length === 0 && (
            <div className="empty-state">
              <Laptop size={28} />
              <strong>{t("remote.empty")}</strong>
              <span>{t("remote.emptyHint")}</span>
            </div>
          )}
          {hosts.map((host) => {
            const pendingFingerprint = pendingFingerprints[host.id];
            const probed: RemoteHostSnapshot = snapshots[host.id] ?? {
              health: host.trusted ? "checking" : "unverified",
            };
            const linked = host.kind === "link";
            const presence = linkPresence(host, fleet);
            // A machine connected to no hub is offline whatever a probe says;
            // one the hub sees connected is healthy even where this machine
            // cannot reach it itself.
            const snapshot: RemoteHostSnapshot = presence && !presence.online
              ? {
                health: "offline",
                error: presence.lastSeenAt ? t("remote.lastSeen", { time: new Date(presence.lastSeenAt).toLocaleString() }) : undefined,
              }
              : presence?.viaHub && probed.health !== "healthy"
                ? { health: "healthy", version: presence.version }
                : probed;
            const updateAvailable = !linked && isRemoteUpdateAvailable(snapshot.version, localVersion);
            const checking = snapshot.health === "checking";
            const active = activeRemoteID() === host.id;
            const deviceID = pairedDeviceID(host);
            const device = deviceID ? owners?.devices.find((item) => item.id === deviceID) : undefined;
            const healthTone = snapshot.health === "healthy"
              ? "success"
              : snapshot.health === "error"
                ? "danger"
                : "warning";
            const healthLabel = snapshot.health === "healthy"
              ? t("remote.healthHealthy")
              : snapshot.health === "error"
                ? t("remote.healthError")
                : snapshot.health === "offline"
                  ? t("remote.healthOffline")
                  : snapshot.health === "unverified"
                    ? t("remote.healthUnverified")
                    : t("remote.healthChecking");
            return (
              <article key={host.id} className={`remote-host-card${active ? " active" : ""}`}>
                <div className="remote-host-icon">
                  <Server size={19} />
                </div>
                <div className="remote-host-main">
                  <header>
                    <div>
                      <strong>{host.name}</strong>
                      <span>
                        {linked
                          ? t(host.link_mode === "passive" ? "remote.linkPassiveSubtitle" : presence?.viaHub ? "remote.linkSubtitleViaHub" : "remote.linkSubtitle")
                          : `${host.user}@${host.host}:${host.port}`}
                        {linked && host.reported_at && ` · ${t("remote.linkReported", { time: new Date(host.reported_at).toLocaleString() })}`}
                      </span>
                      {!linked && host.ssh_alias && host.ssh_alias !== host.name && (
                        <span>{t("remote.sshAlias", { alias: host.ssh_alias })}</span>
                      )}
                    </div>
                    <span className="remote-host-badges">
                      {host.hub_role && (
                        <span className="status-badge" title={t(host.hub_role === "hub" ? "remote.hubRoleHubHint" : "remote.hubRoleStandbyHint")}>
                          <Server size={13} />
                          {t(host.hub_role === "hub" ? "remote.hubRoleHub" : "remote.hubRoleStandby")}
                        </span>
                      )}
                      <span className={`status-badge ${healthTone}`} title={snapshot.error}>
                        {snapshot.health === "checking"
                          ? <RefreshCw size={13} className="spin" />
                          : snapshot.health === "healthy"
                            ? <CheckCircle2 size={13} />
                            : snapshot.health === "offline"
                              ? <WifiOff size={13} />
                              : <ShieldAlert size={13} />}
                        {healthLabel}
                      </span>
                    </span>
                  </header>
                  {host.paths && host.paths.length > 1 && (
                    <ul className="remote-host-paths" aria-label={t("remote.paths")}>
                      {host.paths.map((path) => (
                        <li
                          key={`${path.via}:${path.host_id ?? ""}`}
                          className={`status-badge ${path.ok ? "success" : "danger"}${path.active ? " active" : ""}`}
                          title={path.error || path.name}
                        >
                          {path.ok ? <CheckCircle2 size={13} /> : <ShieldAlert size={13} />}
                          {t(`remote.path.${path.via}`)}
                          {typeof path.rtt_ms === "number" && path.rtt_ms > 0 && ` · ${t("remote.pathRTT", { ms: path.rtt_ms })}`}
                          {path.active && ` · ${t("remote.pathActive")}`}
                          {path.host_id && path.host_id !== host.id && path.via !== "relay" && (
                            <button
                              aria-label={t("remote.pathRemove", { path: t(`remote.path.${path.via}`) })}
                              className="icon-button"
                              disabled={deleting}
                              onClick={() => {
                                setDeleteError("");
                                setDeleteCandidate({
                                  ...host, id: path.host_id ?? "", name: path.name || host.name,
                                  kind: path.via === "ssh" ? "ssh" : "link", paths: undefined,
                                });
                              }}
                              type="button"
                            >
                              <Trash2 size={12} />
                            </button>
                          )}
                        </li>
                      ))}
                    </ul>
                  )}
                  {pendingFingerprint && (
                    <div className="host-key-confirmation">
                      <Fingerprint size={18} />
                      <div>
                        <strong>{t("remote.confirmFingerprint")}</strong>
                        <code>{pendingFingerprint}</code>
                        <span>{t("remote.confirmFingerprintHint")}</span>
                      </div>
                      <button className="action" onClick={() => void verifyHost(host)}>
                        {t("remote.trustAndConnect")}
                      </button>
                    </div>
                  )}
                </div>
                <div className="remote-host-actions">
                  {device && owners && (
                    <select
                      className="remote-host-owner"
                      aria-label={t("accounts.assignDevice", { name: host.name })}
                      title={t("accounts.deviceOwner")}
                      value={device.owner_account_id ?? ""}
                      disabled={assigningID === device.id}
                      onChange={(event) => void assignOwner(device.id, event.target.value)}
                    >
                      <option value="">{t("accounts.ownerAdmins")}</option>
                      {device.owner_account_id && !owners.accounts.some((account) => account.id === device.owner_account_id) && (
                        <option value={device.owner_account_id}>{device.owner_account_name || device.owner_account_id}</option>
                      )}
                      {owners.accounts.filter((account) => account.status !== "pending").map((account) => (
                        <option key={account.id} value={account.id}>
                          {accountLabel(account)}{account.status === "disabled" ? ` (${t("accounts.disabled")})` : ""}
                        </option>
                      ))}
                    </select>
                  )}
                  <button
                    className="ghost-action"
                    disabled={!host.trusted || checking || updatingID === host.id || updatingAll}
                    onClick={() => void (updateAvailable ? updateRemote(host) : inspectHosts([host]))}
                    title={host.trusted ? undefined : t("remote.healthUnverified")}
                  >
                    <RefreshCw size={14} className={updatingID === host.id ? "spin" : ""} />
                    {updatingID === host.id
                      ? t("remote.updating")
                      : checking
                        ? t("remote.checkingUpdates")
                        : updateAvailable
                          ? t("remote.update")
                          : t("remote.checkUpdates")}
                  </button>
                  {!linked && (
                    <button className="ghost-action" onClick={() => edit(host)}>
                      <Pencil size={14} />
                      {t("common.edit")}
                    </button>
                  )}
                  <button
                    className="ghost-action danger-action"
                    type="button"
                    disabled={deleting || updatingID === host.id || updatingAll}
                    onClick={() => {
                      setDeleteError("");
                      setDeleteCandidate(host);
                    }}
                  >
                    <Trash2 size={14} />
                    {t("common.delete")}
                  </button>
                </div>
              </article>
            );
          })}
          {fleetOnlyMachines(hosts, fleet).map((machine) => (
            <article key={machine.id} className="remote-host-card">
              <div className="remote-host-icon">
                <Server size={19} />
              </div>
              <div className="remote-host-main">
                <header>
                  <div>
                    <strong>{machine.name}</strong>
                    <span>
                      {t("remote.fleetViaHub")}
                      {machine.version && ` · v${machine.version.replace(/^v/, "")}`}
                      {!machine.online && machine.last_seen_at && ` · ${t("remote.lastSeen", { time: new Date(machine.last_seen_at).toLocaleString() })}`}
                    </span>
                  </div>
                  <span className="remote-host-badges">
                    {machine.hub_role && (
                      <span className="status-badge" title={t(machine.hub_role === "hub" ? "remote.hubRoleHubHint" : "remote.hubRoleStandbyHint")}>
                        <Server size={13} />
                        {t(machine.hub_role === "hub" ? "remote.hubRoleHub" : "remote.hubRoleStandby")}
                      </span>
                    )}
                    <span className={`status-badge ${machine.online ? "success" : "warning"}`}>
                      {machine.online ? <CheckCircle2 size={13} /> : <WifiOff size={13} />}
                      {t(machine.online ? "remote.healthHealthy" : "remote.healthOffline")}
                    </span>
                  </span>
                </header>
              </div>
              <div className="remote-host-actions" />
            </article>
          ))}
        </div>
      </section>
    </div>
  );
}
