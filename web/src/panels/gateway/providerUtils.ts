import {
  Bot,
  Cloud,
  Cpu,
  Gem,
  Globe2,
  Layers3,
  Rocket,
  Sparkles,
} from "lucide-react";
import { Provider, ProviderProbeCheck } from "../../api";

export const TOOL_ORDER = ["claudecode", "claude-desktop", "codex", "codex-app", "traecli", "gemini", "cursor", "qoder", "opencode", "iflow", "kimi"];
export const DEFAULT_ROUTE_TOOLS = ["claudecode", "claude-desktop", "codex", "codex-app", "gemini"];
export const CURATED_PRESET_IDS = [
  "anthropic-official",
  "openai-official",
  "claude-desktop-official",
  "claude-desktop-builtin",
  "openrouter",
  "gemini-official",
  "deepseek-claude",
  "deepseek",
  "zhipu-glm-claude",
  "moonshot-kimi-claude",
  "qwen-dashscope",
];
export const PROVIDER_MODEL_COLLAPSE_THRESHOLD = 8;
export const PROVIDER_MODEL_PREVIEW_COUNT = 4;

export type ProbeCapabilities = {
  formats: ProviderProbeCheck[];
  protocols: ProviderProbeCheck[];
  inferredTools: string[];
  apiFormat?: string;
};

export type ProviderDraft = {
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
  claude_auth_scheme: string;
  claude_sonnet_model: string;
  claude_opus_model: string;
  claude_haiku_model: string;
  supported_models: string[];
  supported_api_formats: string[];
  supported_protocols: string[];
	// Explicit values used by Agent runtime setting cards. These are never
	// inferred from a protocol probe because cross-protocol conversion may not
	// preserve the semantics.
	supported_reasoning_efforts: string;
	default_reasoning_effort: string;
	supported_service_tiers: string;
	default_service_tier: string;
  claude_desktop_mode: string;
  manual_models: boolean;
  model_list: string;
  tools: string[];
  enabled: boolean;
};

export type RouteDraft = {
  tool: string;
  provider_id: string;
  original_tool?: string;
  local_mode: LocalRouteMode;
  claude_auth_scheme: string;
  claude_sonnet_model: string;
  claude_opus_model: string;
  claude_haiku_model: string;
  claude_desktop_mode: string;
  manual_models: boolean;
  model_list: string;
};

export type LocalRouteMode = "takeover" | "direct";

export type ModelMappingRow = {
  desktopModel: string;
  upstreamModel: string;
};

export const CLAUDE_DESKTOP_ROUTE_MODELS = ["claude-sonnet-5", "claude-opus-4-8", "claude-haiku-4-5", "claude-fable-5"];
export const CLAUDE_CODE_TIER_ROWS: {
  key: string;
  labelKey: string;
  visibleModel: string;
  draftKey: "claude_sonnet_model" | "claude_opus_model" | "claude_haiku_model";
  placeholder: string;
}[] = [
  {
    key: "sonnet",
    labelKey: "gateway.sonnetModel",
    visibleModel: "claude-sonnet-*",
    draftKey: "claude_sonnet_model",
    placeholder: "deepseek-v4-pro",
  },
  {
    key: "opus",
    labelKey: "gateway.opusModel",
    visibleModel: "claude-opus-*",
    draftKey: "claude_opus_model",
    placeholder: "deepseek-v4-pro",
  },
  {
    key: "haiku",
    labelKey: "gateway.haikuModel",
    visibleModel: "claude-haiku-*",
    draftKey: "claude_haiku_model",
    placeholder: "deepseek-v4-flash",
  },
];

export const emptyDraft: ProviderDraft = {
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
  claude_auth_scheme: "",
  claude_sonnet_model: "",
  claude_opus_model: "",
  claude_haiku_model: "",
  supported_models: [],
  supported_api_formats: [],
  supported_protocols: [],
	supported_reasoning_efforts: "",
	default_reasoning_effort: "",
	supported_service_tiers: "",
	default_service_tier: "",
  claude_desktop_mode: "",
  manual_models: false,
  model_list: "",
  tools: ["codex", "codex-app"],
  enabled: false,
};

export const emptyRouteDraft: RouteDraft = {
  tool: "",
  provider_id: "",
  local_mode: "takeover",
  claude_auth_scheme: "",
  claude_sonnet_model: "",
  claude_opus_model: "",
  claude_haiku_model: "",
  claude_desktop_mode: "",
  manual_models: false,
  model_list: "",
};

export function metaString(provider: Provider, key: string) {
  const value = provider.meta?.[key];
  return typeof value === "string" ? value : "";
}

export function metaRecordString(meta: Record<string, unknown> | undefined, key: string) {
  const value = meta?.[key];
  return typeof value === "string" ? value : "";
}

export function extraString(provider: Provider, key: string) {
  return provider.extra?.[key] ?? "";
}

export function metaStringArray(provider: Provider, key: string) {
  const value = provider.meta?.[key];
  if (!Array.isArray(value)) return [];
  return value.filter((item): item is string => typeof item === "string" && item.trim().length > 0);
}

export function metaValuesString(meta: Record<string, unknown> | undefined, key: string) {
	const values = meta?.[key];
	if (!Array.isArray(values)) return "";
	return values.filter((value): value is string => typeof value === "string" && value.trim().length > 0).join(", ");
}

export function parseRuntimeValues(value: string) {
	return uniqueValues(value.split(/[\n,]/).map((item) => item.trim()).filter(Boolean));
}

export function modelListString(provider: Provider) {
  return modelListStringFromMeta(provider.meta);
}

export function modelListStringFromMeta(meta: Record<string, unknown> | undefined) {
  const models = meta?.claude_desktop_models;
  if (!Array.isArray(models)) return "";
  const rows = models
    .map((item) => {
      if (!item || typeof item !== "object") return "";
      const model = item as Record<string, unknown>;
      const id = typeof model.id === "string" ? model.id : typeof model.name === "string" ? model.name : "";
      if (!id) return "";
      const upstream = typeof model.upstream_model === "string" ? model.upstream_model : "";
      return { desktopModel: id, upstreamModel: upstream };
    })
    .filter((row): row is ModelMappingRow => Boolean(row));
  return modelRowsToList(repairClaudeDesktopModelRows(rows));
}

export function parseModelRows(value: string): ModelMappingRow[] {
  return value
    .split(/[\n,]/)
    .map((model) => model.trim())
    .filter(Boolean)
    .map((entry) => {
      const eq = entry.indexOf("=");
      if (eq > 0) {
        return {
          desktopModel: entry.slice(0, eq).trim(),
          upstreamModel: entry.slice(eq + 1).trim(),
        };
      }
      return { desktopModel: entry, upstreamModel: "" };
    })
    .filter((row) => row.desktopModel.length > 0);
}

export function modelRowsToList(rows: ModelMappingRow[]) {
  return rows
    .map((row) => {
      const desktopModel = row.desktopModel.trim();
      const upstreamModel = row.upstreamModel.trim();
      if (!desktopModel) return "";
      return upstreamModel && upstreamModel !== desktopModel ? `${desktopModel}=${upstreamModel}` : desktopModel;
    })
    .filter(Boolean)
    .join("\n");
}

export function claudeDesktopRouteModelForIndex(index: number) {
  if (index < CLAUDE_DESKTOP_ROUTE_MODELS.length) return CLAUDE_DESKTOP_ROUTE_MODELS[index];
  return `${CLAUDE_DESKTOP_ROUTE_MODELS[0]}-r${index - CLAUDE_DESKTOP_ROUTE_MODELS.length + 2}`;
}

export function isClaudeDesktopVisibleModel(model: string) {
  const normalized = model.trim().toLowerCase();
  if (normalized.includes("[1m]")) return false;
  const tail = normalized.startsWith("anthropic/claude-")
    ? normalized.slice("anthropic/claude-".length)
    : normalized.startsWith("claude-")
      ? normalized.slice("claude-".length)
      : "";
  return ["sonnet-", "opus-", "haiku-", "fable-"].some((prefix) => tail.startsWith(prefix) && tail.length > prefix.length);
}

export function nextClaudeDesktopVisibleModel(rows: ModelMappingRow[], reserved: Set<string>) {
  for (let index = 0; ; index += 1) {
    const route = claudeDesktopRouteModelForIndex(index);
    if (!reserved.has(route) && !rows.some((row) => row.desktopModel === route)) return route;
  }
}

export function repairClaudeDesktopModelRows(rows: ModelMappingRow[]) {
  const reserved = new Set(rows.map((row) => row.desktopModel.trim()).filter(isClaudeDesktopVisibleModel));
  const repaired: ModelMappingRow[] = [];
  rows.forEach((row) => {
    const originalVisible = row.desktopModel.trim();
    const upstreamModel = row.upstreamModel.trim() || originalVisible;
    if (!originalVisible && !upstreamModel) return;
    const desktopModel = isClaudeDesktopVisibleModel(originalVisible)
      ? originalVisible
      : nextClaudeDesktopVisibleModel(repaired, reserved);
    if (repaired.some((existing) => existing.desktopModel === desktopModel)) return;
    repaired.push({ desktopModel, upstreamModel });
  });
  return repaired;
}

export function desktopProxyModelListForModels(models: string[]) {
  return modelRowsToList(
    uniqueValues(models).map((model, index) => ({
      desktopModel: claudeDesktopRouteModelForIndex(index),
      upstreamModel: model,
    }))
  );
}

export function routeMetaToDraft(meta: Record<string, unknown> | undefined) {
  const modelList = modelListStringFromMeta(meta);
  return {
    claude_auth_scheme: metaRecordString(meta, "claude_auth_scheme"),
    claude_sonnet_model: metaRecordString(meta, "claude_sonnet_model"),
    claude_opus_model: metaRecordString(meta, "claude_opus_model"),
    claude_haiku_model: metaRecordString(meta, "claude_haiku_model"),
    claude_desktop_mode: metaRecordString(meta, "claude_desktop_mode"),
    manual_models: modelList.length > 0,
    model_list: modelList,
  };
}

export function providerToDraft(provider: Provider): ProviderDraft {
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
    claude_auth_scheme: metaString(provider, "claude_auth_scheme"),
    claude_sonnet_model: metaString(provider, "claude_sonnet_model"),
    claude_opus_model: metaString(provider, "claude_opus_model"),
    claude_haiku_model: metaString(provider, "claude_haiku_model"),
    supported_models: metaStringArray(provider, "supported_models"),
    supported_api_formats: metaStringArray(provider, "supported_api_formats"),
    supported_protocols: metaStringArray(provider, "supported_protocols"),
		supported_reasoning_efforts: metaValuesString(provider.meta, "supported_reasoning_efforts"),
		default_reasoning_effort: metaString(provider, "default_reasoning_effort"),
		supported_service_tiers: metaValuesString(provider.meta, "supported_service_tiers"),
		default_service_tier: metaString(provider, "default_service_tier"),
    claude_desktop_mode: metaString(provider, "claude_desktop_mode"),
    manual_models: modelList.length > 0,
    model_list: modelList,
    tools: routeToolsForProvider(provider),
    enabled: provider.enabled,
  };
}

export function parseModelList(value: string) {
  return repairClaudeDesktopModelRows(parseModelRows(value)).map((row) => {
    // "route=upstream" maps a Claude Desktop route id to a real upstream
    // model (used by local routing proxy mode).
    const id = row.desktopModel;
    const upstream = row.upstreamModel;
    return upstream ? { id, name: id, display_name: id, upstream_model: upstream } : { id, name: id, display_name: id };
  });
}

export function capabilityName(name: string) {
  if (name === "anthropic") return "Anthropic";
  if (["chat", "chat_completions", "openai_chat"].includes(name)) return "OpenAI Chat Completions";
  if (["responses", "openai_responses"].includes(name)) return "OpenAI Responses";
  return name;
}

export function uniqueValues(values: string[]) {
  const out: string[] = [];
  const seen = new Set<string>();
  values.forEach((value) => {
    const trimmed = value.trim();
    if (!trimmed || seen.has(trimmed)) return;
    seen.add(trimmed);
    out.push(trimmed);
  });
  return out;
}

export function normalizeTool(tool: string) {
  const value = tool.trim();
  if (["claude", "claudecode-cli", "claude-code-cli"].includes(value)) return "claudecode";
  if (["claudecode-desktop", "claude-code-desktop"].includes(value)) return "claude-desktop";
  if (["codex-cli"].includes(value)) return "codex";
  if (["codex-desktop", "codex-app-server"].includes(value)) return "codex-app";
  return value;
}

export function routeToolsForCapability(tool: string) {
  const normalized = normalizeTool(tool);
  if (normalized === "codex") return ["codex", "codex-app"];
  return normalized ? [normalized] : [];
}

export function localRoutingTool(tool: string) {
  const normalized = normalizeTool(tool);
  return normalized === "codex-app" ? "codex" : normalized;
}

export function supportsLocalRouting(tool: string) {
  return ["claudecode", "claude-desktop", "codex"].includes(localRoutingTool(tool));
}

// requiresLocalRouting reports whether a route can only work through the local
// routing proxy. Codex dropped chat/completions support in Feb 2026, so a
// chat-only upstream has to be translated by the proxy; pointing Codex at it
// directly makes Codex reject its own config.
export function requiresLocalRouting(provider: Provider | undefined, tool: string) {
  if (!provider) return false;
  const localTool = localRoutingTool(tool);
  const values = providerProtocolValues(provider);
  if (localTool === "codex") {
    if (hasAnyCapability(values, ["openai_responses", "responses"])) return false;
    return hasAnyCapability(values, ["openai_chat", "chat", "chat_completions", "anthropic", "gemini", "gemini_native"]);
  }
  if (localTool === "claudecode" || localTool === "claude-desktop") {
    if (hasAnyCapability(values, ["anthropic"])) return false;
    return hasAnyCapability(values, ["openai_chat", "openai_responses", "chat", "chat_completions", "responses", "gemini", "gemini_native"]);
  }
  return false;
}

export function providerSupportsRouteTool(provider: Provider, tool: string) {
  const normalizedTool = normalizeTool(tool);
  return routeToolsForProvider(provider).some((candidate) => {
    const normalizedCandidate = normalizeTool(candidate);
    return (
      normalizedCandidate === normalizedTool ||
      (normalizedTool === "codex-app" && normalizedCandidate === "codex") ||
      (normalizedTool === "codex" && normalizedCandidate === "codex-app")
    );
  });
}

export function okCheckNames(checks: ProviderProbeCheck[]) {
  return checks.filter((check) => check.ok).map((check) => check.name);
}

export function claudeDesktopModelIDs(provider: Provider) {
  return parseModelRows(modelListString(provider)).map((row) => row.desktopModel);
}

export function providerSupportedModels(provider: Provider) {
  return uniqueValues([
    ...metaStringArray(provider, "supported_models"),
    provider.model || "",
  ]);
}

export function providerProtocolValues(provider: Provider) {
  return uniqueValues([
    ...metaStringArray(provider, "supported_api_formats"),
    ...metaStringArray(provider, "supported_protocols"),
    metaString(provider, "api_format"),
  ]);
}

export function protocolLabels(values: string[]) {
  return uniqueValues(values.map(capabilityName));
}

export function providerSupportedProtocols(provider: Provider) {
  const labels = protocolLabels(providerProtocolValues(provider));
  return labels.length > 0 ? labels : [providerProtocol(provider)];
}

export function slugifyProviderID(name: string) {
  const slug = name
    .trim()
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, "-")
    .replace(/^-+|-+$/g, "");
  return slug || "provider";
}

export function generateProviderID(name: string, providers: Provider[]) {
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

export type ToolInferenceSignals = {
  apiFormat?: string;
  supportedAPIFormats?: string[];
  supportedProtocols?: string[];
  claudeDesktopMode?: string;
  hasClaudeDesktopRoutes?: boolean;
  hasModels?: boolean;
};

export function hasAnyCapability(values: string[], candidates: string[]) {
  const normalized = new Set(values.map((value) => value.trim()).filter(Boolean));
  return candidates.some((candidate) => normalized.has(candidate));
}

export function inferredToolsForSignals(signals: ToolInferenceSignals) {
  const formats = uniqueValues([...(signals.supportedAPIFormats ?? []), signals.apiFormat ?? ""]);
  const protocols = uniqueValues(signals.supportedProtocols ?? []);
  const hasAnthropic = hasAnyCapability(formats, ["anthropic"]);
  const hasOpenAI = hasAnyCapability(formats, ["openai_chat", "openai_responses"]) ||
    hasAnyCapability(protocols, ["chat", "chat_completions", "responses", "openai_chat", "openai_responses"]);
  const hasGemini = hasAnyCapability(formats, ["gemini", "gemini_native"]) || hasAnyCapability(protocols, ["gemini", "gemini_native"]);
  const hasDesktopProxyRoute = Boolean(signals.hasClaudeDesktopRoutes || ((hasAnthropic || hasOpenAI) && signals.hasModels));
  const tools: string[] = [];

  if (hasAnthropic) tools.push("claudecode");
  if (hasOpenAI) {
    tools.push("codex", "codex-app");
    // Local routing can adapt OpenAI-compatible providers for Claude Code.
    tools.push("claudecode");
  }
  if (hasGemini) tools.push("gemini");
  if (signals.claudeDesktopMode || hasDesktopProxyRoute) tools.push("claude-desktop");

  return uniqueValues(tools).sort(sortTools);
}

export function inferredToolsForDraft(draft: ProviderDraft) {
  return inferredToolsForSignals({
    apiFormat: draft.api_format,
    supportedAPIFormats: draft.supported_api_formats,
    supportedProtocols: draft.supported_protocols,
    claudeDesktopMode: draft.claude_desktop_mode,
    hasClaudeDesktopRoutes: draft.manual_models && draft.model_list.trim().length > 0,
    hasModels: draft.model.trim().length > 0 || draft.supported_models.length > 0 || draft.model_list.trim().length > 0,
  });
}

export function inferredToolsForProvider(provider: Provider) {
  const desktopModels = claudeDesktopModelIDs(provider);
  return inferredToolsForSignals({
    apiFormat: metaString(provider, "api_format"),
    supportedAPIFormats: metaStringArray(provider, "supported_api_formats"),
    supportedProtocols: metaStringArray(provider, "supported_protocols"),
    claudeDesktopMode: metaString(provider, "claude_desktop_mode"),
    hasClaudeDesktopRoutes: desktopModels.length > 0,
    hasModels: (provider.model ?? "").trim().length > 0 || desktopModels.length > 0 || metaStringArray(provider, "supported_models").length > 0,
  });
}

export function routeToolsForProvider(provider: Provider) {
  return uniqueValues(inferredToolsForProvider(provider)).sort(sortTools);
}

export function fallbackToolsForDraft(draft: ProviderDraft) {
  if (draft.claude_desktop_mode) return ["claude-desktop"];
  if (draft.api_format === "anthropic") return ["claudecode"];
  return ["codex", "codex-app"];
}

export function toolsForDraft(draft: ProviderDraft) {
  const inferred = inferredToolsForDraft(draft);
  return inferred.length > 0 ? inferred : draft.tools.length ? uniqueValues(draft.tools).sort(sortTools) : fallbackToolsForDraft(draft);
}

export function desktopProxyModelListForProvider(provider?: Provider) {
  if (!provider) return "";
  const existing = modelListString(provider);
  if (existing) return existing;
  return desktopProxyModelListForModels(uniqueValues([provider.model || "", ...metaStringArray(provider, "supported_models")]));
}

export function providerNeedsClaudeDesktopProxy(provider?: Provider) {
  if (!provider) return false;
  const mode = metaString(provider, "claude_desktop_mode");
  if (mode) return false;
  return routeToolsForProvider(provider).includes("claude-desktop");
}

export function routeMetaDraftForTool(provider: Provider | undefined, tool: string, meta: Record<string, unknown> | undefined) {
  const draft = routeMetaToDraft(meta);
  if (normalizeTool(tool) !== "claude-desktop" || draft.claude_desktop_mode || !providerNeedsClaudeDesktopProxy(provider)) {
    return draft;
  }
  const modelList = draft.model_list || desktopProxyModelListForProvider(provider);
  return {
    ...draft,
    claude_desktop_mode: "proxy",
    manual_models: modelList.length > 0,
    model_list: modelList,
  };
}

export function draftToProvider(draft: ProviderDraft, providers: Provider[]): Provider {
  const meta: Record<string, unknown> = {};
  const extra: Record<string, string> = {};
  const id = draft.id.trim() || generateProviderID(draft.name, providers);
  if (draft.api_format) meta.api_format = draft.api_format;
  if (draft.claude_auth_scheme) meta.claude_auth_scheme = draft.claude_auth_scheme;
  if (draft.claude_sonnet_model.trim()) meta.claude_sonnet_model = draft.claude_sonnet_model.trim();
  if (draft.claude_opus_model.trim()) meta.claude_opus_model = draft.claude_opus_model.trim();
  if (draft.claude_haiku_model.trim()) meta.claude_haiku_model = draft.claude_haiku_model.trim();
  if (draft.supported_models.length > 0) meta.supported_models = uniqueValues(draft.supported_models);
  if (draft.supported_api_formats.length > 0) meta.supported_api_formats = uniqueValues(draft.supported_api_formats);
  if (draft.supported_protocols.length > 0) meta.supported_protocols = uniqueValues(draft.supported_protocols);
	const reasoningEfforts = parseRuntimeValues(draft.supported_reasoning_efforts);
	const serviceTiers = parseRuntimeValues(draft.supported_service_tiers);
	if (reasoningEfforts.length > 0) meta.supported_reasoning_efforts = reasoningEfforts;
	if (draft.default_reasoning_effort.trim()) meta.default_reasoning_effort = draft.default_reasoning_effort.trim();
	if (serviceTiers.length > 0) meta.supported_service_tiers = serviceTiers;
	if (draft.default_service_tier.trim()) meta.default_service_tier = draft.default_service_tier.trim();
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
    extra,
    meta,
    enabled: draft.enabled,
  };
}

export function routeDraftToMeta(draft: RouteDraft) {
  const meta: Record<string, unknown> = {};
  if (draft.claude_auth_scheme) meta.claude_auth_scheme = draft.claude_auth_scheme;
  if (draft.claude_sonnet_model.trim()) meta.claude_sonnet_model = draft.claude_sonnet_model.trim();
  if (draft.claude_opus_model.trim()) meta.claude_opus_model = draft.claude_opus_model.trim();
  if (draft.claude_haiku_model.trim()) meta.claude_haiku_model = draft.claude_haiku_model.trim();
  if (draft.claude_desktop_mode) {
    meta.claude_desktop_mode = draft.claude_desktop_mode;
    meta.claude_desktop_auth_mode = "bearer";
  }
  if (draft.manual_models && draft.model_list.trim()) {
    meta.claude_desktop_models = parseModelList(draft.model_list);
  }
  return meta;
}

export function sortTools(a: string, b: string) {
  const ai = TOOL_ORDER.indexOf(a);
  const bi = TOOL_ORDER.indexOf(b);
  if (ai === -1 && bi === -1) return a.localeCompare(b);
  if (ai === -1) return 1;
  if (bi === -1) return -1;
  return ai - bi;
}

export function iconForProvider(id: string) {
  if (id.includes("anthropic") || id.includes("claude")) return Sparkles;
  if (id.includes("openai")) return Bot;
  if (id.includes("openrouter")) return Globe2;
  if (id.includes("gemini")) return Gem;
  if (id.includes("deepseek")) return Cpu;
  if (id.includes("kimi") || id.includes("moonshot")) return Rocket;
  if (id.includes("qwen") || id.includes("dashscope")) return Cloud;
  return Layers3;
}

export function providerProtocol(provider: Provider) {
  return protocolLabels(providerProtocolValues(provider))[0] || "OpenAI compatible";
}

export function toolLabel(tool: string) {
  const normalized = normalizeTool(tool);
  if (normalized === "claudecode") return "Claude Code CLI";
  if (normalized === "claude-desktop") return "Claude Desktop";
  if (normalized === "opencode") return "OpenCode";
  if (normalized === "qoder") return "Qoder";
  if (normalized === "iflow") return "iFlow";
  if (normalized === "kimi") return "Kimi";
  if (normalized === "gemini") return "Gemini";
  if (normalized === "codex") return "Codex CLI";
  if (normalized === "codex-app") return "Codex Desktop";
  if (normalized === "cursor") return "Cursor";
  if (normalized === "traecli") return "TRAE CLI";
  return tool;
}
