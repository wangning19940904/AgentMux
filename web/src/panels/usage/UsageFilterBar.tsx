import { CalendarDays, Check, ChevronDown, Search, X } from "lucide-react";
import { useEffect, useId, useMemo, useRef, useState, type ReactNode } from "react";
import { useI18n } from "../../i18n";
import { formatTokens } from "./format";
import {
  GRANULARITIES, RANGE_PRESETS, activeFilterCount, hourlyAllowed, parseLocalDate, rangeBounds, toggleFilterValue,
  type DateBounds, type FilterDimension, type Granularity, type Period, type UsageView,
} from "./usageQuery";

export type FilterOption = { value: string; label: string; title?: string; tokens?: number };
export type FilterOptions = Record<FilterDimension, FilterOption[]>;

// A menu anchored under its button. It closes on Escape, on a click outside,
// and when focus leaves it.
function Popover({ label, summary, icon, active, align = "left", children }: {
  label: string;
  summary: ReactNode;
  icon?: ReactNode;
  active?: boolean;
  align?: "left" | "right";
  children: (close: () => void) => ReactNode;
}) {
  const [open, setOpen] = useState(false);
  const root = useRef<HTMLDivElement>(null);
  const panelID = useId();
  useEffect(() => {
    if (!open) return;
    const onPointerDown = (event: PointerEvent) => {
      if (root.current && !root.current.contains(event.target as Node)) setOpen(false);
    };
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key === "Escape") {
        setOpen(false);
        root.current?.querySelector<HTMLButtonElement>(".usage-filter-button")?.focus();
      }
    };
    document.addEventListener("pointerdown", onPointerDown);
    document.addEventListener("keydown", onKeyDown);
    return () => {
      document.removeEventListener("pointerdown", onPointerDown);
      document.removeEventListener("keydown", onKeyDown);
    };
  }, [open]);
  return (
    <div className="usage-filter" ref={root}>
      <button
        type="button"
        className={`usage-filter-button${active ? " active" : ""}`}
        aria-haspopup="dialog"
        aria-expanded={open}
        aria-controls={open ? panelID : undefined}
        onClick={() => setOpen((value) => !value)}
      >
        {icon}
        <span className="usage-filter-label">{label}</span>
        <span className="usage-filter-summary">{summary}</span>
        <ChevronDown size={14} aria-hidden="true" />
      </button>
      {open && (
        <div id={panelID} className={`usage-filter-panel align-${align}`} role="dialog" aria-label={label}>
          {children(() => setOpen(false))}
        </div>
      )}
    </div>
  );
}

function DateRangeFilter({ view, bounds, today, onChange }: {
  view: UsageView;
  bounds: DateBounds;
  today: string;
  onChange: (next: UsageView) => void;
}) {
  const { t } = useI18n();
  const [from, setFrom] = useState(bounds.from || today);
  const [to, setTo] = useState(bounds.to || today);
  // The custom fields start from whatever range is showing.
  useEffect(() => {
    setFrom(bounds.from || today);
    setTo(bounds.to || today);
  }, [bounds.from, bounds.to, today]);
  const valid = Boolean(parseLocalDate(from) && parseLocalDate(to));
  const summary = view.preset === "custom"
    ? (bounds.from === bounds.to ? bounds.from : `${bounds.from} – ${bounds.to}`)
    : t(`range.${view.preset}`);
  return (
    <Popover label={t("usage.range")} summary={summary} icon={<CalendarDays size={15} aria-hidden="true" />} active>
      {(close) => (
        <>
          <div className="usage-filter-options" role="listbox" aria-label={t("usage.range")}>
            {RANGE_PRESETS.map((preset) => {
              const presetBounds = rangeBounds({ preset, from: "", to: "" }, today);
              const selected = view.preset === preset;
              return (
                <button
                  key={preset}
                  type="button"
                  role="option"
                  aria-selected={selected}
                  className={`usage-filter-option${selected ? " selected" : ""}`}
                  onClick={() => { onChange({ ...view, preset, from: "", to: "" }); close(); }}
                >
                  <span className="usage-filter-check">{selected && <Check size={16} strokeWidth={3} aria-hidden="true" />}</span>
                  <span className="usage-filter-option-label">{t(`range.${preset}`)}</span>
                  <small>{presetBounds.from ? (presetBounds.from === presetBounds.to ? presetBounds.from.slice(5) : `${presetBounds.from.slice(5)} – ${presetBounds.to.slice(5)}`) : ""}</small>
                </button>
              );
            })}
          </div>
          <form
            className={`usage-filter-custom${view.preset === "custom" ? " selected" : ""}`}
            onSubmit={(event) => {
              event.preventDefault();
              if (!valid) return;
              onChange({ ...view, preset: "custom", from: from <= to ? from : to, to: from <= to ? to : from });
              close();
            }}
          >
            <span className="usage-filter-custom-title">{t("range.custom")}</span>
            <label>
              <span>{t("usage.from")}</span>
              <input type="date" value={from} max={today} onChange={(event) => setFrom(event.target.value)} />
            </label>
            <label>
              <span>{t("usage.to")}</span>
              <input type="date" value={to} max={today} onChange={(event) => setTo(event.target.value)} />
            </label>
            <button className="action" type="submit" disabled={!valid}>{t("usage.applyRange")}</button>
          </form>
        </>
      )}
    </Popover>
  );
}

const SEARCH_THRESHOLD = 8;

function DimensionFilter({ label, options, selected, onToggle, onClear }: {
  label: string;
  options: FilterOption[];
  selected: string[];
  onToggle: (value: string) => void;
  onClear: () => void;
}) {
  const { t } = useI18n();
  const [query, setQuery] = useState("");
  // A selected value stays listed even when the range no longer contains it,
  // so it can always be cleared.
  const all = useMemo(() => {
    const known = new Set(options.map((option) => option.value));
    return [...options, ...selected.filter((value) => !known.has(value)).map((value): FilterOption => ({ value, label: value || "—" }))];
  }, [options, selected]);
  const needle = query.trim().toLocaleLowerCase();
  const visible = needle
    ? all.filter((option) => `${option.label} ${option.title ?? ""} ${option.value}`.toLocaleLowerCase().includes(needle))
    : all;
  const summary = selected.length === 0
    ? t("usage.filterAll")
    : selected.length === 1
      ? all.find((option) => option.value === selected[0])?.label ?? selected[0]
      : t("usage.filterCount", { count: selected.length });
  return (
    <Popover label={label} summary={summary} active={selected.length > 0}>
      {() => (
        <>
          {all.length > SEARCH_THRESHOLD && (
            <label className="usage-filter-search">
              <Search size={14} aria-hidden="true" />
              <input
                type="search"
                value={query}
                placeholder={t("usage.filterSearch", { label })}
                aria-label={t("usage.filterSearch", { label })}
                onChange={(event) => setQuery(event.target.value)}
                autoFocus
              />
            </label>
          )}
          <div className="usage-filter-options" role="listbox" aria-multiselectable="true" aria-label={label}>
            {visible.map((option) => {
              const checked = selected.includes(option.value);
              return (
                <button
                  key={option.value}
                  type="button"
                  role="option"
                  aria-selected={checked}
                  className={`usage-filter-option${checked ? " selected" : ""}`}
                  title={option.title ?? option.label}
                  onClick={() => onToggle(option.value)}
                >
                  <span className="usage-filter-check">{checked && <Check size={16} strokeWidth={3} aria-hidden="true" />}</span>
                  <span className="usage-filter-option-label">{option.label}</span>
                  {option.tokens !== undefined && <small>{formatTokens(option.tokens)}</small>}
                </button>
              );
            })}
            {visible.length === 0 && <div className="usage-filter-empty">{all.length === 0 ? t("usage.filterNoValues") : t("usage.filterNoMatch")}</div>}
          </div>
          {selected.length > 0 && (
            <div className="usage-filter-footer">
              <button type="button" className="ghost-action" onClick={onClear}>{t("usage.filterClear")}</button>
            </div>
          )}
        </>
      )}
    </Popover>
  );
}

// One row of filters above everything they scope: the date range first, then
// the bucket size and the dimensions.
export function UsageFilterBar({ view, bounds, period, today, options, showMachines, onChange }: {
  view: UsageView;
  bounds: DateBounds;
  period: Period;
  today: string;
  options: FilterOptions;
  showMachines: boolean;
  onChange: (next: UsageView) => void;
}) {
  const { t } = useI18n();
  const dimensions: Array<{ id: FilterDimension; label: string }> = [
    ...(showMachines ? [{ id: "machines" as const, label: t("usage.filter.machines") }] : []),
    { id: "runtimes", label: t("usage.filter.runtimes") },
    { id: "models", label: t("usage.filter.models") },
    { id: "agents", label: t("usage.filter.agents") },
  ];
  const filterCount = activeFilterCount(view.filters);
  const canHourly = hourlyAllowed(bounds);
  return (
    <div className="usage-filter-bar" role="group" aria-label={t("usage.filters")}>
      <DateRangeFilter view={view} bounds={bounds} today={today} onChange={onChange} />
      <label className="usage-filter-select">
        <span>{t("usage.groupBy")}</span>
        <select value={view.granularity} onChange={(event) => onChange({ ...view, granularity: event.target.value as Granularity })}>
          <option value="auto">{t("usage.granularityAuto", { period: t(`period.${period}`) })}</option>
          {GRANULARITIES.map((value) => (
            <option key={value} value={value} disabled={value === "hourly" && !canHourly}>{t(`period.${value}`)}</option>
          ))}
        </select>
      </label>
      {dimensions.map((dimension) => (
        <DimensionFilter
          key={dimension.id}
          label={dimension.label}
          options={options[dimension.id]}
          selected={view.filters[dimension.id]}
          onToggle={(value) => onChange({ ...view, filters: toggleFilterValue(view.filters, dimension.id, value) })}
          onClear={() => onChange({ ...view, filters: { ...view.filters, [dimension.id]: [] } })}
        />
      ))}
      {filterCount > 0 && (
        <button
          type="button"
          className="ghost-action usage-filter-reset"
          onClick={() => onChange({ ...view, filters: { machines: [], runtimes: [], models: [], agents: [] } })}
        >
          <X size={14} aria-hidden="true" />
          {t("usage.filterReset", { count: filterCount })}
        </button>
      )}
    </div>
  );
}
