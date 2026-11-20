import type { UsageBucket, UsageReport } from "../../api";
import { totalTokens } from "./format";
import { bucketKeyFor, formatLocalDate, parseLocalDate, timelineKeys, type DateBounds, type Period } from "./usageQuery";

export const TREND_DIMENSIONS = ["total", "framework", "model", "machine"] as const;
export type TrendDimension = (typeof TREND_DIMENSIONS)[number];

// slot is the series' colour slot; OTHER_SLOT marks the folded tail, drawn in
// a neutral colour.
export type TrendSeries = { id: string; label: string; tokens: number; values: number[]; slot: number };
export const OTHER_SLOT = -1;
export const SERIES_SLOTS = 5;

export type TrendLabels = {
  total: string;
  other: string;
  unknownModel: string;
  unattributed: string;
  runtime: (id: string) => string;
  machine: (id: string, name: string) => string;
};

// timelineFor lists the bucket keys a report's chart spans. A bounded range
// uses its own dates; "all time" runs from the first bucket with usage to now.
export function timelineFor(report: UsageReport | null, bounds: DateBounds, period: Period, now: Date): string[] {
  if (bounds.from && bounds.to) return timelineKeys(bounds, period, now);
  const keys = [
    ...(report?.buckets ?? []).map((bucket) => bucket.key),
    ...(report?.by_machine ?? []).flatMap((machine) => (machine.buckets ?? []).map((bucket) => bucket.key)),
  ].filter(Boolean).sort();
  if (keys.length === 0) return [];
  const start = bucketStartDate(keys[0]);
  if (!start) return [...new Set(keys)];
  return timelineKeys({ from: start, to: formatLocalDate(now) }, period, now);
}

// bucketStartDate is the first local date a bucket key covers.
export function bucketStartDate(key: string): string | null {
  const week = key.match(/^(\d{4})-W(\d{2})$/);
  if (week) {
    // Week 1 holds January 4th; weeks start on Monday.
    const january4 = new Date(Number(week[1]), 0, 4);
    const monday = new Date(january4.getFullYear(), 0, 4 - ((january4.getDay() + 6) % 7) + (Number(week[2]) - 1) * 7);
    return formatLocalDate(monday);
  }
  if (/^\d{4}-\d{2}$/.test(key)) return `${key}-01`;
  return parseLocalDate(key.slice(0, 10)) ? key.slice(0, 10) : null;
}

type Stat = { tokens: number };

// alignValues spreads per-bucket values over the timeline. A machine on an
// older build may answer an hourly request with whole days; such a day is
// shared evenly across its hours instead of being dropped.
function alignValues<T>(buckets: UsageBucket[], keys: string[], period: Period, read: (bucket: UsageBucket) => Map<T, number>): Map<T, number[]> {
  const index = new Map(keys.map((key, position) => [key, position]));
  const dayPositions = new Map<string, number[]>();
  if (period === "hourly") {
    keys.forEach((key, position) => {
      const day = key.slice(0, 10);
      const positions = dayPositions.get(day);
      if (positions) positions.push(position);
      else dayPositions.set(day, [position]);
    });
  }
  const series = new Map<T, number[]>();
  const add = (id: T, position: number, value: number) => {
    let values = series.get(id);
    if (!values) {
      values = Array<number>(keys.length).fill(0);
      series.set(id, values);
    }
    values[position] += value;
  };
  for (const bucket of buckets) {
    const position = index.get(bucket.key);
    if (position !== undefined) {
      for (const [id, value] of read(bucket)) add(id, position, value);
      continue;
    }
    const positions = period === "hourly" && !bucket.key.includes(" ") ? dayPositions.get(bucket.key) : undefined;
    if (!positions?.length) continue;
    for (const [id, value] of read(bucket)) {
      for (const hour of positions) add(id, hour, value / 24);
    }
  }
  return series;
}

const TOTAL = "total";

function bucketTotal(bucket: UsageBucket) {
  return new Map([[TOTAL, totalTokens(bucket.totals)]]);
}

function byStat<S extends Stat>(stats: S[] | undefined, id: (stat: S) => string) {
  const values = new Map<string, number>();
  for (const stat of stats ?? []) values.set(id(stat), (values.get(id(stat)) ?? 0) + stat.tokens);
  return values;
}

// Colour follows the series, not its rank: a model keeps its colour when a
// filter or a new range reorders the chart.
const rememberedSlots = new Map<string, number>();

export function assignSlots(dimension: string, ids: string[]): number[] {
  const taken = new Set<number>();
  const slots = ids.map((id) => {
    const remembered = rememberedSlots.get(`${dimension}:${id}`);
    if (remembered === undefined || taken.has(remembered)) return -1;
    taken.add(remembered);
    return remembered;
  });
  return slots.map((slot, position) => {
    if (slot >= 0) return slot;
    let free = 0;
    while (taken.has(free)) free += 1;
    taken.add(free);
    rememberedSlots.set(`${dimension}:${ids[position]}`, free);
    return free;
  });
}

export function resetSeriesSlots() {
  rememberedSlots.clear();
}

// foldSeries keeps the largest series and folds the rest into "Other": a
// chart never generates a colour for a sixth series.
function foldSeries(dimension: string, all: Array<{ id: string; label: string; values: number[] }>, otherLabel: string): TrendSeries[] {
  const sum = (values: number[]) => values.reduce((total, value) => total + value, 0);
  const ranked = all.map((item) => ({ ...item, tokens: sum(item.values) }))
    .filter((item) => item.tokens > 0)
    .sort((left, right) => right.tokens - left.tokens || left.id.localeCompare(right.id));
  const visible = ranked.slice(0, SERIES_SLOTS);
  const hidden = ranked.slice(SERIES_SLOTS);
  const slots = assignSlots(dimension, visible.map((item) => item.id));
  const series: TrendSeries[] = visible.map((item, position) => ({ ...item, slot: slots[position] }));
  if (hidden.length > 0) {
    const values = hidden[0].values.map((_, position) => hidden.reduce((total, item) => total + item.values[position], 0));
    series.push({ id: `other-${dimension}`, label: otherLabel, tokens: sum(values), values, slot: OTHER_SLOT });
  }
  return series;
}

export function buildTrendSeries(
  dimension: TrendDimension,
  report: UsageReport | null,
  keys: string[],
  period: Period,
  labels: TrendLabels,
): TrendSeries[] {
  if (!report || keys.length === 0) return [];
  const buckets = report.buckets ?? [];
  const totals = alignValues(buckets, keys, period, bucketTotal).get(TOTAL) ?? Array<number>(keys.length).fill(0);
  if (dimension === "total") {
    const tokens = totals.reduce((sum, value) => sum + value, 0);
    return tokens > 0 ? [{ id: TOTAL, label: labels.total, tokens, values: totals, slot: 0 }] : [];
  }
  if (dimension === "machine") {
    const machines = report.by_machine ?? [];
    // One machine, or a single-machine view: the total is that machine's.
    if (machines.length === 0) return buildTrendSeries("total", report, keys, period, labels);
    return foldSeries(dimension, machines.map((machine) => ({
      id: machine.target_id,
      label: labels.machine(machine.target_id, machine.target_name),
      values: alignValues(machine.buckets ?? [], keys, period, bucketTotal).get(TOTAL) ?? Array<number>(keys.length).fill(0),
    })), labels.other);
  }
  const aligned = dimension === "framework"
    ? alignValues(buckets, keys, period, (bucket) => byStat(bucket.by_runtime, (stat) => stat.runtime))
    : alignValues(buckets, keys, period, (bucket) => byStat(bucket.by_model, (stat) => stat.model));
  const series = [...aligned].map(([id, values]) => ({
    id: dimension === "model" ? `model:${id}` : id,
    label: dimension === "framework" ? labels.runtime(id) : id.trim() || labels.unknownModel,
    values,
  }));
  // A machine on an older build may return bucket totals without the
  // breakdown. That usage stays visible without inventing anyone's share.
  const missing = totals.map((total, position) => {
    const attributed = series.reduce((sum, item) => sum + item.values[position], 0);
    const rest = total - attributed;
    return rest > Math.max(1, total) * 1e-9 ? rest : 0;
  });
  if (missing.some((value) => value > 0)) series.push({ id: `unattributed-${dimension}`, label: labels.unattributed, values: missing });
  return foldSeries(dimension, series, labels.other);
}

// stackTotals is the height of each stacked column.
export function stackTotals(series: TrendSeries[], length: number): number[] {
  return Array.from({ length }, (_, position) => series.reduce((sum, item) => sum + (item.values[position] ?? 0), 0));
}

// niceCeiling rounds a maximum up to a round step so the axis reads cleanly
// without leaving half the plot empty.
export function niceCeiling(value: number): number {
  if (!(value > 0)) return 1;
  const magnitude = 10 ** Math.floor(Math.log10(value));
  for (const step of [1, 1.2, 1.5, 2, 2.5, 3, 4, 5, 6, 8, 10]) {
    if (value <= step * magnitude) return step * magnitude;
  }
  return 10 * magnitude;
}

// bucketLabel words a bucket key for an axis tick or tooltip.
export function bucketLabel(key: string, period: Period, options: { singleDay: boolean; long?: boolean }): string {
  if (period === "hourly") {
    const [date, time = ""] = key.split(" ");
    if (options.long) return `${date} ${time}`;
    return options.singleDay ? time : `${date.slice(5)} ${time}`;
  }
  if (period === "daily") return options.long ? key : key.slice(5);
  return key;
}

export function currentBucketKey(period: Period, now: Date) {
  return bucketKeyFor(now, period);
}
