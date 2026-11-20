// The Usage dashboard's view state: which time range, how it is bucketed, and
// which machines, frameworks, models and agents it is narrowed to. The view
// lives in the page's hash route, so a link reopens the same slice.

export const RANGE_PRESETS = ["today", "yesterday", "7d", "30d", "90d", "month", "lastMonth", "all"] as const;
export type RangePreset = (typeof RANGE_PRESETS)[number] | "custom";

export const GRANULARITIES = ["hourly", "daily", "weekly", "monthly"] as const;
export type Period = (typeof GRANULARITIES)[number];
export type Granularity = Period | "auto";

export const FILTER_DIMENSIONS = ["machines", "runtimes", "models", "agents"] as const;
export type FilterDimension = (typeof FILTER_DIMENSIONS)[number];
export type UsageFilters = Record<FilterDimension, string[]>;

export type UsageView = {
  preset: RangePreset;
  // Only meaningful for the custom preset: inclusive local dates.
  from: string;
  to: string;
  granularity: Granularity;
  filters: UsageFilters;
};

export type DateBounds = { from: string; to: string };

export const DEFAULT_VIEW: UsageView = {
  preset: "today",
  from: "",
  to: "",
  granularity: "auto",
  filters: { machines: [], runtimes: [], models: [], agents: [] },
};

const DATE_PATTERN = /^\d{4}-\d{2}-\d{2}$/;
const DAY_MS = 24 * 60 * 60 * 1000;
// More hourly buckets than this make a chart nobody can read and a response
// of several megabytes across a fleet.
export const MAX_HOURLY_DAYS = 31;

export function formatLocalDate(value: Date) {
  const year = value.getFullYear();
  const month = String(value.getMonth() + 1).padStart(2, "0");
  const day = String(value.getDate()).padStart(2, "0");
  return `${year}-${month}-${day}`;
}

export function parseLocalDate(value: string): Date | null {
  if (!DATE_PATTERN.test(value)) return null;
  const [year, month, day] = value.split("-").map(Number);
  const parsed = new Date(year, month - 1, day);
  return parsed.getFullYear() === year && parsed.getMonth() === month - 1 && parsed.getDate() === day ? parsed : null;
}

function shiftDays(date: string, days: number) {
  const parsed = parseLocalDate(date)!;
  parsed.setDate(parsed.getDate() + days);
  return formatLocalDate(parsed);
}

// rangeBounds resolves a view to inclusive local dates. Both are empty for
// "all time".
export function rangeBounds(view: Pick<UsageView, "preset" | "from" | "to">, today: string): DateBounds {
  const now = parseLocalDate(today)!;
  switch (view.preset) {
    case "today":
      return { from: today, to: today };
    case "yesterday": {
      const day = shiftDays(today, -1);
      return { from: day, to: day };
    }
    case "7d":
      return { from: shiftDays(today, -6), to: today };
    case "30d":
      return { from: shiftDays(today, -29), to: today };
    case "90d":
      return { from: shiftDays(today, -89), to: today };
    case "month":
      return { from: formatLocalDate(new Date(now.getFullYear(), now.getMonth(), 1)), to: today };
    case "lastMonth":
      return {
        from: formatLocalDate(new Date(now.getFullYear(), now.getMonth() - 1, 1)),
        to: formatLocalDate(new Date(now.getFullYear(), now.getMonth(), 0)),
      };
    case "custom": {
      const from = parseLocalDate(view.from) ? view.from : today;
      const to = parseLocalDate(view.to) ? view.to : from;
      return from <= to ? { from, to } : { from: to, to: from };
    }
    default:
      return { from: "", to: "" };
  }
}

export function rangeDays(bounds: DateBounds): number | null {
  const from = parseLocalDate(bounds.from);
  const to = parseLocalDate(bounds.to);
  if (!from || !to) return null;
  return Math.round((to.getTime() - from.getTime()) / DAY_MS) + 1;
}

// A range that reaches today still changes; an earlier one is final.
export function includesToday(bounds: DateBounds, today: string) {
  return !bounds.to || bounds.to >= today;
}

// resolvePeriod picks the bucket size. "auto" keeps the number of buckets
// readable: hours for up to a week, days for up to a quarter, then weeks and
// months. An explicit hourly choice falls back to days on a range too long
// for it.
export function resolvePeriod(granularity: Granularity, bounds: DateBounds): Period {
  const days = rangeDays(bounds);
  if (granularity === "hourly") return days !== null && days <= MAX_HOURLY_DAYS ? "hourly" : "daily";
  if (granularity !== "auto") return granularity;
  if (days === null) return "weekly";
  if (days <= 7) return "hourly";
  if (days <= 92) return "daily";
  if (days <= 731) return "weekly";
  return "monthly";
}

export function hourlyAllowed(bounds: DateBounds) {
  const days = rangeDays(bounds);
  return days !== null && days <= MAX_HOURLY_DAYS;
}

export function activeFilterCount(filters: UsageFilters) {
  return FILTER_DIMENSIONS.reduce((count, dimension) => count + filters[dimension].length, 0);
}

export function toggleFilterValue(filters: UsageFilters, dimension: FilterDimension, value: string): UsageFilters {
  const current = filters[dimension];
  const next = current.includes(value) ? current.filter((item) => item !== value) : [...current, value];
  return { ...filters, [dimension]: next };
}

const HASH_KEYS: Record<FilterDimension, string> = { machines: "machine", runtimes: "framework", models: "model", agents: "agent" };

// viewToHashQuery writes only what differs from the default view, so the
// plain "#usage" route stays the default dashboard.
export function viewToHashQuery(view: UsageView): string {
  const params = new URLSearchParams();
  if (view.preset !== DEFAULT_VIEW.preset) params.set("range", view.preset);
  if (view.preset === "custom") {
    params.set("from", view.from);
    params.set("to", view.to);
  }
  if (view.granularity !== "auto") params.set("by", view.granularity);
  for (const dimension of FILTER_DIMENSIONS) {
    for (const value of view.filters[dimension]) params.append(HASH_KEYS[dimension], value);
  }
  return params.toString();
}

export function viewFromHashQuery(query: string): UsageView {
  const params = new URLSearchParams(query);
  const range = params.get("range") ?? "";
  const from = params.get("from") ?? "";
  const to = params.get("to") ?? "";
  let preset: RangePreset = DEFAULT_VIEW.preset;
  if ((RANGE_PRESETS as ReadonlyArray<string>).includes(range)) preset = range as RangePreset;
  else if (range === "custom" && parseLocalDate(from) && parseLocalDate(to)) preset = "custom";
  const by = params.get("by") ?? "";
  const filters = { ...DEFAULT_VIEW.filters };
  for (const dimension of FILTER_DIMENSIONS) {
    filters[dimension] = [...new Set(params.getAll(HASH_KEYS[dimension]))];
  }
  return {
    preset,
    from: preset === "custom" ? (from <= to ? from : to) : "",
    to: preset === "custom" ? (from <= to ? to : from) : "",
    granularity: (GRANULARITIES as ReadonlyArray<string>).includes(by) ? by as Period : "auto",
    filters,
  };
}

// --- bucket keys -----------------------------------------------------------
// The server labels buckets "YYYY-MM-DD HH:00", "YYYY-MM-DD", "YYYY-Www"
// (ISO week) and "YYYY-MM". The dashboard lays the same keys out over the
// whole range so a period without usage shows as an empty bucket, not a gap
// closed up.

function isoWeekKey(date: Date) {
  // The ISO week belongs to the year of its Thursday.
  const thursday = new Date(date.getFullYear(), date.getMonth(), date.getDate() + 3 - ((date.getDay() + 6) % 7));
  const firstThursday = new Date(thursday.getFullYear(), 0, 4);
  const week = 1 + Math.round(((thursday.getTime() - firstThursday.getTime()) / DAY_MS - 3 + ((firstThursday.getDay() + 6) % 7)) / 7);
  return `${thursday.getFullYear()}-W${String(week).padStart(2, "0")}`;
}

export function bucketKeyFor(date: Date, period: Period) {
  const day = formatLocalDate(date);
  if (period === "hourly") return `${day} ${String(date.getHours()).padStart(2, "0")}:00`;
  if (period === "weekly") return isoWeekKey(date);
  if (period === "monthly") return day.slice(0, 7);
  return day;
}

// timelineKeys lists every bucket key of the range in order. An hourly range
// that reaches today stops at the current hour: the rest of the day has not
// happened, and drawing it as zero usage reads as a drop.
export function timelineKeys(bounds: DateBounds, period: Period, now: Date): string[] {
  const start = parseLocalDate(bounds.from);
  const end = parseLocalDate(bounds.to);
  if (!start || !end || start > end) return [];
  const keys: string[] = [];
  if (period === "hourly") {
    const last = new Date(end.getFullYear(), end.getMonth(), end.getDate(), 23);
    const stop = last > now ? now : last;
    for (const current = new Date(start); current <= stop; current.setHours(current.getHours() + 1)) {
      keys.push(bucketKeyFor(current, period));
    }
    return keys;
  }
  let previous = "";
  for (const current = new Date(start); current <= end; current.setDate(current.getDate() + 1)) {
    const key = bucketKeyFor(current, period);
    if (key !== previous) keys.push(key);
    previous = key;
  }
  return keys;
}
