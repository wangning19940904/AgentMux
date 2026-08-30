import { useEffect, useMemo, useState } from "react";
import {
  Activity,
  AlertTriangle,
  ArrowRightLeft,
  CheckCircle2,
  ChevronDown,
  Clock,
  DatabaseZap,
  Download,
  Plus,
  PlugZap,
  PowerOff,
  RefreshCw,
  Save,
  Settings2,
  Trash2,
  Workflow,
  X,
} from "lucide-react";
import {
  activeMachineScope,
  api,
  Provider,
  ProviderMonitorConfig,
  ProviderMonitorProviderStatus,
  ProviderRoute,
  ProxyToolConfig,
} from "../../api";
import { useI18n } from "../../i18n";
import { useAsync } from "../../useAsync";
import { TargetBadge, targetKey } from "../../components/TargetBadge";
import { usePolling } from "../../hooks/usePolling";
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
  requiresLocalRouting,
  toolLabel,
  toolsForDraft,
  uniqueValues,
} from "./providerUtils";
import { CapabilityBadges, ProviderMark } from "./ProviderMark";
import { ProviderModelBadges } from "./ProviderModelBadges";
import { ProviderSyncDialog, ProviderTransferForm } from "./ProviderTransfer";
import { providerModelHealthRows } from "./providerModelHealth";

const defaultMonitorConfig: ProviderMonitorConfig = {
  enabled: true,
  interval_minutes: 360,
  probe_models: true,
  max_models_per_provider: 20,
};

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

function formatMonitorInterval(minutes: number) {
  if (minutes >= 24 * 60 && minutes % (24 * 60) === 0) return `${minutes / (24 * 60)}d`;
  if (minutes >= 60 && minutes % 60 === 0) return `${minutes / 60}h`;
  return `${minutes}m`;
}

function monitorBadgeClass(state?: string) {
  if (state === "healthy") return "status-badge success";
  if (state === "warning" || state === "checking") return "status-badge warning";
  if (state === "error") return "status-badge danger";
  return "status-badge";
}

export function GatewayPanel() {
  const { t } = useI18n();
  const agents = useAsync(() => api.agents(), []);
  const frameworks = useAsync(() => api.frameworks(), []);
  const providers = useAsync(() => api.providers(), []);
  const allProviders = useAsync(() => api.allProviders(), []);
  const fleetTargets = useAsync(() => api.fleetTargets(), []);
  const presets = useAsync(() => api.presets(), []);
  const activeRoutes = useAsync(() => api.activeRoutes(), []);
  const proxyStatus = useAsync(() => api.proxyStatus(), []);
  const providerMonitor = useAsync(() => api.providerMonitor(), []);
  const [activeTab, setActiveTab] = useState<"providers" | "routing">("providers");
  const [providerFormOpen, setProviderFormOpen] = useState(false);
  const [providerFormMode, setProviderFormMode] = useState<"configure" | "import">("configure");
  const [syncProvider, setSyncProvider] = useState<Provider | null>(null);
  const [routeFormOpen, setRouteFormOpen] = useState(false);
  const [busy, setBusy] = useState<string | null>(null);
  const [notice, setNotice] = useState("");
  const [monitorNotice, setMonitorNotice] = useState<{ kind: "success" | "error"; text: string } | null>(null);
  const [monitorConfig, setMonitorConfig] = useState<ProviderMonitorConfig>(defaultMonitorConfig);
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
  const targetIDForProvider = (provider: Provider) => {
    if (provider.target_id) return provider.target_id;
    const scope = activeMachineScope();
    return scope === "all" ? "local" : scope;
  };
  const defaultTransferDestinationID = activeMachineScope() === "all" ? "local" : activeMachineScope();
  const providerHealthByKey = useMemo(() => {
    const statuses = new Map<string, ProviderMonitorProviderStatus>();
    (providerMonitor.data?.providers ?? []).forEach((status) => {
      statuses.set(targetKey(status.target_id, status.provider_id), status);
    });
    return statuses;
  }, [providerMonitor.data?.providers]);

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
    (proxyStatus.data?.tools ?? []).forEach((cfg) => next.set(targetKey(cfg.target_id, localRoutingTool(cfg.tool)), cfg));
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
    if (!providerFormOpen && !routeFormOpen && !syncProvider) return;
    const previousOverflow = document.body.style.overflow;
    const handleKeyDown = (event: KeyboardEvent) => {
      if (event.key === "Escape") {
        setProviderFormOpen(false);
        setRouteFormOpen(false);
        setSyncProvider(null);
      }
    };
    document.body.style.overflow = "hidden";
    window.addEventListener("keydown", handleKeyDown);
    return () => {
      document.body.style.overflow = previousOverflow;
      window.removeEventListener("keydown", handleKeyDown);
    };
  }, [providerFormOpen, routeFormOpen, syncProvider]);

  useEffect(() => {
    if (providerMonitor.data?.config) setMonitorConfig(providerMonitor.data.config);
  }, [providerMonitor.data?.config]);

  usePolling(providerMonitor.reload, 30_000, {
    enabled: monitorConfig.enabled || Boolean(providerMonitor.data?.running),
  });

  function reloadProviders() {
    providers.reload();
    allProviders.reload();
    activeRoutes.reload();
    proxyStatus.reload();
  }

  async function toggleProviderMonitor(enabled: boolean) {
    const previous = monitorConfig;
    const next = { ...monitorConfig, enabled, probe_models: true };
    setMonitorConfig(next);
    setBusy("provider-monitor-config");
    setMonitorNotice(null);
    try {
      const snapshot = await api.saveProviderMonitor(next);
      setMonitorConfig(snapshot.config);
      setMonitorNotice({
        kind: "success",
        text: enabled ? t("gateway.modelMonitorEnabled") : t("gateway.modelMonitorDisabled"),
      });
      await providerMonitor.reload();
    } catch (error) {
      setMonitorConfig(previous);
      setMonitorNotice({ kind: "error", text: error instanceof Error ? error.message : String(error) });
    } finally {
      setBusy(null);
    }
  }

  async function runProviderMonitor() {
    setBusy("provider-monitor-run");
    setMonitorNotice(null);
    try {
      if (!monitorConfig.probe_models) {
        const next = { ...monitorConfig, probe_models: true };
        const snapshot = await api.saveProviderMonitor(next);
        setMonitorConfig(snapshot.config);
      }
      await api.runProviderMonitor();
      setMonitorNotice({ kind: "success", text: t("gateway.modelMonitorComplete") });
      await Promise.all([providerMonitor.reload(), providers.reload(), allProviders.reload()]);
    } catch (error) {
      setMonitorNotice({ kind: "error", text: error instanceof Error ? error.message : String(error) });
      await providerMonitor.reload();
    } finally {
      setBusy(null);
    }
  }

  async function refreshProviderModels() {
    setBusy("provider-model-refresh");
    setMonitorNotice(null);
    try {
      await api.refreshProviderModels();
      setMonitorNotice({ kind: "success", text: t("gateway.modelRefreshComplete") });
      await Promise.all([providerMonitor.reload(), providers.reload(), allProviders.reload()]);
    } catch (error) {
      setMonitorNotice({ kind: "error", text: error instanceof Error ? error.message : String(error) });
      await providerMonitor.reload();
    } finally {
      setBusy(null);
    }
  }

  function monitorStateLabel(status?: ProviderMonitorProviderStatus) {
    if (!status) return t("gateway.modelMonitorPending");
    const key = `providers.monitorState.${status.state}`;
    const label = t(key);
    return label === key ? status.state : label;
  }

  function monitorHealthSummary(status?: ProviderMonitorProviderStatus) {
    if (!status) return t("gateway.modelMonitorPending");
    if (status.checked_models > 0) {
      return t("gateway.modelMonitorAvailability", {
        healthy: status.healthy_models,
        total: status.checked_models,
      });
    }
    return t("gateway.modelMonitorCatalog", { count: status.catalog_count });
  }

  function monitorFailureDetail(status?: ProviderMonitorProviderStatus) {
    if (!status) return "";
    const failed = status.models?.find((model) => model.state !== "healthy");
    if (failed) return `${failed.model}: ${failed.message || failed.state}`;
    return status.message || "";
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

  async function toggleTakeover(tool: string, enabled: boolean, targetID?: string) {
    setBusy(`takeover:${tool}`);
    setNotice("");
    try {
      await api.setTakeover(tool, enabled, targetID);
      setNotice(t("gateway.takeoverUpdated"));
      reloadProviders();
    } catch (error) {
      setNotice(error instanceof Error ? error.message : String(error));
    } finally {
      setBusy(null);
    }
  }

  async function toggleAutoFailover(cfg: { tool: string; auto_failover: boolean; target_id?: string }) {
    setBusy(`auto-failover:${cfg.tool}`);
    setNotice("");
    try {
      await api.setProxyToolConfig({ tool: cfg.tool, auto_failover: !cfg.auto_failover }, cfg.target_id);
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
      await api.setFailoverQueue(provider.id, !provider.in_failover_queue, provider.sort_index ?? 0, provider.target_id);
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

  function routeDraftForTool(tool: string, providerID = "", originalTool?: string, targetID?: string): RouteDraft {
    const routeTool = normalizeTool(tool);
    const candidates = providersForTool(routeTool).filter((provider) => !targetID || provider.target_id === targetID);
    const compatibleProviderID = candidates.some((provider) => provider.id === providerID) ? providerID : "";
    const selectedProviderID = compatibleProviderID || candidates[0]?.id || "";
    const currentRoute = routeList.find((route) => (!targetID || route.target_id === targetID) && normalizeTool(route.tool) === normalizeTool(originalTool || routeTool));
    const provider = providerList.find((item) => (!targetID || item.target_id === targetID) && item.id === selectedProviderID);
    const metaSource = currentRoute && currentRoute.provider_id === selectedProviderID ? currentRoute.meta : provider?.meta;
    const routeMetaDraft = routeMetaDraftForTool(provider, routeTool, metaSource);
    const localTool = localRoutingTool(routeTool);
    const localCfg = proxyConfigByTool.get(targetKey(currentRoute?.target_id, localTool));
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
    if (supportsTakeover && requiresLocalRouting(provider, routeTool)) {
      localMode = "takeover";
    }
    return {
      tool: routeTool,
      provider_id: selectedProviderID,
      original_tool: originalTool,
      target_id: targetID,
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
    setRouteDraft(routeDraftForTool(route.tool, route.provider_id, route.tool, route.target_id));
    setActiveTab("routing");
    setProviderFormOpen(false);
    setRouteFormOpen(true);
  }

  function updateRouteTool(tool: string) {
    setRouteDraft((current) => routeDraftForTool(tool, current.provider_id, current.original_tool, current.target_id));
  }

  function updateRouteProvider(providerID: string) {
    const provider = providerList.find((item) => (!routeDraft.target_id || item.target_id === routeDraft.target_id) && item.id === providerID);
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
      if (supportsLocalRouting(current.tool) && requiresLocalRouting(provider, current.tool)) {
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
    setProviderFormMode("configure");
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
    setProviderFormMode("configure");
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
    setProviderFormMode("configure");
    setProviderFormOpen(true);
  }

  function openImportProvider() {
    resetProviderDraft();
    setProviderFormMode("import");
    setActiveTab("providers");
    setRouteFormOpen(false);
    setProviderFormOpen(true);
  }

  function openProviderSync(provider: Provider) {
    setProviderFormOpen(false);
    setRouteFormOpen(false);
    setSyncProvider(provider);
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
      });
      if (models.length > 0) {
        setDraft((current) => ({
          ...current,
          api_format: result.api_format || current.api_format,
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
          tools: inferredTools.length > 0 ? inferredTools : current.tools,
          supported_api_formats: supportedAPIFormats,
          supported_protocols: supportedProtocols,
        }));
      }
      setProbeNotice({
        kind: "success",
        text:
          models.length > 0
            ? t("gateway.modelsFetched", { count: models.length })
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

  async function deleteProvider(id: string, targetID?: string) {
    setBusy(`delete:${id}`);
    setNotice("");
    try {
      await api.deleteProvider(id, targetID);
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
      const provider = providerList.find((item) => (!routeDraft.target_id || item.target_id === routeDraft.target_id) && item.id === providerID);
      const localTakeover = supportsLocalRouting(tool)
        ? routeDraft.local_mode === "takeover" || requiresLocalRouting(provider, tool)
        : undefined;
      await api.switchProvider(providerID, tool, routeDraftToMeta(routeDraft), localTakeover, routeDraft.target_id);
      if (routeDraft.original_tool && routeDraft.original_tool !== tool) {
        await api.disableRoute(routeDraft.original_tool, routeDraft.target_id);
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

  async function disableRoute(tool: string, targetID?: string) {
    setBusy(`disable-route:${tool}`);
    setNotice("");
    try {
      const localTool = localRoutingTool(tool);
      const localCfg = proxyConfigByTool.get(targetKey(targetID, localTool));
      if (supportsLocalRouting(tool) && localCfg?.enabled) {
        await api.setTakeover(localTool, false, targetID);
      }
      await api.disableRoute(tool, targetID);
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
              {providerFormMode === "import" ? (
                <span className="provider-transfer-icon large"><Download size={20} /></span>
              ) : (
                <ProviderMark id={selectedPreset} name={draft.name} size="large" custom={customSelected && !draft.id} />
              )}
              <div>
                <h2 id="provider-drawer-title">{providerFormMode === "import" ? t("gateway.importProvider") : t("gateway.addProvider")}</h2>
                <span className="muted">{providerFormMode === "import" ? t("gateway.importProviderHint") : t("gateway.addProviderSubtitle")}</span>
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
              <button className={providerFormMode === "configure" && customSelected ? "preset-tile selected" : "preset-tile"} onClick={openNewProvider}>
                <ProviderMark id="custom" size="large" custom />
                <strong>{t("gateway.customProvider")}</strong>
                <span>{t("gateway.customProviderHint")}</span>
              </button>

              <button className={providerFormMode === "import" ? "preset-tile selected" : "preset-tile"} onClick={openImportProvider}>
                <span className="provider-transfer-icon"><Download size={17} /></span>
                <strong>{t("gateway.importProvider")}</strong>
                <span>{t("gateway.importProviderTileHint")}</span>
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

            {providerFormMode === "import" ? (
              <ProviderTransferForm
                key={`provider-import:${defaultTransferDestinationID}`}
                mode="import"
                providers={allProviders.data ?? []}
                targets={fleetTargets.data ?? []}
                defaultDestinationID={defaultTransferDestinationID}
                loading={allProviders.loading || fleetTargets.loading}
                loadError={allProviders.error || fleetTargets.error || ""}
                onApplied={reloadProviders}
              />
            ) : (
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
                      <small>{t("gateway.modelOptionsHint", { count: modelOptions.length })}</small>
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
                  {probeCapabilities.apiFormat && (
                    <span className="muted">
                      {`${t("gateway.detectedApiFormat")}: ${capabilityName(probeCapabilities.apiFormat)}`}
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
            )}
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
    const routeRequiresTakeover = routeSupportsTakeover && requiresLocalRouting(selectedRouteProvider, routeDraft.tool);
    const routeTakeoverSelected = Boolean(routeSupportsTakeover && (routeRequiresTakeover || routeDraft.local_mode === "takeover"));
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
                      disabled={routeRequiresTakeover}
                      onClick={() => updateRouteLocalMode("direct")}
                      type="button"
                    >
                      <PowerOff size={16} />
                      <span>
                        <strong>{t("gateway.routeDirectMode")}</strong>
                        <small>
                          {routeRequiresTakeover
                            ? t("gateway.routeDirectUnsupported")
                            : t("gateway.routeDirectModeDescription")}
                        </small>
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
          <div className="surface-body provider-health-strip">
            <div className="provider-health-strip-copy">
              <span className="provider-health-strip-icon"><Activity size={17} /></span>
              <span>
                <strong>{t("gateway.modelMonitorTitle")}</strong>
                <small>
                  {t("gateway.modelMonitorSummary", { interval: formatMonitorInterval(monitorConfig.interval_minutes) })}
                </small>
              </span>
            </div>
            <div className="provider-health-strip-time">
              <Clock size={14} />
              <span>
                {t("providers.monitorLastRun")}: {formatMonitorTime(providerMonitor.data?.last_run_at)}
                {providerMonitor.data?.next_run_at
                  ? ` · ${t("providers.monitorNextRun")}: ${formatMonitorTime(providerMonitor.data.next_run_at)}`
                  : ""}
              </span>
            </div>
            <div className="provider-health-strip-actions">
              <label className="provider-health-toggle">
                <span>{monitorConfig.enabled ? t("common.enabled") : t("common.disabled")}</span>
                <input
                  type="checkbox"
                  checked={monitorConfig.enabled}
                  disabled={busy === "provider-monitor-config"}
                  onChange={(event) => void toggleProviderMonitor(event.target.checked)}
                />
              </label>
              <button
                className="ghost-action"
                type="button"
                disabled={busy === "provider-model-refresh" || busy === "provider-monitor-run" || providerMonitor.data?.running}
                onClick={() => void refreshProviderModels()}
              >
                <RefreshCw size={15} className={busy === "provider-model-refresh" ? "spin" : ""} />
                {busy === "provider-model-refresh" ? t("gateway.modelRefreshRunning") : t("gateway.modelRefreshNow")}
              </button>
              <button
                className="ghost-action"
                type="button"
                disabled={busy === "provider-model-refresh" || busy === "provider-monitor-run" || providerMonitor.data?.running}
                onClick={() => void runProviderMonitor()}
              >
                <Activity size={15} />
                {busy === "provider-monitor-run" || providerMonitor.data?.running
                  ? t("providers.monitorRunning")
                  : t("providers.monitorRunNow")}
              </button>
            </div>
            {(monitorNotice || providerMonitor.error) && (
              <div className={`provider-health-strip-notice ${(monitorNotice?.kind === "error" || providerMonitor.error) ? "error" : ""}`}>
                {monitorNotice?.text || providerMonitor.error}
              </div>
            )}
          </div>
          <div className="surface-body provider-list-grid">
            {providerList.map((provider) => {
              const keyReady = Boolean(provider.api_key_available);
              const health = providerHealthByKey.get(targetKey(provider.target_id, provider.id));
              const healthFailure = monitorFailureDetail(health);
              const supportedModels = providerSupportedModels(provider);
              const modelHealthRows = providerModelHealthRows(supportedModels, health?.models);
              const supportedProtocols = providerSupportedProtocols(provider);
              const modelsCollapsible = modelHealthRows.length > PROVIDER_MODEL_COLLAPSE_THRESHOLD;
              const modelsExpanded = expandedProviderModels.has(provider.id);
              const visibleModelRows =
                modelsCollapsible && !modelsExpanded ? modelHealthRows.slice(0, PROVIDER_MODEL_PREVIEW_COUNT) : modelHealthRows;
              const hiddenModelCount = modelHealthRows.length - PROVIDER_MODEL_PREVIEW_COUNT;
              return (
                <article className="provider-card" key={targetKey(provider.target_id, provider.id)}>
                  <header>
                    <span className="provider-card-title">
                      <ProviderMark id={provider.id} name={provider.name} />
                      <span>
                        <strong>{provider.name}</strong>
                        <small>{provider.id}</small>
                        <TargetBadge target_id={provider.target_id} target_name={provider.target_name} />
                      </span>
                    </span>
                    <span className="provider-card-badges">
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
                      <span className={monitorBadgeClass(health?.state)} title={healthFailure || undefined}>
                        {health?.state === "healthy" ? (
                          <CheckCircle2 size={13} />
                        ) : health?.state === "error" || health?.state === "warning" ? (
                          <AlertTriangle size={13} />
                        ) : health?.state === "checking" ? (
                          <RefreshCw size={13} className="spin" />
                        ) : (
                          <Clock size={13} />
                        )}
                        {monitorStateLabel(health)}
                      </span>
                    </span>
                  </header>
                  <dl className="provider-facts">
                    <div className="provider-fact-wide">
                      <dt>{t("providers.model")}</dt>
                      <dd className="provider-fact-chips">
                        <div className="provider-model-list">
                          <div className="provider-fact-chips">
                            <ProviderModelBadges rows={visibleModelRows} />
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
                      <dt>{t("gateway.modelMonitorService")}</dt>
                      <dd className={health?.state === "error" || health?.state === "warning" ? "provider-health-failure" : ""} title={healthFailure || undefined}>
                        {monitorHealthSummary(health)}
                      </dd>
                    </div>
                    <div>
                      <dt>{t("providers.baseUrl")}</dt>
                      <dd className="mono">{provider.base_url || "—"}</dd>
                    </div>
                  </dl>
                  <footer className="provider-card-actions">
                    <button className="ghost-action" onClick={() => openProviderSync(provider)}>
                      <ArrowRightLeft size={14} />
                      {t("gateway.syncProvider")}
                    </button>
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
                      onClick={() => deleteProvider(provider.id, provider.target_id)}
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
        {syncProvider && (
          <ProviderSyncDialog
            key={`${targetIDForProvider(syncProvider)}:${syncProvider.id}`}
            provider={syncProvider}
            sourceTargetID={targetIDForProvider(syncProvider)}
            providers={allProviders.data ?? []}
            targets={fleetTargets.data ?? []}
            loading={allProviders.loading || fleetTargets.loading}
            loadError={allProviders.error || fleetTargets.error || ""}
            onApplied={reloadProviders}
            onClose={() => setSyncProvider(null)}
          />
        )}
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
              const localCfg = supportsLocalRouting(route.tool) ? proxyConfigByTool.get(targetKey(route.target_id, localTool)) : undefined;
              const routeProvider = providerList.find((provider) => provider.target_id === route.target_id && provider.id === route.provider_id);
              const configured = Boolean(route.configured && routeProvider);
              const busyRoute = busy === `disable-route:${route.tool}`;
              const takeoverBusy = busy === `takeover:${localTool}`;
              const failoverBusy = busy === `auto-failover:${localTool}`;
              const routeProviderName = routeProvider?.name || route.provider_name || route.provider_id || t("gateway.unrouted");
              const routeProviderID = routeProvider?.id || route.provider_id || "";
              return (
                <article className={configured ? "route-card enabled" : "route-card"} key={targetKey(route.target_id, route.tool)}>
                  <header>
                    <span className="route-card-title">
                      <Workflow size={17} />
                      <span>
                        <strong>{toolLabel(normalizeTool(route.tool))}</strong>
                        <small>{route.tool}</small>
                        <TargetBadge target_id={route.target_id} target_name={route.target_name} />
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
                          onClick={() => toggleTakeover(localTool, !localCfg.enabled, route.target_id)}
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
                      onClick={() => disableRoute(route.tool, route.target_id)}
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
