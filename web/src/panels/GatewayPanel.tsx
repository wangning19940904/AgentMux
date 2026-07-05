import { useEffect, useMemo, useState } from "react";
import {
  Bot,
  CheckCircle2,
  CirclePlus,
  Cloud,
  Cpu,
  DatabaseZap,
  Gem,
  Globe2,
  Layers3,
  Plus,
  PlugZap,
  PowerOff,
  Rocket,
  Save,
  Settings2,
  Sparkles,
  Trash2,
  Workflow,
  X,
} from "lucide-react";
import { api, Provider, ProviderProbeCheck, ProviderRoute } from "../api";
import { useI18n } from "../i18n";
import { useAsync } from "../useAsync";

const TOOL_ORDER = ["claudecode", "codex", "claude-desktop", "gemini", "cursor", "qoder", "opencode", "iflow", "kimi"];
const CURATED_PRESET_IDS = [
  "anthropic-official",
  "openai-official",
  "claude-desktop-official",
  "openrouter",
  "gemini-official",
  "deepseek",
  "moonshot-kimi",
  "qwen-dashscope",
];

type ProbeCapabilities = {
  formats: ProviderProbeCheck[];
  protocols: ProviderProbeCheck[];
  apiFormat?: string;
  codexWireAPI?: string;
};

type ProviderDraft = {
  id: string;
  name: string;
  category: string;
  base_url: string;
  api_key_env: string;
  api_key: string;
  model: string;
  note: string;
  website: string;
  api_format: string;
  codex_wire_api: string;
  claude_desktop_mode: string;
  manual_models: boolean;
  model_list: string;
  tools: string[];
  enabled: boolean;
};

type RouteDraft = {
  tool: string;
  provider_id: string;
};

const emptyDraft: ProviderDraft = {
  id: "",
  name: "",
  category: "custom",
  base_url: "",
  api_key_env: "",
  api_key: "",
  model: "",
  note: "",
  website: "",
  api_format: "",
  codex_wire_api: "",
  claude_desktop_mode: "",
  manual_models: false,
  model_list: "",
  tools: ["codex"],
  enabled: false,
};

const emptyRouteDraft: RouteDraft = {
  tool: "",
  provider_id: "",
};

function metaString(provider: Provider, key: string) {
  const value = provider.meta?.[key];
  return typeof value === "string" ? value : "";
}

function extraString(provider: Provider, key: string) {
  return provider.extra?.[key] ?? "";
}

function modelListString(provider: Provider) {
  const models = provider.meta?.claude_desktop_models;
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

function providerToDraft(provider: Provider): ProviderDraft {
  const modelList = modelListString(provider);
  return {
    id: provider.id,
    name: provider.name,
    category: provider.category || "custom",
    base_url: provider.base_url,
    api_key_env: provider.api_key_env || "",
    api_key: "",
    model: provider.model || "",
    note: extraString(provider, "note"),
    website: extraString(provider, "website"),
    api_format: metaString(provider, "api_format"),
    codex_wire_api: metaString(provider, "codex_wire_api"),
    claude_desktop_mode: metaString(provider, "claude_desktop_mode"),
    manual_models: modelList.length > 0,
    model_list: modelList,
    tools: provider.tools.length ? provider.tools : ["codex"],
    enabled: provider.enabled,
  };
}

function parseModelList(value: string) {
  return value
    .split(/[\n,]/)
    .map((model) => model.trim())
    .filter(Boolean)
    .map((model) => ({ id: model, name: model, display_name: model }));
}

function capabilityName(name: string) {
  if (name === "chat_completions") return "chat/completions";
  if (name === "openai_responses") return "openai responses";
  if (name === "openai_chat") return "openai chat";
  return name;
}

function slugifyProviderID(name: string) {
  const slug = name
    .trim()
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, "-")
    .replace(/^-+|-+$/g, "");
  return slug || "provider";
}

function generateProviderID(name: string, providers: Provider[]) {
  const used = new Set(providers.map((provider) => provider.id));
  const base = slugifyProviderID(name);
  let id = base;
  let suffix = 2;
  while (used.has(id)) {
    id = `${base}-${suffix}`;
    suffix += 1;
  }
  return id;
}

function defaultToolsForDraft(draft: ProviderDraft) {
  if (draft.claude_desktop_mode) return ["claude-desktop"];
  if (draft.api_format === "anthropic") return ["claudecode"];
  return ["codex"];
}

function toolsForDraft(draft: ProviderDraft) {
  return draft.id.trim() && draft.tools.length ? draft.tools : defaultToolsForDraft(draft);
}

function draftToProvider(draft: ProviderDraft, providers: Provider[]): Provider {
  const meta: Record<string, unknown> = {};
  const extra: Record<string, string> = {};
  const id = draft.id.trim() || generateProviderID(draft.name, providers);
  if (draft.api_format) meta.api_format = draft.api_format;
  if (draft.codex_wire_api) meta.codex_wire_api = draft.codex_wire_api;
  if (draft.claude_desktop_mode) {
    meta.claude_desktop_mode = draft.claude_desktop_mode;
    meta.claude_desktop_auth_mode = "bearer";
  }
  if (draft.manual_models && draft.model_list.trim()) {
    meta.claude_desktop_models = parseModelList(draft.model_list);
  }
  if (draft.note.trim()) extra.note = draft.note.trim();
  if (draft.website.trim()) extra.website = draft.website.trim();
  return {
    id,
    name: draft.name.trim(),
    category: draft.category,
    base_url: draft.base_url.trim(),
    api_key_env: draft.api_key_env.trim(),
    api_key: draft.api_key.trim(),
    model: draft.model.trim(),
    tools: toolsForDraft(draft),
    extra,
    meta,
    enabled: draft.enabled,
  };
}

function sortTools(a: string, b: string) {
  const ai = TOOL_ORDER.indexOf(a);
  const bi = TOOL_ORDER.indexOf(b);
  if (ai === -1 && bi === -1) return a.localeCompare(b);
  if (ai === -1) return 1;
  if (bi === -1) return -1;
  return ai - bi;
}

function iconForProvider(id: string) {
  if (id.includes("anthropic") || id.includes("claude")) return Sparkles;
  if (id.includes("openai")) return Bot;
  if (id.includes("openrouter")) return Globe2;
  if (id.includes("gemini")) return Gem;
  if (id.includes("deepseek")) return Cpu;
  if (id.includes("kimi") || id.includes("moonshot")) return Rocket;
  if (id.includes("qwen") || id.includes("dashscope")) return Cloud;
  return Layers3;
}

function ProviderMark({
  id,
  name,
  size = "small",
  custom = false,
}: {
  id: string;
  name?: string;
  size?: "small" | "large" | "avatar";
  custom?: boolean;
}) {
  const Icon = custom ? CirclePlus : iconForProvider(id);
  const iconSize = size === "avatar" ? 34 : size === "large" ? 18 : 14;
  const className = size === "avatar" ? "provider-avatar" : size === "large" ? "provider-icon large" : "provider-icon";

  return (
    <span className={className} title={name}>
      <Icon size={iconSize} />
    </span>
  );
}

function providerProtocol(provider: Provider) {
  const desktopMode = metaString(provider, "claude_desktop_mode");
  const apiFormat = metaString(provider, "api_format");
  const wireAPI = metaString(provider, "codex_wire_api");
  if (desktopMode) return "Claude Desktop";
  if (apiFormat === "anthropic") return "Anthropic";
  if (apiFormat === "openai_responses") return "OpenAI Responses";
  if (apiFormat === "openai_chat") return "OpenAI Chat";
  if (wireAPI === "responses") return "Responses";
  if (wireAPI === "chat") return "Chat Completions";
  return "OpenAI compatible";
}

function toolLabel(tool: string) {
  if (tool === "claudecode") return "Claude Code";
  if (tool === "claude-desktop") return "Claude Desktop";
  if (tool === "opencode") return "OpenCode";
  if (tool === "qoder") return "Qoder";
  if (tool === "iflow") return "iFlow";
  if (tool === "kimi") return "Kimi";
  if (tool === "gemini") return "Gemini";
  if (tool === "codex") return "Codex";
  if (tool === "cursor") return "Cursor";
  return tool;
}

export function GatewayPanel() {
  const { t } = useI18n();
  const agents = useAsync(() => api.agents(), []);
  const providers = useAsync(() => api.providers(), []);
  const presets = useAsync(() => api.presets(), []);
  const activeRoutes = useAsync(() => api.activeRoutes(), []);
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

  const providerList = providers.data ?? [];
  const presetList = presets.data ?? [];
  const routeList = activeRoutes.data ?? [];
  const selectedPreset = draft.id || "custom";
  const customSelected = !draft.id || (draft.category === "custom" && !CURATED_PRESET_IDS.includes(draft.id));

  const curatedPresets = useMemo(() => {
    const byID = new Map(presetList.map((provider) => [provider.id, provider]));
    return CURATED_PRESET_IDS.map((id) => byID.get(id)).filter((provider): provider is Provider => Boolean(provider));
  }, [presetList]);

  const routeByTool = useMemo(() => new Map(routeList.map((route) => [route.tool, route])), [routeList]);

  const activeToolsByProvider = useMemo(() => {
    const next = new Map<string, string[]>();
    routeList.forEach((route) => {
      if (!route.provider_id) return;
      const tools = next.get(route.provider_id) ?? [];
      tools.push(route.tool);
      next.set(route.provider_id, tools);
    });
    return next;
  }, [routeList]);

  const tools = useMemo(() => {
    const names = new Set<string>(["claudecode", "codex", "claude-desktop", "gemini"]);
    (agents.data ?? []).forEach((tool) => names.add(tool));
    providerList.forEach((provider) => provider.tools.forEach((tool) => names.add(tool)));
    curatedPresets.forEach((provider) => provider.tools.forEach((tool) => names.add(tool)));
    return Array.from(names).sort(sortTools);
  }, [agents.data, curatedPresets, providerList]);

  const routeTools = useMemo(() => {
    const names = new Set<string>();
    (agents.data ?? []).forEach((tool) => names.add(tool));
    routeList.forEach((route) => names.add(route.tool));
    if (names.size === 0) tools.forEach((tool) => names.add(tool));
    return Array.from(names).sort(sortTools);
  }, [agents.data, routeList, tools]);

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
    return providerList.filter((provider) => provider.tools.includes(tool));
  }

  function routeDraftForTool(tool: string, providerID = ""): RouteDraft {
    const candidates = providersForTool(tool);
    const compatibleProviderID = candidates.some((provider) => provider.id === providerID) ? providerID : "";
    return {
      tool,
      provider_id: compatibleProviderID || candidates[0]?.id || "",
    };
  }

  function openNewRoute() {
    const firstUnrouted = routeTools.find((tool) => !routeByTool.has(tool));
    const tool = firstUnrouted || routeTools[0] || tools[0] || "";
    setNotice("");
    setRouteDraft(routeDraftForTool(tool));
    setActiveTab("routing");
    setProviderFormOpen(false);
    setRouteFormOpen(true);
  }

  function editRoute(route: ProviderRoute) {
    setNotice("");
    setRouteDraft(routeDraftForTool(route.tool, route.provider_id));
    setActiveTab("routing");
    setProviderFormOpen(false);
    setRouteFormOpen(true);
  }

  function updateRouteTool(tool: string) {
    setRouteDraft(routeDraftForTool(tool));
  }

  function updateRouteProvider(providerID: string) {
    setRouteDraft((current) => ({ ...current, provider_id: providerID }));
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
      setProbeCapabilities({
        formats: result.formats ?? [],
        protocols: result.protocols ?? [],
        apiFormat: result.api_format,
        codexWireAPI: result.codex_wire_api,
      });
      if (models.length > 0) {
        setDraft((current) => ({
          ...current,
          api_format: result.api_format || current.api_format,
          model: current.model.trim() && models.includes(current.model.trim()) ? current.model : models[0],
          model_list: current.manual_models ? models.join("\n") : current.model_list,
        }));
      } else if (result.api_format) {
        setDraft((current) => ({ ...current, api_format: result.api_format || current.api_format }));
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
      await api.switchProvider(providerID, tool);
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
      await api.disableRoute(tool);
      setNotice(t("gateway.routeDisabled"));
      reloadProviders();
    } catch (error) {
      setNotice(error instanceof Error ? error.message : String(error));
    } finally {
      setBusy(null);
    }
  }

  const canSave = Boolean(draft.name.trim() && draft.base_url.trim() && toolsForDraft(draft).length > 0);
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
  const configuredKeyCount = providerList.filter((provider) => provider.api_key_env).length;

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
                    {routeTools.map((tool) => (
                      <option key={tool} value={tool}>
                        {toolLabel(tool)}
                      </option>
                    ))}
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
                    <strong>{selectedRouteProvider.api_key_env || t("gateway.keyMissing")}</strong>
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
              const activeTools = activeToolsByProvider.get(provider.id) ?? [];
              const keyConfigured = Boolean(provider.api_key_env);
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
                        provider.enabled ? "status-badge success" : keyConfigured ? "status-badge" : "status-badge warning"
                      }
                    >
                      <span className="status-dot" />
                      {provider.enabled
                        ? t("common.enabled")
                        : keyConfigured
                          ? t("common.idle")
                          : t("gateway.keyMissing")}
                    </span>
                  </header>
                  <dl className="provider-facts">
                    <div>
                      <dt>{t("providers.model")}</dt>
                      <dd>{provider.model || "—"}</dd>
                    </div>
                    <div>
                      <dt>{t("gateway.protocol")}</dt>
                      <dd>{providerProtocol(provider)}</dd>
                    </div>
                    <div>
                      <dt>{t("gateway.keyStatus")}</dt>
                      <dd>{keyConfigured ? provider.api_key_env : t("gateway.keyMissing")}</dd>
                    </div>
                    <div>
                      <dt>{t("providers.baseUrl")}</dt>
                      <dd className="mono">{provider.base_url || "—"}</dd>
                    </div>
                  </dl>
                  <div className="provider-chip-row">
                    {provider.tools.map((tool) => (
                      <span className={activeTools.includes(tool) ? "pill on" : "pill"} key={tool}>
                        {toolLabel(tool)}
                      </span>
                    ))}
                  </div>
                  <footer className="provider-card-actions">
                    <button className="ghost-action" onClick={() => editProvider(provider)}>
                      <Settings2 size={14} />
                      {t("common.edit")}
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
    return (
      <div className="page-stack">
        <section className="surface routing-overview">
          <div className="surface-header">
            <div>
              <h2>{t("gateway.routingTab")}</h2>
              <p className="subtle-copy">{t("gateway.routingSubtitle")}</p>
            </div>
            <div className="table-actions">
              <span className="pill on">
                {routeList.length} / {routeTools.length}
              </span>
              <button className="action" onClick={openNewRoute} disabled={routeTools.length === 0}>
                <Plus size={15} />
                {t("gateway.newRoute")}
              </button>
            </div>
          </div>
          {(agents.error || activeRoutes.error) && (
            <div className="surface-body error">{agents.error || activeRoutes.error}</div>
          )}
          {notice && <div className="session-notice">{notice}</div>}
          <div className="surface-body provider-list-grid route-list-grid">
            {routeList.map((route) => {
              const routeProvider = providerList.find((provider) => provider.id === route.provider_id);
              const configured = Boolean(route.configured && routeProvider);
              const busyRoute = busy === `disable-route:${route.tool}`;
              return (
                <article className={configured ? "route-card enabled" : "route-card"} key={route.tool}>
                  <header>
                    <span className="route-card-title">
                      <Workflow size={17} />
                      <span>
                        <strong>{toolLabel(route.tool)}</strong>
                        <small>{route.tool}</small>
                      </span>
                    </span>
                    <span className={configured ? "status-badge success" : "status-badge warning"}>
                      {configured ? <CheckCircle2 size={14} /> : <PowerOff size={14} />}
                      {configured ? t("common.enabled") : t("gateway.providerMissing")}
                    </span>
                  </header>

                  <dl className="provider-facts">
                    <div>
                      <dt>{t("gateway.activeProvider")}</dt>
                      <dd>{routeProvider?.name || route.provider_name || route.provider_id || t("gateway.unrouted")}</dd>
                    </div>
                    <div>
                      <dt>{t("providers.model")}</dt>
                      <dd>{routeProvider?.model || route.model || "—"}</dd>
                    </div>
                    <div>
                      <dt>{t("gateway.protocol")}</dt>
                      <dd>{routeProvider ? providerProtocol(routeProvider) : route.api_format || "—"}</dd>
                    </div>
                    <div>
                      <dt>{t("gateway.keyStatus")}</dt>
                      <dd>{routeProvider?.api_key_env || route.api_key_env || t("gateway.keyMissing")}</dd>
                    </div>
                  </dl>

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
