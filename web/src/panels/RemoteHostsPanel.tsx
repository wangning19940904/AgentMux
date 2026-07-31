import {
  CheckCircle2,
  Fingerprint,
  KeyRound,
  Laptop,
  Pencil,
  PlugZap,
  Plus,
  RefreshCw,
  Save,
  Server,
  ShieldAlert,
  Trash2,
  X,
} from "lucide-react";
import { FormEvent, useEffect, useState } from "react";
import {
  activeRemoteID,
  api,
  DiscoveredRemoteHost,
  RemoteConnectionError,
  RemoteHost,
  RemoteTestResult,
  setActiveRemoteID,
} from "../api";
import { useI18n } from "../i18n";

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

export function RemoteHostsPanel() {
  const { t } = useI18n();
  const [hosts, setHosts] = useState<RemoteHost[]>([]);
  const [discoveredHosts, setDiscoveredHosts] = useState<DiscoveredRemoteHost[]>([]);
  const [form, setForm] = useState<RemoteForm | null>(null);
  const [loading, setLoading] = useState(true);
  const [discoveryLoading, setDiscoveryLoading] = useState(true);
  const [discoveryError, setDiscoveryError] = useState("");
  const [message, setMessage] = useState("");
  const [error, setError] = useState("");
  const [testingID, setTestingID] = useState("");
  const [testResults, setTestResults] = useState<Record<string, RemoteTestResult>>({});
  const [pendingFingerprints, setPendingFingerprints] = useState<Record<string, string>>({});

  const load = async () => {
    setLoading(true);
    try {
      setHosts(await api.remoteHosts());
      setError("");
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : String(cause));
    } finally {
      setLoading(false);
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

  useEffect(() => {
    void load();
    void loadDiscovered();
  }, []);

  const addDiscovered = (host: DiscoveredRemoteHost) => {
    setForm({
      ...emptyForm(),
      name: host.name,
      host: host.host,
      port: host.port,
      user: host.user,
      key_path: host.key_path ?? "",
    });
    setMessage("");
    setError("");
    window.requestAnimationFrame(() => {
      document.querySelector(".remote-host-form")?.scrollIntoView({ behavior: "smooth", block: "center" });
    });
  };

  const isConfigured = (candidate: DiscoveredRemoteHost) =>
    hosts.some(
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
      setMessage(t("remote.saved"));
      setForm(null);
      await load();
      if (!saved.trusted) {
        setMessage(t("remote.savedTestHint"));
      }
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : String(cause));
    }
  };

  const test = async (host: RemoteHost, trustOnFirstUse = false) => {
    setTestingID(host.id);
    setMessage("");
    setError("");
    try {
      const result = await api.testRemoteHost(host.id, trustOnFirstUse);
      setTestResults((current) => ({ ...current, [host.id]: result }));
      setPendingFingerprints((current) => {
        const next = { ...current };
        delete next[host.id];
        return next;
      });
      setMessage(t("remote.testSucceeded").replace("{latency}", String(result.latency_ms)));
      await load();
    } catch (cause) {
      if (
        cause instanceof RemoteConnectionError &&
        cause.code === "host_key_untrusted" &&
        cause.fingerprint
      ) {
        setPendingFingerprints((current) => ({ ...current, [host.id]: cause.fingerprint! }));
      } else {
        setError(cause instanceof Error ? cause.message : String(cause));
      }
    } finally {
      setTestingID("");
    }
  };

  const remove = async (host: RemoteHost) => {
    if (!window.confirm(t("remote.deleteConfirm").replace("{name}", host.name))) return;
    try {
      if (activeRemoteID() === host.id) setActiveRemoteID("");
      await api.deleteRemoteHost(host.id);
      setMessage(t("remote.deleted"));
      await load();
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : String(cause));
    }
  };

  const useHost = (id: string) => {
    setActiveRemoteID(id);
    window.location.hash = "#overview";
    window.location.reload();
  };

  return (
    <div className="page-stack remote-hosts-page">
      <section className="surface remote-hosts-hero">
        <div className="surface-header">
          <div>
            <h2>{t("remote.title")}</h2>
            <p className="subtle-copy">{t("remote.subtitle")}</p>
          </div>
          <button className="action" onClick={() => setForm(emptyForm())}>
            <Plus size={15} />
            {t("remote.add")}
          </button>
        </div>
        {(message || error) && (
          <div className={`remote-feedback ${error ? "error" : "success"}`}>
            {error || message}
          </div>
        )}
      </section>

      {form && (
        <section className="surface">
          <div className="surface-header">
            <div>
              <h2>{form.id ? t("remote.edit") : t("remote.add")}</h2>
              <p className="subtle-copy">{t("remote.formHint")}</p>
            </div>
            <button className="ghost-action" onClick={() => setForm(null)} aria-label={t("common.close")}>
              <X size={15} />
              {t("common.close")}
            </button>
          </div>
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

      <section className="surface">
        <div className="surface-header">
          <div>
            <h2>{t("remote.discovered")}</h2>
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
          </div>
        </div>
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
            const configured = isConfigured(host);
            const requiresProxy = Boolean(host.proxy_jump || host.proxy_command);
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
                </div>
                <div className="remote-host-actions">
                  <button
                    className={configured ? "ghost-action" : "action"}
                    type="button"
                    disabled={configured || requiresProxy}
                    title={requiresProxy ? t("remote.proxyUnsupported") : undefined}
                    onClick={() => addDiscovered(host)}
                  >
                    {configured ? <CheckCircle2 size={14} /> : <Plus size={14} />}
                    {configured ? t("remote.imported") : t("remote.import")}
                  </button>
                </div>
              </article>
            );
          })}
        </div>
      </section>

      <section className="surface">
        <div className="surface-header">
          <div>
            <h2>{t("remote.hosts")}</h2>
            <p className="subtle-copy">{t("remote.hostsHint")}</p>
          </div>
          <span className="pill">{hosts.length}</span>
        </div>
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
            const result = testResults[host.id];
            const active = activeRemoteID() === host.id;
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
                    <span className={`status-badge ${host.trusted ? "success" : "warning"}`}>
                      {host.trusted ? <CheckCircle2 size={13} /> : <ShieldAlert size={13} />}
                      {host.trusted ? t("remote.trusted") : t("remote.notTrusted")}
                    </span>
                  </header>
                  <div className="remote-host-facts">
                    <span><PlugZap size={13} />{host.remote_addr}</span>
                    <span><KeyRound size={13} />{host.key_path || t("remote.agentOrDefaultKey")}</span>
                    {host.host_key_fingerprint && (
                      <span className="remote-fingerprint">
                        <Fingerprint size={13} />{host.host_key_fingerprint}
                      </span>
                    )}
                    {result && <span>{result.latency_ms} ms · AgentMux {result.status?.version ?? ""}</span>}
                  </div>
                  {pendingFingerprint && (
                    <div className="host-key-confirmation">
                      <Fingerprint size={18} />
                      <div>
                        <strong>{t("remote.confirmFingerprint")}</strong>
                        <code>{pendingFingerprint}</code>
                        <span>{t("remote.confirmFingerprintHint")}</span>
                      </div>
                      <button className="action" onClick={() => void test(host, true)}>
                        {t("remote.trustAndConnect")}
                      </button>
                    </div>
                  )}
                </div>
                <div className="remote-host-actions">
                  <button
                    className="action"
                    disabled={!host.trusted}
                    onClick={() => useHost(host.id)}
                    title={host.trusted ? t("remote.useMachine") : t("remote.testFirst")}
                  >
                    <Server size={14} />
                    {active ? t("remote.inUse") : t("remote.useMachine")}
                  </button>
                  <button
                    className="ghost-action"
                    disabled={testingID === host.id}
                    onClick={() => void test(host)}
                  >
                    <PlugZap size={14} />
                    {testingID === host.id ? t("remote.testing") : t("remote.test")}
                  </button>
                  <button className="ghost-action" onClick={() => edit(host)}>
                    <Pencil size={14} />
                    {t("common.edit")}
                  </button>
                  <button className="ghost-action danger-action" onClick={() => void remove(host)}>
                    <Trash2 size={14} />
                    {t("common.delete")}
                  </button>
                </div>
              </article>
            );
          })}
        </div>
      </section>

      <section className="surface remote-security-note">
        <div className="surface-body">
          <Fingerprint size={20} />
          <div>
            <strong>{t("remote.securityTitle")}</strong>
            <p className="subtle-copy">{t("remote.securityHint")}</p>
          </div>
        </div>
      </section>
    </div>
  );
}
