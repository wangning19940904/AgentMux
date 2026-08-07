import { useEffect, useMemo, useState } from "react";
import {
  CheckCircle2,
  ChevronDown,
  DatabaseZap,
  Plus,
  PlugZap,
  PowerOff,
  Save,
  Settings2,
  Trash2,
  Workflow,
  X,
} from "lucide-react";
import { api, Provider, ProviderRoute, ProxyToolConfig } from "../../api";
import { useI18n } from "../../i18n";
import { useAsync } from "../../useAsync";
import {
  CLAUDE_CODE_TIER_ROWS,
  CLAUDE_DESKTOP_ROUTE_MODELS,
  CURATED_PRESET_IDS,
  DEFAULT_ROUTE_TOOLS,
  LocalRouteMode,
  ModelMappingRow,
  PROVIDER_MODEL_COLLAPSE_THRESHOLD,
  PROVIDER_MODEL_PREVIEW_COUNT,
  ProbeCapabilities,
  ProviderDraft,
  RouteDraft,
  capabilityName,
  claudeDesktopModelIDs,
  desktopProxyModelListForModels,
  draftToProvider,
  emptyDraft,
  emptyRouteDraft,
  inferredToolsForDraft,
  localRoutingTool,
  metaStringArray,
  modelRowsToList,
  normalizeTool,
  okCheckNames,
  parseModelRows,
  providerProtocol,
  providerSupportedModels,
  providerSupportedProtocols,
  providerSupportsRouteTool,
  providerToDraft,
  repairClaudeDesktopModelRows,
  routeDraftToMeta,
  routeMetaDraftForTool,
  routeToolsForCapability,
  routeToolsForProvider,
  sortTools,
  supportsLocalRouting,
  toolLabel,
  toolsForDraft,
  uniqueValues,
} from "./providerUtils";
import { CapabilityBadges, ProviderMark } from "./ProviderMark";

export function GatewayPanel() {
  const { t } = useI18n();
  const agents = useAsync(() => api.agents(), []);
  const frameworks = useAsync(() => api.frameworks(), []);
  const providers = useAsync(() => api.providers(), []);
  const presets = useAsync(() => api.presets(), []);
  const activeRoutes = useAsync(() => api.activeRoutes(), []);
  const proxyStatus = useAsync(() => api.proxyStatus(), []);
  const [activeTab, setActiveTab] = useState<"providers" | "routing">("providers");
  const [providerFormOpen, setProviderFormOpen] = useState(false);
  const [routeFormOpen, setRouteFormOpen] = useState(false);
  const [busy, setBusy] = useState<string | null>(null);
  const [notice, setNotice] = useState("");
  const [probeNotice, setProbeNotice] = useState<{ kind: "success" | "error"; text: string } | null>(null);
  const [modelOptions, setModelOptions] = useState<string[]>([]);
  const [probeCapabilities, setProbeCapabilities] = useState<ProbeCapabilities | null>(null);
  const [draft, setDraft] = useState<ProviderDraft>(emptyDraft);
  const [routeDraft, setRouteDraft] = useState<RouteDraft>(emptyRouteDraft);
  const [expandedProviderModels, setExpandedProviderModels] = useState<Set<string>>(() => new Set());

  const providerList = providers.data ?? [];
  const presetList = presets.data ?? [];
  const routeList = activeRoutes.data ?? [];
  const selectedPreset = draft.id || "custom";
  const customSelected = !draft.id || (draft.category === "custom" && !CURATED_PRESET_IDS.includes(draft.id));
  const keyStatusLabel = (item?: { api_key_env?: string; api_key_available?: boolean }) => {
    if (!item?.api_key_env) return t("gateway.keyMissing");
    return item.api_key_available ? item.api_key_env : t("gateway.keyNotLoaded");
  };

  const curatedPresets = useMemo(() => {
    const byID = new Map(presetList.map((provider) => [provider.id, provider]));
    return CURATED_PRESET_IDS.map((id) => byID.get(id)).filter((provider): provider is Provider => Boolean(provider));
  }, [presetList]);

  const routeByTool = useMemo(() => {
    const next = new Map<string, ProviderRoute>();
    routeList.forEach((route) => next.set(normalizeTool(route.tool), route));
    return next;
  }, [routeList]);

  const proxyConfigByTool = useMemo(() => {
    const next = new Map<string, ProxyToolConfig>();
    (proxyStatus.data?.tools ?? []).forEach((cfg) => next.set(localRoutingTool(cfg.tool), cfg));
    return next;
  }, [proxyStatus.data]);

  const tools = useMemo(() => {
    const names = new Set<string>(DEFAULT_ROUTE_TOOLS);
    (agents.data ?? []).forEach((tool) => routeToolsForCapability(tool).forEach((routeTool) => names.add(routeTool)));
    providerList.forEach((provider) =>
      routeToolsForProvider(provider).forEach((tool) => routeToolsForCapability(tool).forEach((routeTool) => names.add(routeTool)))
    );
    curatedPresets.forEach((provider) =>
      routeToolsForProvider(provider).forEach((tool) => routeToolsForCapability(tool).forEach((routeTool) => names.add(routeTool)))
    );
    return Array.from(names).sort(sortTools);
  }, [agents.data, curatedPresets, providerList]);

  const routeTools = useMemo(() => {
    const names = new Set<string>(DEFAULT_ROUTE_TOOLS);
    (agents.data ?? []).forEach((tool) => routeToolsForCapability(tool).forEach((routeTool) => names.add(routeTool)));
    routeList.forEach((route) => names.add(normalizeTool(route.tool)));
    tools.forEach((tool) => names.add(tool));
    return Array.from(names).sort(sortTools);
  }, [agents.data, routeList, tools]);

  // uninstalledTools maps a route tool to true when its backing framework is in
  // the catalog but not installed, so it can be shown disabled in the selector.
  const uninstalledTools = useMemo(() => {
    const set = new Set<string>();
    (frameworks.data?.frameworks ?? []).forEach((fw) => {
      if (!fw.installed) set.add(normalizeTool(fw.spec.kind));
    });
    return set;
  }, [frameworks.data]);

  useEffect(() => {
    if (!providerFormOpen && !routeFormOpen) return;
    const previousOverflow = document.body.style.overflow;
    const handleKeyDown = (event: KeyboardEvent) => {
      if (event.key === "Escape") {
        setProviderFormOpen(false);
        setRouteFormOpen(false);
      }
    };
    document.body.style.overflow = "hidden";
    window.addEventListener("keydown", handleKeyDown);
    return () => {
      document.body.style.overflow = previousOverflow;
      window.removeEventListener("keydown", handleKeyDown);
    };
  }, [providerFormOpen, routeFormOpen]);

  function reloadProviders() {
    providers.reload();
    activeRoutes.reload();
    proxyStatus.reload();
  }

  function toggleProviderModels(providerID: string) {
    setExpandedProviderModels((current) => {
      const next = new Set(current);
      if (next.has(providerID)) {
        next.delete(providerID);
      } else {
        next.add(providerID);
      }
      return next;
    });
  }

  async function toggleTakeover(tool: string, enabled: boolean) {
    setBusy(`takeover:${tool}`);
    setNotice("");
    try {
      await api.setTakeover(tool, enabled);
      setNotice(t("gateway.takeoverUpdated"));
      reloadProviders();
    } catch (error) {
      setNotice(error instanceof Error ? error.message : String(error));
    } finally {
      setBusy(null);
    }
  }

  async function toggleAutoFailover(cfg: { tool: string; auto_failover: boolean }) {
    setBusy(`auto-failover:${cfg.tool}`);
    setNotice("");
    try {
      await api.setProxyToolConfig({ tool: cfg.tool, auto_failover: !cfg.auto_failover });
      reloadProviders();
    } catch (error) {
      setNotice(error instanceof Error ? error.message : String(error));
    } finally {
      setBusy(null);
    }
  }

  async function toggleFailoverQueue(provider: Provider) {
    setBusy(`failover:${provider.id}`);
    setNotice("");
    try {
      await api.setFailoverQueue(provider.id, !provider.in_failover_queue, provider.sort_index ?? 0);
      reloadProviders();
    } catch (error) {
      setNotice(error instanceof Error ? error.message : String(error));
    } finally {
      setBusy(null);
    }
  }

  function resetProviderDraft() {
    setNotice("");
    setProbeNotice(null);
    setModelOptions([]);
    setProbeCapabilities(null);
    setDraft(emptyDraft);
  }

  function updateDraft<K extends keyof ProviderDraft>(key: K, value: ProviderDraft[K]) {
    setDraft((current) => ({ ...current, [key]: value }));
  }

  function providersForTool(tool: string) {
    return providerList.filter((provider) => providerSupportsRouteTool(provider, tool));
  }

  function routeDraftForTool(tool: string, providerID = "", originalTool?: string): RouteDraft {
    const routeTool = normalizeTool(tool);
    const candidates = providersForTool(routeTool);
    const compatibleProviderID = candidates.some((provider) => provider.id === providerID) ? providerID : "";
    const selectedProviderID = compatibleProviderID || candidates[0]?.id || "";
    const currentRoute = routeByTool.get(normalizeTool(originalTool || routeTool));
    const provider = providerList.find((item) => item.id === selectedProviderID);
    const metaSource = currentRoute && currentRoute.provider_id === selectedProviderID ? currentRoute.meta : provider?.meta;
    const routeMetaDraft = routeMetaDraftForTool(provider, routeTool, metaSource);
    const localTool = localRoutingTool(routeTool);
    const localCfg = proxyConfigByTool.get(localTool);
    const supportsTakeover = supportsLocalRouting(routeTool);
    let localMode: LocalRouteMode = supportsTakeover ? "takeover" : "direct";
    if (currentRoute && supportsTakeover) {
      localMode = localCfg?.enabled ? "takeover" : "direct";
    }
    if (routeTool === "claude-desktop" && ["direct", "official"].includes(routeMetaDraft.claude_desktop_mode)) {
      localMode = "direct";
    }
    if (routeTool === "claude-desktop" && routeMetaDraft.claude_desktop_mode === "proxy") {
      localMode = "takeover";
    }
    return {
      tool: routeTool,
      provider_id: selectedProviderID,
      original_tool: originalTool,
      local_mode: localMode,
      ...routeMetaDraft,
    };
  }

  function openNewRoute() {
    const firstUnrouted = routeTools.find((tool) => !routeByTool.has(normalizeTool(tool)));
    const tool = firstUnrouted || routeTools[0] || tools[0] || "";
    setNotice("");
    setRouteDraft(routeDraftForTool(tool));
    setActiveTab("routing");
    setProviderFormOpen(false);
    setRouteFormOpen(true);
  }

  function editRoute(route: ProviderRoute) {
    setNotice("");
    setRouteDraft(routeDraftForTool(route.tool, route.provider_id, route.tool));
    setActiveTab("routing");
    setProviderFormOpen(false);
    setRouteFormOpen(true);
  }

  function updateRouteTool(tool: string) {
    setRouteDraft((current) => routeDraftForTool(tool, current.provider_id, current.original_tool));
  }

  function updateRouteProvider(providerID: string) {
    const provider = providerList.find((item) => item.id === providerID);
    setRouteDraft((current) => {
      const routeMetaDraft = routeMetaDraftForTool(provider, current.tool, provider?.meta);
      let localMode = current.local_mode;
      if (current.tool === "claude-desktop" && routeMetaDraft.claude_desktop_mode === "proxy") {
        localMode = "takeover";
      } else if (current.tool === "claude-desktop" && ["direct", "official"].includes(routeMetaDraft.claude_desktop_mode)) {
        localMode = "direct";
      } else if (supportsLocalRouting(current.tool) && !current.original_tool) {
        localMode = "takeover";
      }
      return {
        ...current,
        provider_id: providerID,
        local_mode: localMode,
        ...routeMetaDraft,
      };
    });
  }

  function updateRouteDraft<K extends keyof RouteDraft>(key: K, value: RouteDraft[K]) {
    setRouteDraft((current) => ({ ...current, [key]: value }));
  }

  function openNewProvider() {
    resetProviderDraft();
    setActiveTab("providers");
    setRouteFormOpen(false);
    setProviderFormOpen(true);
  }

  function editProvider(provider: Provider) {
    setNotice("");
    setProbeNotice(null);
    setModelOptions([]);
    setProbeCapabilities(null);
    setDraft(providerToDraft(provider));
    setActiveTab("providers");
    setRouteFormOpen(false);
    setProviderFormOpen(true);
  }

  function openRouteProvider(provider?: Provider) {
    if (!provider) return;
    editProvider(provider);
  }

  function selectPreset(provider: Provider) {
    setNotice("");
    setProbeNotice(null);
    setModelOptions([]);
    setProbeCapabilities(null);
    setDraft(providerToDraft(provider));
    setProviderFormOpen(true);
  }

  async function probeProvider() {
    const provider = draftToProvider(draft, providerList);
    setBusy("probe-provider");
    setProbeNotice(null);
    try {
      const result = await api.probeProvider(provider);
      const models = result.models ?? [];
      setModelOptions(models);
      const supportedAPIFormats = okCheckNames(result.formats ?? []);
      const supportedProtocols = okCheckNames(result.protocols ?? []);
      const probedDraft: ProviderDraft = {
        ...draft,
        api_format: result.api_format || draft.api_format,
        codex_wire_api: result.codex_wire_api || draft.codex_wire_api,
        supported_models: models,
        supported_api_formats: supportedAPIFormats,
        supported_protocols: supportedProtocols,
      };
      const inferredTools = inferredToolsForDraft(probedDraft);
      setProbeCapabilities({
        formats: result.formats ?? [],
        protocols: result.protocols ?? [],
        inferredTools,
        apiFormat: result.api_format,
        codexWireAPI: result.codex_wire_api,
      });
      if (models.length > 0) {
        setDraft((current) => ({
          ...current,
          api_format: result.api_format || current.api_format,
          codex_wire_api: result.codex_wire_api || current.codex_wire_api,
          model: current.model.trim() && models.includes(current.model.trim()) ? current.model : models[0],
          model_list: current.manual_models ? desktopProxyModelListForModels(models) : current.model_list,
          tools: inferredTools.length > 0 ? inferredTools : current.tools,
          supported_models: models,
          supported_api_formats: supportedAPIFormats,
          supported_protocols: supportedProtocols,
        }));
      } else if (result.api_format) {
        setDraft((current) => ({
          ...current,
          api_format: result.api_format || current.api_format,
          codex_wire_api: result.codex_wire_api || current.codex_wire_api,
          tools: inferredTools.length > 0 ? inferredTools : current.tools,
          supported_api_formats: supportedAPIFormats,
          supported_protocols: supportedProtocols,
        }));
      }
      setProbeNotice({
        kind: "success",
        text:
          models.length > 0
            ? t("gateway.modelsFetched").replace("{count}", String(models.length))
            : t("gateway.modelsFetchEmpty"),
      });
    } catch (error) {
      setProbeCapabilities(null);
      setProbeNotice({
        kind: "error",
        text: error instanceof Error ? error.message : String(error),
      });
    } finally {
      setBusy(null);
    }
  }

  async function saveProvider() {
    const provider = draftToProvider(draft, providerList);
    setBusy("save-provider");
    setNotice("");
    try {
      const saved = await api.upsertProvider(provider);
      setNotice(t("gateway.providerSaved"));
      setDraft(providerToDraft(saved));
      reloadProviders();
      setProviderFormOpen(false);
    } catch (error) {
      setNotice(error instanceof Error ? error.message : String(error));
    } finally {
      setBusy(null);
    }
  }

  async function deleteProvider(id: string) {
    setBusy(`delete:${id}`);
    setNotice("");
    try {
      await api.deleteProvider(id);
      if (draft.id === id) {
        setDraft(emptyDraft);
        setProviderFormOpen(false);
      }
      setNotice(t("gateway.providerDeleted"));
      reloadProviders();
    } catch (error) {
      setNotice(error instanceof Error ? error.message : String(error));
    } finally {
      setBusy(null);
    }
  }

  async function saveRoute() {
    const tool = routeDraft.tool;
    const providerID = routeDraft.provider_id;
    if (!tool || !providerID) return;
    setBusy(`save-route:${tool}`);
    setNotice("");
    try {
      const localTakeover = supportsLocalRouting(tool) ? routeDraft.local_mode === "takeover" : undefined;
      await api.switchProvider(providerID, tool, routeDraftToMeta(routeDraft), localTakeover);
      if (routeDraft.original_tool && routeDraft.original_tool !== tool) {
        await api.disableRoute(routeDraft.original_tool);
      }
      setNotice(t("gateway.routeSaved"));
      reloadProviders();
      setRouteFormOpen(false);
    } catch (error) {
      setNotice(error instanceof Error ? error.message : String(error));
    } finally {
      setBusy(null);
    }
  }

  async function disableRoute(tool: string) {
    setBusy(`disable-route:${tool}`);
    setNotice("");
    try {
      const localTool = localRoutingTool(tool);
      const localCfg = proxyConfigByTool.get(localTool);
      if (supportsLocalRouting(tool) && localCfg?.enabled) {
        await api.setTakeover(localTool, false);
      }
      await api.disableRoute(tool);
      setNotice(t("gateway.routeDisabled"));
      reloadProviders();
    } catch (error) {
      setNotice(error instanceof Error ? error.message : String(error));
    } finally {
      setBusy(null);
    }
  }

  // Official presets (e.g. Claude Desktop official login) restore built-in
  // behavior and legitimately have no base URL.
  const canSave = Boolean(
    draft.name.trim() && (draft.base_url.trim() || draft.category === "official") && toolsForDraft(draft).length > 0
  );
  const canProbe = Boolean(draft.base_url.trim() && (draft.api_key.trim() || draft.api_key_env.trim()));
  const routeCandidates = providersForTool(routeDraft.tool);
  const selectedRouteProvider = providerList.find((provider) => provider.id === routeDraft.provider_id);
  const canSaveRoute = Boolean(
    routeDraft.tool &&
      routeDraft.provider_id &&
      routeCandidates.some((provider) => provider.id === routeDraft.provider_id) &&
      busy !== `save-route:${routeDraft.tool}`
  );
  const activeProviderCount = providerList.filter((provider) => provider.enabled).length;
  const configuredKeyCount = providerList.filter((provider) => provider.api_key_available).length;

  function renderProviderForm() {
    if (!providerFormOpen) return null;
    return (
      <div className="provider-drawer-layer">
        <button
          className="provider-drawer-backdrop"
          type="button"
          aria-label={t("common.close")}
          onClick={() => setProviderFormOpen(false)}
        />
        <aside
          className="provider-drawer provider-builder"
          role="dialog"
          aria-modal="true"
          aria-labelledby="provider-drawer-title"
        >
          <div className="provider-builder-head">
            <div className="provider-form-title">
              <ProviderMark id={selectedPreset} name={draft.name} size="large" custom={customSelected && !draft.id} />
              <div>
                <h2 id="provider-drawer-title">{t("gateway.addProvider")}</h2>
                <span className="muted">{t("gateway.addProviderSubtitle")}</span>
              </div>
            </div>
            <div className="table-actions">
              <span className="pill">
                {curatedPresets.length} {t("gateway.curated")}
              </span>
              <button
                className="ghost-action icon-action"
                onClick={() => setProviderFormOpen(false)}
                title={t("common.close")}
              >
                <X size={15} />
              </button>
            </div>
          </div>

          <div className="surface-body provider-builder-body">
            {presets.error && <div className="error">{presets.error}</div>}
            <div className="preset-strip">
              <button className={customSelected ? "preset-tile selected" : "preset-tile"} onClick={openNewProvider}>
                <ProviderMark id="custom" size="large" custom />
                <strong>{t("gateway.customProvider")}</strong>
                <span>{t("gateway.customProviderHint")}</span>
              </button>

              {curatedPresets.map((provider) => (
                <button
                  className={selectedPreset === provider.id ? "preset-tile selected" : "preset-tile"}
                  key={provider.id}
                  onClick={() => selectPreset(provider)}
                >
                  <ProviderMark id={provider.id} name={provider.name} size="large" />
                  <strong>{provider.name.replace(" (Official)", "")}</strong>
                  <span>{provider.model || provider.api_key_env || provider.base_url}</span>
                </button>
              ))}
            </div>

            <div className="provider-form-shell">
              {notice && <div className="session-notice">{notice}</div>}

              <div className="provider-form">
                <div className="field-grid">
                  <label className="field">
                    <span>{t("gateway.providerName")}</span>
                    <input
                      value={draft.name}
                      onChange={(event) => updateDraft("name", event.target.value)}
                      placeholder={t("gateway.providerNamePlaceholder")}
                    />
                  </label>
                  <label className="field">
                    <span>{t("gateway.note")}</span>
                    <input
                      value={draft.note}
                      onChange={(event) => updateDraft("note", event.target.value)}
                      placeholder={t("gateway.notePlaceholder")}
                    />
                  </label>
                  <label className="field">
                    <span>{t("gateway.category")}</span>
                    <select value={draft.category} onChange={(event) => updateDraft("category", event.target.value)}>
                      <option value="official">{t("gateway.categoryOfficial")}</option>
                      <option value="third_party">{t("gateway.categoryThirdParty")}</option>
                      <option value="custom">{t("gateway.categoryCustom")}</option>
                    </select>
                  </label>
                  <label className="field wide">
                    <span>{t("gateway.website")}</span>
                    <input
                      value={draft.website}
                      onChange={(event) => updateDraft("website", event.target.value)}
                      placeholder="https://example.com"
                    />
                  </label>
                  <label className="field wide">
                    <span>{t("gateway.apiKey")}</span>
                    <input
                      type="password"
                      value={draft.api_key}
                      onChange={(event) => updateDraft("api_key", event.target.value)}
                      placeholder="sk-..."
                      autoComplete="new-password"
                      spellCheck={false}
                    />
                  </label>
                  <label className="field wide">
                    <span>{t("gateway.requestUrl")}</span>
                    <input
                      value={draft.base_url}
                      onChange={(event) => updateDraft("base_url", event.target.value)}
                      placeholder="https://api.openai.com/v1"
                    />
                  </label>
                  <label className="field">
                    <span>{t("providers.model")}</span>
                    {modelOptions.length > 0 ? (
                      <select value={draft.model} onChange={(event) => updateDraft("model", event.target.value)}>
                        {draft.model.trim() && !modelOptions.includes(draft.model.trim()) && (
                          <option value={draft.model}>{draft.model}</option>
                        )}
                        {modelOptions.map((model) => (
                          <option key={model} value={model}>
                            {model}
                          </option>
                        ))}
                      </select>
                    ) : (
                      <input
                        value={draft.model}
                        onChange={(event) => updateDraft("model", event.target.value)}
                        placeholder="gpt-5"
                      />
                    )}
                    {modelOptions.length > 0 && (
                      <small>{t("gateway.modelOptionsHint").replace("{count}", String(modelOptions.length))}</small>
                    )}
                  </label>
				  <label className="field">
					<span>{t("gateway.supportedReasoningEfforts")}</span>
					<input value={draft.supported_reasoning_efforts} onChange={(event) => updateDraft("supported_reasoning_efforts", event.target.value)} placeholder="low, medium, high" />
					<small>{t("gateway.runtimeCapabilityHint")}</small>
				  </label>
				  <label className="field">
					<span>{t("gateway.defaultReasoningEffort")}</span>
					<input value={draft.default_reasoning_effort} onChange={(event) => updateDraft("default_reasoning_effort", event.target.value)} placeholder="high" />
				  </label>
				  <label className="field">
					<span>{t("gateway.supportedServiceTiers")}</span>
					<input value={draft.supported_service_tiers} onChange={(event) => updateDraft("supported_service_tiers", event.target.value)} placeholder="default, priority" />
					<small>{t("gateway.runtimeCapabilityHint")}</small>
				  </label>
				  <label className="field">
					<span>{t("gateway.defaultServiceTier")}</span>
					<input value={draft.default_service_tier} onChange={(event) => updateDraft("default_service_tier", event.target.value)} placeholder="default" />
				  </label>
                </div>

                <div className="probe-row">
                  <button
                    className="ghost-action"
                    disabled={!canProbe || busy === "probe-provider"}
                    onClick={probeProvider}
                    type="button"
                  >
                    <PlugZap size={15} />
                    {t("gateway.testAndFetchModels")}
                  </button>
                  {probeNotice && <span className={`probe-message ${probeNotice.kind}`}>{probeNotice.text}</span>}
                </div>

              {probeCapabilities && (
                <div className="capability-panel">
                  <div className="capability-group">
                    <span className="capability-title">{t("gateway.detectedApiFormats")}</span>
                    <div className="capability-badges">
                      {probeCapabilities.formats.map((check) => (
                        <span
                          className={check.ok ? "status-badge success" : "status-badge warning"}
                          key={`${check.kind}:${check.name}`}
                          title={check.message}
                        >
                          {capabilityName(check.name)}
                        </span>
                      ))}
                    </div>
                  </div>
                  <div className="capability-group">
                    <span className="capability-title">{t("gateway.detectedProtocols")}</span>
                    <div className="capability-badges">
                      {probeCapabilities.protocols.map((check) => (
                        <span
                          className={check.ok ? "status-badge success" : "status-badge warning"}
                          key={`${check.kind}:${check.name}`}
                          title={check.message}
                        >
                          {capabilityName(check.name)}
                        </span>
                      ))}
                    </div>
                  </div>
                  {probeCapabilities.inferredTools.length > 0 && (
                    <div className="capability-group">
                      <span className="capability-title">{t("gateway.inferredRoutes")}</span>
                      <div className="capability-badges">
                        {probeCapabilities.inferredTools.map((tool) => (
                          <span className="status-badge success" key={tool}>
                            {toolLabel(tool)}
                          </span>
                        ))}
                      </div>
                    </div>
                  )}
                  {(probeCapabilities.apiFormat || probeCapabilities.codexWireAPI) && (
                    <span className="muted">
                      {[
                        probeCapabilities.apiFormat
                          ? `${t("gateway.detectedApiFormat")}: ${capabilityName(probeCapabilities.apiFormat)}`
                          : "",
                        probeCapabilities.codexWireAPI
                          ? `${t("gateway.detectedWireApi")}: ${probeCapabilities.codexWireAPI}`
                          : "",
                      ]
                        .filter(Boolean)
                        .join(" · ")}
                    </span>
                  )}
                </div>
              )}

              <div className="form-actions">
                <button className="ghost-action" onClick={() => setProviderFormOpen(false)}>
                  <X size={15} />
                  {t("common.close")}
                </button>
                <button className="action" disabled={!canSave || busy === "save-provider"} onClick={saveProvider}>
                  <Save size={15} />
                  {t("gateway.saveProvider")}
                </button>
              </div>
            </div>
            </div>
          </div>
        </aside>
      </div>
    );
  }

  function renderRouteForm() {
    if (!routeFormOpen) return null;
    const currentRoute = routeByTool.get(routeDraft.tool);
    const routeLocalTool = localRoutingTool(routeDraft.tool);
    const showClaudeRouteOptions = ["claudecode", "claude-desktop"].includes(routeLocalTool);
    const showClaudeDesktopRouteOptions = routeLocalTool === "claude-desktop";
    const routeSupportsTakeover = supportsLocalRouting(routeDraft.tool);
    const routeTakeoverSelected = Boolean(routeSupportsTakeover && routeDraft.local_mode === "takeover");
    const routeModelRows = repairClaudeDesktopModelRows(parseModelRows(routeDraft.model_list));
    const visibleRouteOptions = uniqueValues([
      ...CLAUDE_DESKTOP_ROUTE_MODELS,
      ...routeModelRows.map((row) => row.desktopModel),
      ...(selectedRouteProvider ? claudeDesktopModelIDs(selectedRouteProvider) : []),
    ]);
    const upstreamModelOptions = uniqueValues([
      selectedRouteProvider?.model || "",
      ...(selectedRouteProvider ? metaStringArray(selectedRouteProvider, "supported_models") : []),
      ...routeModelRows.map((row) => row.upstreamModel),
    ]);
    const displayRouteModelRows = routeModelRows.length > 0 ? routeModelRows : [{ desktopModel: "", upstreamModel: "" }];
    const isProxyDesktopMode = routeDraft.claude_desktop_mode === "proxy";
    const routeMappingHelp = showClaudeDesktopRouteOptions
      ? t("gateway.claudeDesktopMappingGuide")
      : t("gateway.claudeCodeMappingGuide");
    function setRouteModelRows(rows: ModelMappingRow[]) {
      const modelList = modelRowsToList(rows);
      setRouteDraft((current) => ({
        ...current,
        model_list: modelList,
        manual_models: modelList.trim().length > 0,
      }));
    }
    function updateRouteModelRow(index: number, field: keyof ModelMappingRow, value: string) {
      const rows = [...displayRouteModelRows];
      rows[index] = { ...rows[index], [field]: value };
      setRouteModelRows(rows);
    }
    function addRouteModelRow() {
      const used = new Set(routeModelRows.map((row) => row.desktopModel));
      const desktopModel = visibleRouteOptions.find((model) => !used.has(model)) || "";
      setRouteModelRows([...routeModelRows, { desktopModel, upstreamModel: isProxyDesktopMode ? upstreamModelOptions[0] || "" : "" }]);
    }
    function removeRouteModelRow(index: number) {
      setRouteModelRows(routeModelRows.filter((_, rowIndex) => rowIndex !== index));
    }
    function updateRouteLocalMode(mode: LocalRouteMode) {
      setRouteDraft((current) => {
        const next: RouteDraft = { ...current, local_mode: mode };
        if (current.tool === "claude-desktop") {
          if (mode === "takeover") {
            next.claude_desktop_mode = "proxy";
          } else if (current.claude_desktop_mode === "proxy") {
            next.claude_desktop_mode = "direct";
          }
        }
        return next;
      });
    }
    function updateClaudeDesktopMode(mode: string) {
      setRouteDraft((current) => ({
        ...current,
        claude_desktop_mode: mode,
        local_mode: mode === "proxy" ? "takeover" : mode === "direct" || mode === "official" ? "direct" : current.local_mode,
      }));
    }
    return (
      <div className="provider-drawer-layer">
        <button
          className="provider-drawer-backdrop"
          type="button"
          aria-label={t("common.close")}
          onClick={() => setRouteFormOpen(false)}
        />
        <aside
          className="provider-drawer route-builder"
          role="dialog"
          aria-modal="true"
          aria-labelledby="route-drawer-title"
        >
          <div className="provider-builder-head">
            <div className="provider-form-title">
              <span className="provider-icon large">
                <Workflow size={18} />
              </span>
              <div>
                <h2 id="route-drawer-title">{currentRoute ? t("gateway.editRoute") : t("gateway.newRoute")}</h2>
                <span className="muted">{t("gateway.routeDrawerSubtitle")}</span>
              </div>
            </div>
            <button className="ghost-action icon-action" onClick={() => setRouteFormOpen(false)} title={t("common.close")}>
              <X size={15} />
            </button>
          </div>

          <div className="surface-body provider-builder-body">
            {notice && <div className="session-notice">{notice}</div>}
            <div className="provider-form">
              <div className="field-grid">
                <label className="field">
                  <span>{t("gateway.tool")}</span>
                  <select value={routeDraft.tool} onChange={(event) => updateRouteTool(event.target.value)}>
                    {routeTools.length === 0 && <option value="">{t("gateway.noAgentFrameworks")}</option>}
                    {routeTools.map((tool) => {
                      const notInstalled = uninstalledTools.has(normalizeTool(tool));
                      return (
                        <option key={tool} value={tool} disabled={notInstalled}>
                          {toolLabel(tool)}
                          {notInstalled ? ` (${t("gateway.frameworkNotInstalled")})` : ""}
                        </option>
                      );
                    })}
                  </select>
                </label>
                <label className="field">
                  <span>{t("gateway.routeTo")}</span>
                  <select
                    value={routeDraft.provider_id}
                    disabled={routeCandidates.length === 0}
                    onChange={(event) => updateRouteProvider(event.target.value)}
                  >
                    {routeCandidates.length === 0 && <option value="">{t("gateway.noCompatibleProviders")}</option>}
                    {routeCandidates.map((provider) => (
                      <option key={provider.id} value={provider.id}>
                        {provider.name}
                      </option>
                    ))}
                  </select>
                </label>
              </div>

              {selectedRouteProvider ? (
                <div className="route-detail-grid">
                  <div>
                    <span>{t("providers.model")}</span>
                    <strong>{selectedRouteProvider.model || "—"}</strong>
                  </div>
                  <div>
                    <span>{t("gateway.protocol")}</span>
                    <strong>{providerProtocol(selectedRouteProvider)}</strong>
                  </div>
                  <div>
                    <span>{t("gateway.keyStatus")}</span>
                    <strong title={selectedRouteProvider.api_key_issue || undefined}>
                      {keyStatusLabel(selectedRouteProvider)}
                    </strong>
                  </div>
                  <div>
                    <span>{t("providers.baseUrl")}</span>
                    <strong>{selectedRouteProvider.base_url || "—"}</strong>
                  </div>
                </div>
              ) : (
                <div className="empty-card compact">
                  <Workflow size={22} />
                  <strong>{t("gateway.noCompatibleProviders")}</strong>
                </div>
              )}

              {selectedRouteProvider && (
                <div className="route-mode-guide">
                  <div className="route-mode-guide-head">
                    <span className="capability-title">{t("gateway.routeModeGuide")}</span>
                    <span className={routeTakeoverSelected ? "status-badge success" : "status-badge"}>
                      <span className="status-dot" />
                      {routeTakeoverSelected ? t("gateway.routeModeSelectedTakeover") : t("gateway.routeModeSelectedDirect")}
                    </span>
                  </div>
                  <div className="route-mode-options">
                    <button
                      className={routeTakeoverSelected ? "route-mode-option" : "route-mode-option active"}
                      onClick={() => updateRouteLocalMode("direct")}
                      type="button"
                    >
                      <PowerOff size={16} />
                      <span>
                        <strong>{t("gateway.routeDirectMode")}</strong>
                        <small>{t("gateway.routeDirectModeDescription")}</small>
                      </span>
                    </button>
                    <button
                      className={routeTakeoverSelected ? "route-mode-option active" : "route-mode-option"}
                      disabled={!routeSupportsTakeover}
                      onClick={() => updateRouteLocalMode("takeover")}
                      type="button"
                    >
                      <PlugZap size={16} />
                      <span>
                        <strong>{t("gateway.routeTakeoverMode")}</strong>
                        <small>
                          {routeSupportsTakeover
                            ? t("gateway.routeTakeoverModeDescription")
                            : t("gateway.routeTakeoverUnsupported")}
                        </small>
                      </span>
                    </button>
                  </div>
                </div>
              )}

              {selectedRouteProvider && showClaudeDesktopRouteOptions && (
                <div className="claude-desktop-mode-panel">
                  <div className="route-mode-guide-head">
                    <span className="capability-title">{t("gateway.claudeDesktopMode")}</span>
                    <span className="status-badge success">{t("gateway.claudeDesktopModeGuide")}</span>
                  </div>
                  <div className="desktop-mode-explainer">
                    <button
                      aria-pressed={routeDraft.claude_desktop_mode === "proxy"}
                      className={routeDraft.claude_desktop_mode === "proxy" ? "active" : ""}
                      onClick={() => updateClaudeDesktopMode("proxy")}
                      type="button"
                    >
                      <strong>{t("gateway.claudeDesktopProxyMode")}</strong>
                      <span>{t("gateway.claudeDesktopProxyModeDescription")}</span>
                    </button>
                    <button
                      aria-pressed={routeDraft.claude_desktop_mode === "direct"}
                      className={routeDraft.claude_desktop_mode === "direct" ? "active" : ""}
                      onClick={() => updateClaudeDesktopMode("direct")}
                      type="button"
                    >
                      <strong>{t("gateway.claudeDesktopDirectMode")}</strong>
                      <span>{t("gateway.claudeDesktopDirectModeDescription")}</span>
                    </button>
                    <button
                      aria-pressed={routeDraft.claude_desktop_mode === "official"}
                      className={routeDraft.claude_desktop_mode === "official" ? "active" : ""}
                      onClick={() => updateClaudeDesktopMode("official")}
                      type="button"
                    >
                      <strong>{t("gateway.claudeDesktopOfficialMode")}</strong>
                      <span>{t("gateway.claudeDesktopOfficialModeDescription")}</span>
                    </button>
                  </div>
                </div>
              )}

              {selectedRouteProvider && showClaudeRouteOptions && (
                <details className="capability-panel provider-advanced" open>
                  <summary className="capability-title">{t("gateway.advanced")}</summary>
                  <div className="route-mapping-guide">
                    <strong>{t("gateway.modelMappingGuide")}</strong>
                    <span>{routeMappingHelp}</span>
                    {routeLocalTool === "claudecode" && <small>{t("gateway.claudeCodeMappingCaveat")}</small>}
                  </div>
                  <div className="field-grid">
                    <label className="field">
                      <span>{t("gateway.claudeAuthScheme")}</span>
                      <select
                        value={routeDraft.claude_auth_scheme}
                        onChange={(event) => updateRouteDraft("claude_auth_scheme", event.target.value)}
                      >
                        <option value="">{t("gateway.authSchemeAuto")}</option>
                        <option value="auth_token">ANTHROPIC_AUTH_TOKEN</option>
                        <option value="api_key">ANTHROPIC_API_KEY</option>
                      </select>
                    </label>
                    {routeLocalTool === "claudecode" ? (
                      <div className="field wide">
                        <span>{t("gateway.tierModels")}</span>
                        <div className="model-mapping-editor claude-code-model-map">
                          <div className="model-mapping-head">
                            <span>{t("gateway.claudeVisibleModel")}</span>
                            <span>{t("gateway.upstreamModel")}</span>
                          </div>
                          {CLAUDE_CODE_TIER_ROWS.map((row) => (
                            <div className="model-mapping-row claude-tier-row" key={row.key}>
                              <span className="tier-model-label">
                                <strong>{t(row.labelKey)}</strong>
                                <small>{row.visibleModel}</small>
                              </span>
                              <input
                                list="route-upstream-model-options"
                                value={routeDraft[row.draftKey]}
                                onChange={(event) => updateRouteDraft(row.draftKey, event.target.value)}
                                placeholder={row.placeholder}
                              />
                            </div>
                          ))}
                        </div>
                        <small>{t("gateway.claudeCodeFableCaveat")}</small>
                      </div>
                    ) : (
                      <>
                        <label className="field">
                          <span>{t("gateway.sonnetModel")}</span>
                          <input
                            value={routeDraft.claude_sonnet_model}
                            onChange={(event) => updateRouteDraft("claude_sonnet_model", event.target.value)}
                            placeholder="deepseek-v4-pro"
                          />
                        </label>
                        <label className="field">
                          <span>{t("gateway.opusModel")}</span>
                          <input
                            value={routeDraft.claude_opus_model}
                            onChange={(event) => updateRouteDraft("claude_opus_model", event.target.value)}
                            placeholder="deepseek-v4-pro"
                          />
                        </label>
                        <label className="field">
                          <span>{t("gateway.haikuModel")}</span>
                          <input
                            value={routeDraft.claude_haiku_model}
                            onChange={(event) => updateRouteDraft("claude_haiku_model", event.target.value)}
                            placeholder="deepseek-v4-flash"
                          />
                        </label>
                      </>
                    )}
                    {showClaudeDesktopRouteOptions && (
                      <div className="field wide">
                        <span>{t("gateway.desktopModels")}</span>
                        <div className="model-mapping-editor">
                          <datalist id="route-visible-model-options">
                            {visibleRouteOptions.map((model) => (
                              <option key={model} value={model} />
                            ))}
                          </datalist>
                          <datalist id="route-upstream-model-options">
                            {upstreamModelOptions.map((model) => (
                              <option key={model} value={model} />
                            ))}
                          </datalist>
                          <div className="model-mapping-head">
                            <span>{t("gateway.desktopVisibleModel")}</span>
                            <span>{t("gateway.upstreamModel")}</span>
                          </div>
                          {displayRouteModelRows.map((row, index) => (
                            <div className="model-mapping-row" key={`model-route-${index}`}>
                              <input
                                list="route-visible-model-options"
                                value={row.desktopModel}
                                onChange={(event) => updateRouteModelRow(index, "desktopModel", event.target.value)}
                                placeholder="claude-sonnet-5"
                              />
                              <select
                                value={row.upstreamModel}
                                disabled={!isProxyDesktopMode}
                                onChange={(event) => updateRouteModelRow(index, "upstreamModel", event.target.value)}
                                aria-label={t("gateway.upstreamModel")}
                              >
                                <option value="">{t("gateway.selectUpstreamModel")}</option>
                                {upstreamModelOptions.map((model) => (
                                  <option key={model} value={model}>
                                    {model}
                                  </option>
                                ))}
                              </select>
                              <button
                                className="ghost-action icon-action"
                                type="button"
                                disabled={routeModelRows.length === 0}
                                onClick={() => removeRouteModelRow(index)}
                                title={t("gateway.removeDesktopModelRoute")}
                              >
                                <Trash2 size={14} />
                              </button>
                            </div>
                          ))}
                          <button className="ghost-action model-mapping-add" onClick={addRouteModelRow} type="button">
                            <Plus size={14} />
                            {t("gateway.addDesktopModelRoute")}
                          </button>
                        </div>
                        <small>{t("gateway.desktopModelsHint")}</small>
                      </div>
                    )}
                  </div>
                </details>
              )}

              <div className="form-actions">
                <button className="ghost-action" onClick={() => setRouteFormOpen(false)}>
                  <X size={15} />
                  {t("common.close")}
                </button>
                <button className="action" disabled={!canSaveRoute} onClick={saveRoute}>
                  <Save size={15} />
                  {t("gateway.saveRoute")}
                </button>
              </div>
            </div>
          </div>
        </aside>
      </div>
    );
  }

  function renderProvidersTab() {
    return (
      <div className="page-stack">
        <section className="surface provider-overview">
          <div className="surface-header">
            <div>
              <h2>{t("gateway.llmProviderTab")}</h2>
              <p className="subtle-copy">{t("gateway.providerInventorySubtitle")}</p>
            </div>
            <div className="table-actions">
              <span className="pill">{providerList.length}</span>
              <button className="action" onClick={openNewProvider}>
                <Plus size={15} />
                {t("gateway.addProvider")}
              </button>
            </div>
          </div>
          {(providers.error || activeRoutes.error) && (
            <div className="surface-body error">{providers.error || activeRoutes.error}</div>
          )}
          <div className="surface-body provider-summary">
            <div className="summary-stat">
              <span>{t("gateway.configuredProviders")}</span>
              <strong>{providerList.length}</strong>
            </div>
            <div className="summary-stat">
              <span>{t("gateway.activeProviders")}</span>
              <strong>{activeProviderCount}</strong>
            </div>
            <div className="summary-stat">
              <span>{t("gateway.keysReady")}</span>
              <strong>{configuredKeyCount}</strong>
            </div>
            <div className="summary-stat">
              <span>{t("gateway.activeRoutesCount")}</span>
              <strong>{routeList.length}</strong>
            </div>
          </div>
          <div className="surface-body provider-list-grid">
            {providerList.map((provider) => {
              const keyReady = Boolean(provider.api_key_available);
              const supportedModels = providerSupportedModels(provider);
              const supportedProtocols = providerSupportedProtocols(provider);
              const modelsCollapsible = supportedModels.length > PROVIDER_MODEL_COLLAPSE_THRESHOLD;
              const modelsExpanded = expandedProviderModels.has(provider.id);
              const visibleModels =
                modelsCollapsible && !modelsExpanded ? supportedModels.slice(0, PROVIDER_MODEL_PREVIEW_COUNT) : supportedModels;
              const hiddenModelCount = supportedModels.length - PROVIDER_MODEL_PREVIEW_COUNT;
              return (
                <article className="provider-card" key={provider.id}>
                  <header>
                    <span className="provider-card-title">
                      <ProviderMark id={provider.id} name={provider.name} />
                      <span>
                        <strong>{provider.name}</strong>
                        <small>{provider.id}</small>
                      </span>
                    </span>
                    <span
                      className={
                        provider.enabled && keyReady ? "status-badge success" : keyReady ? "status-badge" : "status-badge warning"
                      }
                      title={provider.api_key_issue || undefined}
                    >
                      <span className="status-dot" />
                      {provider.enabled
                        ? keyReady
                          ? t("common.enabled")
                          : t("gateway.keyNotLoaded")
                        : keyReady
                          ? t("common.idle")
                          : t("gateway.keyMissing")}
                    </span>
                  </header>
                  <dl className="provider-facts">
                    <div className="provider-fact-wide">
                      <dt>{t("providers.model")}</dt>
                      <dd className="provider-fact-chips">
                        <div className="provider-model-list">
                          <div className="provider-fact-chips">
                            <CapabilityBadges items={visibleModels} />
                          </div>
                          {modelsCollapsible && (
                            <button
                              className="provider-model-toggle"
                              type="button"
                              aria-expanded={modelsExpanded}
                              onClick={() => toggleProviderModels(provider.id)}
                            >
                              <span>{modelsExpanded ? t("gateway.collapseModels") : t("gateway.showAllModels")}</span>
                              {!modelsExpanded && <span className="provider-model-toggle-count">+{hiddenModelCount}</span>}
                              <ChevronDown size={14} aria-hidden="true" />
                            </button>
                          )}
                        </div>
                      </dd>
                    </div>
                    <div className="provider-fact-wide">
                      <dt>{t("gateway.protocol")}</dt>
                      <dd className="provider-fact-chips">
                        <CapabilityBadges items={supportedProtocols} />
                      </dd>
                    </div>
                    <div>
                      <dt>{t("gateway.keyStatus")}</dt>
                      <dd title={provider.api_key_issue || undefined}>{keyStatusLabel(provider)}</dd>
                    </div>
                    <div>
                      <dt>{t("providers.baseUrl")}</dt>
                      <dd className="mono">{provider.base_url || "—"}</dd>
                    </div>
                  </dl>
                  <footer className="provider-card-actions">
                    <button className="ghost-action" onClick={() => editProvider(provider)}>
                      <Settings2 size={14} />
                      {t("common.edit")}
                    </button>
                    <button
                      className={provider.in_failover_queue ? "ghost-action active" : "ghost-action"}
                      disabled={busy === `failover:${provider.id}`}
                      title={provider.in_failover_queue ? t("gateway.removeFromFailover") : t("gateway.addToFailover")}
                      onClick={() => toggleFailoverQueue(provider)}
                    >
                      <Workflow size={14} />
                      {provider.in_failover_queue ? t("gateway.inFailoverQueue") : t("gateway.addToFailover")}
                    </button>
                    <button
                      className="ghost-action danger-action"
                      disabled={busy === `delete:${provider.id}`}
                      onClick={() => deleteProvider(provider.id)}
                    >
                      <Trash2 size={14} />
                      {t("common.delete")}
                    </button>
                  </footer>
                </article>
              );
            })}
            {providerList.length === 0 && (
              <div className="empty-card">
                <DatabaseZap size={24} />
                <strong>{t("providers.empty")}</strong>
                <button className="action" onClick={openNewProvider}>
                  <Plus size={15} />
                  {t("gateway.addProvider")}
                </button>
              </div>
            )}
          </div>
        </section>

        {renderProviderForm()}
      </div>
    );
  }

  function renderRoutingTab() {
    const status = proxyStatus.data;
    return (
      <div className="page-stack">
        <section className="surface routing-overview">
          <div className="surface-header">
            <div>
              <h2>{t("gateway.routingTab")}</h2>
              <p className="subtle-copy">{t("gateway.routingSubtitle")}</p>
            </div>
            <div className="table-actions">
              <span className={status?.running ? "status-badge success" : "status-badge"}>
                <span className="status-dot" />
                {status?.running ? t("gateway.proxyRunning") : t("gateway.proxyStopped")}
              </span>
              <span className="pill on">
                {routeList.length} / {routeTools.length}
              </span>
              <button className="action" onClick={openNewRoute} disabled={routeTools.length === 0}>
                <Plus size={15} />
                {t("gateway.newRoute")}
              </button>
            </div>
          </div>
          {(agents.error || activeRoutes.error || proxyStatus.error) && (
            <div className="surface-body error">{agents.error || activeRoutes.error || proxyStatus.error}</div>
          )}
          {notice && <div className="session-notice">{notice}</div>}
          <div className="surface-body provider-list-grid route-list-grid">
            {[...routeList].sort((a, b) => sortTools(normalizeTool(a.tool), normalizeTool(b.tool))).map((route) => {
              const localTool = localRoutingTool(route.tool);
              const localCfg = supportsLocalRouting(route.tool) ? proxyConfigByTool.get(localTool) : undefined;
              const routeProvider = providerList.find((provider) => provider.id === route.provider_id);
              const configured = Boolean(route.configured && routeProvider);
              const busyRoute = busy === `disable-route:${route.tool}`;
              const takeoverBusy = busy === `takeover:${localTool}`;
              const failoverBusy = busy === `auto-failover:${localTool}`;
              const routeProviderName = routeProvider?.name || route.provider_name || route.provider_id || t("gateway.unrouted");
              const routeProviderID = routeProvider?.id || route.provider_id || "";
              return (
                <article className={configured ? "route-card enabled" : "route-card"} key={route.tool}>
                  <header>
                    <span className="route-card-title">
                      <Workflow size={17} />
                      <span>
                        <strong>{toolLabel(normalizeTool(route.tool))}</strong>
                        <small>{route.tool}</small>
                      </span>
                    </span>
                    <span className={configured ? "status-badge success" : "status-badge warning"}>
                      {configured ? <CheckCircle2 size={14} /> : <PowerOff size={14} />}
                      {configured ? t("common.enabled") : t("gateway.providerMissing")}
                    </span>
                  </header>

                  <dl className="route-provider-summary">
                    <div>
                      <dt>{t("gateway.activeProvider")}</dt>
                      <dd>
                        <button
                          className="route-provider-link"
                          disabled={!routeProvider}
                          onClick={() => openRouteProvider(routeProvider)}
                          title={routeProvider ? t("common.edit") : undefined}
                          type="button"
                        >
                          <ProviderMark id={routeProviderID || "provider"} name={routeProviderName} />
                          <span>
                            <strong>{routeProviderName}</strong>
                            <small>{routeProviderID || t("gateway.unrouted")}</small>
                          </span>
                          {routeProvider && <Settings2 size={14} />}
                        </button>
                      </dd>
                    </div>
                  </dl>

                  {localCfg && (
                    <div className={localCfg.enabled ? "route-local-controls active" : "route-local-controls"}>
                      <div className="route-local-status">
                        <span>{t("gateway.localTakeover")}</span>
                        <strong>{localCfg.enabled ? t("gateway.takeoverOn") : t("gateway.takeoverOff")}</strong>
                        <small>
                          {localCfg.enabled
                            ? t("gateway.takeoverManagedDescription")
                            : t("gateway.takeoverDirectDescription")}
                        </small>
                        {localCfg.enabled && status?.running && <small className="mono">{status.base_url}</small>}
                      </div>
                      <div className="route-local-actions">
                        <button
                          className={localCfg.enabled ? "ghost-action danger-action" : "ghost-action"}
                          disabled={takeoverBusy || !configured}
                          onClick={() => toggleTakeover(localTool, !localCfg.enabled)}
                        >
                          <PlugZap size={14} />
                          {(localCfg.enabled ? t("gateway.disableTakeoverForTool") : t("gateway.enableTakeoverForTool")).replace(
                            "{tool}",
                            toolLabel(localTool)
                          )}
                        </button>
                        <button
                          className={localCfg.auto_failover ? "ghost-action active" : "ghost-action"}
                          disabled={failoverBusy || !configured}
                          onClick={() => toggleAutoFailover(localCfg)}
                        >
                          <Workflow size={14} />
                          {t("gateway.autoFailover")}: {localCfg.auto_failover ? "ON" : "OFF"}
                        </button>
                      </div>
                    </div>
                  )}

                  <footer className="provider-card-actions">
                    <button className="ghost-action" onClick={() => editRoute(route)}>
                      <Settings2 size={14} />
                      {t("common.edit")}
                    </button>
                    <button
                      className="ghost-action danger-action"
                      disabled={busyRoute}
                      onClick={() => disableRoute(route.tool)}
                    >
                      <PowerOff size={14} />
                      {t("common.disable")}
                    </button>
                  </footer>
                </article>
              );
            })}
            {routeList.length === 0 && (
              <div className="empty-card">
                <Workflow size={24} />
                <strong>{t("gateway.noRoutesConfigured")}</strong>
                <button className="action" onClick={openNewRoute} disabled={routeTools.length === 0}>
                  <Plus size={15} />
                  {t("gateway.newRoute")}
                </button>
              </div>
            )}
          </div>
        </section>

        {renderRouteForm()}
      </div>
    );
  }

  return (
    <div className="page-stack gateway-workspace connection-workspace">
      <div className="connection-subtabs segmented" aria-label={t("nav.gateway")}>
        <button className={activeTab === "providers" ? "active" : ""} onClick={() => setActiveTab("providers")}>
          <DatabaseZap size={15} />
          <span>{t("gateway.llmProviderTab")}</span>
        </button>
        <button className={activeTab === "routing" ? "active" : ""} onClick={() => setActiveTab("routing")}>
          <Workflow size={15} />
          <span>{t("gateway.routingTab")}</span>
        </button>
      </div>

      {activeTab === "providers" ? renderProvidersTab() : renderRoutingTab()}
    </div>
  );
}
