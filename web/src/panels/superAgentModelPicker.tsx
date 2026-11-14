import { Check, ChevronDown, ChevronLeft, ChevronRight, RotateCcw, Zap } from "lucide-react";
import { useEffect, useLayoutEffect, useMemo, useRef, useState } from "react";
import type { KeyboardEvent as ReactKeyboardEvent, ReactNode } from "react";
import { api } from "../api";
import type { FrameworkRuntimeSettings, RuntimeOption } from "../api";
import { useI18n } from "../i18n";
import { runtimeOptionValues } from "./agents/agentUtils";

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
};

const labels = (options?: RuntimeOption[]) => new Map((options ?? []).filter((item) => item.label).map((item) => [item.value, item.label!]));

// useModelCatalog reads the models a framework offers with its current
// configuration, by asking the framework itself. Whatever configures it (its
// own sign-in, a gateway or a router) is reflected without AgentMux knowing.
// Routers run on this machine.
export function useModelCatalog(runtime: string): ModelCatalog {
  const [state, setState] = useState<CatalogState | null>(null);
  useEffect(() => {
    if (!runtime) return;
    let active = true;
    setState(null);
    void (async () => {
      try {
        const settings = await api.frameworkRuntimeSettings(runtime, "", "local");
        if (active) setState({ runtime, settings, error: "" });
      } catch (error) {
        if (active) setState({ runtime, error: error instanceof Error ? error.message : String(error) });
      }
    })();
    return () => { active = false; };
  }, [runtime]);

  return useMemo(() => {
    const ready = state?.runtime === runtime ? state : null;
    const capabilities = ready?.settings?.capabilities;
    const modelLabels = labels(capabilities?.models);
    // With per-model capabilities, a model only offers what it lists. The
    // framework's default model may be unknown, so it gets the framework's list.
    const modelCapabilities = (model: string) => model && capabilities?.model_capabilities ? capabilities.model_capabilities[model] : capabilities;
    return {
      loading: Boolean(runtime) && !ready,
      error: ready?.error ?? "",
      models: runtimeOptionValues(capabilities?.models),
      modelLabel: (model: string) => modelLabels.get(model) ?? model,
      defaults: { model: ready?.settings?.defaults.model ?? "", reasoning_effort: ready?.settings?.defaults.reasoning_effort ?? "", service_tier: ready?.settings?.defaults.service_tier ?? "" },
      efforts: (model: string) => runtimeOptionValues(modelCapabilities(model)?.reasoning_efforts),
      tiers: (model: string) => runtimeOptionValues(modelCapabilities(model)?.service_tiers),
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

// EFFORT_ORDER ranks reasoning efforts from least to most thinking, so the
// slider reads left to right whatever order the framework lists them in.
const EFFORT_ORDER = ["none", "minimal", "low", "medium", "high", "xhigh", "max", "ultra"];

export function sortEfforts(efforts: string[]) {
  const rank = (effort: string) => {
    const index = EFFORT_ORDER.indexOf(effort);
    return index < 0 ? EFFORT_ORDER.length : index;
  };
  return [...efforts].sort((a, b) => rank(a) - rank(b));
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
// popover, after the model menus of Claude and Codex: a model list, an effort
// slider with a fast-mode lightning button, and a second page for a model the framework
// does not list.
export function ModelPicker({ runtime, value, onChange, labelledBy }: {
  runtime: string;
  value: ModelChoice;
  onChange: (next: ModelChoice) => void;
  labelledBy?: string;
}) {
  const { t } = useI18n();
  const catalog = useModelCatalog(runtime);
  const [open, setOpen] = useState(false);
  const [page, setPage] = useState<"main" | "typed">("main");
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
    const escape = (event: KeyboardEvent) => {
      if (event.key !== "Escape") return;
      // Escape leaves the second page first, then closes.
      setPage((current) => {
        if (current === "main") setOpen(false);
        return "main";
      });
    };
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
      setPage("main");
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
  const efforts = sortEfforts(catalog.efforts(effectiveModel));
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
  const unknownModel = value.model && !catalog.models.includes(value.model);
  const triggerModel = value.model ? catalog.modelLabel(value.model) : t("superAgents.modelDefaultShort");
  const listed = !catalog.loading && catalog.models.length > 0;
  // Fast mode is a lightning button beside the effort, lit while it is on.
  const fastButton = fastTier ? (
    <button type="button" className="model-picker-fast-toggle" aria-pressed={fast} aria-label={t("superAgents.fastMode")}
      title={`${t("superAgents.fastMode")}: ${t("superAgents.fastModeHelp")}`}
      onClick={() => onChange({ ...value, service_tier: fast ? "" : fastTier })}>
      <Zap size={14} fill={fast ? "currentColor" : "none"} aria-hidden />
    </button>
  ) : null;

  return (
    <div className="model-picker" ref={root}>
      <button ref={trigger} type="button" className="model-picker-trigger" aria-haspopup="dialog" aria-expanded={open} aria-labelledby={labelledBy}
        aria-describedby={labelledBy ? `${labelledBy}-value` : undefined} onClick={() => setOpen((current) => !current)}>
        {fast && <Zap size={13} className="model-picker-fast" aria-hidden />}
        <span id={labelledBy ? `${labelledBy}-value` : undefined} className="model-picker-trigger-label" aria-label={describeModelChoice(t, value, catalog)}>
          <span className="model-picker-trigger-model">{triggerModel}</span>
          {value.reasoning_effort && <span className="model-picker-trigger-effort">{effortLabel(t, value.reasoning_effort)}</span>}
        </span>
        <ChevronDown size={14} className="model-picker-chevron" aria-hidden />
      </button>
      {open && (
        <div ref={popover} className="model-picker-popover" role="dialog" aria-label={t("superAgents.routerModel")}
          data-side={placement?.side ?? "bottom"} style={placement ? { maxHeight: placement.maxHeight } : undefined}>
          {page === "typed" && listed ? (
            <TypedModelPage value={unknownModel ? value.model : ""} onBack={() => setPage("main")}
              onSubmit={(model) => { chooseModel(model); setPage("main"); }} />
          ) : (
            <>
              <div className="model-picker-heading">{t("superAgents.modelHeading")}</div>
              {catalog.loading ? (
                <div className="model-picker-note">{t("superAgents.modelLoading")}</div>
              ) : catalog.models.length === 0 ? (
                <label className="model-picker-manual">
                  <small>{catalog.error ? t("superAgents.modelUnavailable", { error: catalog.error }) : t("superAgents.modelNoCatalog")}</small>
                  <input aria-label={t("superAgents.routerModel")} placeholder={t("superAgents.modelCustomPlaceholder")} value={value.model}
                    onChange={(event) => onChange({ ...value, model: event.target.value.trim() })} />
                </label>
              ) : (
                <div className="model-picker-list" role="listbox" aria-label={t("superAgents.routerModel")}>
                  <ModelOption selected={!value.model} onSelect={() => chooseModel("")}
                    label={t("superAgents.modelDefaultShort")} detail={defaultModel} />
                  {catalog.models.map((model) => (
                    <ModelOption key={model} selected={value.model === model} label={catalog.modelLabel(model)}
                      detail={catalog.modelLabel(model) !== model ? model : ""} onSelect={() => chooseModel(model)} />
                  ))}
                  {unknownModel && <ModelOption selected label={value.model} detail={t("superAgents.modelTypedTag")} onSelect={() => undefined} />}
                </div>
              )}
              {listed && (
                <button type="button" className="model-picker-row model-picker-more" onClick={() => setPage("typed")}>
                  <span>{t("superAgents.modelTyped")}</span>
                  <ChevronRight size={14} aria-hidden />
                </button>
              )}
              {efforts.length > 0 ? (
                <EffortSlider efforts={efforts} value={value.reasoning_effort} defaultEffort={catalog.defaults.reasoning_effort}
                  onChange={(effort) => onChange({ ...value, reasoning_effort: effort })} extra={fastButton} />
              ) : fastButton && (
                <div className="model-picker-effort model-picker-speed">
                  <div className="model-picker-effort-head">
                    <span className="model-picker-effort-title">{t("superAgents.fastMode")}</span>
                    {fastButton}
                  </div>
                </div>
              )}
            </>
          )}
        </div>
      )}
    </div>
  );
}

// EffortSlider picks a reasoning effort on a track of stops, least thinking on
// the left. With no effort chosen the thumb rests, hollow, on the framework's
// default; the reset button returns to it.
function EffortSlider({ efforts, value, defaultEffort, onChange, extra }: {
  efforts: string[];
  value: string;
  defaultEffort: string;
  onChange: (effort: string) => void;
  extra?: ReactNode;
}) {
  const { t } = useI18n();
  const track = useRef<HTMLDivElement>(null);
  const dragging = useRef(false);
  const chosen = efforts.indexOf(value);
  const resting = chosen >= 0 ? chosen : efforts.indexOf(defaultEffort);
  const last = Math.max(1, efforts.length - 1);
  const position = (index: number) => efforts.length === 1 ? 50 : (index / last) * 100;
  const label = chosen >= 0 ? effortLabel(t, value)
    : defaultEffort ? t("superAgents.effortDefaultWith", { effort: effortLabel(t, defaultEffort) }) : t("superAgents.effortDefault");

  const pick = (index: number) => {
    const effort = efforts[Math.max(0, Math.min(efforts.length - 1, index))];
    if (effort !== value) onChange(effort);
  };
  const pickAt = (clientX: number) => {
    const rect = track.current?.getBoundingClientRect();
    if (!rect || rect.width === 0) return;
    pick(Math.round(((clientX - rect.left) / rect.width) * last));
  };
  const onKeyDown = (event: ReactKeyboardEvent) => {
    const from = resting >= 0 ? resting : -1;
    const moves: Record<string, number> = { ArrowRight: from + 1, ArrowUp: from + 1, ArrowLeft: Math.max(0, from - 1), ArrowDown: Math.max(0, from - 1), Home: 0, End: efforts.length - 1 };
    if (!(event.key in moves)) return;
    event.preventDefault();
    pick(moves[event.key]);
  };

  return (
    <div className="model-picker-effort">
      <div className="model-picker-effort-head">
        <span className="model-picker-effort-title">{t("superAgents.reasoningEffort")}</span>
        <span className="model-picker-effort-value" data-default={chosen < 0 || undefined}>{label}</span>
        <button type="button" className="model-picker-reset" aria-label={t("superAgents.effortReset")} title={t("superAgents.effortReset")}
          disabled={chosen < 0} onClick={() => onChange("")}>
          <RotateCcw size={13} aria-hidden />
        </button>
        {extra}
      </div>
      <div ref={track} className="model-picker-slider" role="slider" tabIndex={0} data-default={chosen < 0 || undefined}
        aria-label={t("superAgents.reasoningEffort")} aria-valuemin={0} aria-valuemax={efforts.length - 1}
        aria-valuenow={resting >= 0 ? resting : undefined} aria-valuetext={label} onKeyDown={onKeyDown}
        onPointerDown={(event) => {
          if (event.button !== 0) return;
          dragging.current = true;
          event.currentTarget.setPointerCapture?.(event.pointerId);
          pickAt(event.clientX);
        }}
        onPointerMove={(event) => { if (dragging.current) pickAt(event.clientX); }}
        onPointerUp={() => { dragging.current = false; }}
        onPointerCancel={() => { dragging.current = false; }}>
        <div className="model-picker-slider-fill" style={{ width: resting >= 0 ? `${position(resting)}%` : 0 }} />
        {efforts.map((effort, index) => (
          <button key={effort} type="button" tabIndex={-1} className="model-picker-stop" aria-label={effortLabel(t, effort)} title={effortLabel(t, effort)}
            data-filled={resting >= 0 && index <= resting || undefined} style={{ left: `${position(index)}%` }}
            onPointerDown={(event) => event.stopPropagation()} onClick={() => pick(index)} />
        ))}
        {resting >= 0 && <div className="model-picker-thumb" style={{ left: `${position(resting)}%` }} />}
      </div>
      <div className="model-picker-slider-ends" aria-hidden>
        <span>{effortLabel(t, efforts[0])}</span>
        {efforts.length > 1 && <span>{effortLabel(t, efforts[efforts.length - 1])}</span>}
      </div>
    </div>
  );
}

// TypedModelPage takes a model ID the framework does not list, such as a
// router's routing group. The framework test checks that it works.
function TypedModelPage({ value, onBack, onSubmit }: { value: string; onBack: () => void; onSubmit: (model: string) => void }) {
  const { t } = useI18n();
  const [draft, setDraft] = useState(value);
  const input = useRef<HTMLInputElement>(null);
  useEffect(() => setDraft(value), [value]);
  useEffect(() => input.current?.focus(), []);
  const submit = () => {
    const model = draft.trim();
    if (model) onSubmit(model);
  };
  return (
    <div className="model-picker-typed">
      <button type="button" className="model-picker-row model-picker-back" onClick={onBack}>
        <ChevronLeft size={14} aria-hidden />
        <span>{t("superAgents.modelTyped")}</span>
      </button>
      <small className="model-picker-note">{t("superAgents.modelFromFramework")}</small>
      <div className="model-picker-typed-row">
        <input ref={input} aria-label={t("superAgents.modelTyped")} placeholder={t("superAgents.modelTypedPlaceholder")} value={draft}
          onChange={(event) => setDraft(event.target.value)}
          onKeyDown={(event) => { if (event.key === "Enter") { event.preventDefault(); submit(); } }} />
        <button type="button" className="action" disabled={!draft.trim() || draft.trim() === value} onClick={submit}>{t("superAgents.modelTypedUse")}</button>
      </div>
    </div>
  );
}

function ModelOption({ label, detail, selected, onSelect }: { label: string; detail?: string; selected: boolean; onSelect: () => void }) {
  return (
    <button type="button" role="option" aria-selected={selected} className="model-picker-option" onClick={onSelect}>
      <span className="model-picker-option-label">{label}</span>
      {detail && <span className="model-picker-option-detail">{detail}</span>}
      <Check size={15} className="model-picker-check" aria-hidden />
    </button>
  );
}
