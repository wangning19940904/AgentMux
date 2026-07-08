import { useEffect, useMemo, useState } from "react";
import { Power, PowerOff, RefreshCw, Save } from "lucide-react";
import { api, Provider } from "../api";
import { useI18n } from "../i18n";
import { useAsync } from "../useAsync";

// providerProtocol reports the upstream wire protocol a provider speaks. With
// the unified translation layer a provider only declares its own protocol; the
// proxy adapts it to whatever tool routes to it.
function providerProtocol(provider: Provider): string {
  const format = provider.meta?.api_format;
  return typeof format === "string" && format ? format : "anthropic";
}

// defaultToolForProvider picks the natural tool entry for a provider's protocol
// so the one-click switch button has a sensible target. Any tool can still be
// routed to any provider via the routing UI.
function defaultToolForProvider(provider: Provider): string {
  switch (providerProtocol(provider)) {
    case "openai_chat":
    case "openai_responses":
      return "codex";
    case "gemini":
    case "gemini_native":
      return "gemini";
    default:
      return "claudecode";
  }
}

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

export function ProvidersPanel() {
  const { t } = useI18n();
  const providers = useAsync(() => api.providers(), []);
  const presets = useAsync(() => api.presets(), []);
  const claude3p = useAsync(() => api.claude3pStatus(), []);
  const [busy, setBusy] = useState<string | null>(null);
  const [selectedClaudeProvider, setSelectedClaudeProvider] = useState("");
  const [claudeModelMapping, setClaudeModelMapping] = useState(false);
  const [claudeModelList, setClaudeModelList] = useState("");
  const [notice, setNotice] = useState("");

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

  async function importPreset(p: Provider) {
    setBusy(p.id);
    try {
      await api.upsertProvider(p);
      providers.reload();
    } finally {
      setBusy(null);
    }
  }

  async function switchTo(p: Provider) {
    const tool = defaultToolForProvider(p);
    setBusy(p.id);
    try {
      await api.switchProvider(p.id, tool);
      claude3p.reload();
      providers.reload();
    } finally {
      setBusy(null);
    }
  }

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

  const claudeStatus = claude3p.data;
  const canEnableClaude3p = selectedClaudeProvider !== "" && busy !== "claude3p";
  const canSaveClaudeModels =
    Boolean(selectedClaudeProviderData) &&
    busy !== "claude-models" &&
    (!claudeModelMapping || Boolean(claudeModelList.trim()));

  return (
    <div className="page-stack">
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

      <section className="surface">
        <div className="surface-header">
          <h2>{t("providers.configured")}</h2>
          <span className="pill on">{providers.data?.length ?? 0}</span>
        </div>
        {providers.error && <div className="surface-body error">{providers.error}</div>}
        <div className="table-wrap">
          <table>
            <thead>
              <tr>
                <th>{t("common.name")}</th>
                <th>{t("providers.protocol")}</th>
                <th>{t("providers.model")}</th>
                <th>{t("common.status")}</th>
                <th></th>
              </tr>
            </thead>
            <tbody>
              {(providers.data ?? []).map((p) => (
                <tr key={p.id}>
                  <td>
                    <span className="provider-name">
                      <span className="provider-icon">{p.name.slice(0, 1).toUpperCase()}</span>
                      {p.name}
                    </span>
                  </td>
                  <td>
                    <span className="pill">{providerProtocol(p)}</span>
                  </td>
                  <td className="muted">{p.model || "—"}</td>
                  <td>
                    {p.enabled ? (
                      <span className="status-badge success">
                        <span className="status-dot" />
                        {t("common.enabled")}
                      </span>
                    ) : (
                      <span className="status-badge">
                        <span className="status-dot" />
                        {t("common.idle")}
                      </span>
                    )}
                  </td>
                  <td>
                    <button className="action" disabled={busy === p.id} onClick={() => switchTo(p)}>
                      {t("providers.switch")}
                    </button>
                  </td>
                </tr>
              ))}
              {providers.data?.length === 0 && (
                <tr>
                  <td colSpan={5} className="empty-state">
                    {t("providers.empty")}
                  </td>
                </tr>
              )}
            </tbody>
          </table>
        </div>
      </section>

      <section className="surface">
        <div className="surface-header">
          <h2>{t("providers.presets")}</h2>
          <span className="pill">{presets.data?.length ?? 0}</span>
        </div>
        <div className="table-wrap">
          <table>
            <thead>
              <tr>
                <th>{t("common.name")}</th>
                <th>{t("providers.protocol")}</th>
                <th>{t("providers.baseUrl")}</th>
                <th></th>
              </tr>
            </thead>
            <tbody>
              {(presets.data ?? []).map((p) => (
                <tr key={p.id}>
                  <td>
                    <span className="provider-name">
                      <span className="provider-icon">{p.name.slice(0, 1).toUpperCase()}</span>
                      {p.name}
                    </span>
                  </td>
                  <td>
                    <span className="pill">{providerProtocol(p)}</span>
                  </td>
                  <td className="muted mono">{p.base_url}</td>
                  <td>
                    <button className="action" disabled={busy === p.id} onClick={() => importPreset(p)}>
                      {t("providers.import")}
                    </button>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      </section>
    </div>
  );
}
