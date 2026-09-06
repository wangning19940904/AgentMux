import {
  CheckCircle2,
  Fingerprint,
  KeyRound,
  Laptop,
  Pencil,
  Plus,
  RefreshCw,
  Save,
  Server,
  ShieldAlert,
  Trash2,
  X,
} from "lucide-react";
import { FormEvent, useEffect, useRef, useState } from "react";
import {
  activeRemoteID,
  setActiveMachineScope,
  api,
  DiscoveredRemoteHost,
  notifyRemoteHostsChanged,
  RemoteConnectionError,
  RemoteHost,
} from "../api";
import { useI18n } from "../i18n";
import { DeleteRemoteHostDialog } from "./DeleteRemoteHostDialog";
import {
  isRemoteUpdateAvailable,
  remoteUpdateCandidates,
  type RemoteHostSnapshot,
} from "./remoteHostModel";

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

export function RemoteHostsPanel({ addRequest = 0 }: { addRequest?: number }) {
  const { t } = useI18n();
  const [hosts, setHosts] = useState<RemoteHost[]>([]);
  const [discoveredHosts, setDiscoveredHosts] = useState<DiscoveredRemoteHost[]>([]);
  const [form, setForm] = useState<RemoteForm | null>(null);
  const [hostDialogOpen, setHostDialogOpen] = useState(false);
  const [loading, setLoading] = useState(true);
  const [discoveryLoading, setDiscoveryLoading] = useState(false);
  const [syncingSSHConfig, setSyncingSSHConfig] = useState(false);
  const [discoveryError, setDiscoveryError] = useState("");
  const [message, setMessage] = useState("");
  const [error, setError] = useState("");
  const [deleteCandidate, setDeleteCandidate] = useState<RemoteHost | null>(null);
  const [deleting, setDeleting] = useState(false);
  const [deleteError, setDeleteError] = useState("");
  const deleteInFlight = useRef(false);
  const [updatingID, setUpdatingID] = useState("");
  const [updatingAll, setUpdatingAll] = useState(false);
  const [importingName, setImportingName] = useState("");
  const [localVersion, setLocalVersion] = useState("");
  const [snapshots, setSnapshots] = useState<Record<string, RemoteHostSnapshot>>({});
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

  const load = async (): Promise<RemoteHost[] | null> => {
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
    void load();
  }, []);

  useEffect(() => {
    if (!hostDialogOpen) return;
    const previousOverflow = document.body.style.overflow;
    const closeOnEscape = (event: KeyboardEvent) => {
      if (event.key === "Escape") {
        setHostDialogOpen(false);
        setForm(null);
      }
    };
    document.body.style.overflow = "hidden";
    window.addEventListener("keydown", closeOnEscape);
    return () => {
      document.body.style.overflow = previousOverflow;
      window.removeEventListener("keydown", closeOnEscape);
    };
  }, [hostDialogOpen]);

  const importDiscovered = async (host: DiscoveredRemoteHost, trustOnFirstUse = false) => {
    setImportingName(host.name);
    setMessage("");
    setError("");
    try {
      const result = await api.importRemoteHost(host, trustOnFirstUse);
      setPendingImportFingerprints((current) => {
        const next = { ...current };
        delete next[host.name];
        return next;
      });
      setMessage(result.installed ? t("remote.importInstalled") : t("remote.importSucceeded"));
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
        setError(cause instanceof Error ? cause.message : String(cause));
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
  };

  const openAddDialog = () => {
    setForm(null);
    setMessage("");
    setError("");
    setHostDialogOpen(true);
    void loadDiscovered();
  };

  useEffect(() => {
    if (addRequest <= 0 || handledAddRequest.current === addRequest) return;
    handledAddRequest.current = addRequest;
    openAddDialog();
  }, [addRequest]);

  const updateCandidates = remoteUpdateCandidates(hosts, snapshots, localVersion);
  const checkingHosts = hosts.some((host) => snapshots[host.id]?.health === "checking");
  const updateCheckUnavailable = hosts.some((host) =>
    !host.trusted || snapshots[host.id]?.health === "error" || !snapshots[host.id]?.version,
  );

  const updateAll = async () => {
    const candidates = remoteUpdateCandidates(hosts, snapshots, localVersion);
    if (candidates.length === 0) return;
    const names = candidates.map((host) => host.name).join("\n");
    if (!window.confirm(t("remote.updateAllConfirm", { count: candidates.length, names }))) return;

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
            <h2 id="remote-host-dialog-title">{t("remote.add")}</h2>
            <p className="subtle-copy">{t("remote.discoveredHint")}</p>
          </div>
          <div className="remote-discovery-summary">
            <span className="pill">{discoveredHosts.length}</span>
            <button
              className="ghost-action"
              type="button"
              disabled={discoveryLoading}
              onClick={() => void loadDiscovered()}
            >
              <RefreshCw size={14} className={discoveryLoading ? "spin" : ""} />
              {t("common.refresh")}
            </button>
            <button className="ghost-action" onClick={closeHostDialog} aria-label={t("common.close")}>
              <X size={15} />
              {t("common.close")}
            </button>
          </div>
        </div>
        <div className="remote-host-dialog-tabs" role="tablist" aria-label={t("remote.add")}>
          <button aria-selected="true" className="active" role="tab" type="button">
            {t("remote.discovered")}
          </button>
          <button
            aria-selected="false"
            onClick={() => setForm(emptyForm())}
            role="tab"
            type="button"
          >
            {t("remote.manualAdd")}
          </button>
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
              <span>{t("remote.proxyUnsupported")}</span>
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
            const configured = configuredHost(host);
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
                      <button className="action" onClick={() => void importDiscovered(host, true)}>
                        {t("remote.trustInstallAndConnect")}
                      </button>
                    </div>
                  )}
                </div>
                <div className="remote-host-actions">
                  <button
                    className={configured?.trusted ? "ghost-action" : "action"}
                    type="button"
                    disabled={Boolean(configured?.trusted) || requiresProxy || importing}
                    title={requiresProxy ? t("remote.proxyUnsupported") : undefined}
                    onClick={() => void importDiscovered(host)}
                  >
                    {configured?.trusted ? <CheckCircle2 size={14} /> : <Plus size={14} />}
                    {importing
                      ? t("remote.importing")
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
            <span className="pill">{hosts.length}</span>
          </div>
          <div className="remote-machines-toolbar">
            <button className="action" type="button" onClick={openAddDialog}>
              <Plus size={15} />
              {t("remote.add")}
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
        <div className="remote-host-list">
          {loading && <div className="empty-state">{t("common.loading")}</div>}
          {!loading && hosts.length === 0 && (
            <div className="empty-state">
              <Laptop size={28} />
              <strong>{t("remote.empty")}</strong>
              <span>{t("remote.emptyHint")}</span>
            </div>
          )}
          {hosts.map((host) => {
            const pendingFingerprint = pendingFingerprints[host.id];
            const snapshot = snapshots[host.id] ?? {
              health: host.trusted ? "checking" as const : "unverified" as const,
            };
            const updateAvailable = isRemoteUpdateAvailable(snapshot.version, localVersion);
            const checking = snapshot.health === "checking";
            const active = activeRemoteID() === host.id;
            const healthTone = snapshot.health === "healthy"
              ? "success"
              : snapshot.health === "error"
                ? "danger"
                : "warning";
            const healthLabel = snapshot.health === "healthy"
              ? t("remote.healthHealthy")
              : snapshot.health === "error"
                ? t("remote.healthError")
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
                      <span>{host.user}@{host.host}:{host.port}</span>
                    </div>
                    <span className={`status-badge ${healthTone}`} title={snapshot.error}>
                      {snapshot.health === "checking"
                        ? <RefreshCw size={13} className="spin" />
                        : snapshot.health === "healthy"
                          ? <CheckCircle2 size={13} />
                          : <ShieldAlert size={13} />}
                      {healthLabel}
                    </span>
                  </header>
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
                  <button className="ghost-action" onClick={() => edit(host)}>
                    <Pencil size={14} />
                    {t("common.edit")}
                  </button>
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
        </div>
      </section>
    </div>
  );
}
