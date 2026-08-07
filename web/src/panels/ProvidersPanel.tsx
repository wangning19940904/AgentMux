import { useEffect, useMemo, useState } from "react";
import { AlertTriangle, Bell, CheckCircle2, Clock, Power, PowerOff, RefreshCw, Save, X } from "lucide-react";
import { api, Provider, ProviderMonitorAlert, ProviderMonitorConfig } from "../api";
import { useI18n } from "../i18n";
import { useAsync } from "../useAsync";
import { usePolling } from "../hooks/usePolling";

function claudeDesktopModelList(provider?: Provider) {
  const models = provider?.meta?.claude_desktop_models;
  if (!Array.isArray(models)) return "";
  return models
    .map((item) => {
      if (!item || typeof item !== "object") return "";
      const model = item as Record<string, unknown>;
      const id = model.id;
      const name = model.name;
      return typeof id === "string" ? id : typeof name === "string" ? name : "";
    })
    .filter(Boolean)
    .join("\n");
}

function parseClaudeDesktopModelList(value: string) {
  return value
    .split(/[\n,]/)
    .map((model) => model.trim())
    .filter(Boolean)
    .map((model) => ({ id: model, name: model, display_name: model }));
}

const defaultMonitorConfig: ProviderMonitorConfig = {
  enabled: false,
  interval_minutes: 360,
  probe_models: true,
  max_models_per_provider: 20,
};

function monitorBadgeClass(state: string) {
  if (state === "healthy") return "status-badge success";
  if (state === "warning" || state === "checking") return "status-badge warning";
  if (state === "error") return "status-badge danger";
  return "status-badge";
}

function formatMonitorTime(value?: string) {
  if (!value) return "—";
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return "—";
  return new Intl.DateTimeFormat(undefined, {
    month: "short",
    day: "numeric",
    hour: "2-digit",
    minute: "2-digit",
  }).format(date);
}

export function ProvidersPanel() {
  const { t } = useI18n();
  const providers = useAsync(() => api.providers(), []);
  const claude3p = useAsync(() => api.claude3pStatus(), []);
  const monitor = useAsync(() => api.providerMonitor(), []);
  const [busy, setBusy] = useState<string | null>(null);
  const [selectedClaudeProvider, setSelectedClaudeProvider] = useState("");
  const [claudeModelMapping, setClaudeModelMapping] = useState(false);
  const [claudeModelList, setClaudeModelList] = useState("");
  const [notice, setNotice] = useState("");
  const [monitorNotice, setMonitorNotice] = useState("");
  const [monitorConfig, setMonitorConfig] = useState<ProviderMonitorConfig>(defaultMonitorConfig);

  // Any provider can back Claude Desktop now that the proxy converts protocols.
  const claudeProviders = useMemo(() => providers.data ?? [], [providers.data]);
  const selectedClaudeProviderData = useMemo(
    () => claudeProviders.find((provider) => provider.id === selectedClaudeProvider),
    [claudeProviders, selectedClaudeProvider]
  );

  useEffect(() => {
    const statusProvider = claude3p.data?.provider_id;
    const statusProviderValid = Boolean(statusProvider && claudeProviders.some((provider) => provider.id === statusProvider));
    const selectedValid = Boolean(
      selectedClaudeProvider && claudeProviders.some((provider) => provider.id === selectedClaudeProvider)
    );
    if (statusProvider && statusProviderValid && selectedClaudeProvider !== statusProvider) {
      setSelectedClaudeProvider(statusProvider);
    } else if (!selectedValid) {
      setSelectedClaudeProvider(claudeProviders[0]?.id ?? "");
    }
  }, [claude3p.data?.provider_id, claudeProviders, selectedClaudeProvider]);

  useEffect(() => {
    const modelList = claudeDesktopModelList(selectedClaudeProviderData);
    setClaudeModelMapping(modelList.length > 0);
    setClaudeModelList(modelList);
  }, [selectedClaudeProviderData]);

  useEffect(() => {
    if (monitor.data?.config) setMonitorConfig(monitor.data.config);
  }, [monitor.data?.config]);

  usePolling(monitor.reload, 30_000, { enabled: monitorConfig.enabled });

  async function saveClaudeModelMapping() {
    if (!selectedClaudeProviderData) return;
    setBusy("claude-models");
    setNotice("");
    try {
      const meta = { ...(selectedClaudeProviderData.meta ?? {}) };
      if (claudeModelMapping && claudeModelList.trim()) {
        meta.claude_desktop_models = parseClaudeDesktopModelList(claudeModelList);
      } else {
        delete meta.claude_desktop_models;
      }
      await api.upsertProvider({ ...selectedClaudeProviderData, meta });
      if (claude3p.data?.enabled && claude3p.data.provider_id === selectedClaudeProviderData.id) {
        await api.setClaude3p(true, selectedClaudeProviderData.id);
      }
      setNotice(t("providers.modelMappingSaved"));
      claude3p.reload();
      providers.reload();
    } catch (error) {
      setNotice(error instanceof Error ? error.message : String(error));
    } finally {
      setBusy(null);
    }
  }

  async function setClaude3p(enabled: boolean) {
    setBusy("claude3p");
    setNotice("");
    try {
      const status = await api.setClaude3p(enabled, enabled ? selectedClaudeProvider : "");
      setNotice(
        status.backup_path
          ? `${status.message ?? ""} ${t("providers.backupPath")}: ${status.backup_path}`.trim()
          : status.message ?? ""
      );
      claude3p.reload();
      providers.reload();
    } catch (error) {
      setNotice(error instanceof Error ? error.message : String(error));
    } finally {
      setBusy(null);
    }
  }

  async function saveMonitorConfig() {
    setBusy("provider-monitor-save");
    setMonitorNotice("");
    try {
      await api.saveProviderMonitor(monitorConfig);
      setMonitorNotice(t("providers.monitorSaved"));
      await monitor.reload();
    } catch (error) {
      setMonitorNotice(error instanceof Error ? error.message : String(error));
    } finally {
      setBusy(null);
    }
  }

  async function runProviderMonitor() {
    setBusy("provider-monitor-run");
    setMonitorNotice("");
    try {
      await api.runProviderMonitor();
      setMonitorNotice(t("providers.monitorRunComplete"));
      await Promise.all([monitor.reload(), providers.reload()]);
    } catch (error) {
      setMonitorNotice(error instanceof Error ? error.message : String(error));
      await monitor.reload();
    } finally {
      setBusy(null);
    }
  }

  async function dismissMonitorAlert(id = "") {
    setBusy(id ? `provider-monitor-dismiss:${id}` : "provider-monitor-dismiss-all");
    try {
      await api.dismissProviderMonitorAlert(id);
      await monitor.reload();
    } finally {
      setBusy(null);
    }
  }

  function monitorAlertText(alert: ProviderMonitorAlert) {
    const models = alert.models?.join(", ") || alert.model || "—";
    switch (alert.type) {
      case "new_models":
        return t("providers.monitorNewModels", { provider: alert.provider_name, models });
      case "removed_models":
        return t("providers.monitorRemovedModels", { provider: alert.provider_name, models });
      case "model_error":
        return t("providers.monitorModelError", {
          provider: alert.provider_name,
          model: alert.model || "—",
          error: alert.message || "—",
        });
      default:
        return t("providers.monitorProviderError", {
          provider: alert.provider_name,
          error: alert.message || "—",
        });
    }
  }

  function monitorStateLabel(state: string) {
    const key = `providers.monitorState.${state}`;
    const label = t(key);
    return label === key ? state : label;
  }

  const claudeStatus = claude3p.data;
  const canEnableClaude3p = selectedClaudeProvider !== "" && busy !== "claude3p";
  const canSaveClaudeModels =
    Boolean(selectedClaudeProviderData) &&
    busy !== "claude-models" &&
    (!claudeModelMapping || Boolean(claudeModelList.trim()));

  return (
    <div className="page-stack">
      <section className="surface provider-monitor">
        <div className="surface-header">
          <div>
            <h2>{t("providers.monitorTitle")}</h2>
            <p className="subtle-copy">{t("providers.monitorSubtitle")}</p>
          </div>
          <span className={monitorConfig.enabled ? "status-badge success" : "status-badge"}>
            <span className="status-dot" />
            {monitorConfig.enabled ? t("common.enabled") : t("common.disabled")}
          </span>
        </div>

        <div className="surface-body provider-monitor-body">
          {(monitor.data?.alerts ?? []).length > 0 && (
            <div className="provider-monitor-alerts">
              <div className="provider-monitor-alert-head">
                <span>
                  <Bell size={15} />
                  <strong>
                    {t("providers.monitorAlerts", { count: monitor.data?.alerts.length ?? 0 })}
                  </strong>
                </span>
                <button
                  className="ghost-action"
                  disabled={busy === "provider-monitor-dismiss-all"}
                  onClick={() => dismissMonitorAlert()}
                >
                  {t("providers.monitorDismissAll")}
                </button>
              </div>
              {(monitor.data?.alerts ?? []).map((alert) => (
                <div className={`provider-monitor-alert ${alert.severity === "error" ? "error" : "warning"}`} key={alert.id}>
                  {alert.severity === "error" ? <AlertTriangle size={16} /> : <Bell size={16} />}
                  <span>
                    <strong>{monitorAlertText(alert)}</strong>
                    <small>{formatMonitorTime(alert.created_at)}</small>
                  </span>
                  <button
                    className="ghost-action icon-only"
                    disabled={busy === `provider-monitor-dismiss:${alert.id}`}
                    title={t("providers.monitorDismiss")}
                    onClick={() => dismissMonitorAlert(alert.id)}
                  >
                    <X size={14} />
                  </button>
                </div>
              ))}
            </div>
          )}

          <div className="provider-monitor-settings">
            <label className="switch-row provider-monitor-switch">
              <span>
                <strong>{t("providers.monitorEnabled")}</strong>
                <small>{t("providers.monitorEnabledHint")}</small>
              </span>
              <input
                type="checkbox"
                checked={monitorConfig.enabled}
                onChange={(event) => setMonitorConfig((current) => ({ ...current, enabled: event.target.checked }))}
              />
            </label>
            <label className="field">
              <span>{t("providers.monitorInterval")}</span>
              <input
                type="number"
                min={15}
                max={10080}
                value={monitorConfig.interval_minutes}
                onChange={(event) =>
                  setMonitorConfig((current) => ({
                    ...current,
                    interval_minutes: Number(event.target.value),
                  }))
                }
              />
              <small>{t("providers.monitorIntervalHint")}</small>
            </label>
            <label className="field">
              <span>{t("providers.monitorMaxModels")}</span>
              <input
                type="number"
                min={1}
                max={100}
                value={monitorConfig.max_models_per_provider}
                onChange={(event) =>
                  setMonitorConfig((current) => ({
                    ...current,
                    max_models_per_provider: Number(event.target.value),
                  }))
                }
              />
              <small>{t("providers.monitorMaxModelsHint")}</small>
            </label>
            <label className="switch-row provider-monitor-switch">
              <span>
                <strong>{t("providers.monitorProbeModels")}</strong>
                <small>{t("providers.monitorProbeModelsHint")}</small>
              </span>
              <input
                type="checkbox"
                checked={monitorConfig.probe_models}
                onChange={(event) =>
                  setMonitorConfig((current) => ({ ...current, probe_models: event.target.checked }))
                }
              />
            </label>
          </div>

          <div className="provider-monitor-actions">
            <span className="muted">
              <Clock size={14} />
              {t("providers.monitorLastRun")}: {formatMonitorTime(monitor.data?.last_run_at)}
              {monitor.data?.next_run_at
                ? ` · ${t("providers.monitorNextRun")}: ${formatMonitorTime(monitor.data.next_run_at)}`
                : ""}
            </span>
            <div className="table-actions">
              <button
                className="ghost-action"
                disabled={busy === "provider-monitor-save"}
                onClick={saveMonitorConfig}
              >
                <Save size={15} />
                {t("common.save")}
              </button>
              <button
                className="action"
                disabled={busy === "provider-monitor-run" || monitor.data?.running}
                onClick={runProviderMonitor}
              >
                <RefreshCw size={15} className={busy === "provider-monitor-run" ? "spin" : ""} />
                {monitor.data?.running ? t("providers.monitorRunning") : t("providers.monitorRunNow")}
              </button>
            </div>
          </div>

          {(monitorNotice || monitor.error) && (
            <div className={`probe-message ${monitor.error ? "error" : "success"}`}>
              {monitorNotice || monitor.error}
            </div>
          )}

          {(monitor.data?.providers ?? []).length > 0 && (
            <div className="provider-monitor-status-grid">
              {(monitor.data?.providers ?? []).map((status) => (
                <div className="provider-monitor-status" key={status.provider_id}>
                  <span className="provider-monitor-status-icon">
                    {status.state === "healthy" ? <CheckCircle2 size={17} /> : <AlertTriangle size={17} />}
                  </span>
                  <span>
                    <strong>{status.provider_name}</strong>
                    <small>
                      {t("providers.monitorCatalogCount", { count: status.catalog_count })}
                      {status.checked_models > 0
                        ? ` · ${t("providers.monitorHealthCount", {
                            healthy: status.healthy_models,
                            total: status.checked_models,
                          })}`
                        : ""}
                    </small>
                    {status.message && <small title={status.message}>{status.message}</small>}
                  </span>
                  <span className={monitorBadgeClass(status.state)}>{monitorStateLabel(status.state)}</span>
                </div>
              ))}
            </div>
          )}
        </div>
      </section>

      <section className="surface claude3p-control">
        <div className="surface-header">
          <div>
            <h2>{t("providers.claude3p")}</h2>
            <p className="subtle-copy">{t("providers.claude3pSubtitle")}</p>
          </div>
          <span className={claudeStatus?.enabled ? "status-badge success" : "status-badge"}>
            <span className="status-dot" />
            {claudeStatus?.enabled ? t("common.enabled") : t("common.disabled")}
          </span>
        </div>
        <div className="surface-body claude3p-grid">
          <div className="claude3p-state">
            <div>
              <span>{t("providers.activeProfile")}</span>
              <strong>{claudeStatus?.active_profile_name || claudeStatus?.active_profile_id || "—"}</strong>
            </div>
            <div>
              <span>{t("providers.configDir")}</span>
              <strong className="mono">{claudeStatus?.config_dir || "—"}</strong>
            </div>
            <div>
              <span>{t("providers.baseUrl")}</span>
              <strong className="mono">{claudeStatus?.base_url || "—"}</strong>
            </div>
            <div>
              <span>{t("providers.modelCount")}</span>
              <strong>{claudeStatus?.model_count ?? 0}</strong>
            </div>
          </div>

          <div className="claude3p-actions">
            <label className="field">
              <span>{t("providers.selectedProvider")}</span>
              <select
                value={selectedClaudeProvider}
                onChange={(event) => setSelectedClaudeProvider(event.target.value)}
                disabled={busy === "claude3p" || claudeProviders.length === 0}
              >
                {claudeProviders.map((provider) => (
                  <option key={provider.id} value={provider.id}>
                    {provider.name}
                  </option>
                ))}
                {claudeProviders.length === 0 && <option value="">{t("providers.noClaude3pProvider")}</option>}
              </select>
            </label>
            <div className="claude3p-model-config">
              <label className="switch-row">
                <span>
                  <strong>{t("providers.modelMapping")}</strong>
                  <small>{t("providers.modelMappingHint")}</small>
                </span>
                <input
                  type="checkbox"
                  checked={claudeModelMapping}
                  disabled={!selectedClaudeProviderData || busy === "claude-models"}
                  onChange={(event) => {
                    const checked = event.target.checked;
                    setClaudeModelMapping(checked);
                    if (checked && !claudeModelList.trim()) {
                      setClaudeModelList(selectedClaudeProviderData?.model || "");
                    }
                  }}
                />
              </label>
              {claudeModelMapping && (
                <label className="field">
                  <span>{t("providers.modelList")}</span>
                  <textarea
                    value={claudeModelList}
                    onChange={(event) => setClaudeModelList(event.target.value)}
                    placeholder={"claude-sonnet-5\nclaude-opus-4-8"}
                    rows={3}
                    disabled={busy === "claude-models"}
                  />
                </label>
              )}
              <button className="ghost-action" onClick={saveClaudeModelMapping} disabled={!canSaveClaudeModels}>
                <Save size={15} />
                {t("providers.saveModelMapping")}
              </button>
            </div>
            <div className="table-actions">
              <button className="ghost-action" onClick={claude3p.reload} disabled={busy === "claude3p"}>
                <RefreshCw size={15} />
                {t("common.refresh")}
              </button>
              <button className="action" onClick={() => setClaude3p(true)} disabled={!canEnableClaude3p}>
                <Power size={15} />
                {t("providers.enableClaude3p")}
              </button>
              <button
                className="ghost-action"
                onClick={() => setClaude3p(false)}
                disabled={busy === "claude3p" || (!claudeStatus?.enabled && !claudeStatus?.configured)}
              >
                <PowerOff size={15} />
                {t("providers.disableClaude3p")}
              </button>
            </div>
            {(notice || claude3p.error) && <div className="probe-message">{notice || claude3p.error}</div>}
          </div>
        </div>
      </section>

    </div>
  );
}
