import { Check, ChevronDown, Zap } from "lucide-react";
import { useEffect, useLayoutEffect, useMemo, useRef, useState } from "react";
import { api } from "../api";
import type { FrameworkRuntimeSettings, Provider, RuntimeOption } from "../api";
import { useI18n } from "../i18n";
import { activeRouteForTool, providerModelOptions, routeToolForRuntime, runtimeOptionValues, runtimeProviderOptions } from "./agents/agentUtils";

// ModelChoice is what a router agent runs with. Empty values are the
// framework's own defaults.
export type ModelChoice = { model: string; reasoning_effort: string; service_tier: string };

// Service tiers that are a framework's fast mode, such as Codex's "priority".
const FAST_TIERS = ["priority", "fast"];

export function isFastTier(tier?: string) {
  return Boolean(tier && FAST_TIERS.includes(tier));
}

export function effortLabel(t: (key: string) => string, effort: string) {
  const key = `superAgents.effort.${effort}`;
  const label = t(key);
  return label === key ? effort : label;
}

type CatalogState = {
  runtime: string;
  provider?: Provider;
  settings?: FrameworkRuntimeSettings;
  error: string;
};

export type ModelCatalog = {
  loading: boolean;
  error: string;
  models: string[];
  modelLabel: (model: string) => string;
  defaults: ModelChoice;
  efforts: (model: string) => string[];
  tiers: (model: string) => string[];
  // The provider a configured route uses; empty for the framework's own login.
  providerName: string;
};

const isLocal = (item: { target_id?: string }) => !item.target_id || item.target_id === "local";
const labels = (options?: RuntimeOption[]) => new Map((options ?? []).filter((item) => item.label).map((item) => [item.value, item.label!]));

// useModelCatalog reads what a router on this framework can run with, the way
// the Agents form does: the provider of the framework's active route, or else
// the framework's own login catalog. Routers run on this machine.
export function useModelCatalog(runtime: string): ModelCatalog {
  const [state, setState] = useState<CatalogState | null>(null);
  useEffect(() => {
    if (!runtime) return;
    let active = true;
    setState(null);
    void (async () => {
      try {
        const [providers, routes] = await Promise.all([api.providers().catch(() => null), api.activeRoutes().catch(() => null)]);
        const route = activeRouteForTool((routes ?? []).filter(isLocal), routeToolForRuntime(runtime));
        const provider = route?.configured ? (providers ?? []).filter(isLocal).find((item) => item.id === route.provider_id) : undefined;
        const settings = provider ? undefined : await api.frameworkRuntimeSettings(runtime, "", "local");
        if (active) setState({ runtime, provider, settings, error: "" });
      } catch (error) {
        if (active) setState({ runtime, error: error instanceof Error ? error.message : String(error) });
      }
    })();
    return () => { active = false; };
  }, [runtime]);

  return useMemo(() => {
    const ready = state?.runtime === runtime ? state : null;
    const provider = ready?.provider;
    const capabilities = ready?.settings?.capabilities;
    const modelLabels = labels(capabilities?.models);
    // With per-model capabilities, a model only offers what it lists. The
    // framework's default model may be unknown, so it gets the framework's list.
    const modelCapabilities = (model: string) => model && capabilities?.model_capabilities ? capabilities.model_capabilities[model] : capabilities;
    return {
      loading: Boolean(runtime) && !ready,
      error: ready?.error ?? "",
      models: provider ? providerModelOptions(provider) : runtimeOptionValues(capabilities?.models),
      modelLabel: (model: string) => modelLabels.get(model) ?? model,
      defaults: provider
        ? { model: provider.model ?? "", reasoning_effort: "", service_tier: "" }
        : { model: ready?.settings?.defaults.model ?? "", reasoning_effort: ready?.settings?.defaults.reasoning_effort ?? "", service_tier: ready?.settings?.defaults.service_tier ?? "" },
      efforts: (model: string) => provider
        ? runtimeProviderOptions(provider, "supported_reasoning_efforts")
        : runtimeOptionValues(modelCapabilities(model)?.reasoning_efforts),
      tiers: (model: string) => provider
        ? runtimeProviderOptions(provider, "supported_service_tiers")
        : runtimeOptionValues(modelCapabilities(model)?.service_tiers),
      providerName: provider?.name ?? "",
    };
  }, [runtime, state]);
}

// Popover geometry, in CSS pixels. The gap matches the CSS offset from the
// trigger; the margin keeps the popover off the edge of whatever clips it.
const POPOVER_GAP = 6;
const POPOVER_MARGIN = 12;
const POPOVER_MAX_HEIGHT = 420;

export type PopoverPlacement = { side: "bottom" | "top"; maxHeight: number };

// placePopover opens the popover below its trigger when its content fits
// there, and otherwise on the side with more room, never taller than that room.
export function placePopover(trigger: { top: number; bottom: number }, bounds: { top: number; bottom: number }, contentHeight: number): PopoverPlacement {
  const below = Math.max(0, bounds.bottom - POPOVER_MARGIN - trigger.bottom - POPOVER_GAP);
  const above = Math.max(0, trigger.top - POPOVER_GAP - bounds.top - POPOVER_MARGIN);
  const needed = Math.min(contentHeight, POPOVER_MAX_HEIGHT);
  const side = needed > below && above > below ? "top" : "bottom";
  return { side, maxHeight: Math.floor(Math.min(POPOVER_MAX_HEIGHT, side === "top" ? above : below)) };
}

// visibleBounds is the part of the window that the element's scrolling or
// clipping ancestors, such as a drawer, still show.
function visibleBounds(element: HTMLElement) {
  let top = 0;
  let bottom = window.innerHeight;
  for (let node = element.parentElement; node && node !== document.body; node = node.parentElement) {
    if (getComputedStyle(node).overflowY === "visible") continue;
    const rect = node.getBoundingClientRect();
    top = Math.max(top, rect.top);
    bottom = Math.min(bottom, rect.bottom);
  }
  return { top, bottom };
}

// contentHeight is how tall the popover would be with nothing scrolled away.
function contentHeight(popover: HTMLElement) {
  const list = popover.querySelector<HTMLElement>(".model-picker-list");
  const listHidden = list ? list.scrollHeight - list.clientHeight : 0;
  return popover.scrollHeight + (popover.offsetHeight - popover.clientHeight) + listHidden;
}

// revealSelected scrolls the model list so the chosen model is in view.
function revealSelected(popover: HTMLElement) {
  const list = popover.querySelector<HTMLElement>(".model-picker-list");
  const option = list?.querySelector<HTMLElement>("[aria-selected=true]");
  if (!list || !option) return;
  const top = option.offsetTop - list.offsetTop;
  if (top < list.scrollTop || top + option.offsetHeight > list.scrollTop + list.clientHeight) {
    list.scrollTop = Math.max(0, top - (list.clientHeight - option.offsetHeight) / 2);
  }
}

// describeModelChoice is the one-line form of a choice, as in "gpt-6 · High · Fast".
export function describeModelChoice(t: (key: string, values?: Record<string, string | number>) => string, value: ModelChoice, catalog?: ModelCatalog) {
  const model = value.model ? catalog?.modelLabel(value.model) ?? value.model : t("superAgents.modelDefaultShort");
  const parts = [model];
  if (value.reasoning_effort) parts.push(effortLabel(t, value.reasoning_effort));
  if (isFastTier(value.service_tier)) parts.push(t("superAgents.fastShort"));
  return parts.join(" · ");
}

// ModelPicker chooses a model, its reasoning effort and its speed in one
// popover, like the model menus of Codex and Claude Code.
export function ModelPicker({ runtime, value, onChange, labelledBy }: {
  runtime: string;
  value: ModelChoice;
  onChange: (next: ModelChoice) => void;
  labelledBy?: string;
}) {
  const { t } = useI18n();
  const catalog = useModelCatalog(runtime);
  const [open, setOpen] = useState(false);
  const [placement, setPlacement] = useState<PopoverPlacement | null>(null);
  const root = useRef<HTMLDivElement>(null);
  const trigger = useRef<HTMLButtonElement>(null);
  const popover = useRef<HTMLDivElement>(null);
  const revealed = useRef(false);
  // place fits the popover into the room the drawer or window leaves it.
  const place = () => {
    if (!trigger.current || !popover.current) return;
    const next = placePopover(trigger.current.getBoundingClientRect(), visibleBounds(trigger.current), contentHeight(popover.current));
    setPlacement((current) => current?.side === next.side && current.maxHeight === next.maxHeight ? current : next);
  };
  useEffect(() => {
    if (!open) return;
    const close = (event: MouseEvent) => { if (!root.current?.contains(event.target as Node)) setOpen(false); };
    const escape = (event: KeyboardEvent) => { if (event.key === "Escape") setOpen(false); };
    // Scrolling the drawer or resizing the window moves the trigger.
    const reposition = (event: Event) => { if (!popover.current?.contains(event.target as Node)) place(); };
    document.addEventListener("mousedown", close);
    document.addEventListener("keydown", escape);
    document.addEventListener("scroll", reposition, true);
    window.addEventListener("resize", reposition);
    return () => {
      document.removeEventListener("mousedown", close);
      document.removeEventListener("keydown", escape);
      document.removeEventListener("scroll", reposition, true);
      window.removeEventListener("resize", reposition);
      setPlacement(null);
      revealed.current = false;
    };
  }, [open]);
  // Content changes, such as the catalog loading, can change the fit, so
  // measure after every render while open. place only sets a changed fit.
  useLayoutEffect(() => {
    if (!open) return;
    place();
    if (placement && !revealed.current && !catalog.loading && popover.current) {
      revealSelected(popover.current);
      revealed.current = true;
    }
  });

  const effectiveModel = value.model || catalog.defaults.model;
  const efforts = catalog.efforts(effectiveModel);
  const fastTier = catalog.tiers(effectiveModel).find((tier) => FAST_TIERS.includes(tier));
  const fast = isFastTier(value.service_tier);
  // Effort and speed that the new model does not offer go back to its default.
  const chooseModel = (model: string) => {
    const next = model || catalog.defaults.model;
    const nextEfforts = catalog.efforts(next);
    const nextTiers = catalog.tiers(next);
    onChange({
      model,
      reasoning_effort: nextEfforts.includes(value.reasoning_effort) ? value.reasoning_effort : "",
      service_tier: nextTiers.includes(value.service_tier) ? value.service_tier : "",
    });
  };
  const defaultModel = catalog.defaults.model ? catalog.modelLabel(catalog.defaults.model) : "";
  const defaultEffort = catalog.defaults.reasoning_effort ? effortLabel(t, catalog.defaults.reasoning_effort) : "";
  const unknownModel = value.model && !catalog.models.includes(value.model);

  return (
    <div className="model-picker" ref={root}>
      <button ref={trigger} type="button" className="model-picker-trigger" aria-haspopup="dialog" aria-expanded={open} aria-labelledby={labelledBy}
        aria-describedby={labelledBy ? `${labelledBy}-value` : undefined} onClick={() => setOpen((current) => !current)}>
        {fast && <Zap size={13} className="model-picker-fast" />}
        <span id={labelledBy ? `${labelledBy}-value` : undefined}>{describeModelChoice(t, value, catalog)}</span>
        <ChevronDown size={14} />
      </button>
      {open && (
        <div ref={popover} className="model-picker-popover" role="dialog" aria-label={t("superAgents.routerModel")}
          data-side={placement?.side ?? "bottom"} style={placement ? { maxHeight: placement.maxHeight } : undefined}>
          {catalog.loading ? (
            <small className="muted">{t("superAgents.modelLoading")}</small>
          ) : catalog.models.length === 0 ? (
            <label className="field">
              <small>{catalog.error ? t("superAgents.modelUnavailable", { error: catalog.error }) : t("superAgents.modelNoCatalog")}</small>
              <input aria-label={t("superAgents.routerModel")} placeholder={t("superAgents.modelCustomPlaceholder")} value={value.model}
                onChange={(event) => onChange({ ...value, model: event.target.value.trim() })} />
            </label>
          ) : (
            <div className="model-picker-list" role="listbox" aria-label={t("superAgents.routerModel")}>
              <ModelOption selected={!value.model} onSelect={() => chooseModel("")}
                label={defaultModel ? t("superAgents.modelDefault", { model: defaultModel }) : t("superAgents.modelDefaultShort")} />
              {catalog.models.map((model) => (
                <ModelOption key={model} selected={value.model === model} label={catalog.modelLabel(model)} onSelect={() => chooseModel(model)} />
              ))}
              {unknownModel && <ModelOption selected label={value.model} onSelect={() => undefined} />}
            </div>
          )}
          {efforts.length > 0 && (
            <div className="model-picker-section" role="radiogroup" aria-label={t("superAgents.reasoningEffort")}>
              <small>{t("superAgents.reasoningEffort")}</small>
              <div className="model-picker-segments">
                <button type="button" role="radio" aria-checked={!value.reasoning_effort} onClick={() => onChange({ ...value, reasoning_effort: "" })}>
                  {defaultEffort ? t("superAgents.effortDefaultWith", { effort: defaultEffort }) : t("superAgents.effortDefault")}
                </button>
                {efforts.map((effort) => (
                  <button key={effort} type="button" role="radio" aria-checked={value.reasoning_effort === effort} onClick={() => onChange({ ...value, reasoning_effort: effort })}>
                    {effortLabel(t, effort)}
                  </button>
                ))}
              </div>
            </div>
          )}
          {fastTier && (
            <label className="model-picker-section model-picker-toggle">
              <span><Zap size={13} /> {t("superAgents.fastMode")}</span>
              <small>{t("superAgents.fastModeHelp")}</small>
              <input type="checkbox" role="switch" checked={fast} onChange={(event) => onChange({ ...value, service_tier: event.target.checked ? fastTier : "" })} />
            </label>
          )}
          {!catalog.loading && catalog.models.length > 0 && (
            <small className="muted">{catalog.providerName ? t("superAgents.modelFromProvider", { provider: catalog.providerName }) : t("superAgents.modelFromLogin")}</small>
          )}
        </div>
      )}
    </div>
  );
}

function ModelOption({ label, selected, onSelect }: { label: string; selected: boolean; onSelect: () => void }) {
  return (
    <button type="button" role="option" aria-selected={selected} className="model-picker-option" onClick={onSelect}>
      <span>{label}</span>
      {selected && <Check size={14} />}
    </button>
  );
}
